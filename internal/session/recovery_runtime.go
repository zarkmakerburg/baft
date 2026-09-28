package session

import (
	"bytes"
	"context"
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
)

var ErrCarrierUnavailable = errors.New("session carrier unavailable")

type RecoveryOffer struct {
	CandidateID string            `json:"candidate_id"`
	NextEpoch   uint64            `json:"next_epoch"`
	Snapshot    recovery.Snapshot `json:"snapshot"`
	Routes      map[uint64]string `json:"routes"`
}

type RecoveryDone struct {
	CandidateID string `json:"candidate_id"`
	NextEpoch uint64 `json:"next_epoch"`
}

type RecoveryStats struct {
	Attempts      uint64
	Commits       uint64
	Aborts        uint64
	CurrentEpoch  uint64
	ReplayedBytes uint64
	Failures      map[string]uint64
}

type RecoveryAdapter struct {
	peer *Peer
	beforeCommit func() error
	engine *recovery.Engine
	mu sync.Mutex
	frozen bool
	pendingCandidate string
	pendingPlan recovery.Plan
	hasPlan bool
	attempts atomic.Uint64
	commits atomic.Uint64
	aborts atomic.Uint64
	replayed atomic.Uint64
	failures map[string]uint64
}

func newRecoveryAdapter(p *Peer, eng *recovery.Engine) *RecoveryAdapter {
	return &RecoveryAdapter{peer:p,engine:eng,failures:map[string]uint64{}}
}

func (a *RecoveryAdapter) IsFrozen() bool {
	a.mu.Lock(); defer a.mu.Unlock()
	return a.frozen
}

func (a *RecoveryAdapter) recordFailure(reason string) {
	switch reason {
	case "candidate_setup","snapshot_exchange","peer_restart","state_mismatch","replay_unavailable","lease_conflict","commit":
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
		Attempts:a.attempts.Load(),Commits:a.commits.Load(),Aborts:a.aborts.Load(),
		CurrentEpoch:a.engine.CurrentEpoch(),ReplayedBytes:a.replayed.Load(),Failures:fail,
	}
}

func (p *Peer) RecoveryNeeded() <-chan error { return p.recoveryNeeded }

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
	a.mu.Lock();a.frozen=true;a.pendingCandidate=candidateID;a.pendingPlan=recovery.Plan{};a.hasPlan=false;a.mu.Unlock()
	return RecoveryOffer{CandidateID:candidateID,NextEpoch:next,Snapshot:snap,Routes:routes},nil
}

func (p *Peer) ReconcileRecovery(candidateID string,peer RecoveryOffer) error {
	if p.recovery==nil{return errors.New("recovery is disabled")}
	a:=p.recovery
	a.mu.Lock()
	if !a.frozen||a.pendingCandidate!=candidateID { a.mu.Unlock();return recovery.ErrNotPrepared }
	a.mu.Unlock()
	if peer.CandidateID!=candidateID||peer.NextEpoch!=a.engine.CurrentEpoch()+1 {
		a.recordFailure("state_mismatch");return recovery.ErrStateMismatch
	}
	local,routes,err:=p.recoverySnapshot();if err!=nil{a.recordFailure("snapshot_exchange");return err}
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

func (p *Peer) CommitRecovery(ctx context.Context,candidateID string,c Carrier) error {
	if c.In==nil||c.Out==nil{return errors.New("candidate carrier input/output required")}
	if p.recovery==nil{return errors.New("recovery is disabled")}
	a:=p.recovery
	a.mu.Lock()
	if !a.frozen||a.pendingCandidate!=candidateID||!a.hasPlan{a.mu.Unlock();return recovery.ErrNotPrepared}
	plan:=a.pendingPlan
	a.mu.Unlock()
	next:=a.engine.CurrentEpoch()+1
	if err:=p.validateReplayPlan(plan);err!=nil{a.recordFailure("replay_unavailable");return err}
	if a.beforeCommit!=nil {
		if err:=a.beforeCommit();err!=nil{a.recordFailure("commit");return err}
	}
	if err:=a.engine.Commit(next,candidateID,plan);err!=nil{a.recordFailure("commit");return err}

	p.writer.mu.Lock();p.writer.w=c.Out;p.writer.mu.Unlock()
	newSender:=newOutboundSender(&p.writer,p.recoveryEnabled)
	p.mu.Lock()
	p.carrier=c;p.carrierID=candidateID;p.carrierEpoch=next
	oldSender:=p.sender
	p.sender=newSender
	runCtx:=p.runCtx
	flows:=make([]*flow,0,len(p.flows));for _,fl:=range p.flows{flows=append(flows,fl)}
	p.mu.Unlock()
	if runCtx==nil{runCtx=ctx}
	if oldSender!=nil{oldSender.stop(ErrCarrierUnavailable)}
	for _,fl:=range flows {
		fl.mu.Lock();open:=fl.openOK&&!fl.closed;fl.mu.Unlock()
		if open { if err:=newSender.addFlow(fl.id);err!=nil{return err} }
	}
	p.wg.Add(1);go func(){defer p.wg.Done();newSender.run(runCtx)}()

	var replayed uint64
	for _,fp:=range plan.Flows {
		fl,err:=p.getOpenFlow(fp.StreamID);if err!=nil{return err}
		if fp.LocalAckAdvanceTo>0 { if err:=fl.onAck(fp.LocalAckAdvanceTo);err!=nil{return err} }
		frames,n,err:=fl.replayFramesFrom(fp.LocalReplayFrom);if err!=nil{return err}
		for _,fr:=range frames { if err:=newSender.sendData(ctx,fl,fr);err!=nil{return err} }
		replayed+=n
		fl.mu.Lock()
		if fp.LocalFinAckCanAdvance { fl.finAcked=true }
		resendFIN:=fl.finSent&&!fl.finAcked
		final:=fl.txNext
		fl.mu.Unlock()
		if resendFIN { if err:=newSender.sendControl(protocol.Frame{Type:protocol.TypeFin,StreamID:fl.id,Offset:final});err!=nil{return err} }
		p.finishIfComplete(fl)
		fl.mu.Lock();closed:=fl.closed;fl.mu.Unlock()
		if !closed { p.ensurePumpsAfterRecovery(runCtx,fl) }
	}
	a.replayed.Add(replayed);a.commits.Add(1)
	a.mu.Lock();a.frozen=false;a.pendingCandidate="";a.pendingPlan=recovery.Plan{};a.hasPlan=false;a.mu.Unlock()
	p.replacementMu.Lock()
	close(p.replacementWait)
	p.replacementWait=make(chan struct{})
	p.replacementMu.Unlock()
	return nil
}

func (p *Peer) AbortRecovery(candidateID string) {
	if p.recovery==nil{return}
	a:=p.recovery
	next:=a.engine.CurrentEpoch()+1
	a.engine.Abort(next,candidateID)
	a.aborts.Add(1)
	a.mu.Lock();a.frozen=false;a.pendingCandidate="";a.pendingPlan=recovery.Plan{};a.hasPlan=false;a.mu.Unlock()
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


func EncodeRecoveryDone(w io.Writer,done RecoveryDone) error {
	if done.CandidateID==""||done.NextEpoch==0{return recovery.ErrStateMismatch}
	b,err:=json.Marshal(done);if err!=nil{return err}
	return protocol.Encode(w,protocol.Frame{Type:protocol.TypeResumeDone,Payload:b})
}
func DecodeRecoveryDone(fr protocol.Frame)(RecoveryDone,error){
	if fr.Type!=protocol.TypeResumeDone{return RecoveryDone{},errors.New("expected RESUME_DONE")}
	dec:=json.NewDecoder(bytes.NewReader(fr.Payload));dec.DisallowUnknownFields()
	var d RecoveryDone
	if err:=dec.Decode(&d);err!=nil{return RecoveryDone{},err}
	if d.CandidateID==""||d.NextEpoch==0{return RecoveryDone{},recovery.ErrStateMismatch}
	return d,nil
}
