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
	dataChunk                      = 32 * 1024
	maxClosedFlowTombstones         = 256
)

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
	TrafficObserver    TrafficObserver
	LatencyObserver    LatencyObserver
	PingInterval       time.Duration
	RecoveryEnabled    bool
	RecoveryRetention  time.Duration
	CarrierID          string
}

type Peer struct {
	role               Role
	carrier            Carrier
	writer             frameWriter
	peerID             string
	routes             *routes.Table
	dial               func(context.Context, string, string) (net.Conn, error)
	allocator          *resources.Allocator
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
	closedOrder        []uint64
	nextID             uint64
	closed             bool
	wg                 sync.WaitGroup
	trafficObserver    TrafficObserver
	latencyObserver    LatencyObserver
	pingInterval       time.Duration
	recoveryEnabled    bool
	recoveryRetention  time.Duration
	recovery           *RecoveryAdapter
	recoveryNeeded     chan error
	replacementMu      sync.Mutex
	replacementWait    chan struct{}
	carrierID          string
	carrierEpoch       uint64
	peerBootID         string
	runCtx             context.Context
	recoveryGate       sync.Mutex
}

type replayChunk struct {
	start uint64
	end   uint64
	data  []byte
}

type flow struct {
	id              uint64
	resourceID      uint64
	routeID         string
	nonce           string
	conn            net.Conn
	allocator       *resources.Allocator
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
	finRecvFinal    uint64
	finAckSent      bool
	writeClosed     bool
	replay          []replayChunk
	creditWait      chan struct{}
	finSent         bool
	finRecv         bool
	finAcked        bool
	closed          bool
	resetCode       protocol.ErrorCode
	localPumpRunning bool
	targetPumpRunning bool
	localPumpDone chan struct{}
	targetPumpDone chan struct{}
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
	bootID, err := randomHex128()
	if err != nil {
		return nil, err
	}
	p := &Peer{
		role: role, carrier: c, writer: frameWriter{w: c.Out}, peerID: peerID,
		routes: table, allocator: opts.Resources, flows: make(map[uint64]*flow),
		closedFlows: make(map[uint64]struct{}),
		nodeID: opts.NodeID, expectedPeerNodeID: opts.ExpectedPeerNodeID,
		bootID: bootID, shardID: opts.ShardID, profileID: opts.ProfileID,
		profileVersion: opts.ProfileVersion, configRevision: opts.ConfigRevision,
		epoch: "1", readyCh: make(chan struct{}), trafficObserver: opts.TrafficObserver,
		latencyObserver: opts.LatencyObserver, pingInterval: opts.PingInterval,
		recoveryEnabled: opts.RecoveryEnabled, recoveryRetention: opts.RecoveryRetention,
		recoveryNeeded: make(chan error,1), replacementWait: make(chan struct{}), carrierEpoch:1,
	}
	if role == Dialer {
		p.nextID = 1
		p.sessionID, err = randomHex128()
		if err != nil {
			return nil, err
		}
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

func (p *Peer) senderNow() *outboundSender {
	if !p.recoveryEnabled{return p.sender}
	p.mu.Lock()
	s:=p.sender
	p.mu.Unlock()
	return s
}

func (p *Peer) Run(ctx context.Context) error { return p.run(ctx,nil) }

func (p *Peer) RunWithFirstFrame(ctx context.Context, first protocol.Frame) error {
	return p.run(ctx,&first)
}

func (p *Peer) run(ctx context.Context, first *protocol.Frame) error {
	runCtx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.runCtx = runCtx
	p.mu.Unlock()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.senderNow().run(runCtx)
	}()
	defer func() {
		cancel()
		p.closeAll()
		p.wg.Wait()
	}()
	if p.role == Dialer {
		if err := p.sendHello(); err != nil { return err }
		if p.pingInterval > 0 {
			p.wg.Add(1)
			go func() { defer p.wg.Done(); p.pingLoop(runCtx) }()
		}
	}
	if first != nil {
		epoch,carrierID:=p.currentCarrierIdentity()
		if err:=p.handleFrameFrom(runCtx,epoch,carrierID,*first);err!=nil{return err}
	}
	for {
		carrier,epoch,carrierID:=p.currentCarrier()
		f, err := protocol.Decode(carrier.In)
		if err != nil {
			if ctx.Err() != nil { return ctx.Err() }
			if !p.recoveryEnabled {
				if errors.Is(err,io.EOF){return ctx.Err()}
				return err
			}
			p.onCarrierFailure(err)
			if err:=p.waitForReplacement(runCtx,epoch,carrierID);err!=nil{return err}
			continue
		}
		if err := p.handleFrameFrom(runCtx,epoch,carrierID,f); err != nil {
			if p.recoveryEnabled && errors.Is(err,ErrCarrierUnavailable) {
				p.onCarrierFailure(err)
				if werr:=p.waitForReplacement(runCtx,epoch,carrierID);werr!=nil{return werr}
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
	if p.recoveryEnabled{p.recoveryGate.Lock();defer p.recoveryGate.Unlock()}
	if p.recovery!=nil && p.recovery.IsFrozen(){ return recovery.ErrResumeFrozen }
	id, err := p.allocateStreamID()
	if err != nil {
		return err
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	fl := newFlow(id, routeID, hex.EncodeToString(nonceBytes), conn, p.allocator)
	p.mu.Lock()
	p.flows[id] = fl
	p.mu.Unlock()

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

func newFlow(id uint64, routeID, nonce string, conn net.Conn, alloc ...*resources.Allocator) *flow {
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
		allocator: a, openDone: make(chan error, 1), creditWait: make(chan struct{}),
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
	case protocol.TypePing:
		return p.handlePing(fr)
	case protocol.TypePong:
		return p.handlePong(fr)
	case protocol.TypeOpen:
		return p.handleOpen(ctx, fr)
	case protocol.TypeOpenOK:
		fl, err := p.getFlow(fr.StreamID)
		if err != nil {
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
		if err := fl.onAck(fr.Offset); err != nil {
			return err
		}
		p.senderNow().updatePressure(fl.id, fl.replayPressure())
		return nil
	case protocol.TypeData:
		fl, err := p.getOpenFlow(fr.StreamID)
		if err != nil {
			return err
		}
		return p.handleData(fl, fr)
	case protocol.TypeFin:
		fl, err := p.getOpenFlow(fr.StreamID)
		if err != nil {
			return err
		}
		return p.handleFin(fl, fr.Offset)
	case protocol.TypeFinAck:
		fl, err := p.getOpenFlow(fr.StreamID)
		if err != nil {
			if p.isClosedFlow(fr.StreamID) {
				return nil
			}
			return err
		}
		if err := fl.onFinAck(fr.Offset); err != nil {
			return err
		}
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
			MaxFramePayloadBytes: protocol.MaxPayloadSize, MaxFlowsPerShard: 64,
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
	if p.recovery!=nil && p.recovery.IsFrozen(){ return recovery.ErrResumeFrozen }
	req, err := protocol.DecodeOpen(fr.Payload)
	if err != nil {
		return err
	}
	p.mu.Lock()
	if existing := p.flows[fr.StreamID]; existing != nil {
		same := existing.routeID == req.RouteID && existing.nonce == req.OpenNonce
		openOK := existing.openOK
		p.mu.Unlock()
		if !same {
			return errors.New("duplicate stream_id with different OPEN identity")
		}
		if !openOK {
			return errors.New("duplicate OPEN before original completed")
		}
		return p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenOK, StreamID: fr.StreamID, Payload: []byte("{}")})
	}
	p.mu.Unlock()

	target, err := p.routes.Resolve(p.peerID, req.RouteID)
	if err != nil {
		payload, _ := protocol.EncodeControl(protocol.OpenError{Code: protocol.ErrorCode(err.Error())})
		return p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenErr, StreamID: fr.StreamID, Payload: payload})
	}
	conn, err := p.dial(ctx, "tcp", target)
	if err != nil {
		payload, _ := protocol.EncodeControl(protocol.OpenError{Code: targetDialErrorCode(err)})
		return p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenErr, StreamID: fr.StreamID, Payload: payload})
	}
	fl := newFlow(fr.StreamID, req.RouteID, req.OpenNonce, conn, p.allocator)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		fl.close()
		return errors.New("session closed")
	}
	if old := p.flows[fr.StreamID]; old != nil {
		p.mu.Unlock()
		fl.close()
		return errors.New("concurrent OPEN conflict")
	}
	p.flows[fr.StreamID] = fl
	p.mu.Unlock()

	window, err := p.reserveReceiveWindow(fl)
	if err != nil {
		fl.close()
		p.removeFlow(fr.StreamID)
		payload, _ := protocol.EncodeControl(protocol.OpenError{Code: protocol.ErrorResourceExhausted})
		return p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenErr, StreamID: fr.StreamID, Payload: payload})
	}
	fl.mu.Lock()
	fl.openOK = true
	fl.mu.Unlock()
	if err := p.senderNow().addFlow(fl.id); err != nil {
		fl.close()
		p.removeFlow(fr.StreamID)
		payload, _ := protocol.EncodeControl(protocol.OpenError{Code: protocol.ErrorResourceExhausted})
		return p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenErr, StreamID: fr.StreamID, Payload: payload})
	}
	if err := p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeOpenOK, StreamID: fr.StreamID, Payload: []byte("{}")}); err != nil {
		fl.close()
		p.removeFlow(fr.StreamID)
		return err
	}
	if err := p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeWindow, StreamID: fl.id, Offset: window}); err != nil {
		fl.close()
		p.removeFlow(fr.StreamID)
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
	return nil
}

func (p *Peer) reserveReceiveWindow(fl *flow) (uint64, error) {
	fl.mu.Lock()
	if fl.closed {
		fl.mu.Unlock()
		return 0, errors.New("flow closed")
	}
	need := int64(defaultWindow)
	if fl.receiveReserved >= need && fl.rxRing != nil {
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
		return 0, errors.New("flow closed")
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
	fl.mu.Lock()
	if finalOffset != fl.rxNext {
		fl.mu.Unlock()
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

	if ready {
		return p.ackRemoteFin(fl)
	}
	return nil
}

func (p *Peer) ackRemoteFin(fl *flow) error {
	fl.mu.Lock()
	if fl.finAckSent {
		fl.mu.Unlock()
		return nil
	}
	if !fl.finRecv || fl.rxWritten != fl.finRecvFinal {
		fl.mu.Unlock()
		return nil
	}
	final := fl.finRecvFinal
	needCloseWrite:=!fl.writeClosed
	if needCloseWrite{fl.writeClosed=true}
	fl.mu.Unlock()

	if needCloseWrite {
		if cw, ok := fl.conn.(interface{ CloseWrite() error }); ok {
			if err := cw.CloseWrite(); err != nil {
				return p.sendReset(fl, protocol.ErrorTargetUnreachable)
			}
	}
	}
	if err := p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeFinAck, StreamID: fl.id, Offset: final}); err != nil {
		return err
	}
	fl.mu.Lock()
	fl.finAckSent=true
	fl.mu.Unlock()
	p.finishIfComplete(fl)
	return nil
}

func (p *Peer) finishIfComplete(fl *flow) {
	fl.mu.Lock()
	done := fl.finAckSent && fl.finAcked
	fl.mu.Unlock()
	if !done {
		return
	}
	fl.close()
	p.removeFlow(fl.id)
}

func (p *Peer) startPump(ctx context.Context, fl *flow) {
	fl.mu.Lock()
	if fl.closed||fl.finSent||fl.localPumpRunning { fl.mu.Unlock(); return }
	done:=make(chan struct{})
	fl.localPumpRunning=true
	fl.localPumpDone=done
	fl.mu.Unlock()
	p.wg.Add(1)
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
	if fl.closed||fl.finAckSent||fl.targetPumpRunning { fl.mu.Unlock(); return }
	done:=make(chan struct{})
	fl.targetPumpRunning=true
	fl.targetPumpDone=done
	fl.mu.Unlock()
	p.wg.Add(1)
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
	if !localRunning { p.startPump(ctx,fl) } else if localDone!=nil {
		p.wg.Add(1);go func(){defer p.wg.Done();select{case <-ctx.Done():case <-localDone:p.startPump(ctx,fl)}}()
	}
	if !targetRunning { p.startTargetPump(ctx,fl) } else if targetDone!=nil {
		p.wg.Add(1);go func(){defer p.wg.Done();select{case <-ctx.Done():case <-targetDone:p.startTargetPump(ctx,fl)}}()
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
		finalReady := fl.finRecv && !fl.finAckSent && delivered == fl.finRecvFinal
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
			err := p.grantReceive(fl)
			if err==nil{break}
			// FIN completion can race the credit refresh. A Flow that became
			// terminal while this goroutine was between delivery and WINDOW
			// must absorb that late credit intent instead of emitting RESET.
			fl.mu.Lock()
			closed = fl.closed
			finalReady = fl.finRecv && !fl.finAckSent && fl.rxWritten == fl.finRecvFinal
			fl.mu.Unlock()
			if closed{return}
			if finalReady{_ = p.ackRemoteFin(fl);return}
			if p.recoveryEnabled {
				epoch,owner:=p.currentCarrierIdentity()
				p.onCarrierFailure(err)
				if werr:=p.waitForReplacement(ctx,epoch,owner);werr==nil{continue}
			}
			_ = p.sendReset(fl, protocol.ErrorResourceExhausted)
			return
		}
	}
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
			off, payload, err := fl.commitSend(buf[:n])
			if err != nil {
				_ = fl.allocator.Release(fl.resourceID, resources.Replay, int64(n))
				return
			}
			if err := p.senderNow().sendData(ctx, fl, protocol.Frame{Type: protocol.TypeData, StreamID: fl.id, Offset: off, Payload: payload}); err != nil {
				if p.recoveryEnabled {
					epoch,owner:=p.currentCarrierIdentity()
					p.onCarrierFailure(err)
					if werr:=p.waitForReplacement(ctx,epoch,owner);werr==nil{continue}
				}
				return
			}
			if p.trafficObserver != nil {
				p.trafficObserver(0,uint64(n))
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				fl.mu.Lock()
				final := fl.txNext
				fl.finSent = true
				fl.mu.Unlock()
				_ = p.senderNow().sendControl(protocol.Frame{Type: protocol.TypeFin, StreamID: fl.id, Offset: final})
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
			return 0, errors.New("flow closed")
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
			return 0, errors.New("flow closed")
		}
		f.mu.Unlock()
		return int(want), nil
	}
}

func (f *flow) commitSend(data []byte) (uint64, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, nil, errors.New("flow closed")
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
	if offset > f.rxNext {
		return 0, false, errors.New("DATA gap is not allowed")
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

func (f *flow) close() {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	close(f.creditWait)
	conn := f.conn
	allocator := f.allocator
	resourceID := f.resourceID
	ring := f.rxRing
	f.mu.Unlock()
	if ring != nil {
		ring.Close()
	}
	if conn != nil {
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
			}
		}
	}
	p.mu.Unlock()
	if s:=p.senderNow();s!=nil {
		s.removeFlow(id, errors.New("flow closed"))
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
