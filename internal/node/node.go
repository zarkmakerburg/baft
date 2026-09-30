package node

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	carrierh2 "github.com/zarkmakerburg/baft/internal/carrier/h2"
	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/identity"
	baftmetrics "github.com/zarkmakerburg/baft/internal/metrics"
	"github.com/zarkmakerburg/baft/internal/resources"
	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/routes"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
	"github.com/zarkmakerburg/baft/internal/session"
	"github.com/zarkmakerburg/baft/internal/telemetry"
)

type ListenerStartupState struct {
	GoroutineStarted bool
	BindAttempted bool
	Bound bool
	ServeStarted bool
	ConfiguredAddress string
	ActualAddress string
	StartupError error
}

type Runtime struct {
	Revocations *identity.RevocationSet
	Resources   *resources.Allocator
	peerMu      sync.Mutex
	peers       map[*session.Peer]struct{}
	ingressBytes    atomic.Uint64
	egressBytes     atomic.Uint64
	handshakeErrors atomic.Uint64
	noiseLatencyMS  atomic.Int64
	routeMu         sync.Mutex
	routeStats      map[string]telemetry.RouteSnapshot
	sessionMu       sync.Mutex
	sessions        map[string]*session.Peer
	recoverySeq     atomic.Uint64
	recoveryFaultMu sync.RWMutex
	recoveryFault   func(string) error
	recoveryControlMu sync.RWMutex
	recoveryControlHook func(string,*session.RecoveryControl) int
	bootID          string
	listenerReadyOnce sync.Once
	listenerReady chan struct{}
	listenerReadyMu sync.Mutex
	listenerReadyAddr string
	listenerReadyErr error
	listenerStartupMu sync.Mutex
	listenerStartup ListenerStartupState
	metricsTestMu sync.Mutex
	metricsListenerForTest net.Listener
	listenerProviderMu sync.Mutex
	listenerProviderForTest ListenerProvider
	dialerReadyOnce sync.Once
	dialerReady chan struct{}
	dialerReadyMu sync.Mutex
	dialerReadyErr error
}

type RecoveryAuthoritySnapshot struct {
	SessionID string
	Epoch uint64
	Owner string
	CarrierGeneration uint64
	PreparedIncarnation uint64
	PreparedID string
	SenderID string
	SenderStopped bool
	PostCommitFailures uint64
	RecoveryAttempts uint64
	RecoverySignalPending bool
	Frozen bool
	ActivationComplete bool
	ApplicationReady bool
	TransactionStable bool
	ReplayHighWatermark uint64
	ReplayPeerAccepted uint64
	ReplayOutstanding bool
	FinStable bool
	FinalizationStable bool
	TxnState session.RecoveryTxnState
	CandidateID string
	NextEpoch uint64
	PlanDigest string
	Flows []session.RecoveryFlowFrontier
}

func (r *Runtime) SetRecoveryFaultHookForTest(fn func(string) error) {
	r.recoveryFaultMu.Lock()
	r.recoveryFault=fn
	r.recoveryFaultMu.Unlock()
}

func (r *Runtime) SetRecoveryControlHookForTest(fn func(string,*session.RecoveryControl) int) {
	r.recoveryControlMu.Lock()
	r.recoveryControlHook=fn
	r.recoveryControlMu.Unlock()
}

func (r *Runtime) recoveryControlCopiesForTest(stage string,ctl *session.RecoveryControl) int {
	r.recoveryControlMu.RLock()
	fn:=r.recoveryControlHook
	r.recoveryControlMu.RUnlock()
	if fn==nil{return 1}
	n:=fn(stage,ctl)
	if n<1{return 1}
	if n>4{return 4}
	return n
}

func (r *Runtime) SetRecoveryFrameHookForTest(fn func(string,protocol.Frame) bool) {
	r.peerMu.Lock()
	peers:=make([]*session.Peer,0,len(r.peers))
	for p:=range r.peers{peers=append(peers,p)}
	r.peerMu.Unlock()
	for _,p:=range peers{p.SetRecoveryFrameHookForTest(fn)}
}

func (r *Runtime) SetRecoveryFinalizeOwnershipHookForTest(fn func(string,session.RecoveryPreparedOwnershipForTest)) {
	r.peerMu.Lock()
	peers:=make([]*session.Peer,0,len(r.peers))
	for p:=range r.peers{peers=append(peers,p)}
	r.peerMu.Unlock()
	for _,p:=range peers{p.SetRecoveryFinalizeOwnershipHookForTest(fn)}
}

func (r *Runtime) SetRecoveryDiagnosticHookForTest(fn func(session.RecoveryDiagnosticEvent)) {
	r.peerMu.Lock()
	peers:=make([]*session.Peer,0,len(r.peers))
	for p:=range r.peers{peers=append(peers,p)}
	r.peerMu.Unlock()
	for _,p:=range peers{p.SetRecoveryDiagnosticHookForTest(fn)}
}

func (r *Runtime) RecoveryDiagnosticsForTest() []session.RecoveryDiagnosticEvent {
	r.peerMu.Lock()
	peers:=make([]*session.Peer,0,len(r.peers))
	for p:=range r.peers{peers=append(peers,p)}
	r.peerMu.Unlock()
	var out []session.RecoveryDiagnosticEvent
	for _,p:=range peers{out=append(out,p.RecoveryDiagnosticsForTest()...)}
	sort.Slice(out,func(i,j int)bool{return out[i].Sequence<out[j].Sequence})
	return out
}

type recoveryTestCarrierReader struct{ctx context.Context}
func (r *recoveryTestCarrierReader) Read([]byte)(int,error){<-r.ctx.Done();return 0,r.ctx.Err()}
type recoveryTestCarrierWriter struct{}
func (*recoveryTestCarrierWriter) Write(p []byte)(int,error){return len(p),nil}

func (r *Runtime) RebindCurrentPreparedRecoveryForTest(ctx context.Context) (session.RecoveryPreparedOwnershipForTest,error) {
	r.peerMu.Lock()
	peers:=make([]*session.Peer,0,len(r.peers))
	for p:=range r.peers{peers=append(peers,p)}
	r.peerMu.Unlock()
	if len(peers)!=1{return session.RecoveryPreparedOwnershipForTest{},fmt.Errorf("expected exactly one recovery peer, got %d",len(peers))}
	p:=peers[0]
	ctl,ok:=p.RecoveryTransactionIdentity()
	if !ok{return session.RecoveryPreparedOwnershipForTest{},errors.New("recovery transaction unavailable")}
	in:=&recoveryTestCarrierReader{ctx:ctx}
	out:=&recoveryTestCarrierWriter{}
	if err:=p.RebindPreparedRecovery(ctx,ctl,session.Carrier{In:in,Out:out});err!=nil{return session.RecoveryPreparedOwnershipForTest{},err}
	return p.RecoveryPreparedOwnershipForTest(),nil
}


func (r *Runtime) FinalizeCurrentPreparedRecoveryForTest(ctx context.Context) (session.RecoveryCarrierOwner,error) {
	r.peerMu.Lock()
	peers:=make([]*session.Peer,0,len(r.peers))
	for p:=range r.peers{peers=append(peers,p)}
	r.peerMu.Unlock()
	if len(peers)!=1{return session.RecoveryCarrierOwner{},fmt.Errorf("expected exactly one recovery peer, got %d",len(peers))}
	p:=peers[0]
	ctl,ok:=p.RecoveryTransactionIdentity()
	if !ok{return session.RecoveryCarrierOwner{},errors.New("recovery transaction unavailable")}
	ctl.Phase=session.RecoveryPhaseFinalize
	generation,err:=p.FinalizeRecoveryCommitWithGeneration(ctx,ctl)
	if err!=nil{return session.RecoveryCarrierOwner{},err}
	owner,ok:=p.RecoveryCarrierOwnerForGeneration(ctl,generation)
	if !ok{return session.RecoveryCarrierOwner{},session.ErrStaleRecoveryIncarnation}
	return owner,nil
}

func (r *Runtime) RecoveryCurrentCarrierOwnerForTest() (session.RecoveryCarrierOwner,bool,error) {
	r.peerMu.Lock()
	peers:=make([]*session.Peer,0,len(r.peers))
	for p:=range r.peers{peers=append(peers,p)}
	r.peerMu.Unlock()
	if len(peers)!=1{return session.RecoveryCarrierOwner{},false,fmt.Errorf("expected exactly one recovery peer, got %d",len(peers))}
	owner,ok:=peers[0].RecoveryCurrentCarrierOwnerForTest()
	return owner,ok,nil
}


func (r *Runtime) RecoveryPreparedOwnershipForTest() (session.RecoveryPreparedOwnershipForTest,error) {
	r.peerMu.Lock()
	peers:=make([]*session.Peer,0,len(r.peers))
	for p:=range r.peers{peers=append(peers,p)}
	r.peerMu.Unlock()
	if len(peers)!=1{return session.RecoveryPreparedOwnershipForTest{},fmt.Errorf("expected exactly one recovery peer, got %d",len(peers))}
	return peers[0].RecoveryPreparedOwnershipForTest(),nil
}

func (r *Runtime) StaleFinalizeFailureForTest(old session.RecoveryPreparedOwnershipForTest,err error) error {
	r.peerMu.Lock()
	peers:=make([]*session.Peer,0,len(r.peers))
	for p:=range r.peers{peers=append(peers,p)}
	r.peerMu.Unlock()
	if len(peers)!=1{return fmt.Errorf("expected exactly one recovery peer, got %d",len(peers))}
	return peers[0].StaleFinalizeFailureForTest(old,err)
}

func (r *Runtime) FenceRecoveryCarrierOwnerForTest(owner session.RecoveryCarrierOwner) (bool,error) {
	r.peerMu.Lock()
	peers:=make([]*session.Peer,0,len(r.peers))
	for p:=range r.peers{peers=append(peers,p)}
	r.peerMu.Unlock()
	if len(peers)!=1{return false,fmt.Errorf("expected exactly one recovery peer, got %d",len(peers))}
	return peers[0].FenceRecoveryCarrierOwner(owner),nil
}

func (r *Runtime) SetRecoveryPostCommitFaultForTest(fn func(string) error) {
	r.peerMu.Lock()
	peers:=make([]*session.Peer,0,len(r.peers))
	for p:=range r.peers{peers=append(peers,p)}
	r.peerMu.Unlock()
	for _,p:=range peers{p.SetRecoveryPostCommitFaultForTest(fn)}
}

func (r *Runtime) EnsureRecoverySignalForTest(err error) error {
	r.peerMu.Lock()
	peers:=make([]*session.Peer,0,len(r.peers))
	for p:=range r.peers{peers=append(peers,p)}
	r.peerMu.Unlock()
	if len(peers)!=1{return fmt.Errorf("expected exactly one recovery peer, got %d",len(peers))}
	peers[0].EnsureRecoverySignal(err)
	return nil
}

func (r *Runtime) BeginRecoveryForTest(candidate string) error {
	r.peerMu.Lock()
	peers:=make([]*session.Peer,0,len(r.peers))
	for p:=range r.peers{peers=append(peers,p)}
	r.peerMu.Unlock()
	if len(peers)!=1{return fmt.Errorf("expected exactly one recovery peer, got %d",len(peers))}
	_,err:=peers[0].BeginRecovery(candidate)
	return err
}

func (r *Runtime) RecoveryAuthoritiesForTest() []RecoveryAuthoritySnapshot {
	r.peerMu.Lock()
	peers:=make([]*session.Peer,0,len(r.peers))
	for p:=range r.peers{peers=append(peers,p)}
	r.peerMu.Unlock()
	out:=make([]RecoveryAuthoritySnapshot,0,len(peers))
	for _,p:=range peers{
		tx,ok:=p.RecoveryTransactionIdentity()
		prep:=p.RecoveryPreparedOwnershipForTest()
		stats:=p.RecoveryStats()
		st:=p.RecoveryStability()
		s:=RecoveryAuthoritySnapshot{SessionID:p.SessionID(),Epoch:p.RecoveryEpoch(),Owner:p.RecoveryOwner(),CarrierGeneration:p.RecoveryCarrierGeneration(),PreparedIncarnation:prep.PreparedIncarnation,PreparedID:prep.PreparedID,SenderID:prep.SenderID,SenderStopped:prep.SenderStopped,PostCommitFailures:stats.PostCommitFailures,RecoveryAttempts:stats.Attempts,RecoverySignalPending:p.RecoverySignalPendingForTest(),Frozen:p.RecoveryFrozen(),ActivationComplete:p.RecoveryActivationComplete(),ApplicationReady:st.ApplicationReady,TransactionStable:st.TransactionStable,ReplayHighWatermark:st.ReplayHighWatermark,ReplayPeerAccepted:st.ReplayPeerAccepted,ReplayOutstanding:st.ReplayOutstanding,FinStable:st.FinStable,FinalizationStable:st.FinalizationStable,TxnState:p.RecoveryTransactionState(),Flows:p.RecoveryFlowFrontiersForTest()}
		if ok{s.CandidateID=tx.CandidateID;s.NextEpoch=tx.NextEpoch;s.PlanDigest=tx.PlanDigest}
		out=append(out,s)
	}
	return out
}

func NewRuntime() *Runtime {
	r:=&Runtime{
		Revocations: identity.NewRevocationSet(),
		peers: map[*session.Peer]struct{}{},
		routeStats: map[string]telemetry.RouteSnapshot{},
		sessions: map[string]*session.Peer{},
		listenerReady: make(chan struct{}),
		dialerReady: make(chan struct{}),
	}
	r.noiseLatencyMS.Store(-1)
	return r
}

func (r *Runtime) signalListenerReady(addr string,err error) {
	r.listenerReadyMu.Lock()
	if r.listenerReadyAddr==""&&addr!=""{r.listenerReadyAddr=addr}
	if r.listenerReadyErr==nil&&err!=nil{r.listenerReadyErr=err}
	r.listenerReadyMu.Unlock()
	r.listenerStartupMu.Lock()
	if addr!="" { r.listenerStartup.ActualAddress=addr }
	if err!=nil && r.listenerStartup.StartupError==nil { r.listenerStartup.StartupError=err }
	r.listenerStartupMu.Unlock()
	r.listenerReadyOnce.Do(func(){close(r.listenerReady)})
}

func (r *Runtime) setListenerStartup(configured string, mutate func(*ListenerStartupState)) {
	r.listenerStartupMu.Lock()
	if r.listenerStartup.ConfiguredAddress=="" { r.listenerStartup.ConfiguredAddress=configured }
	mutate(&r.listenerStartup)
	r.listenerStartupMu.Unlock()
}

func (r *Runtime) ListenerReadyForTest() <-chan struct{} { return r.listenerReady }

func (r *Runtime) ListenerReadinessForTest() (string,error) {
	r.listenerReadyMu.Lock();defer r.listenerReadyMu.Unlock()
	return r.listenerReadyAddr,r.listenerReadyErr
}

func (r *Runtime) signalDialerReady(err error) {
	r.dialerReadyMu.Lock()
	if r.dialerReadyErr==nil&&err!=nil{r.dialerReadyErr=err}
	r.dialerReadyMu.Unlock()
	r.dialerReadyOnce.Do(func(){close(r.dialerReady)})
}

func (r *Runtime) DialerReady() <-chan struct{} { return r.dialerReady }

func (r *Runtime) DialerReadiness() error {
	r.dialerReadyMu.Lock();defer r.dialerReadyMu.Unlock()
	return r.dialerReadyErr
}

func (r *Runtime) DialerReadyForTest() <-chan struct{} { return r.DialerReady() }
func (r *Runtime) DialerReadinessForTest() error { return r.DialerReadiness() }

func (r *Runtime) ListenerStartupStateForTest() ListenerStartupState {
	r.listenerStartupMu.Lock();defer r.listenerStartupMu.Unlock()
	return r.listenerStartup
}

func (r *Runtime) SetMetricsListenerForTest(ln net.Listener) {
	r.metricsTestMu.Lock()
	r.metricsListenerForTest=ln
	r.metricsTestMu.Unlock()
}

func (r *Runtime) takeMetricsListenerForTest(addr string) (net.Listener,error) {
	r.metricsTestMu.Lock()
	ln:=r.metricsListenerForTest
	r.metricsListenerForTest=nil
	r.metricsTestMu.Unlock()
	if ln==nil{return nil,nil}
	if ln.Addr().String()!=addr{
		_ = ln.Close()
		return nil,fmt.Errorf("test metrics listener address mismatch got=%s want=%s",ln.Addr().String(),addr)
	}
	return ln,nil
}

func (r *Runtime) Run(ctx context.Context, cfg config.Config) (retErr error) {
	// Listener readiness is a startup lifecycle event, not TCP polling. Any
	// failure before runListener reaches Serve must wake readiness waiters with
	// the real startup error instead of leaving them to infer a timeout.
	if cfg.Node.Role=="listener" {
		defer func(){
			if retErr!=nil { r.signalListenerReady("",retErr) }
		}()
	}
	if cfg.Node.Role=="dialer" {
		defer func(){
			if retErr!=nil { r.signalDialerReady(retErr) }
		}()
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}
	if r.bootID=="" {
		var b [16]byte
		if _,err:=rand.Read(b[:]);err!=nil{return fmt.Errorf("runtime boot id: %w",err)}
		r.bootID=hex.EncodeToString(b[:])
	}
	if err := requirePrivateKeyPermissions(cfg.TLS.KeyFile); err != nil {
		return err
	}
	if r.Resources == nil {
		a, err := allocatorFromConfig(cfg)
		if err != nil {
			return err
		}
		r.Resources = a
	}
	runCtx, cancel := context.WithCancel(ctx)
	var backgroundWG sync.WaitGroup
	// Runtime must not return while telemetry/probe writers can still mutate
	// persistent state. This makes shutdown a deterministic lifecycle boundary
	// for the durable spool and its atomic temp files.
	defer func(){
		cancel()
		backgroundWG.Wait()
	}()

	if cfg.Telemetry.Enabled {
		tokenEnv := cfg.Telemetry.AgentTokenEnv
		if tokenEnv == "" { tokenEnv = "BAFT_AGENT_TOKEN" }
		token := os.Getenv(tokenEnv)
		if token == "" { return fmt.Errorf("telemetry agent token environment %s is empty", tokenEnv) }
		interval := 60 * time.Second
		if cfg.Telemetry.IntervalSeconds > 0 { interval = time.Duration(cfg.Telemetry.IntervalSeconds) * time.Second }
		spoolPath:=strings.TrimSpace(cfg.Telemetry.SpoolPath)
		if spoolPath==""{spoolPath=cfg.Management.UnixSocket+".telemetry-spool.json"}
		queueLimit:=cfg.Telemetry.SpoolMaxPending
		if queueLimit<=0{queueLimit=telemetry.DefaultQueueLimit}
		exp, err := telemetry.NewPersistent(cfg.Node.ID, cfg.Telemetry.BCCURL, token, spoolPath, interval, queueLimit, r.telemetrySnapshot)
		if err != nil { return fmt.Errorf("telemetry spool: %w",err) }
		backgroundWG.Add(1)
		go func(){defer backgroundWG.Done();exp.Run(runCtx)}()
		probeInterval := 10 * time.Second
		if cfg.Telemetry.RouteProbeIntervalSeconds > 0 {
			probeInterval = time.Duration(cfg.Telemetry.RouteProbeIntervalSeconds) * time.Second
		}
		backgroundWG.Add(1)
		go func(){defer backgroundWG.Done();r.routeHealthLoop(runCtx, cfg, probeInterval, exp)}()
	}

	metricsDone, stopMetrics, err := r.startMetrics(runCtx, cfg.Management.MetricsListen)
	if err != nil {
		return err
	}
	defer stopMetrics()

	roleDone := make(chan error, 1)
	go func() {
		switch cfg.Node.Role {
		case "listener":
			err:=r.runListener(runCtx,cfg)
			if err!=nil{r.signalListenerReady("",err)}
			roleDone<-err
		case "dialer":
			roleDone <- r.runDialer(runCtx, cfg)
		default:
			roleDone <- fmt.Errorf("unsupported node role %q", cfg.Node.Role)
		}
	}()

	select {
	case err := <-roleDone:
		cancel()
		return err
	case err := <-metricsDone:
		cancel()
		if err == nil {
			err = errors.New("metrics server stopped unexpectedly")
		}
		return fmt.Errorf("metrics: %w", err)
	case <-ctx.Done():
		cancel()
		<-roleDone
		return nil
	}
}

func allocatorFromConfig(cfg config.Config) (*resources.Allocator, error) {
	total := int64(cfg.Limits.DataMemoryMiB) * resources.MiB
	receive := total / 2
	replay := total - receive
	return resources.NewAllocator(resources.Limits{
		Total:          total,
		Receive:        receive,
		Replay:         replay,
		PerFlowReceive: int64(cfg.Limits.ReceiveMaxMiB) * resources.MiB,
		PerFlowReplay:  int64(cfg.Limits.ReplayMaxMiB) * resources.MiB,
	})
}

func requirePrivateKeyPermissions(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("private key: %w", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("private key must not be readable or writable by group/other")
	}
	return nil
}

func loadTLSMaterial(c config.TLS) (*identityMaterial, error) {
	ca, err := identity.LoadCertPool(c.CAFile)
	if err != nil {
		return nil, fmt.Errorf("load CA: %w", err)
	}
	cert, err := identity.LoadKeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load certificate/key: %w", err)
	}
	return &identityMaterial{ca: ca, cert: cert}, nil
}

type identityMaterial struct {
	ca   *x509.CertPool
	cert tls.Certificate
}

func (r *Runtime) runListener(ctx context.Context, cfg config.Config) error {
	r.setListenerStartup(cfg.Server.Listen,func(s *ListenerStartupState){s.GoroutineStarted=true})
	log.Printf("baft listener startup: goroutine created configured=%s",cfg.Server.Listen)
	mat, err := loadTLSMaterial(cfg.TLS)
	if err != nil {
		return err
	}
	allowed := make(map[string]struct{}, len(cfg.Server.AllowedPeerIdentities))
	for _, id := range cfg.Server.AllowedPeerIdentities {
		allowed[id] = struct{}{}
	}
	tlsCfg, err := identity.ServerTLS(mat.ca, mat.cert, allowed)
	if err != nil {
		return err
	}

	var defs []routes.Route
	for _, cr := range cfg.Routes {
		if cr.Direction != "inbound" {
			continue
		}
		peers := make(map[string]struct{}, len(cr.AllowedPeers))
		for _, id := range cr.AllowedPeers {
			peers[id] = struct{}{}
		}
		defs = append(defs, routes.Route{ID: cr.ID, Target: cr.Target, AllowedPeers: peers})
	}
	if len(defs) == 0 {
		return errors.New("listener requires at least one inbound route")
	}
	table, err := routes.New(defs)
	if err != nil {
		return err
	}

	stream := func(hctx context.Context, in io.Reader, out io.Writer, peer carrierh2.PeerInfo) error {
		expected, err := nodeIDFromIdentity(peer.Identity)
		if err != nil { return err }
		if cfg.Recovery.Enabled {
			first,err:=protocol.Decode(in);if err!=nil{return err}
			if handled,err:=r.handleIncomingRecovery(hctx,cfg,in,out,peer,first);handled{
				if err!=nil{log.Printf("baft recovery candidate rejected: %v",err)}
				return err
			}
			if first.Type!=protocol.TypeHello{return errors.New("new carrier must begin with HELLO or RESUME_STATE")}
			h,err:=protocol.DecodeHello(first.Payload);if err!=nil{return err}
			owner:=fmt.Sprintf("shard-%d-carrier-1",h.ShardID)
			p,err:=session.New(session.Listener,session.Carrier{In:in,Out:out},peer.Identity,table,session.Options{
				NodeID:cfg.Node.ID,ExpectedPeerNodeID:expected,
				ProfileID:cfg.Transport.Profile,ProfileVersion:1,ConfigRevision:"config-v1",
				Resources:r.Resources,RecoveryEnabled:true,RecoveryRetention:recoveryRetention(cfg),CarrierID:owner,BootID:r.bootID,
				TrafficObserver:func(in,out uint64){r.ingressBytes.Add(in);r.egressBytes.Add(out)},
			})
			if err!=nil{return err}
			// The listener learns SessionID from the authenticated first HELLO.
			if h.SessionID==""{return errors.New("empty session id")}
			r.sessionMu.Lock()
			if old:=r.sessions[h.SessionID];old!=nil&&old!=p{r.sessionMu.Unlock();return errors.New("duplicate live session id")}
			r.sessions[h.SessionID]=p
			r.sessionMu.Unlock()
			r.registerPeer(p)
			defer func(){r.unregisterPeer(p);r.unregisterSession(p)}()
			return p.RunWithFirstFrame(ctx,first)
		}
		p, err := session.New(session.Listener, session.Carrier{In: in, Out: out}, peer.Identity, table, session.Options{
			NodeID: cfg.Node.ID, ExpectedPeerNodeID: expected,
			ProfileID: cfg.Transport.Profile, ProfileVersion: 1, ConfigRevision: "config-v1",
			Resources: r.Resources,
			TrafficObserver: func(in,out uint64){ r.ingressBytes.Add(in); r.egressBytes.Add(out) },
		})
		if err != nil { return err }
		r.registerPeer(p)
		defer r.unregisterPeer(p)
		return p.Run(hctx)
	}
	var handler http.Handler
	if cfg.Noise != nil {
		nc, allowedPeers, legacyIdentity, err := noiseListenerOptions(cfg)
		if err != nil {
			return err
		}
		cover, err := coverHandler(cfg.Noise.CoverHTMLFile)
		if err != nil {
			return err
		}
		handler, err = carrierh2.HandlerWithNoise(stream, carrierh2.NoiseOptions{
			Handshake: nc, PeerIdentity: legacyIdentity, AllowedPeers: allowedPeers,
			Cover: cover, Revocations: r.Revocations,
			OnHandshakeError: func(){ r.handshakeErrors.Add(1) },
		})
		if err != nil {
			return err
		}
		// Public website TLS; Noise authenticates the pinned peer before session.New.
		tlsCfg.ClientAuth = tls.NoClientCert
		tlsCfg.ClientCAs = nil
		tlsCfg.VerifyConnection = nil
		tlsCfg.NextProtos = []string{"h2", "http/1.1"}
	} else {
		handler = carrierh2.HandlerWithRevocation(stream, r.Revocations)
	}

	srv := &http.Server{Handler: handler, TLSConfig: tlsCfg, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	r.setListenerStartup(cfg.Server.Listen,func(s *ListenerStartupState){s.BindAttempted=true})
	log.Printf("baft listener startup: acquire configured=%s",cfg.Server.Listen)
	ln, err := r.takeEndpointListenerForTest(EndpointServer, cfg.Node.ID, cfg.Server.Listen)
	if err==nil && ln==nil { ln,err=net.Listen("tcp",cfg.Server.Listen) }
	if err != nil {
		r.setListenerStartup(cfg.Server.Listen,func(s *ListenerStartupState){s.StartupError=fmt.Errorf("listen %s: %w",cfg.Server.Listen,err)})
		log.Printf("baft listener startup: bind failed configured=%s err=%v",cfg.Server.Listen,err)
		return fmt.Errorf("listen %s: %w", cfg.Server.Listen, err)
	}
	actual:=ln.Addr().String()
	r.setListenerStartup(cfg.Server.Listen,func(s *ListenerStartupState){s.Bound=true;s.ActualAddress=actual})
	log.Printf("baft listener startup: bind success configured=%s actual=%s",cfg.Server.Listen,actual)
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		r.setListenerStartup(cfg.Server.Listen,func(s *ListenerStartupState){s.ServeStarted=true})
		log.Printf("baft listener startup: serve started actual=%s",actual)
		err := srv.ServeTLS(ln, "", "")
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		if err!=nil{log.Printf("baft listener startup: serve stopped actual=%s err=%v context=%v",actual,err,ctx.Err())}
		done <- err
	}()
	r.signalListenerReady(actual,nil)
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-done:
		return err
	}
}

type dialerShard struct {
	mu     sync.Mutex
	peer   *session.Peer
	client *carrierh2.Client
	pw     *io.PipeWriter
	body   io.ReadCloser
}

func (s *dialerShard) close() {
	s.mu.Lock()
	client,pw,body:=s.client,s.pw,s.body
	s.client=nil;s.pw=nil;s.body=nil
	s.mu.Unlock()
	if pw!=nil{_ = pw.Close()}
	if body!=nil{_ = body.Close()}
	if client!=nil{client.CloseIdleConnections()}
}

func (s *dialerShard) replaceCarrier(o *openedRuntimeCarrier) {
	s.mu.Lock()
	oldClient,oldPW,oldBody:=s.client,s.pw,s.body
	s.client=o.client;s.pw=o.pw;s.body=o.body
	s.mu.Unlock()
	if oldPW!=nil{_ = oldPW.Close()}
	if oldBody!=nil{_ = oldBody.Close()}
	if oldClient!=nil{oldClient.CloseIdleConnections()}
}

func (r *Runtime) runDialer(ctx context.Context, cfg config.Config) error {
	mat, err := loadTLSMaterial(cfg.TLS)
	if err != nil {
		return err
	}
	tlsCfg, err := identity.ClientTLS(mat.ca, mat.cert, cfg.Peer.ServerName)
	if err != nil {
		return err
	}
	if cfg.Noise != nil {
		tlsCfg.Certificates = nil
	}
	expectedPeerNode, err := nodeIDFromIdentity(cfg.Peer.AllowedIdentity)
	if err != nil {
		return err
	}

	shards := make([]*dialerShard, 0, cfg.Transport.Shards)
	runErr := make(chan error, cfg.Transport.Shards+len(cfg.Routes)+1)
	for i := 0; i < cfg.Transport.Shards; i++ {
		client, err := carrierh2.NewClient("https://"+cfg.Peer.Address, tlsCfg)
		if err != nil {
			r.closeShards(shards)
			return err
		}
		var pw *io.PipeWriter
		var resp *http.Response
		var secure *securityinternal.Conn
		if cfg.Noise != nil {
			nc, e := noiseConfig(cfg)
			if e != nil {
				r.closeShards(shards)
				client.CloseIdleConnections()
				return e
			}
			resp, pw, secure, err = client.OpenNoise(ctx, nc)
			if err != nil { r.handshakeErrors.Add(1) }
		} else {
			pr, writer := io.Pipe()
			pw = writer
			resp, err = client.Open(ctx, pr)
		}
		if err != nil {
			if pw != nil {
				_ = pw.Close()
			}
			client.CloseIdleConnections()
			r.closeShards(shards)
			return fmt.Errorf("open shard %d: %w", i, err)
		}
		carrier := session.Carrier{In: resp.Body, Out: pw}
		if secure != nil {
			carrier = session.Carrier{In: secure, Out: secure}
		}
		p, err := session.New(session.Dialer, carrier, cfg.Peer.AllowedIdentity, nil, session.Options{
			NodeID: cfg.Node.ID, ExpectedPeerNodeID: expectedPeerNode, ShardID: uint8(i),
			ProfileID: cfg.Transport.Profile, ProfileVersion: 1, ConfigRevision: "config-v1",
			Resources: r.Resources,
			TrafficObserver: func(in,out uint64){ r.ingressBytes.Add(in); r.egressBytes.Add(out) },
			LatencyObserver: func(rtt time.Duration){ r.noiseLatencyMS.Store(rtt.Milliseconds()) },
			PingInterval: func() time.Duration { if cfg.Noise!=nil { return 5*time.Second }; return 0 }(),
			RecoveryEnabled: cfg.Recovery.Enabled, RecoveryRetention: recoveryRetention(cfg),
			CarrierID: fmt.Sprintf("shard-%d-carrier-1",i), BootID:r.bootID,
		})
		if err != nil {
			_ = pw.Close()
			_ = resp.Body.Close()
			client.CloseIdleConnections()
			r.closeShards(shards)
			return err
		}
		sh := &dialerShard{peer: p, client: client, pw: pw, body: resp.Body}
		r.registerPeer(p)
		if cfg.Recovery.Enabled {
			if err:=r.registerSession(p);err!=nil{r.unregisterPeer(p);sh.close();r.closeShards(shards);return err}
		}
		shards = append(shards, sh)
		go func(index int, sh *dialerShard) {
			err := sh.peer.Run(ctx)
			if ctx.Err() == nil && err != nil {
				runErr <- fmt.Errorf("shard %d: %w", index, err)
			}
		}(i, sh)
		if cfg.Recovery.Enabled {
			go func(index int,sh *dialerShard){
				for{
					select{
					case <-ctx.Done():return
					case <-sh.peer.RecoveryNeeded():
						// A signal starts recovery work, but unresolved exact-transaction
						// obligations are level-triggered from state, not edge-triggered
						// by the channel. This prevents a coalesced/stale wake from
						// stranding FINALIZED-but-activation-incomplete replay.
						for sh.peer.NeedsRecovery()&&ctx.Err()==nil {
							err:=r.recoverDialerShard(ctx,cfg,tlsCfg,index,sh)
							if err==nil {
								sh.peer.DrainRecoverySignals()
								break
							}
							log.Printf("baft shard %d recovery attempt failed: %v",index,err)

							retry:=false
							if errors.Is(err,session.ErrPostCommitFailure)||errors.Is(err,session.ErrCommitUncertain)||sh.peer.NeedsExactTransactionResolution(){
								retry=sh.peer.NeedsRecovery()
							} else if errors.Is(err,errRecoverySnapshotTransient)&&sh.peer.NeedsRecovery(){
								retry=true
							}
							if !retry {
								sh.peer.DrainRecoverySignals()
								break
							}
							// Drain any coalesced wake; state is the source of truth for
							// the immediate retry. A short bounded backoff avoids a hot
							// loop if the authenticated peer is temporarily unavailable.
							sh.peer.DrainRecoverySignals()
							timer:=time.NewTimer(10*time.Millisecond)
							select{
							case <-ctx.Done():
								if !timer.Stop(){<-timer.C}
								return
							case <-timer.C:
							}
						}
					}
				}
			}(i,sh)
		}
	}
	defer r.closeShards(shards)

	var listeners []net.Listener
	defer func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}()

	var rr atomic.Uint64
	var wg sync.WaitGroup
	for _, cr := range cfg.Routes {
		if cr.Direction != "outbound" {
			continue
		}
		ln, err := r.takeEndpointListenerForTest(EndpointRoute, cr.ID, cr.Listen)
		if err==nil && ln==nil { ln,err=net.Listen("tcp",cr.Listen) }
		if err != nil {
			return fmt.Errorf("route %s listen: %w", cr.ID, err)
		}
		listeners = append(listeners, ln)
		route := cr
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				conn, err := ln.Accept()
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					runErr <- fmt.Errorf("route %s accept: %w", route.ID, err)
					return
				}
				idx := int((rr.Add(1) - 1) % uint64(len(shards)))
				go func(c net.Conn, sh *dialerShard) {
					if err := sh.peer.OpenFlow(ctx, route.RemoteRoute, c); err != nil {
						_ = c.Close()
					}
				}(conn, shards[idx])
			}
		}()
	}
	if len(listeners) == 0 {
		return errors.New("dialer requires at least one outbound route")
	}
	// Route listener readiness is a lifecycle event. Tests and cluster startup
	// may wait for this exact post-bind point instead of probing TCP until it
	// happens to accept.
	r.signalDialerReady(nil)

	select {
	case <-ctx.Done():
		for _, ln := range listeners {
			_ = ln.Close()
		}
		wg.Wait()
		return nil
	case err := <-runErr:
		for _, ln := range listeners {
			_ = ln.Close()
		}
		wg.Wait()
		return err
	}
}

func (r *Runtime) closeShards(shards []*dialerShard) {
	for _, sh := range shards {
		r.unregisterPeer(sh.peer)
		r.unregisterSession(sh.peer)
		sh.close()
	}
}

func (r *Runtime) registerPeer(p *session.Peer) {
	if p == nil {
		return
	}
	r.peerMu.Lock()
	if r.peers == nil {
		r.peers = map[*session.Peer]struct{}{}
	}
	r.peers[p] = struct{}{}
	r.peerMu.Unlock()
}

func (r *Runtime) unregisterPeer(p *session.Peer) {
	if p == nil {
		return
	}
	r.peerMu.Lock()
	delete(r.peers, p)
	r.peerMu.Unlock()
}

func (r *Runtime) metricsSnapshot() baftmetrics.Snapshot {
	r.peerMu.Lock()
	peers := make([]*session.Peer, 0, len(r.peers))
	for p := range r.peers {
		peers = append(peers, p)
	}
	r.peerMu.Unlock()

	out := baftmetrics.Snapshot{}
	if r.Resources != nil {
		rs := r.Resources.Snapshot()
		out.ReceiveUsedBytes = rs.ReceiveUsed
		out.ReplayUsedBytes = rs.ReplayUsed
		out.TotalUsedBytes = rs.TotalUsed
	}
	out.RecoveryFailures = map[string]uint64{}
	for _, p := range peers {
		s := p.ConservationSnapshot()
		out.ActiveFlows += len(s.Flows)
		out.InvariantViolations += s.Violations
		for _, f := range s.Flows {
			if f.Accepted >= f.Delivered {
				out.AcceptedBacklogBytes += f.Accepted - f.Delivered
			}
			if f.Credit >= f.Delivered {
				out.CreditExposureBytes += f.Credit - f.Delivered
			}
			out.ReplayOutstandingBytes += f.ReplayOutstanding
		}
		rs:=p.RecoveryStats()
		out.RecoveryAttempts += rs.Attempts
		out.RecoveryCommits += rs.Commits
		out.RecoveryAborts += rs.Aborts
		out.RecoveryPostCommitFailures += rs.PostCommitFailures
		out.RecoveryCommitUncertain += rs.CommitUncertain
		out.RecoveryCommitResolutionCommitted += rs.ResolutionCommitted
		out.RecoveryCommitResolutionNotCommitted += rs.ResolutionNotCommitted
		out.RecoveryCommitResolutionConflict += rs.ResolutionConflict
		out.RecoveryReplayedBytes += rs.ReplayedBytes
		if rs.CurrentEpoch > out.RecoveryCurrentEpoch { out.RecoveryCurrentEpoch = rs.CurrentEpoch }
		for reason,n:=range rs.Failures { out.RecoveryFailures[reason]+=n }
	}
	return out
}

func nodeIDFromIdentity(identity string) (string, error) {
	const prefix = "urn:baft:node:"
	if !strings.HasPrefix(identity, prefix) {
		return "", fmt.Errorf("unsupported peer identity format")
	}
	id := strings.TrimPrefix(identity, prefix)
	if id == "" || len(id) > 64 {
		return "", fmt.Errorf("invalid peer node identity")
	}
	return id, nil
}


func (r *Runtime) telemetrySnapshot() telemetry.Snapshot {
	r.peerMu.Lock()
	active := uint64(len(r.peers))
	r.peerMu.Unlock()
	r.routeMu.Lock()
	routes := make([]telemetry.RouteSnapshot,0,len(r.routeStats))
	for _,v := range r.routeStats { routes = append(routes,v) }
	r.routeMu.Unlock()
	sort.Slice(routes,func(i,j int)bool{return routes[i].RouteID<routes[j].RouteID})
	return telemetry.Snapshot{
		IngressBytes: r.ingressBytes.Load(),
		EgressBytes: r.egressBytes.Load(),
		ActiveSessions: active,
		HandshakeErrors: r.handshakeErrors.Load(),
		NoiseLatencyMS: r.noiseLatencyMS.Load(),
		Routes: routes,
	}
}

func (r *Runtime) routeHealthLoop(ctx context.Context,cfg config.Config,interval time.Duration,exp *telemetry.Exporter) {
	if interval<=0{interval=10*time.Second}
	r.routeMu.Lock()
	for _,cr:=range cfg.Routes {
		r.routeStats[cr.ID]=telemetry.RouteSnapshot{RouteID:cr.ID,Status:"unknown",LatencyMS:-1,ProbeKind:"tcp"}
	}
	r.routeMu.Unlock()

	probeAll:=func(){
		changed:=false
		for _,cr:=range cfg.Routes {
			addr:=""
			if cr.Direction=="inbound" { addr=cr.Target } else if cfg.Peer!=nil { addr=cfg.Peer.Address }
			next:=telemetry.RouteSnapshot{RouteID:cr.ID,Status:"unknown",LatencyMS:-1,ProbeKind:"tcp"}
			if addr!="" {
				start:=time.Now()
				d:=net.Dialer{Timeout:1500*time.Millisecond}
				conn,err:=d.DialContext(ctx,"tcp",addr)
				if err==nil {
					next.Status="up"
					next.LatencyMS=time.Since(start).Milliseconds()
					_ = conn.Close()
					if cr.Direction=="outbound" {
						r.peerMu.Lock();active:=len(r.peers);r.peerMu.Unlock()
						if active==0 {
							next.Status="down"
						} else if cfg.Noise!=nil {
							if rtt:=r.noiseLatencyMS.Load();rtt>=0 {
								next.LatencyMS=rtt
								next.ProbeKind="noise"
							}
						}
					}
				} else {
					next.Status="down"
				}
			}
			r.routeMu.Lock()
			prev:=r.routeStats[cr.ID]
			next.ErrorCount=prev.ErrorCount
			if next.Status=="down" { next.ErrorCount++ }
			if prev.Status!=next.Status { changed=true }
			r.routeStats[cr.ID]=next
			r.routeMu.Unlock()
		}
		// Keep route-change telemetry inside the Runtime lifecycle. A detached
		// SendOnce could outlive Run() and recreate spool temp files while the
		// caller is already tearing down its state directory.
		if changed && exp!=nil { _ = exp.SendOnce(ctx) }
	}
	probeAll()
	t:=time.NewTicker(interval);defer t.Stop()
	for{
		select{
		case <-ctx.Done():return
		case <-t.C:probeAll()
		}
	}
}
