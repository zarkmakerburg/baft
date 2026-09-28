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
		return to==RecoveryTxnCommitted||to==RecoveryTxnAborted||to==RecoveryTxnFinalizing
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

func (p *Peer) HasCommitUncertainty() bool {
	if p.recovery==nil{return false}
	a:=p.recovery;a.mu.Lock();defer a.mu.Unlock()
	return a.txnState==RecoveryTxnCommitSent||a.txnState==RecoveryTxnUncertain||a.txnState==RecoveryTxnFinalizationUncertain
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


func (p *Peer) MarkFinalizationUncertain(ctl RecoveryControl) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	a:=p.recovery;a.mu.Lock();defer a.mu.Unlock()
	if a.lastCommit.SessionID==""||!sameRecoveryTransaction(a.lastCommit,ctl)||a.engine.CurrentEpoch()!=ctl.NextEpoch||a.engine.Owner()!=ctl.CandidateID{return recovery.ErrStateMismatch}
	entered:=false
	switch a.txnState{
	case RecoveryTxnCommitted,RecoveryTxnFinalizing:
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
	if a.txnState!=RecoveryTxnUncertain&&a.txnState!=RecoveryTxnCommitSent&&a.txnState!=RecoveryTxnFinalizationUncertain{return RecoveryControl{},ErrCommitUncertain}
	if a.uncertain.SessionID==""{return RecoveryControl{},recovery.ErrStateMismatch}
	q:=a.uncertain;q.Phase=RecoveryPhaseStatusQuery;q.Status=RecoveryResolutionNone
	return q,nil
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
		if err:=p.RebindPreparedRecovery(ctx,ctl,c);err!=nil{return err}
		a.mu.Lock();prep=a.prepared;sender:=prep.sender;runCtx:=prep.runCtx;a.mu.Unlock()
		p.mu.Lock();oldSender:=p.sender;p.carrier=c;p.carrierID=ctl.CandidateID;p.carrierEpoch=ctl.NextEpoch;p.carrierGeneration++;p.sender=sender;p.mu.Unlock()
		p.writer.mu.Lock();p.writer.w=c.Out;p.writer.mu.Unlock()
		if oldSender!=nil&&oldSender!=sender{oldSender.stop(ErrCarrierUnavailable)}
		if sender!=nil&&!sender.isStarted(){p.wg.Add(1);go func(){defer p.wg.Done();sender.run(runCtx)}()}
		// Do not wake the session reader until COMMIT status exchange is complete.
		return nil
	}

	newSender:=newOutboundSender(&frameWriter{w:c.Out},p.recoveryEnabled)
	p.mu.Lock()
	flows:=make([]*flow,0,len(p.flows))
	for _,fl:=range p.flows{flows=append(flows,fl)}
	runCtx:=p.runCtx
	oldSender:=p.sender
	p.carrier=c;p.carrierID=ctl.CandidateID;p.carrierEpoch=ctl.NextEpoch;p.carrierGeneration++;p.sender=newSender
	p.mu.Unlock()
	for _,fl:=range flows{
		fl.mu.Lock();open:=fl.openOK&&!fl.closed;fl.mu.Unlock()
		if open{if err:=newSender.addFlow(fl.id);err!=nil{return err}}
	}
	if runCtx==nil{runCtx=ctx}
	if oldSender!=nil{oldSender.stop(ErrCarrierUnavailable)}
	p.wg.Add(1);go func(){defer p.wg.Done();newSender.run(runCtx)}()
	p.writer.mu.Lock();p.writer.w=c.Out;p.writer.mu.Unlock()
	p.carrierSwitchMu.Lock();close(p.carrierSwitchWait);p.carrierSwitchWait=make(chan struct{});p.carrierSwitchMu.Unlock()
	p.replacementMu.Lock();close(p.replacementWait);p.replacementWait=make(chan struct{});p.replacementMu.Unlock()
	return nil
}
