package session

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/recovery"
	"github.com/zarkmakerburg/baft/internal/resources"
)

var (
	ErrCarrierUnavailable = errors.New("session carrier unavailable")
	ErrPostCommitFailure = errors.New("recovery post-commit failure")
	ErrCommitUncertain = errors.New("recovery commit outcome uncertain")
	ErrFinalizationUncertain = fmt.Errorf("%w: finalization outcome uncertain",ErrCommitUncertain)
	ErrRecoveryTransition = errors.New("invalid recovery transaction transition")
)

type RecoveryOffer struct {
	CandidateID string            `json:"candidate_id"`
	NextEpoch   uint64            `json:"next_epoch"`
	Snapshot    recovery.Snapshot `json:"snapshot"`
	Routes      map[uint64]string `json:"routes"`
}

type RecoveryPhase string

const (
	RecoveryPhasePrepared       RecoveryPhase = "PREPARED"
	RecoveryPhaseCommitReady    RecoveryPhase = "COMMIT_READY"
	RecoveryPhaseCommit         RecoveryPhase = "COMMIT"
	RecoveryPhaseCommitAck      RecoveryPhase = "COMMIT_ACK"
	RecoveryPhaseStatusQuery    RecoveryPhase = "COMMIT_STATUS_QUERY"
	RecoveryPhaseStatusReply    RecoveryPhase = "COMMIT_STATUS_REPLY"
	RecoveryPhaseFinalize       RecoveryPhase = "FINALIZE"
	RecoveryPhaseFinalizeAck    RecoveryPhase = "FINALIZE_ACK"
)

type RecoveryTxnState string

const (
	RecoveryTxnIdle          RecoveryTxnState = "IDLE"
	RecoveryTxnPreparing     RecoveryTxnState = "PREPARING"
	RecoveryTxnPrepared      RecoveryTxnState = "PREPARED"
	RecoveryTxnCommitReady   RecoveryTxnState = "COMMIT_READY"
	RecoveryTxnCommitSent    RecoveryTxnState = "COMMIT_SENT"
	RecoveryTxnUncertain     RecoveryTxnState = "COMMIT_UNCERTAIN"
	RecoveryTxnCommitted     RecoveryTxnState = "COMMITTED"
	RecoveryTxnFinalizing    RecoveryTxnState = "FINALIZING"
	RecoveryTxnFinalizationUncertain RecoveryTxnState = "FINALIZATION_UNCERTAIN"
	RecoveryTxnFinalized     RecoveryTxnState = "FINALIZED"
	RecoveryTxnAborted       RecoveryTxnState = "ABORTED"
)

type RecoveryResolutionStatus string

const (
	RecoveryResolutionNone         RecoveryResolutionStatus = ""
	RecoveryResolutionCommitted    RecoveryResolutionStatus = "COMMITTED"
	RecoveryResolutionFinalized    RecoveryResolutionStatus = "FINALIZED"
	RecoveryResolutionNotCommitted RecoveryResolutionStatus = "NOT_COMMITTED"
	RecoveryResolutionConflict     RecoveryResolutionStatus = "CONFLICT"
	RecoveryResolutionUnknown      RecoveryResolutionStatus = "UNKNOWN"
)

type RecoveryControl struct {
	Phase       RecoveryPhase            `json:"phase"`
	SessionID   string                   `json:"session_id"`
	CandidateID string                   `json:"candidate_id"`
	NextEpoch   uint64                   `json:"next_epoch"`
	PlanDigest  string                   `json:"plan_digest"`
	Status      RecoveryResolutionStatus `json:"status,omitempty"`
}

type CommitResult struct {
	Committed  bool
	Epoch      uint64
	CandidateID string
	PlanDigest string
}

type preparedFlowRecovery struct {
	flow *flow
	replay []protocol.Frame
	replayed uint64
	replayFrom uint64
	ackAdvance uint64
	creditAdvance uint64
	creditApplied bool
	finAckAdvance bool
	finAckConfirmAdvance bool
	resendFIN bool
	finFinal uint64
	ackPeerFIN bool
	ackApplied bool
	finAckAdvanceApplied bool
	replayApplied int
	finSentApplied bool
	peerFinAckApplied bool
	pumpsRestored bool
}

type preparedRecovery struct {
	control RecoveryControl
	carrier Carrier
	sender *outboundSender
	runCtx context.Context
	flows []preparedFlowRecovery
	published bool
	finalizing bool
	finalized bool
	activationComplete bool
	rebindPending bool
	activatedGeneration uint64
}

type RecoveryPreparedOwnershipForTest struct {
	Transaction RecoveryControl
	PreparedID string
	PreparedIncarnation uint64
	SenderID string
	CarrierInID string
	CarrierOutID string
	ActivatedGeneration uint64
	RebindPending bool
}

func preparedOwnershipSnapshotForTest(prep *preparedRecovery) RecoveryPreparedOwnershipForTest {
	if prep==nil{return RecoveryPreparedOwnershipForTest{}}
	return RecoveryPreparedOwnershipForTest{
		Transaction:prep.control,
		PreparedID:fmt.Sprintf("%p",prep),
		PreparedIncarnation:0,
		SenderID:fmt.Sprintf("%p",prep.sender),
		CarrierInID:fmt.Sprintf("%p",prep.carrier.In),
		CarrierOutID:fmt.Sprintf("%p",prep.carrier.Out),
		ActivatedGeneration:prep.activatedGeneration,
		RebindPending:prep.rebindPending,
	}
}

type RecoveryFlowFrontier struct {
	StreamID uint64
	ReplaySource uint64
	PeerAccepted uint64
	TxAcked uint64
	TxNext uint64
	FinSent bool
	FinAcked bool
	FinAckSent bool
	FinAckConfirmed bool
}

type RecoveryStats struct {
	Attempts                    uint64
	Commits                     uint64
	Aborts                      uint64
	PostCommitFailures          uint64
	CommitUncertain             uint64
	ResolutionCommitted         uint64
	ResolutionNotCommitted      uint64
	ResolutionConflict          uint64
	ResolutionFinalized         uint64
	FinalizationUncertain       uint64
	CurrentEpoch                uint64
	ReplayedBytes               uint64
	Failures                    map[string]uint64
}

type RecoveryAdapter struct {
	peer *Peer
	beforeCommit func() error
	postCommitFault func(string) error
	// peerAcceptanceTestHook is nil in Runtime. In-package deterministic unit
	// fixtures may use it to model an authenticated peer ACK after replay.
	peerAcceptanceTestHook func(*flow,uint64)
	finalizeOwnershipTestHook func(string,RecoveryPreparedOwnershipForTest)
	engine *recovery.Engine
	mu sync.Mutex
	frozen bool
	pendingCandidate string
	pendingPlan recovery.Plan
	pendingSnapshot recovery.Snapshot
	pendingPeerSnapshot recovery.Snapshot
	pendingRoutes map[uint64]string
	hasPlan bool
	prepared *preparedRecovery
	lastCommit RecoveryControl
	lastNotCommitted RecoveryControl
	lastResolutionCommitted RecoveryControl
	lastResolutionConflict RecoveryControl
	txnState RecoveryTxnState
	uncertain RecoveryControl
	attempts atomic.Uint64
	commits atomic.Uint64
	aborts atomic.Uint64
	postCommitFailures atomic.Uint64
	commitUncertain atomic.Uint64
	resolutionCommitted atomic.Uint64
	resolutionNotCommitted atomic.Uint64
	resolutionConflict atomic.Uint64
	resolutionFinalized atomic.Uint64
	finalizationUncertain atomic.Uint64
	replayed atomic.Uint64
	failures map[string]uint64
}

func newRecoveryAdapter(p *Peer, eng *recovery.Engine) *RecoveryAdapter {
	return &RecoveryAdapter{peer:p,engine:eng,txnState:RecoveryTxnIdle,failures:map[string]uint64{}}
}

func cloneRecoverySnapshot(s recovery.Snapshot) recovery.Snapshot {
	out:=s
	out.Flows=append([]recovery.FlowSnapshot(nil),s.Flows...)
	return out
}
func cloneRecoveryRoutes(in map[uint64]string) map[uint64]string {
	out:=make(map[uint64]string,len(in))
	for k,v:=range in{out[k]=v}
	return out
}

func (a *RecoveryAdapter) IsFrozen() bool {
	a.mu.Lock(); defer a.mu.Unlock()
	return a.frozen
}

func (a *RecoveryAdapter) recordFailure(reason string) {
	switch reason {
	case "candidate_setup","snapshot_exchange","peer_restart","state_mismatch","replay_unavailable","lease_conflict","commit","post_commit_failure":
	default: reason="other"
	}
	a.mu.Lock(); a.failures[reason]++; a.mu.Unlock()
}

func (a *RecoveryAdapter) Stats() RecoveryStats {
	a.mu.Lock()
	fail:=make(map[string]uint64,len(a.failures))
	for k,v:=range a.failures { fail[k]=v }
	a.mu.Unlock()
	return RecoveryStats{
		Attempts:a.attempts.Load(),Commits:a.commits.Load(),Aborts:a.aborts.Load(),PostCommitFailures:a.postCommitFailures.Load(),
		CommitUncertain:a.commitUncertain.Load(),ResolutionCommitted:a.resolutionCommitted.Load(),
		ResolutionNotCommitted:a.resolutionNotCommitted.Load(),ResolutionConflict:a.resolutionConflict.Load(),
		ResolutionFinalized:a.resolutionFinalized.Load(),FinalizationUncertain:a.finalizationUncertain.Load(),
		CurrentEpoch:a.engine.CurrentEpoch(),ReplayedBytes:a.replayed.Load(),Failures:fail,
	}
}

func (p *Peer) RecoveryNeeded() <-chan error { return p.recoveryNeeded }
func (p *Peer) RecordRecoveryFailure(reason string) {
	if p.recovery!=nil{p.recovery.recordFailure(reason)}
}

func (p *Peer) PeerIdentity() string { p.mu.Lock(); defer p.mu.Unlock(); return p.peerID }
func (p *Peer) SessionID() string { p.mu.Lock(); defer p.mu.Unlock(); return p.sessionID }
func (p *Peer) BootID() string { p.mu.Lock(); defer p.mu.Unlock(); return p.bootID }
func (p *Peer) PeerBootID() string { p.mu.Lock(); defer p.mu.Unlock(); return p.peerBootID }
func (p *Peer) RecoveryOwner() string {
	if p.recovery==nil{return ""}
	return p.recovery.engine.Owner()
}
func (p *Peer) RecoveryEpoch() uint64 {
	if p.recovery==nil{return 1}
	return p.recovery.engine.CurrentEpoch()
}
func (p *Peer) RecoveryFrozen() bool {
	if p.recovery==nil{return false}
	return p.recovery.IsFrozen()
}

func (p *Peer) SetRecoveryFinalizeOwnershipHookForTest(fn func(string,RecoveryPreparedOwnershipForTest)) {
	if p.recovery==nil{return}
	p.recovery.mu.Lock()
	p.recovery.finalizeOwnershipTestHook=fn
	p.recovery.mu.Unlock()
}

func (p *Peer) RecoveryPreparedOwnershipForTest() RecoveryPreparedOwnershipForTest {
	if p.recovery==nil{return RecoveryPreparedOwnershipForTest{}}
	a:=p.recovery
	a.mu.Lock();defer a.mu.Unlock()
	return preparedOwnershipSnapshotForTest(a.prepared)
}

func (p *Peer) SetRecoveryPostCommitFaultForTest(fn func(string) error) {
	if p.recovery==nil{return}
	p.recovery.mu.Lock()
	p.recovery.postCommitFault=fn
	p.recovery.mu.Unlock()
}

func (p *Peer) RecoveryCarrierGeneration() uint64 {
	p.mu.Lock();defer p.mu.Unlock()
	return p.carrierGeneration
}

type unavailableCarrierWriter struct{}
func (unavailableCarrierWriter) Write([]byte)(int,error){return 0,ErrCarrierUnavailable}

func (p *Peer) FenceRecoveryCarrierWriter(generation uint64) {
	if !p.recoveryEnabled{return}
	// Recovered carriers use generation-local frameWriters owned by their
	// outboundSender. p.writer belongs to the original carrier and must not be
	// used as a cross-generation fence. Stop only the sender that still owns
	// the generation whose HTTP stream is ending; a newer generation is never
	// affected by an older handler teardown.
	p.mu.Lock()
	if p.carrierGeneration!=generation{p.mu.Unlock();return}
	s:=p.sender
	p.mu.Unlock()
	if s!=nil{s.stop(ErrCarrierUnavailable)}
}

func (p *Peer) RecoveryFlowFrontiersForTest() []RecoveryFlowFrontier {
	if p.recovery==nil{return nil}
	a:=p.recovery
	a.mu.Lock()
	sources:=map[uint64]uint64{}
	if a.prepared!=nil{
		for i:=range a.prepared.flows{
			act:=&a.prepared.flows[i]
			act.flow.mu.Lock()
			id:=act.flow.id
			act.flow.mu.Unlock()
			sources[id]=act.replayFrom
		}
	}
	a.mu.Unlock()

	p.mu.Lock()
	flows:=make(map[uint64]*flow,len(p.flows))
	for id,fl:=range p.flows{flows[id]=fl}
	p.mu.Unlock()

	out:=make([]RecoveryFlowFrontier,0,len(flows))
	for id,fl:=range flows{
		fl.mu.Lock()
		src,ok:=sources[id]
		if !ok{src=fl.txAcked}
		out=append(out,RecoveryFlowFrontier{StreamID:id,ReplaySource:src,PeerAccepted:fl.txAcked,TxAcked:fl.txAcked,TxNext:fl.txNext,FinSent:fl.finSent,FinAcked:fl.finAcked,FinAckSent:fl.finAckSent,FinAckConfirmed:fl.finAckConfirmed})
		fl.mu.Unlock()
	}
	sort.Slice(out,func(i,j int)bool{return out[i].StreamID<out[j].StreamID})
	return out
}

func (p *Peer) RecoveryActivationComplete() bool {
	if p.recovery==nil{return false}
	a:=p.recovery
	a.mu.Lock();defer a.mu.Unlock()
	if a.txnState!=RecoveryTxnFinalized{return false}
	return a.prepared==nil || a.prepared.activationComplete
}

func (p *Peer) RecoveryStats() RecoveryStats {
	if p.recovery==nil{return RecoveryStats{CurrentEpoch:1,Failures:map[string]uint64{}}}
	return p.recovery.Stats()
}

func (p *Peer) currentCarrier() (Carrier,uint64,string,uint64) {
	p.mu.Lock(); defer p.mu.Unlock()
	return p.carrier,p.carrierEpoch,p.carrierID,p.carrierGeneration
}
func (p *Peer) currentCarrierIdentity()(uint64,string,uint64){
	p.mu.Lock();defer p.mu.Unlock()
	return p.carrierEpoch,p.carrierID,p.carrierGeneration
}

func (p *Peer) onCarrierFailure(err error) {
	_,_,_,generation:=p.currentCarrier()
	p.onCarrierFailureForGeneration(err,generation)
}

// onCarrierFailureForGeneration fences failure reporting to the physical
// carrier generation that actually failed. A blocked write on an old sender
// may return only after a newer carrier has committed; that stale error must
// never stop or invalidate the newer sender.
func (p *Peer) onCarrierFailureForGeneration(err error,generation uint64) bool {
	p.mu.Lock()
	if p.carrierGeneration!=generation {
		p.mu.Unlock()
		return false
	}
	s:=p.sender
	p.mu.Unlock()
	if s!=nil{s.stop(ErrCarrierUnavailable)}
	select { case p.recoveryNeeded<-err: default: }
	return true
}

func (p *Peer) currentSenderState()(*outboundSender,uint64,string,uint64){
	p.mu.Lock();defer p.mu.Unlock()
	return p.sender,p.carrierEpoch,p.carrierID,p.carrierGeneration
}

func (p *Peer) NeedsRecovery() bool {
	if !p.recoveryEnabled{return false}
	return p.NeedsExactTransactionResolution() || p.NeedsFreshRecovery()
}

func (p *Peer) DrainRecoverySignals() {
	for {
		select { case <-p.recoveryNeeded: continue; default: return }
	}
}

func (p *Peer) waitForCarrierSwitch(ctx context.Context,oldEpoch uint64,oldCarrier string,oldGeneration uint64) error {
	t:=time.NewTimer(p.recoveryRetention);defer t.Stop()
	for {
		e,id,g:=p.currentCarrierIdentity()
		if g>oldGeneration && (e>oldEpoch || (e==oldEpoch&&id==oldCarrier)){return nil}
		p.carrierSwitchMu.Lock();wait:=p.carrierSwitchWait;p.carrierSwitchMu.Unlock()
		e,id,g=p.currentCarrierIdentity()
		if g>oldGeneration && (e>oldEpoch || (e==oldEpoch&&id==oldCarrier)){return nil}
		select{case <-ctx.Done():return ctx.Err();case <-t.C:return fmt.Errorf("%w: recovery retention expired",ErrCarrierUnavailable);case <-wait:}
	}
}

func (p *Peer) waitForReplacement(ctx context.Context,oldEpoch uint64,oldCarrier string,oldGeneration uint64) error {
	t:=time.NewTimer(p.recoveryRetention);defer t.Stop()
	for {
		p.replacementMu.Lock()
		ready:=p.replacementReadyGeneration
		wait:=p.replacementWait
		p.replacementMu.Unlock()
		if ready>oldGeneration{return nil}
		select{case <-ctx.Done():return ctx.Err();case <-t.C:return fmt.Errorf("%w: recovery retention expired",ErrCarrierUnavailable);case <-wait:}
	}
}

func (p *Peer) publishReplacementReady(generation uint64) bool {
	p.replacementMu.Lock()
	defer p.replacementMu.Unlock()
	// Readiness is a monotonic application-data frontier. A stale generation
	// may never move it backwards or emit a wake that looks like progress.
	if generation<=p.replacementReadyGeneration{return false}
	p.replacementReadyGeneration=generation
	close(p.replacementWait)
	p.replacementWait=make(chan struct{})
	return true
}

func (p *Peer) HandleCarrierFrame(ctx context.Context,epoch uint64,carrierID string,fr protocol.Frame) error {
	return p.handleFrameFrom(ctx,epoch,carrierID,fr)
}

func (p *Peer) handleFrameFrom(ctx context.Context,epoch uint64,carrierID string,fr protocol.Frame,generation ...uint64) error {
	if p.recovery!=nil {
		if !p.recovery.engine.Authorize(epoch,carrierID) {
			return recovery.ErrStaleEpoch
		}
		if len(generation)>0 {
			currentEpoch,currentCarrier,currentGeneration:=p.currentCarrierIdentity()
			if generation[0]!=currentGeneration||epoch!=currentEpoch||carrierID!=currentCarrier {
				return recovery.ErrStaleEpoch
			}
		}
	}
	if p.recoveryEnabled{
		stage:=""
		switch fr.Type{
		case protocol.TypeData:stage="before_data_accept"
		case protocol.TypeFin:stage="before_fin_accept"
		case protocol.TypeFinAck:stage="before_fin_ack_accept"
		case protocol.TypeFinAckConfirm:stage="before_fin_ack_confirm_accept"
		}
		if stage!=""&&p.dropRecoveryFrameForTest(stage,fr){return nil}
	}
	return p.handleFrame(ctx,fr)
}

func (p *Peer) recoverySnapshot() (recovery.Snapshot,map[uint64]string,error) {
	p.mu.Lock()
	sessionID:=p.sessionID
	bootID:=p.bootID
	flows:=make([]*flow,0,len(p.flows))
	for _,fl:=range p.flows { flows=append(flows,fl) }
	p.mu.Unlock()
	if sessionID==""||bootID=="" { return recovery.Snapshot{},nil,recovery.ErrStateMismatch }
	sort.Slice(flows,func(i,j int)bool{return flows[i].id<flows[j].id})
	out:=recovery.Snapshot{SessionID:sessionID,BootID:bootID,Epoch:p.RecoveryEpoch(),Flows:make([]recovery.FlowSnapshot,0,len(flows))}
	routes:=make(map[uint64]string,len(flows))
	for _,fl:=range flows {
		fl.mu.Lock()
		if !fl.openOK && !fl.closed { fl.mu.Unlock(); return recovery.Snapshot{},nil,recovery.ErrResumeFrozen }
		if fl.closed { fl.mu.Unlock(); continue }
		out.Flows=append(out.Flows,recovery.FlowSnapshot{
			StreamID:fl.id,OpenNonce:fl.nonce,TxNext:fl.txNext,TxAcked:fl.txAcked,
			RxAccepted:fl.rxNext,RxDelivered:fl.rxWritten,RxCredit:fl.rxMax,
			FinSent:fl.finSent,FinRecv:fl.finRecv,FinAcked:fl.finAcked,FinAckSent:fl.finAckSent,FinAckConfirmed:fl.finAckConfirmed,
		})
		routes[fl.id]=fl.routeID
		fl.mu.Unlock()
	}
	return out,routes,nil
}

func (p *Peer) BeginRecovery(candidateID string)(RecoveryOffer,error){
	if p.recovery==nil{return RecoveryOffer{},errors.New("recovery is disabled")}
	p.recoveryGate.Lock()
	defer p.recoveryGate.Unlock()
	a:=p.recovery
	a.mu.Lock()
	if a.txnState==RecoveryTxnCommitSent||a.txnState==RecoveryTxnUncertain||a.txnState==RecoveryTxnFinalizationUncertain {
		a.mu.Unlock()
		return RecoveryOffer{},ErrCommitUncertain
	}
	if a.txnState==RecoveryTxnFinalized&&a.prepared!=nil&&!a.prepared.activationComplete {
		a.mu.Unlock()
		return RecoveryOffer{},ErrFinalizationUncertain
	}
	if a.txnState==RecoveryTxnFinalized||a.txnState==RecoveryTxnAborted {
		// lastCommit/lastNotCommitted remain in-process identity evidence after
		// activation is complete; only then may a new epoch start.
		a.prepared=nil
		a.uncertain=RecoveryControl{}
	}
	if a.frozen {
		pending:=a.pendingCandidate
		a.mu.Unlock()
		if pending==candidateID{return RecoveryOffer{},recovery.ErrResumeFrozen}
		a.recordFailure("lease_conflict")
		return RecoveryOffer{},recovery.ErrLeaseConflict
	}
	if err:=a.transitionLocked(RecoveryTxnPreparing);err!=nil{a.mu.Unlock();return RecoveryOffer{},err}
	a.mu.Unlock()
	a.attempts.Add(1)
	snap,routes,err:=p.recoverySnapshot();if err!=nil{a.recordFailure("snapshot_exchange");return RecoveryOffer{},err}
	ids:=make([]recovery.FlowIdentity,0,len(snap.Flows))
	for _,f:=range snap.Flows{ids=append(ids,recovery.FlowIdentity{StreamID:f.StreamID,OpenNonce:f.OpenNonce})}
	if err:=a.engine.SetActiveFlows(ids);err!=nil{a.recordFailure("lease_conflict");return RecoveryOffer{},err}
	next:=a.engine.CurrentEpoch()+1
	if err:=a.engine.Prepare(next,candidateID);err!=nil{a.recordFailure("lease_conflict");return RecoveryOffer{},err}
	a.mu.Lock()
	a.frozen=true
	a.pendingCandidate=candidateID
	a.pendingPlan=recovery.Plan{}
	a.pendingSnapshot=cloneRecoverySnapshot(snap)
	a.pendingPeerSnapshot=recovery.Snapshot{}
	a.pendingRoutes=cloneRecoveryRoutes(routes)
	a.hasPlan=false
	a.mu.Unlock()
	return RecoveryOffer{CandidateID:candidateID,NextEpoch:next,Snapshot:cloneRecoverySnapshot(snap),Routes:cloneRecoveryRoutes(routes)},nil
}

func (p *Peer) ReconcileRecovery(candidateID string,peer RecoveryOffer) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	a:=p.recovery
	a.mu.Lock()
	if !a.frozen||a.pendingCandidate!=candidateID { a.mu.Unlock();return recovery.ErrNotPrepared }
	local:=cloneRecoverySnapshot(a.pendingSnapshot)
	routes:=cloneRecoveryRoutes(a.pendingRoutes)
	a.mu.Unlock()
	if peer.CandidateID!=candidateID||peer.NextEpoch!=a.engine.CurrentEpoch()+1 {
		a.recordFailure("state_mismatch");return recovery.ErrStateMismatch
	}
	if local.SessionID!=peer.Snapshot.SessionID||len(routes)!=len(peer.Routes){a.recordFailure("state_mismatch");return recovery.ErrStateMismatch}
	for id,route:=range routes { if peer.Routes[id]!=route { a.recordFailure("state_mismatch");return recovery.ErrStateMismatch } }
	expected:=p.PeerBootID()
	plan,err:=a.engine.Reconcile(candidateID,local,peer.Snapshot,expected)
	if err!=nil {
		if errors.Is(err,recovery.ErrPeerRestarted){a.recordFailure("peer_restart")}else{a.recordFailure("state_mismatch")}
		return err
	}
	if err:=p.validateReplayPlan(plan);err!=nil{a.recordFailure("replay_unavailable");return err}
	a.mu.Lock();a.pendingPlan=plan;a.pendingPeerSnapshot=cloneRecoverySnapshot(peer.Snapshot);a.hasPlan=true;a.mu.Unlock()
	return nil
}

func (p *Peer) validateReplayPlan(plan recovery.Plan) error {
	p.mu.Lock()
	flowMap:=make(map[uint64]*flow,len(p.flows));for id,f:=range p.flows{flowMap[id]=f}
	p.mu.Unlock()
	if plan.SessionID!=p.SessionID()||plan.Epoch!=p.RecoveryEpoch(){return recovery.ErrStateMismatch}
	for _,fp:=range plan.Flows {
		fl:=flowMap[fp.StreamID];if fl==nil{return recovery.ErrStateMismatch}
		if _,_,err:=fl.replayFramesFrom(fp.LocalReplayFrom);err!=nil{return err}
	}
	return nil
}

type recoveryDigestSide struct {
	ReplayFrom uint64 `json:"replay_from"`
	AckAdvanceTo uint64 `json:"ack_advance_to"`
	ReleaseThrough uint64 `json:"release_through"`
	FinAckCanAdvance bool `json:"fin_ack_can_advance"`
	CreditAdvanceTo uint64 `json:"credit_advance_to"`
	FinAckConfirmCanAdvance bool `json:"fin_ack_confirm_can_advance"`
}
type recoveryDigestFlow struct {
	StreamID uint64 `json:"stream_id"`
	OpenNonce string `json:"open_nonce"`
	Route string `json:"route"`
	SideA recoveryDigestSide `json:"side_a"`
	SideB recoveryDigestSide `json:"side_b"`
}
type recoveryDigestEnvelope struct {
	SessionID string `json:"session_id"`
	CurrentEpoch uint64 `json:"current_epoch"`
	NextEpoch uint64 `json:"next_epoch"`
	CandidateID string `json:"candidate_id"`
	Flows []recoveryDigestFlow `json:"flows"`
}

func recoverySideLess(a,b recoveryDigestSide) bool {
	ab,_:=json.Marshal(a);bb,_:=json.Marshal(b)
	return bytes.Compare(ab,bb)<0
}

func recoveryPlanDigest(plan recovery.Plan,snap,peerSnap recovery.Snapshot,routes map[uint64]string,candidateID string,next uint64)(string,error){
	if plan.SessionID==""||candidateID==""||next!=plan.Epoch+1{return "",recovery.ErrStateMismatch}
	nonce:=make(map[uint64]string,len(snap.Flows))
	localCredit:=make(map[uint64]uint64,len(snap.Flows))
	peerCredit:=make(map[uint64]uint64,len(peerSnap.Flows))
	for _,f:=range snap.Flows{nonce[f.StreamID]=f.OpenNonce;localCredit[f.StreamID]=f.RxCredit}
	for _,f:=range peerSnap.Flows{peerCredit[f.StreamID]=f.RxCredit}
	flows:=make([]recoveryDigestFlow,0,len(plan.Flows))
	for _,fp:=range plan.Flows{
		n:=nonce[fp.StreamID];route,ok:=routes[fp.StreamID]
		if n==""||!ok{return "",recovery.ErrStateMismatch}
		pc,okPeer:=peerCredit[fp.StreamID];lc,okLocal:=localCredit[fp.StreamID]
		if !okPeer||!okLocal{return "",recovery.ErrStateMismatch}
		a:=recoveryDigestSide{ReplayFrom:fp.LocalReplayFrom,AckAdvanceTo:fp.LocalAckAdvanceTo,ReleaseThrough:fp.LocalReleaseThrough,FinAckCanAdvance:fp.LocalFinAckCanAdvance,CreditAdvanceTo:pc,FinAckConfirmCanAdvance:fp.LocalFinAckConfirmCanAdvance}
		b:=recoveryDigestSide{ReplayFrom:fp.PeerReplayFrom,AckAdvanceTo:fp.PeerAckAdvanceTo,ReleaseThrough:fp.PeerReleaseThrough,FinAckCanAdvance:fp.PeerFinAckCanAdvance,CreditAdvanceTo:lc,FinAckConfirmCanAdvance:fp.PeerFinAckConfirmCanAdvance}
		if recoverySideLess(b,a){a,b=b,a}
		flows=append(flows,recoveryDigestFlow{StreamID:fp.StreamID,OpenNonce:n,Route:route,SideA:a,SideB:b})
	}
	sort.Slice(flows,func(i,j int)bool{return flows[i].StreamID<flows[j].StreamID})
	raw,err:=json.Marshal(recoveryDigestEnvelope{SessionID:plan.SessionID,CurrentEpoch:plan.Epoch,NextEpoch:next,CandidateID:candidateID,Flows:flows})
	if err!=nil{return "",err}
	sum:=sha256.Sum256(raw)
	return fmt.Sprintf("%x",sum[:]),nil
}

func sameRecoveryTransaction(a,b RecoveryControl) bool {
	return a.SessionID!=""&&a.SessionID==b.SessionID&&a.CandidateID==b.CandidateID&&a.NextEpoch==b.NextEpoch&&a.PlanDigest!=""&&a.PlanDigest==b.PlanDigest
}

func (p *Peer) PrepareRecoveryCommit(ctx context.Context,candidateID string,c Carrier)(RecoveryControl,error){
	if c.In==nil||c.Out==nil{return RecoveryControl{},errors.New("candidate carrier input/output required")}
	if p.recovery==nil{return RecoveryControl{},errors.New("recovery is disabled")}
	a:=p.recovery
	a.mu.Lock()
	if !a.frozen||a.pendingCandidate!=candidateID||!a.hasPlan{a.mu.Unlock();return RecoveryControl{},recovery.ErrNotPrepared}
	plan:=cloneRecoveryPlan(a.pendingPlan)
	snap:=cloneRecoverySnapshot(a.pendingSnapshot)
	peerSnap:=cloneRecoverySnapshot(a.pendingPeerSnapshot)
	routes:=cloneRecoveryRoutes(a.pendingRoutes)
	if a.prepared!=nil {
		ctl:=a.prepared.control
		a.mu.Unlock()
		if ctl.CandidateID!=candidateID{return RecoveryControl{},recovery.ErrStateMismatch}
		return ctl,nil
	}
	a.mu.Unlock()
	if err:=p.validateReplayPlan(plan);err!=nil{a.recordFailure("replay_unavailable");return RecoveryControl{},err}
	next:=a.engine.CurrentEpoch()+1
	digest,err:=recoveryPlanDigest(plan,snap,peerSnap,routes,candidateID,next)
	if err!=nil{a.recordFailure("state_mismatch");return RecoveryControl{},err}
	ctl:=RecoveryControl{Phase:RecoveryPhasePrepared,SessionID:plan.SessionID,CandidateID:candidateID,NextEpoch:next,PlanDigest:digest}

	candidateWriter:=&frameWriter{w:c.Out}
	newSender:=newOutboundSender(candidateWriter,p.recoveryEnabled)
	p.mu.Lock()
	runCtx:=p.runCtx
	flowMap:=make(map[uint64]*flow,len(p.flows));for id,fl:=range p.flows{flowMap[id]=fl}
	p.mu.Unlock()
	peerFlow:=make(map[uint64]recovery.FlowSnapshot,len(peerSnap.Flows))
	for _,sf:=range peerSnap.Flows{peerFlow[sf.StreamID]=sf}
	if runCtx==nil{runCtx=ctx}
	prep:=&preparedRecovery{control:ctl,carrier:c,sender:newSender,runCtx:runCtx,flows:make([]preparedFlowRecovery,0,len(plan.Flows))}
	for _,fp:=range plan.Flows{
		fl:=flowMap[fp.StreamID]
		if fl==nil{return RecoveryControl{},recovery.ErrStateMismatch}
		fl.mu.Lock()
		if !fl.openOK||fl.closed{fl.mu.Unlock();return RecoveryControl{},recovery.ErrStateMismatch}
		if fp.LocalAckAdvanceTo>fl.txNext{fl.mu.Unlock();return RecoveryControl{},recovery.ErrStateMismatch}
		pf,ok:=peerFlow[fp.StreamID]
		if !ok||pf.OpenNonce!=fl.nonce||pf.RxCredit<fl.peerMax||pf.RxCredit<fl.txNext{fl.mu.Unlock();return RecoveryControl{},recovery.ErrStateMismatch}
		creditAdvance:=pf.RxCredit
		var release int64
		if fp.LocalAckAdvanceTo>fl.txAcked{
			for _,ch:=range fl.replay{if ch.end<=fp.LocalAckAdvanceTo{release+=int64(ch.end-ch.start)}}
		}
		if fp.LocalFinAckCanAdvance&&!fl.finSent{fl.mu.Unlock();return RecoveryControl{},recovery.ErrStateMismatch}
		effectiveFinAcked:=fl.finAcked||fp.LocalFinAckCanAdvance
		resendFIN:=fl.finSent&&!effectiveFinAcked
		final:=fl.txNext
		ackPeerFIN:=fl.finRecv&&!fl.finAckSent&&fl.rxWritten==fl.finRecvFinal
		fl.mu.Unlock()
		if release>0 {
			if err:=fl.allocator.CanRelease(fl.resourceID,resources.Replay,release);err!=nil{a.recordFailure("replay_unavailable");return RecoveryControl{},err}
		}
		frames,n,err:=fl.replayFramesFrom(fp.LocalReplayFrom);if err!=nil{a.recordFailure("replay_unavailable");return RecoveryControl{},err}
		if err:=newSender.addFlow(fl.id);err!=nil{a.recordFailure("candidate_setup");return RecoveryControl{},err}
		prep.flows=append(prep.flows,preparedFlowRecovery{flow:fl,replay:frames,replayed:n,replayFrom:fp.LocalReplayFrom,ackAdvance:fp.LocalAckAdvanceTo,creditAdvance:creditAdvance,finAckAdvance:fp.LocalFinAckCanAdvance,finAckConfirmAdvance:fp.LocalFinAckConfirmCanAdvance,resendFIN:resendFIN,finFinal:final,ackPeerFIN:ackPeerFIN})
	}
	a.mu.Lock()
	if !a.frozen||a.pendingCandidate!=candidateID||!a.hasPlan{a.mu.Unlock();return RecoveryControl{},recovery.ErrNotPrepared}
	a.prepared=prep
	if a.txnState==RecoveryTxnPreparing {
		if err:=a.transitionLocked(RecoveryTxnPrepared);err!=nil{a.mu.Unlock();return RecoveryControl{},err}
	}
	a.mu.Unlock()
	return ctl,nil
}

func cloneRecoveryPlan(p recovery.Plan) recovery.Plan {
	out:=p
	out.Flows=append([]recovery.FlowPlan(nil),p.Flows...)
	return out
}

func (p *Peer) ValidateRecoveryControl(ctl RecoveryControl,phase RecoveryPhase) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	if ctl.Phase!=phase||ctl.SessionID!=p.SessionID()||ctl.CandidateID==""||ctl.NextEpoch==0||ctl.PlanDigest==""{return recovery.ErrStateMismatch}
	a:=p.recovery
	a.mu.Lock();defer a.mu.Unlock()
	if a.prepared!=nil {
		expected:=a.prepared.control
		expected.Phase=phase
		if !sameRecoveryTransaction(expected,ctl){return recovery.ErrStateMismatch}
		return nil
	}
	if a.lastCommit.SessionID!="" {
		expected:=a.lastCommit;expected.Phase=phase
		if sameRecoveryTransaction(expected,ctl){return nil}
		if a.lastCommit.NextEpoch==ctl.NextEpoch{return recovery.ErrStateMismatch}
	}
	return recovery.ErrNotPrepared
}

func (p *Peer) markPostCommitFailure(err error,ctl RecoveryControl)(CommitResult,error){
	_,_,_,generation:=p.currentCarrier()
	return p.markPostCommitFailureForGeneration(err,ctl,generation)
}

func (p *Peer) markPostCommitFailureForGeneration(err error,ctl RecoveryControl,generation uint64)(CommitResult,error){
	if generation!=0 {
		p.mu.Lock()
		currentGeneration:=p.carrierGeneration
		p.mu.Unlock()
		if currentGeneration!=generation {
			// A failure reported by an already-fenced physical generation must
			// never regress transaction state or stop the newer exact rebind.
			return CommitResult{Committed:true,Epoch:ctl.NextEpoch,CandidateID:ctl.CandidateID,PlanDigest:ctl.PlanDigest},
				fmt.Errorf("%w: stale carrier generation %d (current %d): %v",ErrPostCommitFailure,generation,currentGeneration,err)
		}
	}
	a:=p.recovery
	a.postCommitFailures.Add(1)
	a.recordFailure("post_commit_failure")
	a.mu.Lock()
	exact:=a.lastCommit.SessionID!=""&&sameRecoveryTransaction(a.lastCommit,ctl)
	if exact {
		if a.prepared!=nil{a.prepared.finalizing=false}
		switch a.txnState{
		case RecoveryTxnFinalized:
			// Distributed finalization is proven, but local replay/FIN delivery
			// may still be incomplete. Keep the exact prepared transaction
			// frozen and retry it on a new authenticated physical carrier.
			a.frozen=true
			a.uncertain=ctl;a.uncertain.Phase=RecoveryPhaseFinalize
		case RecoveryTxnFinalizationUncertain:
			a.frozen=true
		default:
			a.uncertain=ctl;a.uncertain.Phase=RecoveryPhaseCommit
			switch a.txnState{
			case RecoveryTxnCommitted,RecoveryTxnFinalizing,RecoveryTxnCommitSent:
				_ = a.transitionLocked(RecoveryTxnUncertain)
			case RecoveryTxnUncertain:
			default:
				a.txnState=RecoveryTxnUncertain
			}
			a.frozen=true
		}
	}
	a.mu.Unlock()
	if generation!=0 {
		p.onCarrierFailureForGeneration(err,generation)
	} else {
		p.onCarrierFailure(err)
	}
	return CommitResult{Committed:true,Epoch:ctl.NextEpoch,CandidateID:ctl.CandidateID,PlanDigest:ctl.PlanDigest},fmt.Errorf("%w: %v",ErrPostCommitFailure,err)
}

func (p *Peer) PublishRecoveryCommit(ctl RecoveryControl)(CommitResult,error){
	if p.recovery==nil{return CommitResult{},errors.New("recovery is disabled")}
	a:=p.recovery
	a.mu.Lock()
	if a.lastCommit.SessionID!=""&&sameRecoveryTransaction(a.lastCommit,ctl)&&a.engine.CurrentEpoch()==ctl.NextEpoch&&a.engine.Owner()==ctl.CandidateID{
		if a.txnState==RecoveryTxnCommitSent||a.txnState==RecoveryTxnUncertain{a.txnState=RecoveryTxnCommitted}
		a.mu.Unlock()
		return CommitResult{Committed:true,Epoch:ctl.NextEpoch,CandidateID:ctl.CandidateID,PlanDigest:ctl.PlanDigest},nil
	}
	if a.lastCommit.NextEpoch==ctl.NextEpoch&&a.lastCommit.SessionID!=""&&!sameRecoveryTransaction(a.lastCommit,ctl){
		a.mu.Unlock();return CommitResult{},recovery.ErrStateMismatch
	}
	prep:=a.prepared
	if prep==nil||!sameRecoveryTransaction(prep.control,ctl){a.mu.Unlock();return CommitResult{},recovery.ErrNotPrepared}
	plan:=cloneRecoveryPlan(a.pendingPlan)
	a.mu.Unlock()

	if a.beforeCommit!=nil {
		if err:=a.beforeCommit();err!=nil{a.recordFailure("commit");return CommitResult{Committed:false,Epoch:a.engine.CurrentEpoch()},err}
	}
	if err:=a.engine.Commit(ctl.NextEpoch,ctl.CandidateID,plan);err!=nil{a.recordFailure("commit");return CommitResult{Committed:false,Epoch:a.engine.CurrentEpoch()},err}
	a.commits.Add(1)
	a.mu.Lock()
	a.lastCommit=ctl;a.lastCommit.Phase=RecoveryPhaseCommit;a.lastCommit.Status=RecoveryResolutionNone
	a.uncertain=ctl;a.uncertain.Phase=RecoveryPhaseCommit
	if a.prepared!=nil{a.prepared.published=true}
	switch a.txnState{
	case RecoveryTxnCommitReady,RecoveryTxnCommitSent,RecoveryTxnUncertain:
		_ = a.transitionLocked(RecoveryTxnCommitted)
	case RecoveryTxnCommitted:
	default:
		a.txnState=RecoveryTxnCommitted
	}
	a.mu.Unlock()

	// This is the authority boundary only. Old epoch is fenced from here
	// onward, but the candidate remains control-plane-only until distributed
	// FINALIZE proof exists. Installing p.sender/p.carrier here would allow
	// live pumps or pingLoop to race recovery control frames on the same
	// physical stream before FINALIZE/FINALIZE_ACK completes.
	return CommitResult{Committed:true,Epoch:ctl.NextEpoch,CandidateID:ctl.CandidateID,PlanDigest:ctl.PlanDigest},nil
}


func (p *Peer) waitReplayAccepted(ctx context.Context,fl *flow,want uint64,sender *outboundSender) error {
	if want==0{return nil}
	t:=time.NewTimer(p.recoveryRetention)
	defer t.Stop()
	for {
		fl.mu.Lock()
		if fl.txAcked>=want { fl.mu.Unlock(); return nil }
		if fl.closed { fl.mu.Unlock(); return ErrCarrierUnavailable }
		wait:=fl.ackWait
		fl.mu.Unlock()
		if wait==nil {
			return ErrCarrierUnavailable
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sender.done:
			return ErrCarrierUnavailable
		case <-t.C:
			// Timeout is not delivery proof. Treat the frame as unaccepted and
			// force exact-transaction rebind/resolution on a new carrier.
			return fmt.Errorf("%w: replay acceptance proof timeout",ErrCarrierUnavailable)
		case <-wait:
		}
	}
}

func (p *Peer) activatePreparedCarrier(prep *preparedRecovery,ctl RecoveryControl) (uint64,error) {
	if prep==nil||prep.sender==nil||prep.carrier.In==nil||prep.carrier.Out==nil{return 0,recovery.ErrNotPrepared}
	// The recovered sender owns its own frameWriter. Do not wait on p.writer:
	// an in-flight write on the dead generation may still hold that mutex and
	// must not block FINALIZED data-plane activation for every active flow.
	p.mu.Lock()
	oldSender:=p.sender
	p.carrier=prep.carrier
	p.carrierID=ctl.CandidateID
	p.carrierEpoch=ctl.NextEpoch
	p.carrierGeneration++
	activatedGeneration:=p.carrierGeneration
	p.sender=prep.sender
	runCtx:=prep.runCtx
	if runCtx==nil{runCtx=p.runCtx}
	p.mu.Unlock()
	if runCtx==nil{return 0,errors.New("session run context unavailable")}
	if oldSender!=nil&&oldSender!=prep.sender{oldSender.stop(ErrCarrierUnavailable)}
	if !prep.sender.isStarted(){
		p.wg.Add(1)
		go func(s *outboundSender,rc context.Context){defer p.wg.Done();s.run(rc)}(prep.sender,runCtx)
	}
	return activatedGeneration,nil
}

func (p *Peer) FinalizeRecoveryCommitWithGeneration(ctx context.Context,ctl RecoveryControl) (uint64,error) {
	if p.recovery==nil{return 0,errors.New("recovery is disabled")}
	a:=p.recovery
	a.mu.Lock()
	prep:=a.prepared
	if prep==nil {
		// A globally finalized transaction can have lost its old prepared
		// carrier state after a post-finalization carrier failure. There is
		// nothing left to apply on that dead generation.
		if a.lastCommit.SessionID!=""&&sameRecoveryTransaction(a.lastCommit,ctl)&&a.txnState==RecoveryTxnFinalized{a.mu.Unlock();return 0,nil}
		a.mu.Unlock();return 0,recovery.ErrNotPrepared
	}
	if !prep.published||!sameRecoveryTransaction(prep.control,ctl){a.mu.Unlock();return 0,recovery.ErrNotPrepared}
	if a.txnState!=RecoveryTxnFinalized{
		a.mu.Unlock()
		return 0,fmt.Errorf("%w: local activation requires distributed FINALIZED proof, state=%s",ErrRecoveryTransition,a.txnState)
	}
	finalizeHook:=a.finalizeOwnershipTestHook
	capturedOwnership:=preparedOwnershipSnapshotForTest(prep)
	if finalizeHook!=nil {
		a.mu.Unlock()
		finalizeHook("after_capture",capturedOwnership)
		a.mu.Lock()
		// Intentionally continue with the originally captured prep pointer. The
		// falsification test observes whether an exact rebind mutates that object.
	}
	if prep.activationComplete{
		rebound:=prep.rebindPending
		if rebound{prep.rebindPending=false}
		activatedGeneration:=prep.activatedGeneration
		runCtx:=prep.runCtx
		flows:=append([]preparedFlowRecovery(nil),prep.flows...)
		a.mu.Unlock()
		if rebound{
			var err error
			activatedGeneration,err=p.activatePreparedCarrier(prep,ctl);if err!=nil{return 0,err}
			a.mu.Lock();if a.prepared==prep{prep.activatedGeneration=activatedGeneration};a.mu.Unlock()
			p.carrierSwitchMu.Lock();close(p.carrierSwitchWait);p.carrierSwitchWait=make(chan struct{});p.carrierSwitchMu.Unlock()
			for i:=range flows{
				fl:=flows[i].flow
				fl.mu.Lock();closed:=fl.closed;fl.mu.Unlock()
				if !closed{p.ensurePumpsAfterRecovery(runCtx,fl)}
			}
			// Rebind has no new recovery semantics to apply, but application
			// waiters are released only after the replacement sender and pumps
			// are installed for this exact physical generation.
			p.publishReplacementReady(activatedGeneration)
		}
		return activatedGeneration,nil
	}
	prep.finalizing=true
	a.mu.Unlock()

	if finalizeHook!=nil{finalizeHook("before_use",preparedOwnershipSnapshotForTest(prep))}
	if prep.sender==nil||prep.sender.isStopped(){
		_,e:=p.markPostCommitFailure(ErrCarrierUnavailable,ctl);return 0,e
	}
	// Distributed FINALIZED proof is now present. Only at this point may the
	// candidate become the live data-plane carrier. Recovery control has
	// finished consuming frames, so session reader/pumps can safely take over.
	activatedGeneration,err:=p.activatePreparedCarrier(prep,ctl)
	if err!=nil{_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e}
	a.mu.Lock();if a.prepared==prep{prep.activatedGeneration=activatedGeneration};a.mu.Unlock()
	p.carrierSwitchMu.Lock();close(p.carrierSwitchWait);p.carrierSwitchWait=make(chan struct{});p.carrierSwitchMu.Unlock()

	if a.postCommitFault!=nil {
		if err:=a.postCommitFault("after_authority_commit");err!=nil{_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e}
	}
	for i:=range prep.flows{
		act:=&prep.flows[i]
		fl:=act.flow
		if !act.ackApplied&&act.ackAdvance>0 {
			if err:=fl.onAck(act.ackAdvance);err!=nil{_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e}
			act.ackApplied=true
		}
		if !act.finAckAdvanceApplied&&act.finAckAdvance {
			fl.mu.Lock();fl.finAcked=true;fl.mu.Unlock();act.finAckAdvanceApplied=true
		}
		if act.finAckConfirmAdvance {
			fl.mu.Lock()
			if fl.finAckSent{fl.finAckConfirmed=true}
			fl.mu.Unlock()
		}

		// Local write success is not delivery proof. Recompute the replay
		// frontier from authenticated peer evidence (ACK-derived txAcked).
		// If a prior generation wrote bytes without an ACK, they are sent again
		// and the peer's offset dedupe prevents duplicate application delivery.
		fl.mu.Lock()
		accepted:=fl.txAcked
		fl.mu.Unlock()
		from:=act.replayFrom
		if accepted>from{from=accepted}
		frames,_,err:=fl.replayFramesFrom(from)
		if err!=nil{_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e}
		var replayAcceptThrough uint64
		for _,fr:=range frames{
			if a.postCommitFault!=nil {
				if err:=a.postCommitFault("replay_write");err!=nil{_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e}
			}
			if err:=prep.sender.sendData(ctx,fl,fr);err!=nil{_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e}
			a.replayed.Add(uint64(len(fr.Payload)))
			end:=fr.Offset+uint64(len(fr.Payload))
			if end>replayAcceptThrough{replayAcceptThrough=end}
			if a.postCommitFault!=nil {
				if err:=a.postCommitFault("after_replay_write");err!=nil{_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e}
			}
			if a.peerAcceptanceTestHook!=nil { a.peerAcceptanceTestHook(fl,end) }
			// A successful carrier write is not delivery evidence and does not
			// advance txAcked. Replay frames remain ordered on the carrier, but
			// any later rebind recomputes the conservative suffix from the
			// ACK-derived txAcked frontier, so unproven writes are retried safely.
		}
		// Do not declare this exact transaction locally activation-complete from
		// write success alone. A cumulative ACK on the live recovery carrier is
		// the delivery proof for the replay suffix. If that proof is lost, keep
		// the exact transaction frozen and rebind/replay it conservatively on the
		// next authenticated physical carrier instead of opening a fresh epoch.
		if replayAcceptThrough>0 {
			if err:=p.waitReplayAccepted(ctx,fl,replayAcceptThrough,prep.sender);err!=nil{
				_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e
			}
		}

		fl.mu.Lock()
		resendFIN:=fl.finSent&&!fl.finAcked
		final:=fl.txNext
		ackPeerFIN:=fl.finRecv&&fl.rxWritten==fl.finRecvFinal&&!fl.finAckConfirmed
		fl.mu.Unlock()
		if resendFIN {
			if a.postCommitFault!=nil {
				if err:=a.postCommitFault("fin_write");err!=nil{_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e}
			}
			if err:=prep.sender.sendControl(protocol.Frame{Type:protocol.TypeFin,StreamID:fl.id,Offset:final});err!=nil{_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e}
			if a.postCommitFault!=nil {
				if err:=a.postCommitFault("after_fin_write");err!=nil{_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e}
			}
		}
		if ackPeerFIN {
			if a.postCommitFault!=nil {
				if err:=a.postCommitFault("fin_ack_write");err!=nil{_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e}
			}
			if err:=p.ackRemoteFin(fl);err!=nil{_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e}
			if a.postCommitFault!=nil {
				if err:=a.postCommitFault("after_fin_ack_write");err!=nil{_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e}
			}
		}
		// Restore the peer's authenticated receive-credit frontier only after
		// this flow's bounded replay/FIN actions have been written in order.
		// A WINDOW lost with the old carrier must not strand the sender at an
		// obsolete peerMax, and applying the monotonic snapshot credit is
		// idempotent across exact-transaction rebinds.
		if !act.creditApplied {
			if err:=fl.restoreRecoveryPeerCredit(act.creditAdvance);err!=nil{_,e:=p.markPostCommitFailureForGeneration(err,ctl,activatedGeneration);return 0,e}
			act.creditApplied=true
		}
		p.finishIfComplete(fl)
		fl.mu.Lock();closed:=fl.closed;fl.mu.Unlock()
		if !closed&&!act.pumpsRestored{p.ensurePumpsAfterRecovery(prep.runCtx,fl);act.pumpsRestored=true}
	}
	a.mu.Lock()
	if a.prepared==prep{
		prep.finalizing=false;prep.finalized=true;prep.activationComplete=true;prep.rebindPending=false
		a.frozen=false;a.pendingCandidate="";a.pendingPlan=recovery.Plan{};a.pendingSnapshot=recovery.Snapshot{};a.pendingPeerSnapshot=recovery.Snapshot{};a.pendingRoutes=nil;a.hasPlan=false;a.uncertain=RecoveryControl{}
	}
	a.mu.Unlock()
	// Publish application readiness only after replay/ACK/FIN reconciliation
	// and pump restoration are complete. Physical carrier activation alone is
	// not sufficient evidence that blocked application flows may resume.
	p.publishReplacementReady(activatedGeneration)
	return activatedGeneration,nil
}


func (p *Peer) FinalizeRecoveryCommit(ctx context.Context,ctl RecoveryControl) error {
	_,err:=p.FinalizeRecoveryCommitWithGeneration(ctx,ctl)
	return err
}

func (p *Peer) RecoveryTransactionCarrierGeneration(ctl RecoveryControl)(uint64,bool){
	if p.recovery==nil{return 0,false}
	a:=p.recovery
	a.mu.Lock();defer a.mu.Unlock()
	if a.prepared==nil||!sameRecoveryTransaction(a.prepared.control,ctl)||a.prepared.activatedGeneration==0{return 0,false}
	return a.prepared.activatedGeneration,true
}

func (p *Peer) MarkPostCommitFailure(err error,ctl RecoveryControl) error {
	if err==nil{err=ErrCarrierUnavailable}
	_,out:=p.markPostCommitFailure(err,ctl)
	return out
}

func (p *Peer) HandleRecoveryControlFrame(fr protocol.Frame) error {
	ctl,err:=DecodeRecoveryControl(fr);if err!=nil{return err}
	switch ctl.Phase {
	case RecoveryPhaseCommit:
		ack,err:=p.HandleCommittedRecoveryControl(ctl);if err!=nil{return err}
		return p.senderNow().sendControl(protocol.Frame{Type:protocol.TypeResumeDone,Payload:mustRecoveryControlPayload(ack)})
	case RecoveryPhaseCommitAck:
		a:=p.recovery
		if a==nil{return recovery.ErrStateMismatch}
		a.mu.Lock();last:=a.lastCommit;a.mu.Unlock()
		if !sameRecoveryTransaction(last,ctl){return recovery.ErrStateMismatch}
		return nil
	case RecoveryPhaseFinalize:
		if err:=p.ValidateRecoveryControl(ctl,RecoveryPhaseFinalize);err!=nil{return err}
		if err:=p.MarkFinalizationStarted(ctl);err!=nil{return err}
		if err:=p.CompleteRecoveryFinalization(ctl);err!=nil{return err}
		ack:=ctl;ack.Phase=RecoveryPhaseFinalizeAck
		return p.senderNow().sendControl(protocol.Frame{Type:protocol.TypeResumeDone,Payload:mustRecoveryControlPayload(ack)})
	case RecoveryPhaseFinalizeAck:
		// Duplicate FINALIZE_ACK after the distributed proof has already been
		// consumed is side-effect free.
		if err:=p.ValidateRecoveryControl(ctl,RecoveryPhaseFinalizeAck);err!=nil{return err}
		return nil
	default:
		return recovery.ErrStateMismatch
	}
}

func mustRecoveryControlPayload(ctl RecoveryControl) []byte {
	b,err:=json.Marshal(ctl)
	if err!=nil{panic(err)}
	return b
}

// CommitPreparedRecovery is an in-package convenience path used by
// deterministic engine/session tests. It models an already obtained exact
// peer FINALIZE proof before local activation. Runtime distributed recovery
// performs the real FINALIZE/FINALIZE_ACK exchange in internal/node.
func (p *Peer) CommitPreparedRecovery(ctx context.Context,ctl RecoveryControl)(CommitResult,error){
	res,err:=p.PublishRecoveryCommit(ctl)
	if err!=nil{return res,err}
	finalCtl:=ctl;finalCtl.Phase=RecoveryPhaseFinalize;finalCtl.Status=RecoveryResolutionNone
	if err:=p.MarkFinalizationStarted(finalCtl);err!=nil{return res,err}
	if err:=p.CompleteRecoveryFinalization(finalCtl);err!=nil{return res,err}
	if err:=p.FinalizeRecoveryCommit(ctx,finalCtl);err!=nil{return res,err}
	return res,nil
}

func (p *Peer) CommitRecovery(ctx context.Context,candidateID string,c Carrier)(CommitResult,error){
	ctl,err:=p.PrepareRecoveryCommit(ctx,candidateID,c)
	if err!=nil{return CommitResult{Committed:false,Epoch:p.RecoveryEpoch()},err}
	ctl.Phase=RecoveryPhaseCommit
	return p.CommitPreparedRecovery(ctx,ctl)
}

func (p *Peer) AbortRecovery(candidateID string) {
	if p.recovery==nil{return}
	a:=p.recovery
	a.mu.Lock()
	if !a.frozen||a.pendingCandidate!=candidateID {
		a.mu.Unlock()
		return
	}
	if a.txnState==RecoveryTxnCommitSent||a.txnState==RecoveryTxnUncertain||a.txnState==RecoveryTxnCommitted||a.txnState==RecoveryTxnFinalizing||a.txnState==RecoveryTxnFinalizationUncertain||a.txnState==RecoveryTxnFinalized{
		a.mu.Unlock()
		return
	}
	next:=a.engine.CurrentEpoch()+1
	a.mu.Unlock()
	a.engine.Abort(next,candidateID)
	a.aborts.Add(1)
	a.mu.Lock()
	if a.pendingCandidate==candidateID {
		a.frozen=false;a.pendingCandidate="";a.pendingPlan=recovery.Plan{};a.pendingSnapshot=recovery.Snapshot{};a.pendingPeerSnapshot=recovery.Snapshot{};a.pendingRoutes=nil;a.hasPlan=false;a.prepared=nil;a.uncertain=RecoveryControl{}
		a.txnState=RecoveryTxnAborted
	}
	a.mu.Unlock()
}

func (f *flow) replayFramesFrom(from uint64)([]protocol.Frame,uint64,error){
	f.mu.Lock();defer f.mu.Unlock()
	if from<f.txAcked||from>f.txNext{return nil,0,recovery.ErrStateMismatch}
	if from==f.txNext{return nil,0,nil}
	cursor:=from
	var out []protocol.Frame
	var total uint64
	for _,ch:=range f.replay {
		if ch.end<=cursor{continue}
		if ch.start>cursor{return nil,0,fmt.Errorf("%w: replay gap",recovery.ErrStateMismatch)}
		start:=cursor
		if start<ch.start{start=ch.start}
		idx:=start-ch.start
		if idx>uint64(len(ch.data)){return nil,0,recovery.ErrStateMismatch}
		payload:=append([]byte(nil),ch.data[idx:]...)
		if len(payload)==0{continue}
		out=append(out,protocol.Frame{Type:protocol.TypeData,StreamID:f.id,Offset:start,Payload:payload})
		cursor=start+uint64(len(payload));total+=uint64(len(payload))
		if cursor>=f.txNext{break}
	}
	if cursor!=f.txNext{return nil,0,fmt.Errorf("%w: replay bytes unavailable",recovery.ErrStateMismatch)}
	return out,total,nil
}

func EncodeRecoveryOffer(w io.Writer,offer RecoveryOffer) error {
	b,err:=json.Marshal(offer);if err!=nil{return err}
	return protocol.Encode(w,protocol.Frame{Type:protocol.TypeResumeState,Payload:b})
}
func DecodeRecoveryOffer(fr protocol.Frame)(RecoveryOffer,error){
	if fr.Type!=protocol.TypeResumeState{return RecoveryOffer{},errors.New("expected RESUME_STATE")}
	dec:=json.NewDecoder(bytes.NewReader(fr.Payload));dec.DisallowUnknownFields()
	var o RecoveryOffer
	if err:=dec.Decode(&o);err!=nil{return RecoveryOffer{},err}
	if o.CandidateID==""||o.NextEpoch==0||o.Snapshot.SessionID==""{return RecoveryOffer{},recovery.ErrStateMismatch}
	return o,nil
}


func EncodeRecoveryControl(w io.Writer,ctl RecoveryControl) error {
	if ctl.Phase==""||ctl.SessionID==""||ctl.CandidateID==""||ctl.NextEpoch==0||ctl.PlanDigest==""{return recovery.ErrStateMismatch}
	b,err:=json.Marshal(ctl);if err!=nil{return err}
	return protocol.Encode(w,protocol.Frame{Type:protocol.TypeResumeDone,Payload:b})
}
func DecodeRecoveryControl(fr protocol.Frame)(RecoveryControl,error){
	if fr.Type!=protocol.TypeResumeDone{return RecoveryControl{},errors.New("expected RESUME_DONE")}
	dec:=json.NewDecoder(bytes.NewReader(fr.Payload));dec.DisallowUnknownFields()
	var ctl RecoveryControl
	if err:=dec.Decode(&ctl);err!=nil{return RecoveryControl{},err}
	switch ctl.Phase {
	case RecoveryPhasePrepared,RecoveryPhaseCommitReady,RecoveryPhaseCommit,RecoveryPhaseCommitAck,RecoveryPhaseFinalize,RecoveryPhaseFinalizeAck:
		if ctl.Status!=RecoveryResolutionNone{return RecoveryControl{},recovery.ErrStateMismatch}
	case RecoveryPhaseStatusQuery:
		if ctl.Status!=RecoveryResolutionNone{return RecoveryControl{},recovery.ErrStateMismatch}
	case RecoveryPhaseStatusReply:
		switch ctl.Status{case RecoveryResolutionCommitted,RecoveryResolutionFinalized,RecoveryResolutionNotCommitted,RecoveryResolutionConflict,RecoveryResolutionUnknown:default:return RecoveryControl{},recovery.ErrStateMismatch}
	default:return RecoveryControl{},recovery.ErrStateMismatch
	}
	if ctl.SessionID==""||ctl.CandidateID==""||ctl.NextEpoch==0||ctl.PlanDigest==""{return RecoveryControl{},recovery.ErrStateMismatch}
	return ctl,nil
}

func (p *Peer) HandleCommittedRecoveryControl(ctl RecoveryControl)(RecoveryControl,error){
	if ctl.Phase!=RecoveryPhaseCommit{return RecoveryControl{},recovery.ErrStateMismatch}
	if err:=p.ValidateRecoveryControl(ctl,RecoveryPhaseCommit);err!=nil{return RecoveryControl{},err}
	return RecoveryControl{Phase:RecoveryPhaseCommitAck,SessionID:ctl.SessionID,CandidateID:ctl.CandidateID,NextEpoch:ctl.NextEpoch,PlanDigest:ctl.PlanDigest},nil
}
