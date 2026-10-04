package session

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/recovery"
	"github.com/zarkmakerburg/baft/internal/resources"
	"github.com/zarkmakerburg/baft/internal/routes"
)

const (
	defaultWindow           uint64 = 64 * 1024
	// maxWindow caps receive-window autotuning. It stays at or below
	// appConnSocketBuffer so the target socket can absorb a full window.
	maxWindow               uint64 = 4 * 1024 * 1024
	dataChunk                      = 32 * 1024
	maxClosedFlowTombstones         = 256
	maxFlowsPerShard                = 64
)

var ErrRecoverableDataGap = errors.New("recoverable DATA gap on recovery carrier")

type Role uint8

const (
	Dialer Role = iota + 1
	Listener
)

type Carrier struct {
	In  io.Reader
	Out io.Writer
}

type TrafficObserver func(ingressBytes, egressBytes uint64)
type LatencyObserver func(rtt time.Duration)

type Options struct {
	NodeID             string
	ExpectedPeerNodeID string
	ShardID            uint8
	ProfileID          string
	ProfileVersion     uint32
	ConfigRevision     string
	Resources          *resources.Allocator
	// FlowSlots is shared by every Session of a node to enforce
	// limits.max_flows; nil means unlimited.
	FlowSlots          *resources.FlowSlots
	TrafficObserver    TrafficObserver
	LatencyObserver    LatencyObserver
	PingInterval       time.Duration
	RecoveryEnabled    bool
	RecoveryRetention  time.Duration
	CarrierID          string
	BootID             string
}

type Peer struct {
	role               Role
	carrier            Carrier
	writer             frameWriter
	peerID             string
	routes             *routes.Table
	dial               func(context.Context, string, string) (net.Conn, error)
	allocator          *resources.Allocator
	flowSlots          *resources.FlowSlots
	sender             *outboundSender
	nodeID             string
	expectedPeerNodeID string
	bootID             string
	sessionID          string
	shardID            uint8
	profileID          string
	profileVersion     uint32
	configRevision     string
	epoch              string
	readyCh            chan struct{}
	localReady         bool
	peerReady          bool
	helloSeen          bool
	mu                 sync.Mutex
	flows              map[uint64]*flow
	closedFlows        map[uint64]struct{}
	// pendingOpens holds admitted OPENs whose target dial is still running
	// (recovery disabled only); guarded by mu.
	pendingOpens       map[uint64]protocol.OpenRequest
	closedOrder        []uint64
	// finishedFins maps a Flow that finished gracefully to the final offset
	// of this side's FIN, and finishedPeerFins to the final offset of the
	// peer's FIN; both are bounded and evicted together with closedOrder.
	finishedFins       map[uint64]uint64
	finishedPeerFins   map[uint64]uint64
	// resetFlows maps a Flow this side RESET to its code, so a peer that
	// missed the RESET (lost with a failed carrier) and keeps finishing the
	// Flow is told again; bounded and evicted together with closedOrder.
	resetFlows         map[uint64]protocol.ErrorCode
	nextID             uint64
	closed             bool
	runLifecycleMu     sync.RWMutex
	runActive          bool
	runExiting         bool
	wg                 sync.WaitGroup
	trafficObserver    TrafficObserver
	latencyObserver    LatencyObserver
	pingInterval       time.Duration
	recoveryEnabled    bool
	recoveryRetention  time.Duration
	recovery           *RecoveryAdapter
	recoveryNeeded     chan error
	carrierSwitchMu    sync.Mutex
	carrierSwitchWait  chan struct{}
	replacementMu      sync.Mutex
	replacementWait    chan struct{}
	replacementReadyGeneration uint64
	carrierID          string
	carrierEpoch       uint64
	carrierGeneration  uint64
	carrierPhysicalInstanceID uint64
	peerBootID         string
	runCtx             context.Context
	recoveryGate       sync.Mutex
	recoveryFrameHookMu sync.RWMutex
	recoveryFrameHook   func(string, protocol.Frame) bool
	runExitObserverMu sync.RWMutex
	runExitObserver func(error)
	// Test-only physical carrier write/start controls.
	writeFaultMu sync.RWMutex
	writeFaultForTest func(frame protocol.Frame,generation uint64) error
	senderStartHoldForTest func(generation uint64) <-chan struct{}
	recoveryWaitExpiryHookMu sync.RWMutex
	recoveryWaitExpiryHook func() bool
	logicalSessionRetainObserverMu sync.RWMutex
	logicalSessionRetainObserver func(string)
}

type replayChunk struct {
	start uint64
	end   uint64
	data  []byte
}

type deferredLiveFrame struct {
	frame protocol.Frame
	producer DataProducerKind
}

type flow struct {
	id              uint64
	resourceID      uint64
	routeID         string
	nonce           string
	conn            net.Conn
	allocator       *resources.Allocator
	slots           *resources.FlowSlots // node-wide slot this Flow holds until close
	openDone        chan error
	mu              sync.Mutex
	openOK          bool
	peerMax         uint64
	txNext          uint64
	txAcked         uint64
	rxNext          uint64
	rxWritten       uint64
	rxMax           uint64
	receiveReserved int64
	rxRing          *receiveRing
	rxCreditLimited bool   // a full window arrived since rxTurnStart
	rxTurnStart     uint64 // rxNext when the current window turn began
	rxGrowing       bool
	finRecvFinal    uint64
	finAckSent      bool
	finAckWriteInFlight bool
	finAckConfirmed bool
	writeClosed     bool
	replay          []replayChunk
	creditWait      chan struct{}
	ackWait         chan struct{}
	finSent         bool
	finRecv         bool
	finAcked        bool
	closed          bool
	resetCode       protocol.ErrorCode
	localPumpRunning bool
	targetPumpRunning bool
	localPumpDone chan struct{}
	targetPumpDone chan struct{}
	deferredLive []deferredLiveFrame
	deferredLiveRunning bool
	deferredLiveWake chan struct{}
}

type frameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *frameWriter) send(f protocol.Frame) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return protocol.Encode(w.w, f)
}

var nextResourceID atomic.Uint64

func defaultAllocator() *resources.Allocator {
	a, err := resources.NewAllocator(resources.DefaultLimits())
	if err != nil {
		panic(err)
	}
	return a
}

func New(role Role, c Carrier, peerID string, table *routes.Table, opts Options) (*Peer, error) {
	if c.In == nil || c.Out == nil {
		return nil, errors.New("carrier input/output are required")
	}
	if role != Dialer && role != Listener {
		return nil, errors.New("invalid role")
	}
	if role == Listener && (peerID == "" || table == nil) {
		return nil, errors.New("listener requires authenticated peer identity and route table")
	}
	if opts.NodeID == "" || len(opts.NodeID) > 64 || opts.ExpectedPeerNodeID == "" || len(opts.ExpectedPeerNodeID) > 64 {
		return nil, errors.New("node identities are required")
	}
	if opts.ShardID > 7 {
		return nil, errors.New("shard_id out of range")
	}
	if opts.ProfileID == "" {
		opts.ProfileID = "secure-fast"
	}
	if opts.ProfileVersion == 0 {
		opts.ProfileVersion = 1
	}
	if opts.ConfigRevision == "" {
		opts.ConfigRevision = "dev"
	}
	if opts.Resources == nil {
		opts.Resources = defaultAllocator()
	}
	bootID:=opts.BootID
	if bootID=="" {
		var err error
		bootID,err=randomHex128()
		if err!=nil{return nil,err}
	} else {
		b,err:=hex.DecodeString(bootID)
		if err!=nil||len(b)!=16||bootID!=hex.EncodeToString(b){return nil,errors.New("boot_id must be lowercase 128-bit hex")}
	}
	p := &Peer{
		role: role, carrier: c, writer: frameWriter{w: c.Out}, peerID: peerID,
		routes: table, allocator: opts.Resources, flowSlots: opts.FlowSlots, flows: make(map[uint64]*flow),
		closedFlows: make(map[uint64]struct{}), finishedFins: make(map[uint64]uint64), finishedPeerFins: make(map[uint64]uint64), resetFlows: make(map[uint64]protocol.ErrorCode), pendingOpens: make(map[uint64]protocol.OpenRequest),
		nodeID: opts.NodeID, expectedPeerNodeID: opts.ExpectedPeerNodeID,
		bootID: bootID, shardID: opts.ShardID, profileID: opts.ProfileID,
		profileVersion: opts.ProfileVersion, configRevision: opts.ConfigRevision,
		epoch: "1", readyCh: make(chan struct{}), trafficObserver: opts.TrafficObserver,
		latencyObserver: opts.LatencyObserver, pingInterval: opts.PingInterval,
		recoveryEnabled: opts.RecoveryEnabled, recoveryRetention: opts.RecoveryRetention,
		recoveryNeeded: make(chan error,1), carrierSwitchWait: make(chan struct{}), replacementWait: make(chan struct{}), replacementReadyGeneration:1, carrierEpoch:1, carrierGeneration:1, carrierPhysicalInstanceID:physicalCarrierInstanceID(1,1),
	}
	if role == Dialer {
		p.nextID = 1
		sid,err:=randomHex128()
		if err != nil {
			return nil, err
		}
		p.sessionID=sid
	} else {
		p.nextID = 2
	}
	p.dial = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	p.sender = newOutboundSender(&p.writer,p.recoveryEnabled)
	if p.recoveryEnabled {
		if p.recoveryRetention <= 0 { p.recoveryRetention = 30 * time.Second }
		if p.recoveryRetention > 300*time.Second { return nil, errors.New("recovery retention exceeds 300 seconds") }
		p.carrierID = opts.CarrierID
		if p.carrierID == "" { p.carrierID = "carrier-1" }
		eng, err := recovery.NewEngine(1,p.carrierID,recovery.EngineOptions{})
		if err != nil { return nil, err }
		p.recovery = newRecoveryAdapter(p,eng)
	}
	return p, nil
}

func randomHex128() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (p *Peer) SetRecoveryFrameHookForTest(fn func(string, protocol.Frame) bool) {
	p.recoveryFrameHookMu.Lock();p.recoveryFrameHook=fn;p.recoveryFrameHookMu.Unlock()
}

func (p *Peer) dropRecoveryFrameForTest(stage string,fr protocol.Frame) bool {
	p.recoveryFrameHookMu.RLock();fn:=p.recoveryFrameHook;p.recoveryFrameHookMu.RUnlock()
	return fn!=nil&&fn(stage,fr)
}

func (p *Peer) senderNow() *outboundSender {
	if !p.recoveryEnabled{return p.sender}
	p.mu.Lock()
	s:=p.sender
	p.mu.Unlock()
	return s
}

func (p *Peer) SetRunExitObserverForTest(fn func(error)) {
	p.runExitObserverMu.Lock();p.runExitObserver=fn;p.runExitObserverMu.Unlock()
}

// RunContextCauseForTest exposes the cancellation cause of the logical Session
// context without transferring ownership of that context. It is used only for
// lifecycle evidence so a generic "context canceled" exit can be attributed to
// its actual Runtime-level producer.
func (p *Peer) RunContextCauseForTest() error {
	p.mu.Lock()
	runCtx:=p.runCtx
	p.mu.Unlock()
	if runCtx==nil{return nil}
	return context.Cause(runCtx)
}

func (p *Peer) notifyRunExitForTest(err error) {
	p.runExitObserverMu.RLock();fn:=p.runExitObserver;p.runExitObserverMu.RUnlock()
	if fn!=nil{fn(err)}
}

// SetCarrierWriteFaultForTest injects an error at the physical write boundary
// of every recovery sender bound after this call.
func (p *Peer) SetCarrierWriteFaultForTest(fn func(frame protocol.Frame,generation uint64) error) {
	p.writeFaultMu.Lock();p.writeFaultForTest=fn;p.writeFaultMu.Unlock()
}

// SetSenderStartHoldForTest delays the start of an activated recovery sender.
func (p *Peer) SetSenderStartHoldForTest(fn func(generation uint64) <-chan struct{}) {
	p.writeFaultMu.Lock();p.senderStartHoldForTest=fn;p.writeFaultMu.Unlock()
}

func (p *Peer) senderStartHold(generation uint64) <-chan struct{} {
	p.writeFaultMu.RLock();fn:=p.senderStartHoldForTest;p.writeFaultMu.RUnlock()
	if fn==nil{return nil}
	return fn(generation)
}

func (p *Peer) carrierWriteFault(frame protocol.Frame,generation uint64) error {
	p.writeFaultMu.RLock();fn:=p.writeFaultForTest;p.writeFaultMu.RUnlock()
	if fn==nil{return nil}
	return fn(frame,generation)
}

func (p *Peer) SetRecoveryCarrierWaitExpiryHookForTest(fn func() bool) {
	p.recoveryWaitExpiryHookMu.Lock();p.recoveryWaitExpiryHook=fn;p.recoveryWaitExpiryHookMu.Unlock()
}

func (p *Peer) forceRecoveryCarrierWaitExpiryForTest() bool {
	p.recoveryWaitExpiryHookMu.RLock();fn:=p.recoveryWaitExpiryHook;p.recoveryWaitExpiryHookMu.RUnlock()
	return fn!=nil&&fn()
}

func (p *Peer) SetLogicalSessionRetainObserverForTest(fn func(string)) {
	p.logicalSessionRetainObserverMu.Lock();p.logicalSessionRetainObserver=fn;p.logicalSessionRetainObserverMu.Unlock()
}

func (p *Peer) notifyLogicalSessionRetainedForTest(reason string) {
	p.logicalSessionRetainObserverMu.RLock();fn:=p.logicalSessionRetainObserver;p.logicalSessionRetainObserverMu.RUnlock()
	if fn!=nil{fn(reason)}
}

func (p *Peer) activeApplicationFlowCount() int {
	p.mu.Lock();defer p.mu.Unlock()
	return len(p.flows)
}

func (p *Peer) logicalSessionRetentionReason() string {
	if !p.recoveryEnabled{return ""}
	if n:=p.activeApplicationFlowCount();n>0{return fmt.Sprintf("active_application_flows=%d",n)}
	if p.NeedsExactTransactionResolution(){return "exact_recovery_transaction"}
	st:=p.RecoveryStability()
	if st.ReplayOutstanding{return "replay_outstanding"}
	return ""
}

func (p *Peer) beginRunLifecycle() bool {
	p.runLifecycleMu.Lock()
	defer p.runLifecycleMu.Unlock()
	if p.runActive||p.runExiting{return false}
	p.runActive=true
	return true
}

func (p *Peer) beginRunTeardown() {
	// Taking the exclusive lifecycle lock waits for any in-flight recovery
	// activation lease to finish. Once runExiting is published, no later
	// activation or worker admission may mutate this Session.
	p.runLifecycleMu.Lock()
	p.runExiting=true
	p.runActive=false
	p.runLifecycleMu.Unlock()
}

func (p *Peer) admitWorker() bool {
	p.runLifecycleMu.RLock()
	defer p.runLifecycleMu.RUnlock()
	if !p.runActive||p.runExiting{return false}
	p.wg.Add(1)
	return true
}

func (p *Peer) Run(ctx context.Context) error { return p.run(ctx,nil) }

func (p *Peer) RunWithFirstFrame(ctx context.Context, first protocol.Frame) error {
	return p.run(ctx,&first)
}

func (p *Peer) run(ctx context.Context, first *protocol.Frame) (retErr error) {
	if !p.beginRunLifecycle(){return errors.New("session Run lifecycle already active or exiting")}
	runCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.runCtx = runCtx
	p.mu.Unlock()
	if !p.admitWorker(){cancel();return errors.New("session worker admission closed")}
	go func() {
		defer p.wg.Done()
		p.senderNow().run(runCtx)
	}()
	defer func() {
		// Once Run exits, no recovery/pump path may add another goroutine while
		// teardown is waiting for the existing worker set.
		p.beginRunTeardown()
		// Test-only lifecycle proof is emitted before destructive cleanup so it
		// observes the logical Session authority/flows that existed at Run exit.
		p.notifyRunExitForTest(retErr)
		cancel()
		p.closeAll()
		p.wg.Wait()
	}()
	if p.role == Dialer {
		if err := p.sendHello(); err != nil { return err }
		if p.pingInterval > 0 && p.admitWorker() {
			go func() { defer p.wg.Done(); p.pingLoop(runCtx) }()
		}
	}
	if first != nil {
		epoch,carrierID,generation:=p.currentCarrierIdentity()
		if err:=p.handleFrameFrom(runCtx,epoch,carrierID,*first,generation);err!=nil{return err}
	}
	for {
		carrier,epoch,carrierID,generation:=p.currentCarrier()
		f, err := protocol.Decode(carrier.In)
		if err != nil {
			if ctx.Err() != nil { return ctx.Err() }
			if !p.recoveryEnabled {
				if errors.Is(err,io.EOF){return ctx.Err()}
				return err
			}
			p.onCarrierFailureForGeneration(err,generation,SenderStopCarrierReaderDecode)
			if err:=p.waitForCarrierSwitch(runCtx,epoch,carrierID,generation);err!=nil{return err}
			continue
		}
		if err := p.handleFrameFrom(runCtx,epoch,carrierID,f,generation); err != nil {
			if p.recoveryEnabled && errors.Is(err,recovery.ErrStaleEpoch) {
				// A delayed frame from a fenced carrier is expected during
				// replacement. Reject it without mutating flow state, then
				// continue on the currently authoritative carrier.
				continue
			}
			if p.recoveryEnabled && errors.Is(err,ErrCarrierUnavailable) {
				p.onCarrierFailureForGeneration(err,generation,SenderStopFrameProcessing)
				if werr:=p.waitForCarrierSwitch(runCtx,epoch,carrierID,generation);werr!=nil{return werr}
				continue
			}
			return err
		}
	}
}

func (p *Peer) OpenFlow(ctx context.Context, routeID string, conn net.Conn) error {
	if p.role != Dialer {
		return errors.New("only dialer opens outbound flows in baseline")
	}
	if conn == nil || routeID == "" || len(routeID) > 64 {
		return errors.New("valid route and connection required")
	}
	if err := p.waitReady(ctx); err != nil {
		return err
	}
	if p.recoveryEnabled{p.recoveryGate.Lock()}
	if p.recovery!=nil && p.recovery.IsFrozen(){
		if p.recoveryEnabled{p.recoveryGate.Unlock()}
		return recovery.ErrResumeFrozen
	}
	if !p.flowSlots.TryAcquire() {
		if p.recoveryEnabled{p.recoveryGate.Unlock()}
		return resources.ErrResourceExhausted
	}
	id, err := p.allocateStreamID()
	if err != nil {
		p.flowSlots.Release()
		if p.recoveryEnabled{p.recoveryGate.Unlock()}
		return err
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		p.flowSlots.Release()
		if p.recoveryEnabled{p.recoveryGate.Unlock()}
		return err
	}
	fl := newFlow(id, routeID, hex.EncodeToString(nonceBytes), conn, p.allocator)
	fl.slots = p.flowSlots
	p.mu.Lock()
	p.flows[id] = fl
	p.mu.Unlock()
	if p.recoveryEnabled{p.recoveryGate.Unlock()}

	payload, err := protocol.EncodeControl(protocol.OpenRequest{RouteID: routeID, OpenNonce: fl.nonce})
	if err != nil {
		return err
	}
	if err := p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpen, StreamID: id, Payload: payload}); err != nil {
		fl.close()
		p.removeFlow(id)
		return err
	}
	select {
	case err := <-fl.openDone:
		if err != nil {
			fl.close()
			p.removeFlow(id)
			return err
		}
	case <-ctx.Done():
		fl.close()
		p.removeFlow(id)
		return ctx.Err()
	}
	if err := p.senderNow().addFlow(fl.id); err != nil {
		_ = p.sendReset(fl, protocol.ErrorResourceExhausted)
		return err
	}
	if err := p.grantReceive(fl); err != nil {
		_ = p.sendReset(fl, protocol.ErrorResourceExhausted)
		return err
	}
	p.startPump(ctx, fl)
	p.startTargetPump(ctx, fl)
	return nil
}

// appConnSocketBuffer is the kernel send/receive buffer requested for the
// application-facing TCP connection of every Flow (the local client on the
// dialer, the target on the listener). The kernel caps it at
// net.core.{r,w}mem_max.
//
// A fixed size turns off receive-buffer autotuning on that socket. With
// autotuning, BAFT's credit backpressure (a pump that stops reading while the
// peer has no window) leaves the queue full, and on loopback (MSS ~64 KiB) the
// kernel drops segments that overrun the autotuned rcvbuf. TCP then recovers
// only through exponential RTO/persist backoff (observed: backoff 6, rto
// 12.8 s, cwnd 1), which stalls the Flow for seconds to minutes (COR-T1, #65).
// Measured on CI at 32 KiB echo: autotuned 3/12 stalls, fixed buffers 0/20.
const appConnSocketBuffer = 4 * 1024 * 1024

func tuneAppConn(conn net.Conn) {
	tc, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tc.SetReadBuffer(appConnSocketBuffer)
	_ = tc.SetWriteBuffer(appConnSocketBuffer)
}

func newFlow(id uint64, routeID, nonce string, conn net.Conn, alloc ...*resources.Allocator) *flow {
	tuneAppConn(conn)
	a := (*resources.Allocator)(nil)
	if len(alloc) > 0 {
		a = alloc[0]
	}
	if a == nil {
		a = defaultAllocator()
	}
	rid := nextResourceID.Add(1)
	if rid == 0 {
		rid = nextResourceID.Add(1)
	}
	return &flow{
		id: id, resourceID: rid, routeID: routeID, nonce: nonce, conn: conn,
		allocator: a, openDone: make(chan error, 1), creditWait: make(chan struct{}), ackWait: make(chan struct{}),
	}
}

func (p *Peer) allocateStreamID() (uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.nextID == 0 || p.nextID > ^uint64(0)-2 {
		return 0, errors.New("session cannot allocate another stream")
	}
	id := p.nextID
	p.nextID += 2
	return id, nil
}

func (p *Peer) handleFrame(ctx context.Context, fr protocol.Frame) error {
	switch fr.Type {
	case protocol.TypeHello:
		return p.handleHello(fr)
	case protocol.TypeHelloAck:
		return p.handleHelloAck(fr)
	case protocol.TypeReady:
		return p.handleReady(fr)
	}
	if !p.isReady() {
		return errors.New("application frame received before READY")
	}
	switch fr.Type {
	case protocol.TypeResumeDone:
		if p.recovery==nil{return errors.New("unexpected recovery control while recovery disabled")}
		return p.HandleRecoveryControlFrame(fr)
	case protocol.TypePing:
		return p.handlePing(fr)
	case protocol.TypePong:
		return p.handlePong(fr)
	case protocol.TypeOpen:
		return p.handleOpen(ctx, fr)
	case protocol.TypeOpenOK:
		fl, err := p.getFlow(fr.StreamID)
		if err != nil {
			if p.isClosedFlow(fr.StreamID) {
				// The dialer gave up on this OPEN (cancelled, or abandoned when the
				// carrier failed) after the listener had opened it. Have the
				// listener close its side so the target connection is not kept.
				if payload, perr := protocol.EncodeControl(protocol.Reset{Code: protocol.ErrorStateMismatch}); perr == nil {
					_ = p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeReset, StreamID: fr.StreamID, Payload: payload})
				}
				return nil
			}
			return err
		}
		fl.mu.Lock()
		fl.openOK = true
		fl.mu.Unlock()
		select {
		case fl.openDone <- nil:
		default:
		}
		return nil
	case protocol.TypeOpenErr:
		fl, err := p.getFlow(fr.StreamID)
		if err != nil {
			if p.isClosedFlow(fr.StreamID) {
				return nil
			}
			return err
		}
		oe, err := protocol.DecodeOpenError(fr.Payload)
		if err != nil {
			return err
		}
		select {
		case fl.openDone <- errors.New(string(oe.Code)):
		default:
		}
		return nil
	case protocol.TypeWindow:
		fl, err := p.getOpenFlow(fr.StreamID)
		if err != nil {
			if p.isClosedFlow(fr.StreamID) {
				return nil
			}
			return err
		}
		return fl.onWindow(fr.Offset)
	case protocol.TypeAck:
		fl, err := p.getOpenFlow(fr.StreamID)
		if err != nil {
			if p.isClosedFlow(fr.StreamID) {
				return nil
			}
			return err
		}
		fl.mu.Lock()
		beforeAck:=fl.txAcked
		fl.mu.Unlock()
		if err := fl.onAck(fr.Offset); err != nil {
			return err
		}
		p.senderNow().updatePressure(fl.id, fl.replayPressure())
		if p.recoveryEnabled && fr.Offset>beforeAck {
			ctl:=RecoveryControl{}
			inc:=uint64(0)
			if p.recovery!=nil {
				p.recovery.mu.Lock()
				if p.recovery.prepared!=nil {
					ctl=p.recovery.prepared.control
					inc=p.recovery.prepared.incarnation
				}
				p.recovery.mu.Unlock()
			}
			_,_,gen:=p.currentCarrierIdentity()
			p.traceRecoveryDiagnostic("REPLAY_ACK_ACCEPTED",SenderStopUnknown,fmt.Errorf("stream=%d accepted=%d previous=%d",fl.id,fr.Offset,beforeAck),"",p.senderNow(),ctl,inc,gen)
		}
		return nil
	case protocol.TypeData:
		fl, err := p.getOpenFlow(fr.StreamID)
		if err != nil {
			// The peer may still have DATA/FIN in flight when this side RESET
			// or finished the Flow; only that Flow is gone, not the Session.
			if p.isClosedFlow(fr.StreamID) {
				return nil
			}
			return err
		}
		return p.handleData(fl, fr)
	case protocol.TypeFin:
		fl, err := p.getOpenFlow(fr.StreamID)
		if err != nil {
			if p.isClosedFlow(fr.StreamID) {
				if p.resendLostReset(fr.StreamID) {
					return nil
				}
				return p.reackFinishedFin(fr.StreamID, fr.Offset)
			}
			return err
		}
		return p.handleFin(fl, fr.Offset)
	case protocol.TypeFinAck:
		fl, err := p.getOpenFlow(fr.StreamID)
		if err != nil {
			if p.isClosedFlow(fr.StreamID) {
				if p.resendLostReset(fr.StreamID) {
					return nil
				}
				return p.reconfirmFinishedFinAck(fr.StreamID, fr.Offset)
			}
			return err
		}
		if err := fl.onFinAck(fr.Offset); err != nil {
			return err
		}
		if err:=p.senderNow().sendControl(protocol.Frame{Type:protocol.TypeFinAckConfirm,StreamID:fl.id,Offset:fr.Offset});err!=nil{return err}
		p.finishIfComplete(fl)
		return nil
	case protocol.TypeFinAckConfirm:
		fl,err:=p.getOpenFlow(fr.StreamID)
		if err!=nil{
			if p.isClosedFlow(fr.StreamID){return nil}
			return err
		}
		if err:=fl.onFinAckConfirm(fr.Offset);err!=nil{return err}
		p.finishIfComplete(fl)
		return nil
	case protocol.TypeReset:
		fl, err := p.getFlow(fr.StreamID)
		if err != nil {
			if p.isClosedFlow(fr.StreamID) {
				return nil
			}
			return err
		}
		rst, err := protocol.DecodeReset(fr.Payload)
		if err != nil {
			return err
		}
		fl.mu.Lock()
		fl.resetCode = rst.Code
		fl.mu.Unlock()
		fl.close()
		p.removeFlow(fr.StreamID)
		return nil
	default:
		return fmt.Errorf("unsupported frame in stage C session: 0x%02x", uint8(fr.Type))
	}
}

func (p *Peer) sendHello() error {
	p.mu.Lock()
	h := protocol.Hello{
		ProtocolMin: 1, ProtocolMax: 1, NodeID: p.nodeID, BootID: p.bootID,
		SessionID: p.sessionID, ShardID: p.shardID, Epoch: p.epoch, Mode: "new",
		ResumeSnapshotID: nil, ProfileID: p.profileID, ProfileVersion: p.profileVersion,
		ConfigRevision: p.configRevision, Capabilities: []string{},
	}
	p.mu.Unlock()
	payload, err := protocol.EncodeControl(h)
	if err != nil {
		return err
	}
	return p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeHello, Payload: payload})
}

func (p *Peer) handleHello(fr protocol.Frame) error {
	if p.role != Listener {
		return errors.New("dialer received HELLO")
	}
	h, err := protocol.DecodeHello(fr.Payload)
	if err != nil {
		return err
	}
	if h.NodeID != p.expectedPeerNodeID {
		return errors.New("HELLO node_id does not match authenticated peer")
	}
	if h.Mode != "new" {
		return errors.New("resume is not implemented in stage C")
	}
	if h.ProfileID != p.profileID || h.ProfileVersion != p.profileVersion {
		return errors.New("profile mismatch")
	}
	p.mu.Lock()
	if p.helloSeen {
		p.mu.Unlock()
		return errors.New("duplicate HELLO")
	}
	p.helloSeen = true
	p.sessionID = h.SessionID
	p.peerBootID = h.BootID
	p.epoch = h.Epoch
	p.shardID = h.ShardID
	ack := protocol.HelloAck{
		SelectedProtocol: 1, SessionID: p.sessionID, Epoch: p.epoch, PeerBootID: p.bootID,
		AcceptedProfile: protocol.AcceptedProfile{ID: p.profileID, Version: p.profileVersion},
		NegotiatedLimits: protocol.NegotiatedLimits{
			MaxFramePayloadBytes: protocol.MaxPayloadSize, MaxFlowsPerShard: maxFlowsPerShard,
			ReceiveInitialBytes: uint32(defaultWindow), ReceiveMaxBytes: 16 * 1024 * 1024,
			RetentionMS: 30000,
		},
	}
	p.mu.Unlock()
	payload, err := protocol.EncodeControl(ack)
	if err != nil {
		return err
	}
	if err := p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeHelloAck, Payload: payload}); err != nil {
		return err
	}
	return p.sendReady()
}

func (p *Peer) handleHelloAck(fr protocol.Frame) error {
	if p.role != Dialer {
		return errors.New("listener received HELLO_ACK")
	}
	ack, err := protocol.DecodeHelloAck(fr.Payload)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.helloSeen {
		p.mu.Unlock()
		return errors.New("duplicate HELLO_ACK")
	}
	if ack.SessionID != p.sessionID || ack.Epoch != p.epoch || ack.AcceptedProfile.ID != p.profileID || ack.AcceptedProfile.Version != p.profileVersion {
		p.mu.Unlock()
		return errors.New("HELLO_ACK state mismatch")
	}
	p.helloSeen = true
	p.peerBootID = ack.PeerBootID
	p.mu.Unlock()
	return p.sendReady()
}

func (p *Peer) sendReady() error {
	payload, err := protocol.EncodeControl(protocol.Ready{SnapshotID: nil})
	if err != nil {
		return err
	}
	if err := p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeReady, Payload: payload}); err != nil {
		return err
	}
	p.mu.Lock()
	p.localReady = true
	p.markReadyLocked()
	p.mu.Unlock()
	return nil
}

func (p *Peer) handleReady(fr protocol.Frame) error {
	r, err := protocol.DecodeReady(fr.Payload)
	if err != nil {
		return err
	}
	if r.SnapshotID != nil {
		return errors.New("snapshot READY is unsupported before resume")
	}
	p.mu.Lock()
	p.peerReady = true
	p.markReadyLocked()
	p.mu.Unlock()
	return nil
}

func (p *Peer) markReadyLocked() {
	if p.localReady && p.peerReady {
		select {
		case <-p.readyCh:
		default:
			close(p.readyCh)
		}
	}
}

func (p *Peer) isReady() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.readyCh:
		return true
	default:
		return false
	}
}

func (p *Peer) waitReady(ctx context.Context) error {
	select {
	case <-p.readyCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Peer) handleOpen(ctx context.Context, fr protocol.Frame) error {
	if p.role != Listener {
		return errors.New("dialer received unexpected OPEN")
	}
	if p.recoveryEnabled{p.recoveryGate.Lock();defer p.recoveryGate.Unlock()}
	if p.recovery!=nil && p.recovery.IsFrozen(){
		// A frozen listener cannot admit a Flow, but an OPEN still in flight
		// on the carrier must not end the whole Session, which returning
		// ErrResumeFrozen here did. Refuse just this OPEN; if the answer is
		// lost with the carrier, the dialer abandons the unanswered OPEN.
		payload, _ := protocol.EncodeControl(protocol.OpenError{Code: protocol.ErrorStateMismatch})
		_ = p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenErr, StreamID: fr.StreamID, Payload: payload})
		return nil
	}
	req, err := protocol.DecodeOpen(fr.Payload)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if existing := p.flows[fr.StreamID]; existing != nil {
		p.mu.Unlock()
		same := existing.routeID == req.RouteID && existing.nonce == req.OpenNonce
		existing.mu.Lock()
		openOK := existing.openOK
		existing.mu.Unlock()
		if !same {
			return errors.New("duplicate stream_id with different OPEN identity")
		}
		if !openOK {
			// finishOpen is still completing this Flow and will answer it.
			return nil
		}
		return p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenOK, StreamID: fr.StreamID, Payload: []byte("{}")})
	}
	if pending, ok := p.pendingOpens[fr.StreamID]; ok {
		p.mu.Unlock()
		if pending.RouteID != req.RouteID || pending.OpenNonce != req.OpenNonce {
			return errors.New("duplicate stream_id with different OPEN identity")
		}
		// The in-flight dial answers this OPEN with OPEN_OK or OPEN_ERR.
		return nil
	}
	full := len(p.flows)+len(p.pendingOpens) >= maxFlowsPerShard
	p.mu.Unlock()
	if full {
		// HELLO_ACK advertises this per-Shard limit; refuse before dialing the target.
		payload, _ := protocol.EncodeControl(protocol.OpenError{Code: protocol.ErrorResourceExhausted})
		return p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenErr, StreamID: fr.StreamID, Payload: payload})
	}

	target, err := p.routes.Resolve(p.peerID, req.RouteID)
	if err != nil {
		payload, _ := protocol.EncodeControl(protocol.OpenError{Code: protocol.ErrorCode(err.Error())})
		return p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenErr, StreamID: fr.StreamID, Payload: payload})
	}
	if !p.flowSlots.TryAcquire() {
		payload, _ := protocol.EncodeControl(protocol.OpenError{Code: protocol.ErrorResourceExhausted})
		return p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenErr, StreamID: fr.StreamID, Payload: payload})
	}
	if p.recoveryEnabled {
		// Recovery serializes OPEN admission with snapshots through
		// recoveryGate, held for this whole call, so dial inline.
		conn, err := p.dial(ctx, "tcp", target)
		return p.finishOpen(ctx, fr.StreamID, req, conn, err, false)
	}
	// Dial off the frame loop: a slow or blackholed target must not stall
	// every other Flow on this Shard for the dial timeout.
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.flowSlots.Release()
		return errors.New("session closed")
	}
	p.pendingOpens[fr.StreamID] = req
	p.mu.Unlock()
	if !p.admitWorker(){
		p.mu.Lock();delete(p.pendingOpens,fr.StreamID);p.mu.Unlock()
		p.flowSlots.Release()
		return ErrCarrierUnavailable
	}
	go func() {
		defer p.wg.Done()
		conn, err := p.dial(ctx, "tcp", target)
		// Failures here are already turned into OPEN_ERR, RESET or a closed
		// Flow; a broken carrier also ends the frame loop on its own.
		_ = p.finishOpen(ctx, fr.StreamID, req, conn, err, true)
	}()
	return nil
}

// finishOpen completes an admitted OPEN after the target dial returned. It owns
// the node flow slot taken by handleOpen and, when pending, the pendingOpens
// entry, which is replaced by the Flow atomically so a retransmitted OPEN
// never dials twice.
func (p *Peer) finishOpen(ctx context.Context, id uint64, req protocol.OpenRequest, conn net.Conn, dialErr error, pending bool) error {
	if dialErr != nil {
		if pending {
			p.mu.Lock()
			delete(p.pendingOpens, id)
			p.mu.Unlock()
		}
		p.flowSlots.Release()
		payload, _ := protocol.EncodeControl(protocol.OpenError{Code: targetDialErrorCode(dialErr)})
		return p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenErr, StreamID: id, Payload: payload})
	}
	fl := newFlow(id, req.RouteID, req.OpenNonce, conn, p.allocator)
	fl.slots = p.flowSlots
	p.mu.Lock()
	if pending {
		delete(p.pendingOpens, id)
	}
	if p.closed {
		p.mu.Unlock()
		fl.close()
		return errors.New("session closed")
	}
	if old := p.flows[id]; old != nil {
		p.mu.Unlock()
		fl.close()
		return errors.New("concurrent OPEN conflict")
	}
	p.flows[id] = fl
	p.mu.Unlock()

	window, err := p.reserveReceiveWindow(fl)
	if err != nil {
		fl.close()
		p.removeFlow(id)
		payload, _ := protocol.EncodeControl(protocol.OpenError{Code: protocol.ErrorResourceExhausted})
		return p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenErr, StreamID: id, Payload: payload})
	}
	fl.mu.Lock()
	fl.openOK = true
	fl.mu.Unlock()
	if err := p.senderNow().addFlow(fl.id); err != nil {
		fl.close()
		p.removeFlow(id)
		payload, _ := protocol.EncodeControl(protocol.OpenError{Code: protocol.ErrorResourceExhausted})
		return p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenErr, StreamID: id, Payload: payload})
	}
	if err := p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenOK, StreamID: id, Payload: []byte("{}")}); err != nil {
		fl.close()
		p.removeFlow(id)
		return err
	}
	if err := p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeWindow, StreamID: fl.id, Offset: window}); err != nil {
		fl.close()
		p.removeFlow(id)
		return err
	}
	p.startPump(ctx, fl)
	p.startTargetPump(ctx, fl)
	return nil
}

func targetDialErrorCode(err error) protocol.ErrorCode {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return protocol.ErrorTargetTimeout
	}
	return protocol.ErrorTargetUnreachable
}

func (p *Peer) sendReset(fl *flow, code protocol.ErrorCode) error {
	if !protocol.ValidErrorCode(code) {
		return errors.New("invalid local RESET code")
	}
	payload, err := protocol.EncodeControl(protocol.Reset{Code: code})
	if err != nil {
		return err
	}
	if err := p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeReset, StreamID: fl.id, Payload: payload}); err != nil {
		return err
	}
	fl.mu.Lock()
	fl.resetCode = code
	fl.mu.Unlock()
	fl.close()
	p.removeFlow(fl.id)
	p.mu.Lock()
	if _, ok := p.closedFlows[fl.id]; ok {
		p.resetFlows[fl.id] = code
	}
	p.mu.Unlock()
	return nil
}

func (p *Peer) reserveReceiveWindow(fl *flow) (uint64, error) {
	fl.mu.Lock()
	if fl.closed {
		fl.mu.Unlock()
		return 0, errFlowClosed
	}
	need := int64(defaultWindow)
	if fl.receiveReserved >= need && fl.rxRing != nil {
		fl.mu.Unlock()
		p.growReceiveWindow(fl)
		fl.mu.Lock()
		if fl.closed {
			fl.mu.Unlock()
			return 0, errors.New("flow closed")
		}
		max := fl.rxWritten + uint64(fl.receiveReserved)
		if max < fl.rxWritten {
			fl.mu.Unlock()
			return 0, errors.New("receive window overflow")
		}
		if max > fl.rxMax {
			fl.rxMax = max
		}
		out := fl.rxMax
		fl.mu.Unlock()
		return out, nil
	}
	if fl.receiveReserved != 0 || fl.rxRing != nil {
		fl.mu.Unlock()
		return 0, errors.New("partial receive reservation state")
	}
	fl.mu.Unlock()

	if err := fl.allocator.Reserve(fl.resourceID, resources.Receive, need); err != nil {
		return 0, err
	}
	ring, err := newReceiveRing(int(need))
	if err != nil {
		_ = fl.allocator.Release(fl.resourceID, resources.Receive, need)
		return 0, err
	}

	fl.mu.Lock()
	if fl.closed {
		fl.mu.Unlock()
		ring.Close()
		_ = fl.allocator.Release(fl.resourceID, resources.Receive, need)
		return 0, errFlowClosed
	}
	fl.receiveReserved = need
	fl.rxRing = ring
	max := fl.rxWritten + uint64(fl.receiveReserved)
	if max < fl.rxWritten {
		fl.mu.Unlock()
		return 0, errors.New("receive window overflow")
	}
	fl.rxMax = max
	fl.mu.Unlock()
	return max, nil
}

// growReceiveWindow doubles a Flow's receive window, up to maxWindow, when the
// window rather than the target is what limits the Flow: a full window of
// bytes arrived since the last turn (rxCreditLimited), so the peer is sending
// in bulk, while the target kept the ring at most half full. Credit refreshes
// race the bytes in flight, so the receiver cannot see the peer hit the exact
// credit edge; a full window turning over is the observable signal. A slow target therefore never earns a larger window, and its
// backpressure behaviour is unchanged. Growth is opportunistic: it reserves
// memory only while half of the receive pool stays free for new Flows, and a
// failed reservation just keeps the current window. Windows never shrink while
// the Flow lives; close releases the whole reservation.
func (p *Peer) growReceiveWindow(fl *flow) {
	fl.mu.Lock()
	ring := fl.rxRing
	current := fl.receiveReserved
	if fl.closed || fl.rxGrowing || !fl.rxCreditLimited || ring == nil || uint64(current) >= maxWindow {
		fl.mu.Unlock()
		return
	}
	fl.rxCreditLimited = false
	if ring.Len() > int(current/2) {
		fl.mu.Unlock()
		return
	}
	next := current * 2
	if uint64(next) > maxWindow {
		next = int64(maxWindow)
	}
	delta := next - current
	fl.rxGrowing = true
	fl.mu.Unlock()

	keepFree := fl.allocator.Limits().Receive / 2
	grown := false
	if fl.allocator.ReserveIfFree(fl.resourceID, resources.Receive, delta, keepFree) == nil {
		if ring.Grow(int(next)) == nil {
			grown = true
		} else {
			_ = fl.allocator.Release(fl.resourceID, resources.Receive, delta)
		}
	}

	fl.mu.Lock()
	fl.rxGrowing = false
	if grown {
		if fl.closed {
			// close released the Flow's reservations before this one landed.
			fl.mu.Unlock()
			_ = fl.allocator.Release(fl.resourceID, resources.Receive, delta)
			return
		}
		fl.receiveReserved = next
	}
	fl.mu.Unlock()
}

func (p *Peer) grantReceive(fl *flow) error {
	max, err := p.reserveReceiveWindow(fl)
	if err != nil {
		return err
	}
	return p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeWindow, StreamID: fl.id, Offset: max})
}

func (p *Peer) handleData(fl *flow, fr protocol.Frame) error {
	fl.mu.Lock()
	before:=fl.rxNext
	fl.mu.Unlock()
	ack, duplicate, err := fl.acceptData(fr.Offset, fr.Payload)
	if err != nil {
		if p.recoveryEnabled && errors.Is(err,ErrRecoverableDataGap) {
			_,_,currentGeneration:=p.currentCarrierIdentity()
			p.traceRecoveryFrameDiagnostic("DATA_GAP",fr,currentGeneration,currentGeneration,before,fmt.Errorf("%w: offset=%d rx_next=%d",err,fr.Offset,before))
			// A later replay frame can still be buffered on a physical carrier
			// whose earlier frame was written but never accepted. Local write
			// order is not peer-delivery proof. Preserve the Flow and force an
			// exact-transaction carrier rebind so replay restarts from the
			// ACK-derived frontier. Recovery-disabled behavior remains unchanged.
			return fmt.Errorf("%w: %v",ErrCarrierUnavailable,err)
		}
		return p.sendReset(fl, protocol.ErrorFlowControl)
	}
	if err := p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeAck, StreamID: fl.id, Offset: ack}); err != nil {
		return err
	}
	if duplicate {
		return nil
	}
	if p.trafficObserver != nil && ack>before {
		p.trafficObserver(ack-before,0)
	}
	return nil
}

func (p *Peer) handleFin(fl *flow, finalOffset uint64) error {
	exactObligation:=p.recoveryEnabled&&p.NeedsExactTransactionResolution()
	fl.mu.Lock()
	if finalOffset != fl.rxNext {
		accepted:=fl.rxNext
		fl.mu.Unlock()
		if p.recoveryEnabled && finalOffset>accepted {
			// A FIN written after DATA may outlive the physical carrier even when
			// the preceding DATA was not peer-accepted. Do not turn that delivery
			// ambiguity into an application reset; rebind and replay exact state.
			return fmt.Errorf("%w: FIN ahead of accepted DATA",ErrCarrierUnavailable)
		}
		return errors.New("FIN final_offset does not match accepted data")
	}
	if fl.finRecv && fl.finRecvFinal != finalOffset {
		fl.mu.Unlock()
		return errors.New("conflicting FIN final_offset")
	}
	fl.finRecv = true
	fl.finRecvFinal = finalOffset
	ready := fl.rxWritten == finalOffset
	fl.mu.Unlock()
	if exactObligation{p.markExactTerminalObligation(fl,false,true,finalOffset)}

	if ready {
		return p.ackRemoteFin(fl)
	}
	return nil
}

func (p *Peer) ackRemoteFin(fl *flow) error {
	fl.mu.Lock()
	if !fl.finRecv || fl.rxWritten != fl.finRecvFinal {
		fl.mu.Unlock()
		return nil
	}
	if fl.finAckConfirmed {
		fl.mu.Unlock()
		p.finishIfComplete(fl)
		return nil
	}
	// Serialize FIN_ACK emission for this flow. The peer may consume the frame
	// and return FIN_ACK_CONFIRM before sendControl returns, so the local
	// acceptance window must be published before the frame becomes writable.
	if fl.finAckWriteInFlight {
		fl.mu.Unlock()
		return nil
	}
	fl.finAckWriteInFlight=true
	final := fl.finRecvFinal
	needCloseWrite:=!fl.writeClosed
	if needCloseWrite{fl.writeClosed=true}
	fl.mu.Unlock()

	if needCloseWrite {
		if cw, ok := fl.conn.(interface{ CloseWrite() error }); ok {
			if err := cw.CloseWrite(); err != nil {
				fl.mu.Lock()
				fl.finAckWriteInFlight=false
				fl.mu.Unlock()
				return p.sendReset(fl, protocol.ErrorTargetUnreachable)
			}
		}
	}

	fl.mu.Lock()
	fl.finAckSent=true
	fl.mu.Unlock()
	err := p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeFinAck, StreamID: fl.id, Offset: final})
	fl.mu.Lock()
	fl.finAckWriteInFlight=false
	if err!=nil && !fl.finAckConfirmed && !p.recoveryEnabled {
		// Baseline non-recovery sessions may retry from local write failure.
		// Recovery-enabled sessions keep finAckSent monotonic: a carrier error
		// after emission is not proof that the peer missed the FIN_ACK.
		fl.finAckSent=false
	}
	fl.mu.Unlock()
	if err != nil {
		return err
	}
	p.finishIfComplete(fl)
	return nil
}

func (p *Peer) finishIfComplete(fl *flow) {
	fl.mu.Lock()
	// A locally written FIN_ACK is not enough to destroy flow state. The flow
	// can be released only after our FIN was acknowledged and the peer proved
	// receipt of the FIN_ACK we sent for its FIN.
	done := fl.finAcked && fl.finAckSent && fl.finAckConfirmed
	final := fl.txNext
	peerFinal := fl.finRecvFinal
	fl.mu.Unlock()
	if !done {
		return
	}
	fl.close()
	p.removeFlow(fl.id)
	p.mu.Lock()
	if _, ok := p.closedFlows[fl.id]; ok {
		p.finishedFins[fl.id] = final
		p.finishedPeerFins[fl.id] = peerFinal
	}
	p.mu.Unlock()
}

func (p *Peer) startPump(ctx context.Context, fl *flow) {
	fl.mu.Lock()
	if fl.closed||fl.conn==nil||fl.finSent||fl.localPumpRunning { fl.mu.Unlock(); return }
	done:=make(chan struct{})
	fl.localPumpRunning=true
	fl.localPumpDone=done
	fl.mu.Unlock()
	if !p.admitWorker(){
		fl.mu.Lock()
		if fl.localPumpDone==done{fl.localPumpRunning=false;close(done)}
		fl.mu.Unlock()
		return
	}
	go func() {
		defer p.wg.Done()
		defer func(){
			fl.mu.Lock()
			if fl.localPumpDone==done { fl.localPumpRunning=false; close(done) }
			fl.mu.Unlock()
		}()
		p.pumpLocal(ctx, fl)
	}()
}

func (p *Peer) startTargetPump(ctx context.Context, fl *flow) {
	fl.mu.Lock()
	if fl.closed||fl.conn==nil||fl.finAckSent||fl.targetPumpRunning { fl.mu.Unlock(); return }
	done:=make(chan struct{})
	fl.targetPumpRunning=true
	fl.targetPumpDone=done
	fl.mu.Unlock()
	if !p.admitWorker(){
		fl.mu.Lock()
		if fl.targetPumpDone==done{fl.targetPumpRunning=false;close(done)}
		fl.mu.Unlock()
		return
	}
	go func() {
		defer p.wg.Done()
		defer func(){
			fl.mu.Lock()
			if fl.targetPumpDone==done { fl.targetPumpRunning=false; close(done) }
			fl.mu.Unlock()
		}()
		p.pumpTarget(ctx, fl)
	}()
}

func (p *Peer) ensurePumpsAfterRecovery(ctx context.Context,fl *flow){
	fl.mu.Lock()
	localRunning,localDone:=fl.localPumpRunning,fl.localPumpDone
	targetRunning,targetDone:=fl.targetPumpRunning,fl.targetPumpDone
	fl.mu.Unlock()
	if !localRunning { p.startPump(ctx,fl) } else if localDone!=nil && p.admitWorker() {
		go func(){defer p.wg.Done();select{case <-ctx.Done():case <-localDone:p.startPump(ctx,fl)}}()
	}
	if !targetRunning { p.startTargetPump(ctx,fl) } else if targetDone!=nil && p.admitWorker() {
		go func(){defer p.wg.Done();select{case <-ctx.Done():case <-targetDone:p.startTargetPump(ctx,fl)}}()
	}
}

func (p *Peer) pumpTarget(ctx context.Context, fl *flow) {
	fl.mu.Lock()
	ring := fl.rxRing
	fl.mu.Unlock()
	if ring == nil {
		return
	}

	for {
		segment, err := ring.Peek(ctx, dataChunk)
		if err != nil {
			return
		}
		if len(segment) == 0 {
			continue
		}
		if err := writeConnFull(fl.conn, segment); err != nil {
			_ = p.sendReset(fl, protocol.ErrorTargetUnreachable)
			return
		}
		fl.mu.Lock()
		if err := ring.Consume(len(segment)); err != nil {
			fl.mu.Unlock()
			_ = p.sendReset(fl, protocol.ErrorProtocol)
			return
		}
		fl.rxWritten += uint64(len(segment))
		delivered := fl.rxWritten
		finalReady := fl.finRecv && !fl.finAckConfirmed && delivered == fl.finRecvFinal
		closed := fl.closed
		fl.mu.Unlock()

		if closed {
			return
		}
		if finalReady {
			_ = p.ackRemoteFin(fl)
			return
		}
		for {
			epoch,owner,generation:=p.currentCarrierIdentity()
			err := p.grantReceive(fl)
			if err==nil{break}
			// FIN completion can race the credit refresh. A Flow that became
			// terminal while this goroutine was between delivery and WINDOW
			// must absorb that late credit intent instead of emitting RESET.
			fl.mu.Lock()
			closed = fl.closed
			finalReady = fl.finRecv && !fl.finAckConfirmed && fl.rxWritten == fl.finRecvFinal
			fl.mu.Unlock()
			if closed||errors.Is(err,errFlowClosed){return}
			if finalReady{_ = p.ackRemoteFin(fl);return}
			if p.recoveryEnabled {
				p.onCarrierFailureForGeneration(err,generation,SenderStopFrameProcessing)
				if werr:=p.waitForReplacement(ctx,epoch,owner,generation);werr==nil{continue}
			}
			_ = p.sendReset(fl, protocol.ErrorResourceExhausted)
			return
		}
	}
}

func (p *Peer) queueExactLiveFrame(ctx context.Context,fl *flow,fr protocol.Frame,producer DataProducerKind) {
	if fl==nil{return}
	fl.mu.Lock()
	if fl.closed{fl.mu.Unlock();return}
	if fl.deferredLiveWake==nil{fl.deferredLiveWake=make(chan struct{},1)}
	fl.deferredLive=append(fl.deferredLive,deferredLiveFrame{frame:fr,producer:producer})
	start:=!fl.deferredLiveRunning
	if start{fl.deferredLiveRunning=true}
	wake:=fl.deferredLiveWake
	fl.mu.Unlock()
	if start{
		if !p.admitWorker(){
			fl.mu.Lock()
			fl.deferredLiveRunning=false
			fl.mu.Unlock()
			return
		}
		go func(){defer p.wg.Done();p.runExactLiveDelivery(ctx,fl)}()
	}
	select{case wake<-struct{}{}:default:}
}

func (p *Peer) runExactLiveDelivery(ctx context.Context,fl *flow) {
	defer func(){fl.mu.Lock();fl.deferredLiveRunning=false;fl.mu.Unlock()}()
	for{
		fl.mu.Lock()
		if fl.closed{fl.mu.Unlock();return}
		if len(fl.deferredLive)==0{
			wake:=fl.deferredLiveWake
			fl.mu.Unlock()
			select{case <-ctx.Done():return;case <-wake:continue}
		}
		item:=fl.deferredLive[0]
		fl.mu.Unlock()

		for{
			sender,epoch,owner,generation:=p.currentSenderState()
			if sender==nil{return}
			// Exact-transaction live DATA may be committed while the recovery
			// carrier is active but not application-ready. Never send on epoch 1
			// or through a generation that has not crossed the readiness barrier.
			if epoch<=1{
				if err:=p.waitForCarrierSwitch(ctx,epoch,owner,generation);err!=nil{return}
				continue
			}
			if err:=p.waitForGenerationReady(ctx,generation);err!=nil{return}
			currentSender,currentEpoch,currentOwner,currentGeneration:=p.currentSenderState()
			if currentGeneration!=generation||currentEpoch!=epoch||currentOwner!=owner||currentSender!=sender{continue}
			var err error
			if item.frame.Type==protocol.TypeFin{
				// Written only after every DATA item queued before it.
				err=sender.sendControl(item.frame)
			}else{
				if p.recoveryEnabled{p.traceLiveDataAttempt(fl,sender,item.producer,item.frame.Offset,item.frame.Offset+uint64(len(item.frame.Payload)),generation)}
				err=sender.sendDataWithProducer(ctx,fl,item.frame,item.producer)
			}
			// A Flow that ended while this frame waited is not a carrier failure;
			// on a listener, reporting one leaves the carrier marked unusable
			// with nothing to replace it.
			if errors.Is(err,errFlowClosed){return}
			if err!=nil&&p.recoveryEnabled{
				p.onCarrierFailureForGeneration(err,generation,SenderStopFrameProcessing)
				if werr:=p.waitForCarrierSwitch(ctx,epoch,owner,generation);werr==nil{continue}
				return
			}
			if err!=nil{return}
			break
		}

		fl.mu.Lock()
		if len(fl.deferredLive)>0&&fl.deferredLive[0].frame.Offset==item.frame.Offset&&
			fl.deferredLive[0].frame.Offset+uint64(len(fl.deferredLive[0].frame.Payload))==item.frame.Offset+uint64(len(item.frame.Payload)){
			copy(fl.deferredLive,fl.deferredLive[1:])
			fl.deferredLive[len(fl.deferredLive)-1]=deferredLiveFrame{}
			fl.deferredLive=fl.deferredLive[:len(fl.deferredLive)-1]
		}
		fl.mu.Unlock()
	}
}

func (p *Peer) exactRecoveryOwnsLedgerRange(fl *flow,end uint64) bool {
	if p.recovery==nil||fl==nil||end==0{return false}
	a:=p.recovery
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.prepared==nil{return false}
	for i:=range a.prepared.flows{
		act:=&a.prepared.flows[i]
		if act.flow==fl&&end<=act.replayHighWatermark{return true}
	}
	return false
}

func (p *Peer) pumpLocal(ctx context.Context, fl *flow) {
	buf := make([]byte, dataChunk)
	for {
		capacity, err := fl.reserveReadCapacity(ctx, len(buf))
		if err != nil {
			return
		}
		n, readErr := fl.conn.Read(buf[:capacity])
		if n < capacity {
			_ = fl.allocator.Release(fl.resourceID, resources.Replay, int64(capacity-n))
		}
		if n > 0 {
			// Application ledger admission and recovery snapshot/freeze share one
			// authority boundary. Without this gate, BeginRecovery can start
			// between the exact-obligation check and commitSend: the bytes then
			// miss both the recovery snapshot and the exact replay high-watermark,
			// leaving FINALIZED/ReplayOutstanding permanently non-ready.
			if p.recoveryEnabled{p.recoveryGate.Lock()}
			exactObligation:=p.recoveryEnabled&&p.NeedsExactTransactionResolution()
			off, payload, err := fl.commitSend(buf[:n])
			if err == nil && exactObligation {
				p.extendExactReplayHighWatermark(fl,off+uint64(len(payload)))
			}
			if p.recoveryEnabled{p.recoveryGate.Unlock()}
			if err != nil {
				_ = fl.allocator.Release(fl.resourceID, resources.Replay, int64(n))
				return
			}
			// In recovery mode account application bytes exactly once when they
			// enter the session ledger. Carrier replay must never bill them again.
			if p.recoveryEnabled && p.trafficObserver != nil {
				p.trafficObserver(0,uint64(n))
			}
			if exactObligation {
				_,epoch,_,_:=p.currentSenderState()
				producer:=ProducerLivePump
				if epoch>1{producer=ProducerRecoveredPump}
				p.queueExactLiveFrame(ctx,fl,protocol.Frame{Type:protocol.TypeData,StreamID:fl.id,Offset:off,Payload:payload},producer)
			}else{
				var sendErr error
				frameEnd:=off+uint64(len(payload))
				for {
					// If recovery became authoritative after this DATA entered the
					// ledger, the exact replay plan already owns this byte range.
					// Do not keep the application pump blocked behind recovery
					// readiness or resend the same range through the old direct
					// path; hand ownership to replay and continue admitting later
					// application DATA into the exact high-watermark.
					if p.recoveryEnabled&&p.NeedsExactTransactionResolution()&&p.exactRecoveryOwnsLedgerRange(fl,frameEnd){
						sendErr=nil
						break
					}
					sender,epoch,owner,generation:=p.currentSenderState()
					producer:=ProducerLivePump
					if p.recoveryEnabled&&epoch>1{producer=ProducerRecoveredPump}
					// Carrier authority can change before replay/application readiness.
					// A surviving pump from the prior physical generation must never
					// bypass the recovery release barrier merely because its previous
					// write happened to return nil. Wait for this exact generation to
					// be application-ready, then re-read ownership before sending.
					if p.recoveryEnabled&&epoch>1 {
						if err:=p.waitForGenerationReady(ctx,generation);err!=nil{return}
						currentSender,currentEpoch,currentOwner,currentGeneration:=p.currentSenderState()
						if currentGeneration!=generation||currentEpoch!=epoch||currentOwner!=owner||currentSender!=sender{
							continue
						}
					}
					if p.recoveryEnabled{p.traceLiveDataAttempt(fl,sender,producer,off,off+uint64(len(payload)),generation)}
					sendErr=sender.sendDataWithProducer(ctx, fl, protocol.Frame{Type: protocol.TypeData, StreamID: fl.id, Offset: off, Payload: payload},producer)
					if errors.Is(sendErr,errFlowClosed){return}
					if sendErr!=nil&&p.recoveryEnabled {
						p.onCarrierFailureForGeneration(sendErr,generation,SenderStopFrameProcessing)
						// A failed direct write belongs to the retired physical carrier.
						// Wait only until carrier authority changes, then re-evaluate the
						// exact-transaction ownership at the top of this loop. Waiting for
						// application readiness here creates a liveness cycle: replay proof
						// gates readiness while this pump is prevented from admitting later
						// application bytes into the exact replay high-watermark.
						if werr:=p.waitForCarrierSwitch(ctx,epoch,owner,generation);werr==nil{
							continue
						}
					}
					break
				}
				if sendErr!=nil{return}
			}
			if !p.recoveryEnabled && p.trafficObserver != nil {
				p.trafficObserver(0,uint64(n))
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				exactObligation:=p.recoveryEnabled&&p.NeedsExactTransactionResolution()
				fl.mu.Lock()
				final := fl.txNext
				fl.finSent = true
				fl.mu.Unlock()
				if exactObligation{
					p.markExactTerminalObligation(fl,true,false,final)
					// The obligation keeps this FIN for a rebind, but nothing else
					// writes it on the live carrier. Queue it behind this Flow's
					// exact live DATA so it goes out in order once the current
					// generation is application-ready; withholding it left the
					// Flow half-closed, its slot leaked, and the transaction
					// unstable until another carrier failed.
					p.queueExactLiveFrame(ctx,fl,protocol.Frame{Type:protocol.TypeFin,StreamID:fl.id,Offset:final},ProducerRecoveredPump)
				}else{
					_ = p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeFin, StreamID: fl.id, Offset: final})
				}
			} else {
				// A reset application socket must end the Flow on both sides, the
				// same way pumpTarget handles a failed write; otherwise an idle peer
				// keeps the target connection and receive budget indefinitely.
				fl.mu.Lock()
				closed := fl.closed
				fl.mu.Unlock()
				if !closed {
					_ = p.sendReset(fl, protocol.ErrorTargetUnreachable)
				}
			}
			return
		}
	}
}

func (f *flow) reserveReadCapacity(ctx context.Context, max int) (int, error) {
	if max <= 0 {
		return 0, errors.New("read capacity must be positive")
	}
	for {
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			return 0, errFlowClosed
		}
		available := f.peerMax - f.txNext
		wait := f.creditWait
		f.mu.Unlock()
		if available == 0 {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-wait:
				continue
			}
		}
		want := uint64(max)
		if available < want {
			want = available
		}
		if err := f.allocator.ReserveContext(ctx, f.resourceID, resources.Replay, int64(want)); err != nil {
			return 0, err
		}
		f.mu.Lock()
		if f.closed {
			f.mu.Unlock()
			_ = f.allocator.Release(f.resourceID, resources.Replay, int64(want))
			return 0, errFlowClosed
		}
		f.mu.Unlock()
		return int(want), nil
	}
}

func (f *flow) commitSend(data []byte) (uint64, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, nil, errFlowClosed
	}
	n := uint64(len(data))
	if n > ^uint64(0)-f.txNext || f.txNext+n > f.peerMax {
		return 0, nil, errors.New("send exceeds peer credit")
	}
	off := f.txNext
	f.txNext += n
	payload := append([]byte(nil), data...)
	f.replay = append(f.replay, replayChunk{start: off, end: f.txNext, data: payload})
	return off, payload, nil
}

func (f *flow) onWindow(max uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.openOK {
		return errors.New("WINDOW before OPEN_OK")
	}
	// close() has already closed creditWait; a WINDOW still in flight for a
	// Flow that is closing must not close it again.
	if f.closed {
		return nil
	}
	if max < f.peerMax {
		return errors.New("WINDOW moved backwards")
	}
	if max == f.peerMax {
		return nil
	}
	f.peerMax = max
	close(f.creditWait)
	f.creditWait = make(chan struct{})
	return nil
}

func (f *flow) restoreRecoveryPeerCredit(max uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.openOK {
		return errors.New("recovery credit before OPEN_OK")
	}
	if f.closed {
		return nil
	}
	// Recovery snapshots are immutable transaction evidence, but the old
	// authoritative carrier can still deliver a newer WINDOW after that
	// snapshot and before authority commit. Never regress that newer proven
	// frontier; only advance when the recovered peer snapshot is higher.
	if max <= f.peerMax {
		return nil
	}
	f.peerMax = max
	close(f.creditWait)
	f.creditWait = make(chan struct{})
	return nil
}

func (f *flow) replayPressure() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.txAcked >= f.txNext {
		return 0
	}
	return f.txNext - f.txAcked
}

func (f *flow) onAck(ack uint64) error {
	f.mu.Lock()
	if !f.openOK {
		f.mu.Unlock()
		return errors.New("ACK before OPEN_OK")
	}
	if ack > f.txNext {
		f.mu.Unlock()
		return errors.New("ACK exceeds tx_next")
	}
	if ack <= f.txAcked {
		f.mu.Unlock()
		return nil
	}
	f.txAcked = ack
	if f.ackWait!=nil {
		close(f.ackWait)
	}
	f.ackWait=make(chan struct{})
	var released int64
	keep := 0
	for _, chunk := range f.replay {
		if chunk.end <= ack {
			released += int64(chunk.end - chunk.start)
			continue
		}
		f.replay[keep] = chunk
		keep++
	}
	for i := keep; i < len(f.replay); i++ {
		f.replay[i] = replayChunk{}
	}
	f.replay = f.replay[:keep]
	f.mu.Unlock()
	if released > 0 {
		return f.allocator.Release(f.resourceID, resources.Replay, released)
	}
	return nil
}

func (f *flow) acceptData(offset uint64, payload []byte) (uint64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.openOK {
		return 0, false, errors.New("DATA before OPEN_OK")
	}
	if uint64(len(payload)) > ^uint64(0)-offset {
		return 0, false, errors.New("DATA offset overflow")
	}
	end := offset + uint64(len(payload))
	if end > f.rxMax {
		return 0, false, errors.New("FLOW_CONTROL_ERROR")
	}
	if end-f.rxTurnStart >= uint64(f.receiveReserved) {
		// The peer kept a full window's worth of bytes coming: the window,
		// not the sender, may be what limits this Flow.
		f.rxCreditLimited = true
		f.rxTurnStart = end
	}
	if offset > f.rxNext {
		return 0, false, ErrRecoverableDataGap
	}
	if end <= f.rxNext {
		return f.rxNext, true, nil
	}
	skip := uint64(0)
	if offset < f.rxNext {
		skip = f.rxNext - offset
	}
	if f.rxRing == nil {
		return 0, false, errors.New("receive ring is not initialized")
	}
	if err := f.rxRing.Write(payload[skip:]); err != nil {
		return 0, false, fmt.Errorf("receive ring overflow: %w", err)
	}
	f.rxNext = end
	return f.rxNext, false, nil
}

func (f *flow) onFinAck(off uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.openOK {
		return errors.New("FIN_ACK before OPEN_OK")
	}
	if !f.finSent || off != f.txNext {
		return errors.New("invalid FIN_ACK")
	}
	f.finAcked = true
	return nil
}

func (f *flow) onFinAckConfirm(off uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.openOK{return errors.New("FIN_ACK_CONFIRM before OPEN_OK")}
	if !f.finRecv||!f.finAckSent||off!=f.finRecvFinal{return errors.New("invalid FIN_ACK_CONFIRM")}
	f.finAckConfirmed=true
	return nil
}

func (f *flow) close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	close(f.creditWait)
	deferredWake:=f.deferredLiveWake
	conn := f.conn
	// The local side has seen the whole inbound stream only if the peer's FIN
	// was passed on as CloseWrite. Otherwise the Flow is ending early (RESET,
	// failed OPEN, session teardown) and a plain close would hand the local
	// application a clean EOF for a truncated stream, so abort it with RST.
	abortive := !f.writeClosed
	allocator := f.allocator
	resourceID := f.resourceID
	ring := f.rxRing
	slots := f.slots
	f.slots = nil
	f.mu.Unlock()
	slots.Release()
	if deferredWake!=nil{select{case deferredWake<-struct{}{}:default:}}
	if ring != nil {
		ring.Close()
	}
	if conn != nil {
		if abortive {
			if l, ok := conn.(interface{ SetLinger(int) error }); ok {
				_ = l.SetLinger(0)
			}
		}
		_ = conn.Close()
	}
	if allocator != nil {
		allocator.ReleaseFlow(resourceID)
	}
}

func (p *Peer) getFlow(id uint64) (*flow, error) {
	p.mu.Lock()
	fl := p.flows[id]
	p.mu.Unlock()
	if fl == nil {
		return nil, errors.New("frame references unknown flow")
	}
	return fl, nil
}

func (p *Peer) getOpenFlow(id uint64) (*flow, error) {
	fl, err := p.getFlow(id)
	if err != nil {
		return nil, err
	}
	fl.mu.Lock()
	open := fl.openOK
	fl.mu.Unlock()
	if !open {
		return nil, errors.New("flow frame received before OPEN_OK")
	}
	return fl, nil
}

func (p *Peer) removeFlow(id uint64) {
	p.mu.Lock()
	_, existed := p.flows[id]
	delete(p.flows, id)
	if existed {
		if _, ok := p.closedFlows[id]; !ok {
			p.closedFlows[id] = struct{}{}
			p.closedOrder = append(p.closedOrder, id)
			if len(p.closedOrder) > maxClosedFlowTombstones {
				evict := p.closedOrder[0]
				p.closedOrder = p.closedOrder[1:]
				delete(p.closedFlows, evict)
				delete(p.finishedFins, evict)
				delete(p.finishedPeerFins, evict)
				delete(p.resetFlows, evict)
			}
		}
	}
	p.mu.Unlock()
	if s:=p.senderNow();s!=nil {
		s.removeFlow(id, errFlowClosed)
	}
}

func (p *Peer) isClosedFlow(id uint64) bool {
	p.mu.Lock()
	_, ok := p.closedFlows[id]
	p.mu.Unlock()
	return ok
}

func (p *Peer) closeAll() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	flows := make([]*flow, 0, len(p.flows))
	for _, fl := range p.flows {
		flows = append(flows, fl)
	}
	p.flows = map[uint64]*flow{}
	p.mu.Unlock()
	for _, fl := range flows {
		fl.close()
	}
}

func writeConnFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n < 0 || n > len(p) {
			return errors.New("invalid socket write count")
		}
		p = p[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}


func (p *Peer) pingLoop(ctx context.Context) {
	if err:=p.waitReady(ctx);err!=nil{return}
	send:=func() bool {
		var payload [8]byte
		binary.BigEndian.PutUint64(payload[:],uint64(time.Now().UnixNano()))
		if err:=p.senderNow().sendControl(protocol.Frame{Type:protocol.TypePing,Payload:payload[:]});err!=nil{return false}
		return true
	}
	if !send(){return}
	t:=time.NewTicker(p.pingInterval)
	defer t.Stop()
	for{
		select{
		case <-ctx.Done():return
		case <-t.C:
			if !send(){return}
		}
	}
}

func (p *Peer) handlePing(fr protocol.Frame) error {
	if len(fr.Payload)!=8{return errors.New("invalid PING payload")}
	payload:=append([]byte(nil),fr.Payload...)
	return p.senderNow().sendControl(protocol.Frame{Type:protocol.TypePong,Payload:payload})
}

func (p *Peer) handlePong(fr protocol.Frame) error {
	if len(fr.Payload)!=8{return errors.New("invalid PONG payload")}
	sent:=int64(binary.BigEndian.Uint64(fr.Payload))
	now:=time.Now().UnixNano()
	if sent<=0||sent>now{return errors.New("invalid PONG timestamp")}
	rtt:=time.Duration(now-sent)
	if rtt>time.Minute{return errors.New("implausible PONG RTT")}
	if p.latencyObserver!=nil{p.latencyObserver(rtt)}
	return nil
}

// resendLostReset answers a FIN or FIN_ACK for a Flow this side RESET by
// sending the RESET again. Those frames mean the peer is still finishing the
// Flow, so it has not seen the RESET: it was lost with a failed carrier, and
// recovery replays FIN state but not a RESET of a Flow that no longer exists
// here. Absorbing them silently left the peer holding the Flow forever. A
// duplicate RESET is harmless: the peer absorbs it for a closed Flow.
func (p *Peer) resendLostReset(id uint64) bool {
	p.mu.Lock()
	code, ok := p.resetFlows[id]
	p.mu.Unlock()
	if !ok {
		return false
	}
	payload, err := protocol.EncodeControl(protocol.Reset{Code: code})
	if err != nil {
		return true
	}
	if s := p.senderNow(); s != nil {
		_ = s.sendControl(protocol.Frame{Type: protocol.TypeReset, StreamID: id, Payload: payload})
	}
	return true
}

// reackFinishedFin answers a FIN for a Flow this side already finished. As
// with a late FIN_ACK below, recovery can finish this side's Flow from the
// peer's snapshot evidence while the FIN_ACK we wrote for the peer's FIN was
// lost with the failed carrier. The peer then replays its FIN and keeps its
// Flow open until a FIN_ACK arrives; absorbing the replay silently left it
// waiting forever. Only a graceful finish whose peer FIN ended at the same
// offset is acknowledged.
func (p *Peer) reackFinishedFin(id, off uint64) error {
	p.mu.Lock()
	final, ok := p.finishedPeerFins[id]
	p.mu.Unlock()
	if !ok || final != off {
		return nil
	}
	if s := p.senderNow(); s != nil {
		_ = s.sendControl(protocol.Frame{Type: protocol.TypeFinAck, StreamID: id, Offset: off})
	}
	return nil
}

// reconfirmFinishedFinAck answers a FIN_ACK for a Flow this side already
// finished. Recovery can finish a Flow from the peer's snapshot evidence that
// it sent FIN_ACK, without that frame ever arriving here; the peer then waits
// for a FIN_ACK_CONFIRM that nothing would send, keeping its Flow and its
// recovery transaction open. It is confirmed only for a graceful finish whose
// FIN ended at the acknowledged offset.
func (p *Peer) reconfirmFinishedFinAck(id, off uint64) error {
	p.mu.Lock()
	final, ok := p.finishedFins[id]
	p.mu.Unlock()
	if !ok || final != off {
		return nil
	}
	// Best effort, like the RESET for a late OPEN_OK: if this carrier is
	// failing, the next rebind sends the FIN_ACK again.
	if s := p.senderNow(); s != nil {
		_ = s.sendControl(protocol.Frame{Type: protocol.TypeFinAckConfirm, StreamID: id, Offset: off})
	}
	return nil
}
