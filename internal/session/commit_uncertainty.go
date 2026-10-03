package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/recovery"
)

type RecoveryStabilitySnapshot struct {
	ReplayHighWatermark uint64
	ReplayPeerAccepted uint64
	ReplayOutstanding bool
	FinStable bool
	FinalizationStable bool
	ApplicationReady bool
	TransactionStable bool
}

func validTxnTransition(from,to RecoveryTxnState) bool {
	if from==to{return true}
	switch from {
	case RecoveryTxnIdle,RecoveryTxnAborted,RecoveryTxnFinalized:
		return to==RecoveryTxnPreparing
	case RecoveryTxnPreparing:
		return to==RecoveryTxnPrepared||to==RecoveryTxnAborted
	case RecoveryTxnPrepared:
		return to==RecoveryTxnCommitReady||to==RecoveryTxnAborted
	case RecoveryTxnCommitReady:
		return to==RecoveryTxnCommitSent||to==RecoveryTxnCommitted||to==RecoveryTxnAborted
	case RecoveryTxnCommitSent:
		return to==RecoveryTxnUncertain||to==RecoveryTxnCommitted
	case RecoveryTxnUncertain:
		return to==RecoveryTxnCommitted||to==RecoveryTxnAborted||to==RecoveryTxnFinalizing||to==RecoveryTxnFinalizationUncertain
	case RecoveryTxnCommitted:
		return to==RecoveryTxnUncertain||to==RecoveryTxnFinalizing||to==RecoveryTxnFinalizationUncertain
	case RecoveryTxnFinalizing:
		return to==RecoveryTxnFinalized||to==RecoveryTxnUncertain||to==RecoveryTxnFinalizationUncertain
	case RecoveryTxnFinalizationUncertain:
		return to==RecoveryTxnFinalizing||to==RecoveryTxnFinalized
	}
	return false
}

func (a *RecoveryAdapter) transitionLocked(to RecoveryTxnState) error {
	if !validTxnTransition(a.txnState,to){
		return fmt.Errorf("%w: %s -> %s",ErrRecoveryTransition,a.txnState,to)
	}
	a.txnState=to
	return nil
}

func (p *Peer) RecoveryTransactionState() RecoveryTxnState {
	if p.recovery==nil{return RecoveryTxnIdle}
	p.recovery.mu.Lock();defer p.recovery.mu.Unlock()
	return p.recovery.txnState
}

func (p *Peer) RecoveryTransactionIdentity()(RecoveryControl,bool){
	if p.recovery==nil{return RecoveryControl{},false}
	a:=p.recovery
	a.mu.Lock();defer a.mu.Unlock()
	if a.uncertain.SessionID!=""{return a.uncertain,true}
	if a.lastCommit.SessionID!=""&&(a.txnState==RecoveryTxnCommitted||a.txnState==RecoveryTxnFinalizing||a.txnState==RecoveryTxnFinalizationUncertain||a.txnState==RecoveryTxnFinalized){return a.lastCommit,true}
	if a.prepared!=nil{return a.prepared.control,true}
	return RecoveryControl{},false
}

func (p *Peer) RecoveryStability() RecoveryStabilitySnapshot {
	if p.recovery==nil{return RecoveryStabilitySnapshot{}}
	a:=p.recovery
	a.mu.Lock()
	state:=a.txnState
	prep:=a.prepared
	var flows []preparedFlowRecovery
	activationComplete:=false
	activatedGeneration:=uint64(0)
	if prep!=nil{
		flows=append([]preparedFlowRecovery(nil),prep.flows...)
		activationComplete=prep.activationComplete
		activatedGeneration=prep.activatedGeneration
	}
	a.mu.Unlock()

	out:=RecoveryStabilitySnapshot{FinStable:true,FinalizationStable:state==RecoveryTxnFinalized}
	var selectedHigh uint64
	for i:=range flows{
		act:=&flows[i]
		if act.flow==nil{continue}
		act.flow.mu.Lock()
		closed:=act.flow.closed
		acked:=act.flow.txAcked
		finAcked:=act.flow.finAcked
		finAckSent:=act.flow.finAckSent
		finAckConfirmed:=act.flow.finAckConfirmed
		act.flow.mu.Unlock()
		// A closed Flow (finished, reset, or closed from the peer's tombstone)
		// can never prove anything more; counting it pinned the transaction
		// as unstable forever.
		if closed{continue}
		hwm:=act.replayHighWatermark
		if hwm>selectedHigh{selectedHigh=hwm;out.ReplayHighWatermark=hwm;out.ReplayPeerAccepted=acked}
		if hwm>0&&acked<hwm{out.ReplayOutstanding=true}
		// Terminal stability is scoped to obligations captured by this recovery
		// transaction. A later FIN after TransactionStable belongs to current
		// state and is handled by a fresh recovery snapshot if that carrier fails.
		if act.resendFIN&&!finAcked{out.FinStable=false}
		if act.ackPeerFIN&&(!finAckSent||!finAckConfirmed){out.FinStable=false}
	}
	p.replacementMu.Lock()
	readyGeneration:=p.replacementReadyGeneration
	p.replacementMu.Unlock()
	if prep==nil{
		out.ApplicationReady=out.FinalizationStable
	}else{
		out.ApplicationReady=activationComplete&&activatedGeneration>0&&readyGeneration>=activatedGeneration
	}
	out.TransactionStable=out.FinalizationStable&&!out.ReplayOutstanding&&out.FinStable
	return out
}

func (p *Peer) NeedsExactTransactionResolution() bool {
	if p.recovery==nil{return false}
	a:=p.recovery
	a.mu.Lock()
	state:=a.txnState
	prepPresent:=a.prepared!=nil
	peerRequires:=a.peerRequiresExact
	a.mu.Unlock()
	switch state {
	case RecoveryTxnCommitSent,RecoveryTxnUncertain,RecoveryTxnCommitted,RecoveryTxnFinalizing,RecoveryTxnFinalizationUncertain:
		return true
	case RecoveryTxnFinalized:
		if peerRequires{return true}
		if !prepPresent{return false}
		st:=p.RecoveryStability()
		// Carrier Active != Application Ready, and Application Ready !=
		// Transaction Stable.  Any unproven replay/terminal obligation keeps
		// the exact Session/Epoch/Candidate/Plan transaction pinned.
		return !st.TransactionStable||!st.ApplicationReady
	default:
		return false
	}
}

func (p *Peer) NeedsFreshRecovery() bool {
	if !p.recoveryEnabled||p.NeedsExactTransactionResolution(){return false}
	s:=p.senderNow()
	if s!=nil&&!s.isStopped(){return false}
	a:=p.recovery
	a.mu.Lock();state:=a.txnState;a.mu.Unlock()
	if state==RecoveryTxnFinalized{
		st:=p.RecoveryStability()
		return st.TransactionStable&&st.ApplicationReady
	}
	return true
}

// HasCommitUncertainty is retained for callers/tests that still use the older
// name. Its semantics are now deliberately narrow: unresolved exact
// transaction obligation only, never a stable FINALIZED historical commit.
func (p *Peer) HasCommitUncertainty() bool {
	return p.NeedsExactTransactionResolution()
}

func (p *Peer) MarkRecoveryPrepared(ctl RecoveryControl) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	a:=p.recovery;a.mu.Lock();defer a.mu.Unlock()
	if a.prepared==nil||!sameRecoveryTransaction(a.prepared.control,ctl){return recovery.ErrNotPrepared}
	if a.txnState==RecoveryTxnPreparing{return a.transitionLocked(RecoveryTxnPrepared)}
	if a.txnState==RecoveryTxnPrepared{return nil}
	return fmt.Errorf("%w: prepared from %s",ErrRecoveryTransition,a.txnState)
}

func (p *Peer) MarkRecoveryCommitReady(ctl RecoveryControl) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	a:=p.recovery;a.mu.Lock();defer a.mu.Unlock()
	if a.prepared==nil||!sameRecoveryTransaction(a.prepared.control,ctl){return recovery.ErrNotPrepared}
	if a.txnState==RecoveryTxnPrepared{
		return a.transitionLocked(RecoveryTxnCommitReady)
	}
	if a.txnState==RecoveryTxnCommitReady{return nil}
	return fmt.Errorf("%w: commit-ready from %s",ErrRecoveryTransition,a.txnState)
}

func (p *Peer) MarkCommitSent(ctl RecoveryControl) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	a:=p.recovery;a.mu.Lock();defer a.mu.Unlock()
	if a.prepared==nil||!sameRecoveryTransaction(a.prepared.control,ctl){return recovery.ErrNotPrepared}
	if a.txnState==RecoveryTxnCommitReady {
		if err:=a.transitionLocked(RecoveryTxnCommitSent);err!=nil{return err}
		a.uncertain=ctl
		a.uncertain.Phase=RecoveryPhaseCommit
		return nil
	}
	if a.txnState==RecoveryTxnCommitSent&&sameRecoveryTransaction(a.uncertain,ctl){return nil}
	return fmt.Errorf("%w: commit-sent from %s",ErrRecoveryTransition,a.txnState)
}

func (p *Peer) MarkCommitUncertain(ctl RecoveryControl) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	a:=p.recovery;a.mu.Lock();defer a.mu.Unlock()
	if !sameRecoveryTransaction(ctl,a.uncertain)&&!(a.lastCommit.SessionID!=""&&sameRecoveryTransaction(ctl,a.lastCommit)){
		return recovery.ErrStateMismatch
	}
	entered:=false
	switch a.txnState{
	case RecoveryTxnCommitSent,RecoveryTxnCommitted,RecoveryTxnFinalizing:
		if err:=a.transitionLocked(RecoveryTxnUncertain);err!=nil{return err}
		entered=true
	case RecoveryTxnUncertain:
	default:
		return fmt.Errorf("%w: uncertain from %s",ErrRecoveryTransition,a.txnState)
	}
	a.uncertain=ctl;a.uncertain.Phase=RecoveryPhaseCommit
	if entered{a.commitUncertain.Add(1)}
	return nil
}


func (p *Peer) MarkFinalizationStarted(ctl RecoveryControl) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	a:=p.recovery;a.mu.Lock();defer a.mu.Unlock()
	if a.lastCommit.SessionID==""||!sameRecoveryTransaction(a.lastCommit,ctl)||a.engine.CurrentEpoch()!=ctl.NextEpoch||a.engine.Owner()!=ctl.CandidateID{return recovery.ErrStateMismatch}
	switch a.txnState{
	case RecoveryTxnCommitted,RecoveryTxnUncertain:
		if err:=a.transitionLocked(RecoveryTxnFinalizing);err!=nil{return err}
	case RecoveryTxnFinalizing,RecoveryTxnFinalizationUncertain:
		// exact duplicate/retry
	case RecoveryTxnFinalized:
		return nil
	default:
		return fmt.Errorf("%w: finalization start from %s",ErrRecoveryTransition,a.txnState)
	}
	a.uncertain=ctl;a.uncertain.Phase=RecoveryPhaseFinalize
	a.frozen=true
	return nil
}

func (p *Peer) MarkFinalizationUncertain(ctl RecoveryControl) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	a:=p.recovery;a.mu.Lock();defer a.mu.Unlock()
	if a.lastCommit.SessionID==""||!sameRecoveryTransaction(a.lastCommit,ctl)||a.engine.CurrentEpoch()!=ctl.NextEpoch||a.engine.Owner()!=ctl.CandidateID{return recovery.ErrStateMismatch}
	entered:=false
	switch a.txnState{
	case RecoveryTxnCommitted,RecoveryTxnFinalizing,RecoveryTxnUncertain:
		if err:=a.transitionLocked(RecoveryTxnFinalizationUncertain);err!=nil{return err}
		entered=true
	case RecoveryTxnFinalizationUncertain:
	case RecoveryTxnFinalized:
		return nil
	default:
		return fmt.Errorf("%w: finalization uncertain from %s",ErrRecoveryTransition,a.txnState)
	}
	a.uncertain=ctl;a.uncertain.Phase=RecoveryPhaseFinalize
	a.frozen=true
	if a.prepared!=nil{a.prepared.finalizing=false}
	if entered{a.finalizationUncertain.Add(1)}
	return nil
}

func (p *Peer) CompleteRecoveryFinalization(ctl RecoveryControl) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	a:=p.recovery
	a.mu.Lock()
	if a.lastCommit.SessionID==""||!sameRecoveryTransaction(a.lastCommit,ctl)||a.engine.CurrentEpoch()!=ctl.NextEpoch||a.engine.Owner()!=ctl.CandidateID{
		a.mu.Unlock();return recovery.ErrStateMismatch
	}
	if a.txnState==RecoveryTxnFinalized{a.mu.Unlock();return nil}
	switch a.txnState{
	case RecoveryTxnCommitted,RecoveryTxnFinalizing,RecoveryTxnFinalizationUncertain,RecoveryTxnUncertain:
		a.txnState=RecoveryTxnFinalized
	default:
		a.mu.Unlock();return fmt.Errorf("%w: finalize completion from %s",ErrRecoveryTransition,a.txnState)
	}
	// FINALIZED is distributed transaction evidence only. Keep the flow set
	// frozen until local replay/FIN activation completes. This prevents fresh
	// OPEN/data-path mutation from racing the evidence-based replay frontier.
	a.uncertain=RecoveryControl{}
	if a.prepared!=nil{a.prepared.finalizing=false;a.prepared.finalized=true}
	a.mu.Unlock()
	return nil
}

func clonePreparedFlowForIncarnation(in preparedFlowRecovery) preparedFlowRecovery {
	out:=preparedFlowRecovery{
		flow:in.flow,replayed:in.replayed,replayFrom:in.replayFrom,replayHighWatermark:in.replayHighWatermark,ackAdvance:in.ackAdvance,
		creditAdvance:in.creditAdvance,finAckAdvance:in.finAckAdvance,
		finAckConfirmAdvance:in.finAckConfirmAdvance,resendFIN:in.resendFIN,
		finFinal:in.finFinal,ackPeerFIN:in.ackPeerFIN,repeatOpenOK:in.repeatOpenOK,
	}
	out.replay=make([]protocol.Frame,len(in.replay))
	for i,fr:=range in.replay{
		out.replay[i]=fr
		out.replay[i].Payload=append([]byte(nil),fr.Payload...)
	}
	return out
}

// rebindEntryForLiveFlow describes a live Flow outside the transaction's own
// Flow set for an exact rebind. A listener Flow the dialer never granted
// window to may still be waiting for an OPEN_OK lost with the failed carrier:
// the rebind repeats that OPEN_OK (the dialer completes the Flow, or resets it
// if it abandoned the OPEN) instead of replaying data, which it cannot have
// sent without window.
func (p *Peer) rebindEntryForLiveFlow(fl *flow)(preparedFlowRecovery,bool){
	fl.mu.Lock()
	open:=fl.openOK&&!fl.closed
	acked,next,granted:=fl.txAcked,fl.txNext,fl.peerMax>0
	fl.mu.Unlock()
	if !open{return preparedFlowRecovery{},false}
	if p.role==Listener&&!granted{return preparedFlowRecovery{flow:fl,repeatOpenOK:true,pumpsRestored:true},true}
	return preparedFlowRecovery{flow:fl,replayFrom:acked,replayHighWatermark:next,pumpsRestored:true},true
}

func clonePreparedForRebind(old *preparedRecovery,c Carrier,sender *outboundSender,runCtx context.Context) *preparedRecovery {
	if old==nil{return nil}
	flows:=make([]preparedFlowRecovery,len(old.flows))
	for i:=range old.flows{flows[i]=clonePreparedFlowForIncarnation(old.flows[i])}
	inc:=old.incarnation+1
	if inc==0{inc=1}
	return &preparedRecovery{
		control:old.control,incarnation:inc,physicalCarrierInstanceID:physicalCarrierInstanceID(old.control.NextEpoch,inc),carrier:c,sender:sender,runCtx:runCtx,flows:flows,
		published:old.published,finalized:old.finalized,activationComplete:old.activationComplete,
		rebindPending:true,
	}
}

func (p *Peer) RebindPreparedRecovery(ctx context.Context,ctl RecoveryControl,c Carrier) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	if c.In==nil||c.Out==nil{return errors.New("resolution carrier input/output required")}
	// ctx belongs to one physical carrier. Whatever outlives it (the new
	// sender's pumps, the prepared state) is owned by the logical Session.
	runCtx,ok:=p.logicalSessionContext()
	if !ok{return ErrLogicalSessionContextUnavailable}
	a:=p.recovery
	a.ownershipMu.Lock()
	defer a.ownershipMu.Unlock()
	p.mu.Lock()
	live:=make([]*flow,0,len(p.flows))
	for _,fl:=range p.flows{live=append(live,fl)}
	p.mu.Unlock()
	a.mu.Lock()
	oldPrep:=a.prepared
	if oldPrep==nil||!sameRecoveryTransaction(oldPrep.control,ctl){a.mu.Unlock();return recovery.ErrNotPrepared}
	newSender:=newOutboundSender(&frameWriter{w:c.Out},p.recoveryEnabled)
	known:=make(map[uint64]bool,len(oldPrep.flows))
	for _,act:=range oldPrep.flows{
		known[act.flow.id]=true
		if err:=newSender.addFlow(act.flow.id);err!=nil{a.mu.Unlock();return err}
	}
	// After activation the transaction's Flow set is no longer the live set:
	// Flows opened since then have their own unacknowledged data and FINs on
	// the failed carrier. Carry them like RebindCommittedCarrier's synthetic
	// rebind does, or their first frame fails the new sender and their
	// in-flight data is never replayed.
	var extra []preparedFlowRecovery
	if oldPrep.activationComplete{
		for _,fl:=range live{
			if known[fl.id]{continue}
			entry,ok:=p.rebindEntryForLiveFlow(fl)
			if !ok{continue}
			if err:=newSender.addFlow(fl.id);err!=nil{a.mu.Unlock();return err}
			extra=append(extra,entry)
		}
	}
	newPrep:=clonePreparedForRebind(oldPrep,c,newSender,runCtx)
	newPrep.flows=append(newPrep.flows,extra...)
	p.bindRecoverySenderDiagnostic(newSender,newPrep.control,newPrep.incarnation,0)
	p.traceRecoveryDiagnostic("REBIND_CREATED",SenderStopUnknown,nil,"",newSender,newPrep.control,newPrep.incarnation,0)
	oldSender:=oldPrep.sender
	a.prepared=newPrep
	a.mu.Unlock()
	if oldSender!=nil{oldSender.stopWithSource(SenderStopExplicitReplace,ErrCarrierUnavailable)}
	return nil
}

func (p *Peer) CommitStatusQuery() (RecoveryControl,error) {
	if p.recovery==nil{return RecoveryControl{},errors.New("recovery is disabled")}
	a:=p.recovery
	a.mu.Lock()
	state:=a.txnState
	uncertain:=a.uncertain
	last:=a.lastCommit
	prepPresent:=a.prepared!=nil
	peerRequires:=a.peerRequiresExact
	a.mu.Unlock()
	switch state {
	case RecoveryTxnUncertain,RecoveryTxnCommitSent,RecoveryTxnFinalizationUncertain:
		if uncertain.SessionID==""{return RecoveryControl{},recovery.ErrStateMismatch}
		q:=uncertain;q.Phase=RecoveryPhaseStatusQuery;q.Status=RecoveryResolutionNone
		return q,nil
	case RecoveryTxnCommitted,RecoveryTxnFinalizing:
		if last.SessionID==""{return RecoveryControl{},recovery.ErrStateMismatch}
		q:=last;q.Phase=RecoveryPhaseStatusQuery;q.Status=RecoveryResolutionNone
		return q,nil
	case RecoveryTxnFinalized:
		if peerRequires{
			if last.SessionID==""{return RecoveryControl{},recovery.ErrStateMismatch}
			q:=last;q.Phase=RecoveryPhaseStatusQuery;q.Status=RecoveryResolutionNone
			return q,nil
		}
		if !prepPresent{return RecoveryControl{},ErrCommitUncertain}
		st:=p.RecoveryStability()
		if st.TransactionStable&&st.ApplicationReady{return RecoveryControl{},ErrCommitUncertain}
		if last.SessionID==""{return RecoveryControl{},recovery.ErrStateMismatch}
		q:=last;q.Phase=RecoveryPhaseStatusQuery;q.Status=RecoveryResolutionNone
		return q,nil
	default:
		return RecoveryControl{},ErrCommitUncertain
	}
}

func (p *Peer) EvaluateCommitStatus(query RecoveryControl)(RecoveryControl,error){
	if p.recovery==nil{return RecoveryControl{},errors.New("recovery is disabled")}
	if query.Phase!=RecoveryPhaseStatusQuery||query.SessionID!=p.SessionID()||query.CandidateID==""||query.NextEpoch==0||query.PlanDigest==""{
		return RecoveryControl{},recovery.ErrStateMismatch
	}
	a:=p.recovery
	a.mu.Lock();defer a.mu.Unlock()
	reply:=query;reply.Phase=RecoveryPhaseStatusReply
	current:=a.engine.CurrentEpoch();owner:=a.engine.Owner()
	if a.lastCommit.SessionID!=""&&sameRecoveryTransaction(a.lastCommit,query){
		if current==query.NextEpoch&&owner==query.CandidateID{
			if a.txnState==RecoveryTxnFinalized{reply.Status=RecoveryResolutionFinalized}else{reply.Status=RecoveryResolutionCommitted}
			return reply,nil
		}
		reply.Status=RecoveryResolutionConflict
		return reply,nil
	}
	if a.lastCommit.NextEpoch==query.NextEpoch&&a.lastCommit.SessionID!=""&&!sameRecoveryTransaction(a.lastCommit,query){
		reply.Status=RecoveryResolutionConflict
		a.resolutionConflict.Add(1)
		return reply,nil
	}
	if a.lastNotCommitted.SessionID!=""&&sameRecoveryTransaction(a.lastNotCommitted,query){
		reply.Status=RecoveryResolutionNotCommitted
		return reply,nil
	}
	if current==query.NextEpoch-1 {
		samePending:=false
		if a.prepared!=nil&&sameRecoveryTransaction(a.prepared.control,query)&&!a.prepared.published{samePending=true}
		if a.uncertain.SessionID!=""&&sameRecoveryTransaction(a.uncertain,query)&&a.lastCommit.SessionID==""{samePending=true}
		if samePending{
			reply.Status=RecoveryResolutionNotCommitted
			return reply,nil
		}
		reply.Status=RecoveryResolutionUnknown
		return reply,nil
	}
	reply.Status=RecoveryResolutionConflict
	return reply,nil
}

func (p *Peer) ValidateStatusReply(reply RecoveryControl) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	if reply.Phase!=RecoveryPhaseStatusReply{return recovery.ErrStateMismatch}
	a:=p.recovery;a.mu.Lock();defer a.mu.Unlock()
	matched:=a.uncertain.SessionID!=""&&sameRecoveryTransaction(a.uncertain,reply)
	if !matched&&reply.Status==RecoveryResolutionNotCommitted&&a.lastNotCommitted.SessionID!="" {
		matched=sameRecoveryTransaction(a.lastNotCommitted,reply)
	}
	if !matched&&(reply.Status==RecoveryResolutionCommitted||reply.Status==RecoveryResolutionFinalized)&&a.lastCommit.SessionID!="" {
		matched=sameRecoveryTransaction(a.lastCommit,reply)
	}
	if !matched&&(reply.Status==RecoveryResolutionConflict||reply.Status==RecoveryResolutionUnknown)&&a.lastResolutionConflict.SessionID!="" {
		matched=sameRecoveryTransaction(a.lastResolutionConflict,reply)
	}
	// A ResolvePrevious query names lastCommit, so its answer may be a
	// conflict about lastCommit.
	if !matched&&(reply.Status==RecoveryResolutionConflict||reply.Status==RecoveryResolutionUnknown)&&a.peerRequiresExact&&a.lastCommit.SessionID!="" {
		matched=sameRecoveryTransaction(a.lastCommit,reply)
	}
	if !matched{return recovery.ErrStateMismatch}
	switch reply.Status{
	case RecoveryResolutionCommitted,RecoveryResolutionFinalized,RecoveryResolutionNotCommitted,RecoveryResolutionConflict,RecoveryResolutionUnknown:
		return nil
	default:return recovery.ErrStateMismatch
	}
}

func (p *Peer) ResolveNotCommitted(reply RecoveryControl) error {
	if err:=p.ValidateStatusReply(reply);err!=nil{return err}
	if reply.Status!=RecoveryResolutionNotCommitted{return recovery.ErrStateMismatch}
	a:=p.recovery
	a.mu.Lock()
	if a.lastNotCommitted.SessionID!=""&&sameRecoveryTransaction(a.lastNotCommitted,reply){
		a.mu.Unlock();return nil
	}
	if a.engine.CurrentEpoch()!=reply.NextEpoch-1{a.mu.Unlock();return recovery.ErrStateMismatch}
	if a.lastCommit.SessionID!=""&&sameRecoveryTransaction(a.lastCommit,reply){a.mu.Unlock();return recovery.ErrStateMismatch}
	next:=reply.NextEpoch
	candidate:=reply.CandidateID
	a.mu.Unlock()
	a.engine.Abort(next,candidate)
	a.aborts.Add(1);a.resolutionNotCommitted.Add(1)
	a.mu.Lock()
	a.lastNotCommitted=reply;a.lastNotCommitted.Phase=RecoveryPhaseStatusReply;a.lastNotCommitted.Status=RecoveryResolutionNotCommitted
	a.frozen=false;a.pendingCandidate="";a.pendingPlan=recovery.Plan{};a.pendingSnapshot=recovery.Snapshot{};a.pendingRoutes=nil;a.hasPlan=false;a.prepared=nil;a.uncertain=RecoveryControl{}
	a.txnState=RecoveryTxnAborted
	a.mu.Unlock()
	return nil
}

func (p *Peer) NoteCommittedResolution(reply RecoveryControl) error {
	if err:=p.ValidateStatusReply(reply);err!=nil{return err}
	if reply.Status!=RecoveryResolutionCommitted{return recovery.ErrStateMismatch}
	a:=p.recovery
	a.mu.Lock()
	if a.lastResolutionCommitted.SessionID!=""&&sameRecoveryTransaction(a.lastResolutionCommitted,reply){a.mu.Unlock();return nil}
	a.lastResolutionCommitted=reply
	a.mu.Unlock()
	a.resolutionCommitted.Add(1)
	return nil
}


func (p *Peer) NoteFinalizedResolution(reply RecoveryControl) error {
	if err:=p.ValidateStatusReply(reply);err!=nil{return err}
	if reply.Status!=RecoveryResolutionFinalized{return recovery.ErrStateMismatch}
	a:=p.recovery
	a.mu.Lock()
	if a.lastResolutionCommitted.SessionID!=""&&sameRecoveryTransaction(a.lastResolutionCommitted,reply)&&a.txnState==RecoveryTxnFinalized{a.mu.Unlock();return nil}
	a.lastResolutionCommitted=reply
	a.mu.Unlock()
	a.resolutionFinalized.Add(1)
	return nil
}

func (p *Peer) NoteResolutionConflict(reply RecoveryControl) error {
	if err:=p.ValidateStatusReply(reply);err!=nil{return err}
	if reply.Status!=RecoveryResolutionConflict&&reply.Status!=RecoveryResolutionUnknown{return recovery.ErrStateMismatch}
	a:=p.recovery
	a.mu.Lock()
	// The listener does not hold this transaction, so it cannot be what it
	// asked to resolve; fall back to fresh-epoch recovery.
	a.peerRequiresExact=false
	if a.lastResolutionConflict.SessionID!=""&&sameRecoveryTransaction(a.lastResolutionConflict,reply){a.mu.Unlock();return ErrCommitUncertain}
	a.lastResolutionConflict=reply
	a.mu.Unlock()
	a.resolutionConflict.Add(1)
	return ErrCommitUncertain
}

func (p *Peer) RememberNotCommitted(query RecoveryControl) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	a:=p.recovery;a.mu.Lock()
	if a.lastNotCommitted.SessionID!=""&&sameRecoveryTransaction(a.lastNotCommitted,query){a.mu.Unlock();return nil}
	if a.engine.CurrentEpoch()!=query.NextEpoch-1{a.mu.Unlock();return recovery.ErrStateMismatch}
	if a.lastCommit.SessionID!=""&&sameRecoveryTransaction(a.lastCommit,query){a.mu.Unlock();return recovery.ErrStateMismatch}
	next:=query.NextEpoch;candidate:=query.CandidateID
	a.mu.Unlock()
	a.engine.Abort(next,candidate)
	a.aborts.Add(1)
	a.mu.Lock()
	a.lastNotCommitted=query;a.lastNotCommitted.Phase=RecoveryPhaseStatusReply;a.lastNotCommitted.Status=RecoveryResolutionNotCommitted
	a.frozen=false;a.pendingCandidate="";a.pendingPlan=recovery.Plan{};a.pendingSnapshot=recovery.Snapshot{};a.pendingRoutes=nil;a.hasPlan=false;a.prepared=nil;a.uncertain=RecoveryControl{}
	a.txnState=RecoveryTxnAborted
	a.mu.Unlock()
	return nil
}


func (p *Peer) EnsureRecoverySignal(err error) {
	if p==nil||!p.recoveryEnabled{return}
	if err==nil{err=ErrCarrierUnavailable}
	select{case p.recoveryNeeded<-err:default:}
}

func (p *Peer) RebindCommittedCarrier(ctx context.Context,ctl RecoveryControl,c Carrier) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	if c.In==nil||c.Out==nil{return errors.New("resolution carrier input/output required")}
	if _,ok:=p.logicalSessionContext();!ok{return ErrLogicalSessionContextUnavailable}
	a:=p.recovery
	a.mu.Lock()
	if a.lastCommit.SessionID==""||!sameRecoveryTransaction(a.lastCommit,ctl)||a.engine.CurrentEpoch()!=ctl.NextEpoch||a.engine.Owner()!=ctl.CandidateID{
		a.mu.Unlock();return recovery.ErrStateMismatch
	}
	prep:=a.prepared
	a.mu.Unlock()

	if prep!=nil {
		// Status-resolution carrier remains control-plane-only until the exact
		// transaction has distributed FINALIZED proof. Rebind only prepared
		// replay state here; FinalizeRecoveryCommit performs the data-plane
		// activation after FINALIZE/FINALIZE_ACK.
		return p.RebindPreparedRecovery(ctx,ctl,c)
	}

	// A finalized transaction may be rebound after its prior physical carrier
	// disappeared even if no replay actions remain. Build a synthetic prepared
	// carrier so activation is still deferred until status/finalization control
	// exchange is complete; never let application frames race control frames.
	newSender:=newOutboundSender(&frameWriter{w:c.Out},p.recoveryEnabled)
	p.mu.Lock()
	flows:=make([]*flow,0,len(p.flows))
	for _,fl:=range p.flows{flows=append(flows,fl)}
	runCtx:=p.runCtx
	p.mu.Unlock()
	if runCtx==nil{return ErrLogicalSessionContextUnavailable}
	preparedFlows:=make([]preparedFlowRecovery,0,len(flows))
	for _,fl:=range flows{
		entry,ok:=p.rebindEntryForLiveFlow(fl)
		if !ok{continue}
		if err:=newSender.addFlow(fl.id);err!=nil{return err}
		preparedFlows=append(preparedFlows,entry)
	}
	prep=&preparedRecovery{
		control:ctl,incarnation:1,physicalCarrierInstanceID:physicalCarrierInstanceID(ctl.NextEpoch,1),carrier:c,sender:newSender,runCtx:runCtx,flows:preparedFlows,
		published:true,finalized:true,activationComplete:true,rebindPending:true,
	}
	p.bindRecoverySenderDiagnostic(newSender,ctl,1,0)
	p.traceRecoveryDiagnostic("REBIND_CREATED",SenderStopUnknown,nil,"",newSender,ctl,1,0)
	a.mu.Lock()
	if a.prepared==nil{
		a.prepared=prep
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	// Another exact rebind won the race; update that prepared transaction
	// through the normal control-only rebind path.
	return p.RebindPreparedRecovery(ctx,ctl,c)
}

// PreviousTransactionPinned reports that this side refuses a fresh epoch
// because its last finalized transaction still has an unproven obligation.
// A listener answers such an offer with RecoveryOffer.ResolvePrevious.
func (p *Peer) PreviousTransactionPinned() bool {
	return p.RecoveryTransactionState()==RecoveryTxnFinalized&&p.NeedsExactTransactionResolution()
}

// DeferToPreviousTransaction withdraws the fresh candidate the listener
// refused with ResolvePrevious and pins the last finalized transaction, so
// the next attempt resolves it exactly instead of offering another epoch.
// The candidate never prepared, so the finalized transaction is still the
// current authority. Without that evidence it only aborts the candidate.
func (p *Peer) DeferToPreviousTransaction(candidateID string) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	a:=p.recovery
	a.mu.Lock()
	if !a.frozen||a.pendingCandidate!=candidateID||a.txnState!=RecoveryTxnPreparing{a.mu.Unlock();return recovery.ErrStateMismatch}
	last:=a.lastCommit
	current:=last.SessionID!=""&&a.engine.CurrentEpoch()==last.NextEpoch&&a.engine.Owner()==last.CandidateID
	a.mu.Unlock()
	if !current{
		p.AbortRecovery(candidateID)
		return recovery.ErrStateMismatch
	}
	a.engine.Abort(a.engine.CurrentEpoch()+1,candidateID)
	a.aborts.Add(1)
	a.mu.Lock()
	if a.pendingCandidate==candidateID{
		a.frozen=false;a.pendingCandidate="";a.pendingPlan=recovery.Plan{};a.pendingSnapshot=recovery.Snapshot{};a.pendingPeerSnapshot=recovery.Snapshot{};a.pendingRoutes=nil;a.hasPlan=false;a.uncertain=RecoveryControl{}
		a.txnState=RecoveryTxnFinalized
		a.peerRequiresExact=true
	}
	a.mu.Unlock()
	return nil
}

// ClearPeerExactRequirement records that the transaction the listener asked
// for has been resolved.
func (p *Peer) ClearPeerExactRequirement() {
	if p.recovery==nil{return}
	a:=p.recovery
	a.mu.Lock();a.peerRequiresExact=false;a.mu.Unlock()
}

// FreezeForExactResolution stops new Flows from opening while a dialer
// resolves an exact transaction. The rebind captures the live Flow set on a
// new sender; a Flow opened meanwhile would be registered only with the dead
// sender and its first frame would fail the new carrier. Unanswered OPENs are
// abandoned exactly as BeginRecovery does: their answer, if any, was lost with
// the failed carrier, and the listener repeats it on the new one, which makes
// this side reset the Flow there. The returned release undoes the freeze if
// activation did not already lift it.
func (p *Peer) FreezeForExactResolution() func() {
	if p.recovery==nil||p.role!=Dialer{return func(){}}
	a:=p.recovery
	p.recoveryGate.Lock()
	a.mu.Lock()
	took:=!a.frozen
	if took{a.frozen=true;a.resolutionFreeze=true}
	a.mu.Unlock()
	p.abandonUnansweredOpens()
	p.recoveryGate.Unlock()
	return func(){
		if !took{return}
		a.mu.Lock()
		if a.resolutionFreeze{
			a.resolutionFreeze=false
			if a.pendingCandidate==""&&a.txnState==RecoveryTxnFinalized{a.frozen=false}
		}
		a.mu.Unlock()
	}
}
