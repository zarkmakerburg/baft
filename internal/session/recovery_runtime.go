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
)

type RecoveryOffer struct {
	CandidateID string            `json:"candidate_id"`
	NextEpoch   uint64            `json:"next_epoch"`
	Snapshot    recovery.Snapshot `json:"snapshot"`
	Routes      map[uint64]string `json:"routes"`
}

type RecoveryPhase string

const (
	RecoveryPhasePrepared    RecoveryPhase = "PREPARED"
	RecoveryPhaseCommitReady RecoveryPhase = "COMMIT_READY"
	RecoveryPhaseCommit      RecoveryPhase = "COMMIT"
	RecoveryPhaseCommitAck   RecoveryPhase = "COMMIT_ACK"
)

type RecoveryControl struct {
	Phase       RecoveryPhase `json:"phase"`
	SessionID   string        `json:"session_id"`
	CandidateID string        `json:"candidate_id"`
	NextEpoch   uint64        `json:"next_epoch"`
	PlanDigest  string        `json:"plan_digest"`
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
	ackAdvance uint64
	finAckAdvance bool
	resendFIN bool
	finFinal uint64
	ackPeerFIN bool
}

type preparedRecovery struct {
	control RecoveryControl
	carrier Carrier
	sender *outboundSender
	runCtx context.Context
	flows []preparedFlowRecovery
	published bool
	finalized bool
}

type RecoveryStats struct {
	Attempts           uint64
	Commits            uint64
	Aborts             uint64
	PostCommitFailures uint64
	CurrentEpoch       uint64
	ReplayedBytes      uint64
	Failures           map[string]uint64
}

type RecoveryAdapter struct {
	peer *Peer
	beforeCommit func() error
	postCommitFault func(string) error
	engine *recovery.Engine
	mu sync.Mutex
	frozen bool
	pendingCandidate string
	pendingPlan recovery.Plan
	pendingSnapshot recovery.Snapshot
	pendingRoutes map[uint64]string
	hasPlan bool
	prepared *preparedRecovery
	lastCommit RecoveryControl
	attempts atomic.Uint64
	commits atomic.Uint64
	aborts atomic.Uint64
	postCommitFailures atomic.Uint64
	replayed atomic.Uint64
	failures map[string]uint64
}

func newRecoveryAdapter(p *Peer, eng *recovery.Engine) *RecoveryAdapter {
	return &RecoveryAdapter{peer:p,engine:eng,failures:map[string]uint64{}}
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
func (p *Peer) RecoveryStats() RecoveryStats {
	if p.recovery==nil{return RecoveryStats{CurrentEpoch:1,Failures:map[string]uint64{}}}
	return p.recovery.Stats()
}

func (p *Peer) currentCarrier() (Carrier,uint64,string) {
	p.mu.Lock(); defer p.mu.Unlock()
	return p.carrier,p.carrierEpoch,p.carrierID
}
func (p *Peer) currentCarrierIdentity()(uint64,string){
	p.mu.Lock();defer p.mu.Unlock()
	return p.carrierEpoch,p.carrierID
}

func (p *Peer) onCarrierFailure(err error) {
	if s:=p.senderNow();s!=nil { s.stop(ErrCarrierUnavailable) }
	select { case p.recoveryNeeded<-err: default: }
}

func (p *Peer) NeedsRecovery() bool {
	if !p.recoveryEnabled{return false}
	s:=p.senderNow()
	return s==nil||s.isStopped()
}

func (p *Peer) DrainRecoverySignals() {
	for {
		select { case <-p.recoveryNeeded: continue; default: return }
	}
}

func (p *Peer) waitForCarrierSwitch(ctx context.Context,oldEpoch uint64,oldCarrier string) error {
	t:=time.NewTimer(p.recoveryRetention);defer t.Stop()
	for {
		e,id:=p.currentCarrierIdentity()
		if e>oldEpoch && id!=oldCarrier{return nil}
		p.carrierSwitchMu.Lock()
		wait:=p.carrierSwitchWait
		p.carrierSwitchMu.Unlock()
		// Re-check after taking the generation channel so a concurrent commit
		// cannot strand this reader on the next generation.
		e,id=p.currentCarrierIdentity()
		if e>oldEpoch && id!=oldCarrier{return nil}
		select {
		case <-ctx.Done():return ctx.Err()
		case <-t.C:return fmt.Errorf("%w: recovery retention expired",ErrCarrierUnavailable)
		case <-wait:
		}
	}
}

func (p *Peer) waitForReplacement(ctx context.Context,oldEpoch uint64,oldCarrier string) error {
	t:=time.NewTimer(p.recoveryRetention);defer t.Stop()
	for {
		e,id:=p.currentCarrierIdentity()
		if e>oldEpoch && id!=oldCarrier{return nil}
		p.replacementMu.Lock()
		wait:=p.replacementWait
		p.replacementMu.Unlock()
		// Re-check after capturing the broadcast channel so a commit racing with
		// this waiter cannot move us onto the next generation and strand us.
		e,id=p.currentCarrierIdentity()
		if e>oldEpoch && id!=oldCarrier{return nil}
		select {
		case <-ctx.Done():return ctx.Err()
		case <-t.C:return fmt.Errorf("%w: recovery retention expired",ErrCarrierUnavailable)
		case <-wait:
		}
	}
}

func (p *Peer) HandleCarrierFrame(ctx context.Context,epoch uint64,carrierID string,fr protocol.Frame) error {
	return p.handleFrameFrom(ctx,epoch,carrierID,fr)
}

func (p *Peer) handleFrameFrom(ctx context.Context,epoch uint64,carrierID string,fr protocol.Frame) error {
	if p.recovery!=nil && !p.recovery.engine.Authorize(epoch,carrierID) {
		return recovery.ErrStaleEpoch
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
			FinSent:fl.finSent,FinRecv:fl.finRecv,FinAcked:fl.finAcked,FinAckSent:fl.finAckSent,
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
	a.attempts.Add(1)
	a.mu.Lock()
	if a.frozen {
		pending:=a.pendingCandidate
		a.mu.Unlock()
		if pending==candidateID{return RecoveryOffer{},recovery.ErrResumeFrozen}
		a.recordFailure("lease_conflict")
		return RecoveryOffer{},recovery.ErrLeaseConflict
	}
	a.mu.Unlock()
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
	a.mu.Lock();a.pendingPlan=plan;a.hasPlan=true;a.mu.Unlock()
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

func recoveryPlanDigest(plan recovery.Plan,snap recovery.Snapshot,routes map[uint64]string,candidateID string,next uint64)(string,error){
	if plan.SessionID==""||candidateID==""||next!=plan.Epoch+1{return "",recovery.ErrStateMismatch}
	nonce:=make(map[uint64]string,len(snap.Flows))
	for _,f:=range snap.Flows{nonce[f.StreamID]=f.OpenNonce}
	flows:=make([]recoveryDigestFlow,0,len(plan.Flows))
	for _,fp:=range plan.Flows{
		n:=nonce[fp.StreamID];route,ok:=routes[fp.StreamID]
		if n==""||!ok{return "",recovery.ErrStateMismatch}
		a:=recoveryDigestSide{ReplayFrom:fp.LocalReplayFrom,AckAdvanceTo:fp.LocalAckAdvanceTo,ReleaseThrough:fp.LocalReleaseThrough,FinAckCanAdvance:fp.LocalFinAckCanAdvance}
		b:=recoveryDigestSide{ReplayFrom:fp.PeerReplayFrom,AckAdvanceTo:fp.PeerAckAdvanceTo,ReleaseThrough:fp.PeerReleaseThrough,FinAckCanAdvance:fp.PeerFinAckCanAdvance}
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
	digest,err:=recoveryPlanDigest(plan,snap,routes,candidateID,next)
	if err!=nil{a.recordFailure("state_mismatch");return RecoveryControl{},err}
	ctl:=RecoveryControl{Phase:RecoveryPhasePrepared,SessionID:plan.SessionID,CandidateID:candidateID,NextEpoch:next,PlanDigest:digest}

	candidateWriter:=&frameWriter{w:c.Out}
	newSender:=newOutboundSender(candidateWriter,p.recoveryEnabled)
	p.mu.Lock()
	runCtx:=p.runCtx
	flowMap:=make(map[uint64]*flow,len(p.flows));for id,fl:=range p.flows{flowMap[id]=fl}
	p.mu.Unlock()
	if runCtx==nil{runCtx=ctx}
	prep:=&preparedRecovery{control:ctl,carrier:c,sender:newSender,runCtx:runCtx,flows:make([]preparedFlowRecovery,0,len(plan.Flows))}
	for _,fp:=range plan.Flows{
		fl:=flowMap[fp.StreamID]
		if fl==nil{return RecoveryControl{},recovery.ErrStateMismatch}
		fl.mu.Lock()
		if !fl.openOK||fl.closed{fl.mu.Unlock();return RecoveryControl{},recovery.ErrStateMismatch}
		if fp.LocalAckAdvanceTo>fl.txNext{fl.mu.Unlock();return RecoveryControl{},recovery.ErrStateMismatch}
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
		prep.flows=append(prep.flows,preparedFlowRecovery{flow:fl,replay:frames,replayed:n,ackAdvance:fp.LocalAckAdvanceTo,finAckAdvance:fp.LocalFinAckCanAdvance,resendFIN:resendFIN,finFinal:final,ackPeerFIN:ackPeerFIN})
	}
	a.mu.Lock()
	if !a.frozen||a.pendingCandidate!=candidateID||!a.hasPlan{a.mu.Unlock();return RecoveryControl{},recovery.ErrNotPrepared}
	a.prepared=prep
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
	a:=p.recovery
	a.postCommitFailures.Add(1)
	a.recordFailure("post_commit_failure")
	a.mu.Lock()
	a.frozen=false;a.pendingCandidate="";a.pendingPlan=recovery.Plan{};a.pendingSnapshot=recovery.Snapshot{};a.pendingRoutes=nil;a.hasPlan=false;a.prepared=nil
	a.mu.Unlock()
	p.onCarrierFailure(err)
	return CommitResult{Committed:true,Epoch:ctl.NextEpoch,CandidateID:ctl.CandidateID,PlanDigest:ctl.PlanDigest},fmt.Errorf("%w: %v",ErrPostCommitFailure,err)
}

func (p *Peer) PublishRecoveryCommit(ctl RecoveryControl)(CommitResult,error){
	if p.recovery==nil{return CommitResult{},errors.New("recovery is disabled")}
	a:=p.recovery
	a.mu.Lock()
	if a.lastCommit.SessionID!=""&&sameRecoveryTransaction(a.lastCommit,ctl)&&a.engine.CurrentEpoch()==ctl.NextEpoch&&a.engine.Owner()==ctl.CandidateID{
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
	a.lastCommit=ctl;a.lastCommit.Phase=RecoveryPhaseCommit
	if a.prepared!=nil{a.prepared.published=true}
	a.mu.Unlock()

	// This is the authority boundary. Old epoch is fenced from here onward.
	// Candidate activation/replay is intentionally deferred until the peer has
	// acknowledged the same committed transaction identity.
	p.writer.mu.Lock();p.writer.w=prep.carrier.Out;p.writer.mu.Unlock()
	p.mu.Lock()
	p.carrier=prep.carrier;p.carrierID=ctl.CandidateID;p.carrierEpoch=ctl.NextEpoch
	oldSender:=p.sender;p.sender=prep.sender
	p.mu.Unlock()
	if oldSender!=nil{oldSender.stop(ErrCarrierUnavailable)}
	return CommitResult{Committed:true,Epoch:ctl.NextEpoch,CandidateID:ctl.CandidateID,PlanDigest:ctl.PlanDigest},nil
}

func (p *Peer) FinalizeRecoveryCommit(ctx context.Context,ctl RecoveryControl) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	a:=p.recovery
	a.mu.Lock()
	prep:=a.prepared
	if prep==nil {
		if a.lastCommit.SessionID!=""&&sameRecoveryTransaction(a.lastCommit,ctl){a.mu.Unlock();return nil}
		a.mu.Unlock();return recovery.ErrNotPrepared
	}
	if !prep.published||!sameRecoveryTransaction(prep.control,ctl){a.mu.Unlock();return recovery.ErrNotPrepared}
	if prep.finalized{a.mu.Unlock();return nil}
	prep.finalized=true
	a.mu.Unlock()

	p.wg.Add(1);go func(){defer p.wg.Done();prep.sender.run(prep.runCtx)}()
	// Only now may the session reader consume the committed carrier.
	p.carrierSwitchMu.Lock();close(p.carrierSwitchWait);p.carrierSwitchWait=make(chan struct{});p.carrierSwitchMu.Unlock()

	if a.postCommitFault!=nil {
		if err:=a.postCommitFault("after_authority_commit");err!=nil{_,e:=p.markPostCommitFailure(err,ctl);return e}
	}
	var replayed uint64
	for _,act:=range prep.flows{
		fl:=act.flow
		if act.ackAdvance>0 { if err:=fl.onAck(act.ackAdvance);err!=nil{_,e:=p.markPostCommitFailure(err,ctl);return e} }
		if act.finAckAdvance { fl.mu.Lock();fl.finAcked=true;fl.mu.Unlock() }
		for _,fr:=range act.replay {
			if a.postCommitFault!=nil {
				if err:=a.postCommitFault("replay_write");err!=nil{_,e:=p.markPostCommitFailure(err,ctl);return e}
			}
			if err:=prep.sender.sendData(ctx,fl,fr);err!=nil{_,e:=p.markPostCommitFailure(err,ctl);return e}
		}
		replayed+=act.replayed
		if act.resendFIN {
			if err:=prep.sender.sendControl(protocol.Frame{Type:protocol.TypeFin,StreamID:fl.id,Offset:act.finFinal});err!=nil{_,e:=p.markPostCommitFailure(err,ctl);return e}
		}
		if act.ackPeerFIN {
			if err:=p.ackRemoteFin(fl);err!=nil{_,e:=p.markPostCommitFailure(err,ctl);return e}
		}
		p.finishIfComplete(fl)
		fl.mu.Lock();closed:=fl.closed;fl.mu.Unlock()
		if !closed{p.ensurePumpsAfterRecovery(prep.runCtx,fl)}
	}
	a.replayed.Add(replayed)
	a.mu.Lock()
	a.frozen=false;a.pendingCandidate="";a.pendingPlan=recovery.Plan{};a.pendingSnapshot=recovery.Snapshot{};a.pendingRoutes=nil;a.hasPlan=false;a.prepared=nil
	a.mu.Unlock()
	p.replacementMu.Lock();close(p.replacementWait);p.replacementWait=make(chan struct{});p.replacementMu.Unlock()
	return nil
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
		// A duplicate/lost-ACK retry acknowledgement for an already committed
		// transaction has no application side effects.
		a:=p.recovery
		if a==nil{return recovery.ErrStateMismatch}
		a.mu.Lock();last:=a.lastCommit;a.mu.Unlock()
		if !sameRecoveryTransaction(last,ctl){return recovery.ErrStateMismatch}
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

func (p *Peer) CommitPreparedRecovery(ctx context.Context,ctl RecoveryControl)(CommitResult,error){
	res,err:=p.PublishRecoveryCommit(ctl)
	if err!=nil{return res,err}
	if err:=p.FinalizeRecoveryCommit(ctx,ctl);err!=nil{return res,err}
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
	next:=a.engine.CurrentEpoch()+1
	a.mu.Unlock()
	a.engine.Abort(next,candidateID)
	a.aborts.Add(1)
	a.mu.Lock()
	if a.pendingCandidate==candidateID {
		a.frozen=false;a.pendingCandidate="";a.pendingPlan=recovery.Plan{};a.pendingSnapshot=recovery.Snapshot{};a.pendingRoutes=nil;a.hasPlan=false;a.prepared=nil
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
	case RecoveryPhasePrepared,RecoveryPhaseCommitReady,RecoveryPhaseCommit,RecoveryPhaseCommitAck:
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
