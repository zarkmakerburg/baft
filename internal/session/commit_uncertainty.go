package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/zarkmakerburg/baft/internal/recovery"
)

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

func (p *Peer) NeedsExactTransactionResolution() bool {
	if p.recovery==nil{return false}
	a:=p.recovery
	a.mu.Lock()
	defer a.mu.Unlock()
	switch a.txnState {
	case RecoveryTxnCommitSent, RecoveryTxnUncertain, RecoveryTxnFinalizationUncertain:
		return true
	case RecoveryTxnFinalized:
		// FINALIZED is only an exact-rebind obligation while local activation
		// of that exact transaction is still incomplete. Once activation is
		// complete, the transaction is historical evidence only; a later
		// carrier failure must start a fresh recovery from current flow state.
		return a.prepared!=nil && !a.prepared.activationComplete
	default:
		return false
	}
}

func (p *Peer) NeedsFreshRecovery() bool {
	if !p.recoveryEnabled || p.NeedsExactTransactionResolution(){return false}
	s:=p.senderNow()
	return s==nil||s.isStopped()
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

func (p *Peer) RebindPreparedRecovery(ctx context.Context,ctl RecoveryControl,c Carrier) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	if c.In==nil||c.Out==nil{return errors.New("resolution carrier input/output required")}
	a:=p.recovery
	a.mu.Lock()
	prep:=a.prepared
	if prep==nil||!sameRecoveryTransaction(prep.control,ctl){a.mu.Unlock();return recovery.ErrNotPrepared}
	newSender:=newOutboundSender(&frameWriter{w:c.Out},p.recoveryEnabled)
	for _,act:=range prep.flows{
		if err:=newSender.addFlow(act.flow.id);err!=nil{a.mu.Unlock();return err}
	}
	old:=prep.sender
	prep.carrier=c
	prep.sender=newSender
	prep.rebindPending=true
	if p.runCtx!=nil{prep.runCtx=p.runCtx}else{prep.runCtx=ctx}
	a.mu.Unlock()
	if old!=nil{old.stop(ErrCarrierUnavailable)}
	return nil
}

func (p *Peer) CommitStatusQuery() (RecoveryControl,error) {
	if p.recovery==nil{return RecoveryControl{},errors.New("recovery is disabled")}
	a:=p.recovery;a.mu.Lock();defer a.mu.Unlock()
	switch a.txnState {
	case RecoveryTxnUncertain,RecoveryTxnCommitSent,RecoveryTxnFinalizationUncertain:
		if a.uncertain.SessionID==""{return RecoveryControl{},recovery.ErrStateMismatch}
		q:=a.uncertain;q.Phase=RecoveryPhaseStatusQuery;q.Status=RecoveryResolutionNone
		return q,nil
	case RecoveryTxnFinalized:
		if a.prepared==nil||a.prepared.activationComplete{return RecoveryControl{},ErrCommitUncertain}
		if a.lastCommit.SessionID==""{return RecoveryControl{},recovery.ErrStateMismatch}
		q:=a.lastCommit;q.Phase=RecoveryPhaseStatusQuery;q.Status=RecoveryResolutionNone
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
	if runCtx==nil{runCtx=ctx}
	preparedFlows:=make([]preparedFlowRecovery,0,len(flows))
	for _,fl:=range flows{
		fl.mu.Lock();open:=fl.openOK&&!fl.closed;fl.mu.Unlock()
		if open{
			if err:=newSender.addFlow(fl.id);err!=nil{return err}
			preparedFlows=append(preparedFlows,preparedFlowRecovery{flow:fl,pumpsRestored:true})
		}
	}
	prep=&preparedRecovery{
		control:ctl,carrier:c,sender:newSender,runCtx:runCtx,flows:preparedFlows,
		published:true,finalized:true,activationComplete:true,rebindPending:true,
	}
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
