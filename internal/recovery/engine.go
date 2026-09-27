package recovery

import (
	"errors"
	"fmt"
	"math"
	"sync"
)

var (
	ErrEnginePlanMismatch = errors.New("engine reconcile plan mismatch")
	ErrDurableSnapshotConfig = errors.New("durable_snapshot configuration invalid")
	ErrResumeFrozen = errors.New("resume snapshot is frozen")
)

type FlowIdentity struct {
	StreamID uint64
	OpenNonce string
}

type TransitionKind string

const (
	TransitionOpen TransitionKind = "OPEN"
	TransitionData TransitionKind = "DATA"
	TransitionFIN TransitionKind = "FIN"
)

type EngineScheduler interface { Point(name string) }

type EngineOptions struct {
	DurableSnapshot bool
	RReplay uint64
	Scheduler EngineScheduler
}

func (o EngineOptions) validate() error {
	if !o.DurableSnapshot && o.RReplay != 0 {
		return fmt.Errorf("%w: r_replay requires durable_snapshot", ErrDurableSnapshotConfig)
	}
	if o.DurableSnapshot && o.RReplay == 0 {
		return fmt.Errorf("%w: durable_snapshot requires non-zero r_replay", ErrDurableSnapshotConfig)
	}
	return nil
}

type ReplayPolicy struct {
	durableSnapshot bool
	rReplay          uint64
}

func NewReplayPolicy(opts EngineOptions) (ReplayPolicy, error) {
	if err := opts.validate(); err != nil {
		return ReplayPolicy{}, err
	}
	return ReplayPolicy{durableSnapshot: opts.DurableSnapshot, rReplay: opts.RReplay}, nil
}

func (p ReplayPolicy) DurableSnapshot() bool { return p.durableSnapshot }

func (p ReplayPolicy) ReleaseThrough(k, kRelease uint64) uint64 {
	if p.durableSnapshot {
		return kRelease
	}
	return k
}

func (p ReplayPolicy) EffectiveCredit(c, kRelease uint64) uint64 {
	if !p.durableSnapshot {
		return c
	}
	limit := kRelease + p.rReplay
	if limit < kRelease {
		limit = math.MaxUint64
	}
	if c < limit {
		return c
	}
	return limit
}

func (p ReplayPolicy) ValidateOutstanding(s, kRelease uint64) error {
	if !p.durableSnapshot {
		return nil
	}
	if s < kRelease || s-kRelease > p.rReplay {
		return ErrStateMismatch
	}
	return nil
}

type ReconcileCommitEngine interface {
	SetActiveFlows(flows []FlowIdentity) error
	Prepare(next uint64, candidateID string) error
	Reconcile(candidateID string, local, peer Snapshot, expectedPeerBootID string) (Plan, error)
	Commit(next uint64, candidateID string, plan Plan) error
	Abort(next uint64, candidateID string)
	Authorize(epoch uint64, carrierID string) bool
	CurrentEpoch() uint64
	Owner() string
	TransitionAllowed(kind TransitionKind, streamID uint64, nonce string) error
}

type engineFaults struct {
	acceptOldEpoch        bool
	acceptBothCandidates  bool
	replayFromK           bool
	resumeAfterBootChange bool
	acceptAGreaterThanS   bool
	skipKLessEqualA       bool
	twoStageCommit         bool
}

type Engine struct {
	mu sync.Mutex
	currentEpoch uint64
	owner string
	pendingEpoch uint64
	pendingCandidate string
	pendingPlan Plan
	hasPendingPlan bool
	opts EngineOptions
	policy ReplayPolicy
	faults engineFaults
	scheduler EngineScheduler
	activeFlows map[uint64]string
	frozenFlows map[uint64]string
}

func NewEngine(initialEpoch uint64, owner string, opts EngineOptions) (*Engine, error) {
	if initialEpoch == 0 {
		return nil, errors.New("initial epoch must be positive")
	}
	if owner == "" {
		return nil, errors.New("owner is required")
	}
	policy, err := NewReplayPolicy(opts)
	if err != nil {
		return nil, err
	}
	return &Engine{currentEpoch:initialEpoch,owner:owner,opts:opts,policy:policy,scheduler:opts.Scheduler,activeFlows:map[uint64]string{}}, nil
}

func (e *Engine) point(name string){if e.scheduler!=nil{e.scheduler.Point(name)}}
func (e *Engine) SetActiveFlows(flows []FlowIdentity) error{
	e.mu.Lock();defer e.mu.Unlock()
	if e.pendingEpoch!=0{return ErrResumeFrozen}
	m,err:=identitiesToMap(flows);if err!=nil{return err};e.activeFlows=m;return nil
}
func (e *Engine) TransitionAllowed(kind TransitionKind,streamID uint64,nonce string)error{
	e.mu.Lock();defer e.mu.Unlock()
	if e.pendingEpoch!=0{return ErrResumeFrozen}
	if streamID==0{return ErrStateMismatch}
	switch kind{
	case TransitionOpen:
		if nonce==""{return ErrStateMismatch}
	case TransitionData,TransitionFIN:
	default:return ErrStateMismatch
	}
	return nil
}

func (e *Engine) Prepare(next uint64, candidateID string) error {
	if candidateID == "" {
		return errors.New("candidate id is required")
	}
	e.point("prepare_before_lock")
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.currentEpoch == math.MaxUint64 {
		return ErrEpochExhausted
	}
	if next != e.currentEpoch+1 {
		return ErrStaleEpoch
	}
	if e.pendingEpoch != 0 {
		if e.faults.acceptBothCandidates {
			e.pendingEpoch = next
			e.pendingCandidate = candidateID
			e.pendingPlan = Plan{}
			e.hasPendingPlan = false
			e.frozenFlows = cloneIdentityMap(e.activeFlows)
			return nil
		}
		if e.pendingEpoch == next && e.pendingCandidate == candidateID {
			return nil
		}
		return ErrLeaseConflict
	}
	e.pendingEpoch = next
	e.pendingCandidate = candidateID
	e.pendingPlan = Plan{}
	e.hasPendingPlan = false
	e.frozenFlows = cloneIdentityMap(e.activeFlows)
	return nil
}

func (e *Engine) Reconcile(candidateID string, local, peer Snapshot, expectedPeerBootID string) (Plan, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pendingEpoch == 0 || e.pendingCandidate != candidateID || candidateID == "" {
		return Plan{}, ErrNotPrepared
	}
	if local.Epoch != e.currentEpoch || peer.Epoch != e.currentEpoch {
		return Plan{}, ErrStateMismatch
	}
	plan, err := e.reconcileLocked(local, peer, expectedPeerBootID)
	if err != nil {
		return Plan{}, err
	}
	e.pendingPlan = clonePlan(plan)
	e.hasPendingPlan = true
	return plan, nil
}

func (e *Engine) reconcileLocked(local, peer Snapshot, expectedPeerBootID string) (Plan, error) {
	if err := validateSnapshotEngine(local); err != nil {
		return Plan{}, err
	}
	if err := validateSnapshotEngine(peer); err != nil {
		return Plan{}, err
	}
	if !e.faults.resumeAfterBootChange && (expectedPeerBootID == "" || peer.BootID != expectedPeerBootID) {
		return Plan{}, ErrPeerRestarted
	}
	if local.SessionID != peer.SessionID || local.Epoch != peer.Epoch {
		return Plan{}, ErrStateMismatch
	}
	if !snapshotMatchesFrozen(local,e.frozenFlows)||!snapshotMatchesFrozen(peer,e.frozenFlows){return Plan{},ErrStateMismatch}
	peerFlows := make(map[uint64]FlowSnapshot, len(peer.Flows))
	for _, f := range peer.Flows {
		peerFlows[f.StreamID] = f
	}
	if len(local.Flows) != len(peer.Flows) {
		return Plan{}, ErrStateMismatch
	}
	out := Plan{SessionID: local.SessionID, Epoch: local.Epoch, Flows: make([]FlowPlan, 0, len(local.Flows))}
	for _, lf := range local.Flows {
		pf, ok := peerFlows[lf.StreamID]
		if !ok || pf.OpenNonce != lf.OpenNonce {
			return Plan{}, ErrStateMismatch
		}
		if (!e.faults.skipKLessEqualA && pf.RxAccepted < lf.TxAcked) ||
			(!e.faults.acceptAGreaterThanS && pf.RxAccepted > lf.TxNext) ||
			lf.TxNext > pf.RxCredit {
			return Plan{}, ErrStateMismatch
		}
		if lf.RxAccepted < pf.TxAcked || lf.RxAccepted > pf.TxNext || pf.TxNext > lf.RxCredit {
			return Plan{}, ErrStateMismatch
		}
		if lf.FinRecv && !pf.FinSent { return Plan{}, ErrStateMismatch }
		if pf.FinRecv && !lf.FinSent { return Plan{}, ErrStateMismatch }
		if lf.FinAcked && !pf.FinAckSent { return Plan{}, ErrStateMismatch }
		if pf.FinAcked && !lf.FinAckSent { return Plan{}, ErrStateMismatch }

		localReplay := pf.RxAccepted
		if e.faults.replayFromK {
			localReplay = lf.TxAcked
		}
		out.Flows = append(out.Flows, FlowPlan{
			StreamID: lf.StreamID,
			LocalReplayFrom: localReplay,
			PeerReplayFrom: lf.RxAccepted,
			LocalAckAdvanceTo: pf.RxAccepted,
			PeerAckAdvanceTo: lf.RxAccepted,
			LocalReleaseThrough: lf.TxAcked,
			PeerReleaseThrough: pf.TxAcked,
			LocalFinAckCanAdvance: pf.FinAckSent && !lf.FinAcked,
			PeerFinAckCanAdvance: lf.FinAckSent && !pf.FinAcked,
		})
	}
	sortFlowPlans(out.Flows)
	return out, nil
}

func (e *Engine) Commit(next uint64,candidateID string,plan Plan)error{
	e.mu.Lock()
	if e.pendingEpoch!=next||e.pendingCandidate!=candidateID||candidateID==""||!e.hasPendingPlan{e.mu.Unlock();return ErrNotPrepared}
	if next!=e.currentEpoch+1{e.mu.Unlock();return ErrStaleEpoch}
	if !plansEqual(e.pendingPlan,plan){e.mu.Unlock();return ErrEnginePlanMismatch}
	e.point("commit_locked_before_publish")
	if e.faults.twoStageCommit{
		e.owner=candidateID
		e.mu.Unlock()
		e.point("commit_nonatomic_mid")
		e.mu.Lock()
		e.currentEpoch=next
	}else{
		e.currentEpoch=next
		e.owner=candidateID
	}
	e.pendingEpoch=0;e.pendingCandidate="";e.pendingPlan=Plan{};e.hasPendingPlan=false;e.frozenFlows=nil
	e.mu.Unlock();e.point("commit_after_publish");return nil
}

func (e *Engine) Abort(next uint64, candidateID string) {
	e.mu.Lock()
	if e.pendingEpoch == next && e.pendingCandidate == candidateID {
		e.pendingEpoch = 0
		e.pendingCandidate = ""
		e.pendingPlan = Plan{}
		e.hasPendingPlan = false
		e.frozenFlows = nil
	}
	e.mu.Unlock()
}

func (e *Engine) Authorize(epoch uint64, carrierID string) bool {
	e.point("authorize_before_lock")
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.faults.acceptOldEpoch && epoch < e.currentEpoch {
		return true
	}
	return epoch == e.currentEpoch && carrierID == e.owner
}

func (e *Engine) CurrentEpoch() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.currentEpoch
}

func (e *Engine) Owner() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.owner
}

func identitiesToMap(flows []FlowIdentity)(map[uint64]string,error){
	out:=make(map[uint64]string,len(flows))
	for _,f:=range flows{if f.StreamID==0||f.OpenNonce==""{return nil,ErrStateMismatch};if _,ok:=out[f.StreamID];ok{return nil,ErrStateMismatch};out[f.StreamID]=f.OpenNonce}
	return out,nil
}
func cloneIdentityMap(in map[uint64]string)map[uint64]string{out:=make(map[uint64]string,len(in));for k,v:=range in{out[k]=v};return out}
func snapshotMatchesFrozen(s Snapshot,frozen map[uint64]string)bool{if len(s.Flows)!=len(frozen){return false};for _,f:=range s.Flows{if frozen[f.StreamID]!=f.OpenNonce{return false}};return true}
func flowIdentities(s Snapshot)[]FlowIdentity{out:=make([]FlowIdentity,0,len(s.Flows));for _,f:=range s.Flows{out=append(out,FlowIdentity{StreamID:f.StreamID,OpenNonce:f.OpenNonce})};return out}

func validateSnapshotEngine(s Snapshot) error {
	if s.SessionID == "" || s.BootID == "" || s.Epoch == 0 {
		return ErrStateMismatch
	}
	seen := make(map[uint64]struct{}, len(s.Flows))
	for _, f := range s.Flows {
		if f.StreamID == 0 || f.OpenNonce == "" {
			return ErrStateMismatch
		}
		if _, ok := seen[f.StreamID]; ok {
			return ErrStateMismatch
		}
		seen[f.StreamID] = struct{}{}
		if f.TxAcked > f.TxNext { return ErrStateMismatch }
		if f.RxDelivered > f.RxAccepted || f.RxAccepted > f.RxCredit { return ErrStateMismatch }
		if f.FinAcked && !f.FinSent { return ErrStateMismatch }
		if f.FinAckSent && !f.FinRecv { return ErrStateMismatch }
	}
	return nil
}

func sortFlowPlans(flows []FlowPlan) {
	for i := 1; i < len(flows); i++ {
		for j := i; j > 0 && flows[j].StreamID < flows[j-1].StreamID; j-- {
			flows[j], flows[j-1] = flows[j-1], flows[j]
		}
	}
}

func clonePlan(p Plan) Plan {
	out := p
	out.Flows = append([]FlowPlan(nil), p.Flows...)
	return out
}

func plansEqual(a, b Plan) bool {
	if a.SessionID != b.SessionID || a.Epoch != b.Epoch || len(a.Flows) != len(b.Flows) {
		return false
	}
	for i := range a.Flows {
		if a.Flows[i] != b.Flows[i] { return false }
	}
	return true
}
