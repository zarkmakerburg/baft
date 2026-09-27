package recovery

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type referenceStage1Engine struct {
	lease *EpochLease
	owner string
	pendingCandidate string
	pendingEpoch uint64
	pending Plan
	hasPending bool
}

func newReferenceStage1Engine(epoch uint64, owner string) *referenceStage1Engine {
	l, _ := NewEpochLease(epoch)
	return &referenceStage1Engine{lease:l, owner:owner}
}
func (r *referenceStage1Engine) Prepare(next uint64, candidateID string) error {
	if err:=r.lease.Prepare(next,candidateID);err!=nil{return err}
	r.pendingEpoch=next;r.pendingCandidate=candidateID;r.pending=Plan{};r.hasPending=false
	return nil
}
func (r *referenceStage1Engine) Reconcile(candidateID string,local,peer Snapshot,expected string)(Plan,error){
	if r.pendingCandidate!=candidateID||candidateID==""{return Plan{},ErrNotPrepared}
	p,err:=Reconcile(local,peer,expected);if err!=nil{return Plan{},err}
	r.pending=clonePlan(p);r.hasPending=true
	return p,nil
}
func (r *referenceStage1Engine) Commit(next uint64,candidateID string,p Plan)error{
	if !r.hasPending||r.pendingEpoch!=next||r.pendingCandidate!=candidateID{return ErrNotPrepared}
	if !plansEqual(r.pending,p){return ErrEnginePlanMismatch}
	if err:=r.lease.Commit(next,candidateID);err!=nil{return err}
	r.owner=candidateID;r.pendingEpoch=0;r.pendingCandidate="";r.pending=Plan{};r.hasPending=false
	return nil
}
func (r *referenceStage1Engine) Abort(next uint64,candidateID string){r.lease.Abort(next,candidateID)}
func (r *referenceStage1Engine) Authorize(epoch uint64,carrierID string)bool{return r.lease.Authorize(epoch)&&r.owner==carrierID}
func (r *referenceStage1Engine) CurrentEpoch()uint64{return r.lease.Current()}
func (r *referenceStage1Engine) Owner()string{return r.owner}

func engineForMutation(t *testing.T,epoch uint64,owner string)*Engine{
	t.Helper()
	e,err:=NewEngine(epoch,owner,EngineOptions{});if err!=nil{t.Fatal(err)}
	switch mutantNameForEngine(){
	case "f01_accept_old_epoch":e.faults.acceptOldEpoch=true
	case "f02_accept_both_candidates":e.faults.acceptBothCandidates=true
	case "f03_replay_from_k":e.faults.replayFromK=true
	case "f07_resume_after_boot_change":e.faults.resumeAfterBootChange=true
	case "f08_accept_a_gt_s":e.faults.acceptAGreaterThanS=true
	case "f09_skip_k_le_a":e.faults.skipKLessEqualA=true
	}
	return e
}

func stage1Snapshots(epoch uint64)(Snapshot,Snapshot){
	l,p:=validBaseSnapshotsForHarness()
	l.Epoch=epoch;p.Epoch=epoch
	p.Flows[0].RxCredit=160
	l.Flows[0].RxCredit=160
	return l,p
}

func TestEngineECRLF01ZombieCarrier(t *testing.T){
	e:=engineForMutation(t,7,"carrier-old")
	l,p:=stage1Snapshots(7)
	if err:=e.Prepare(8,"carrier-new");err!=nil{t.Fatal(err)}
	plan,err:=e.Reconcile("carrier-new",l,p,"peer-boot");if err!=nil{t.Fatal(err)}
	if err:=e.Commit(8,"carrier-new",plan);err!=nil{t.Fatal(err)}
	if e.Authorize(7,"carrier-old"){t.Fatal("F01 engine: old carrier authorized after commit")}
	if !e.Authorize(8,"carrier-new"){t.Fatal("F01 engine: new carrier not authorized")}
}
func TestEngineECRLF02DualCandidate(t *testing.T){
	e:=engineForMutation(t,1,"old")
	if err:=e.Prepare(2,"a");err!=nil{t.Fatal(err)}
	if err:=e.Prepare(2,"b");!errors.Is(err,ErrLeaseConflict){t.Fatalf("F02 engine: second candidate err=%v",err)}
}
func TestEngineECRLF03LostACK(t *testing.T){
	e:=engineForMutation(t,9,"old")
	l,p:=stage1Snapshots(9)
	if err:=e.Prepare(10,"new");err!=nil{t.Fatal(err)}
	plan,err:=e.Reconcile("new",l,p,"peer-boot");if err!=nil{t.Fatal(err)}
	got:=plan.Flows[0]
	if got.LocalReplayFrom!=p.Flows[0].RxAccepted{t.Fatalf("F03 engine: replay=%d want A=%d",got.LocalReplayFrom,p.Flows[0].RxAccepted)}
	if got.LocalReleaseThrough!=l.Flows[0].TxAcked{t.Fatalf("current mode release=%d want K=%d",got.LocalReleaseThrough,l.Flows[0].TxAcked)}
}
func TestEngineECRLF07PeerRestart(t *testing.T){
	e:=engineForMutation(t,9,"old")
	l,p:=stage1Snapshots(9)
	if err:=e.Prepare(10,"new");err!=nil{t.Fatal(err)}
	if _,err:=e.Reconcile("new",l,p,"different-boot");!errors.Is(err,ErrPeerRestarted){t.Fatalf("F07 engine: err=%v",err)}
}
func TestEngineECRLF08InconsistentSnapshot(t *testing.T){
	e:=engineForMutation(t,9,"old")
	l,p:=stage1Snapshots(9)
	p.Flows[0].RxAccepted=l.Flows[0].TxNext+1
	p.Flows[0].RxCredit=p.Flows[0].RxAccepted+32
	if err:=e.Prepare(10,"new");err!=nil{t.Fatal(err)}
	if _,err:=e.Reconcile("new",l,p,"peer-boot");!errors.Is(err,ErrStateMismatch){t.Fatalf("F08 engine: A>S accepted err=%v",err)}
}
func TestEngineECRLF09DeterministicSnapshotSweep(t *testing.T){
	for k:=uint64(0);k<=3;k++{
		for d:=uint64(0);d<=3;d++{
			for a:=uint64(0);a<=3;a++{
				for s:=uint64(0);s<=3;s++{
					for c:=uint64(0);c<=3;c++{
						e:=engineForMutation(t,1,"old")
						local:=Snapshot{SessionID:"s",BootID:"local",Epoch:1,Flows:[]FlowSnapshot{{StreamID:1,OpenNonce:"n",TxNext:s,TxAcked:k,RxAccepted:0,RxDelivered:0,RxCredit:8}}}
						peer:=Snapshot{SessionID:"s",BootID:"peer",Epoch:1,Flows:[]FlowSnapshot{{StreamID:1,OpenNonce:"n",TxNext:0,TxAcked:0,RxAccepted:a,RxDelivered:d,RxCredit:c}}}
						want:=d<=a&&k<=a&&a<=s&&s<=c
						if err:=e.Prepare(2,"new");err!=nil{t.Fatal(err)}
						_,err:=e.Reconcile("new",local,peer,"peer")
						if (err==nil)!=want{t.Fatalf("F09 engine: k=%d d=%d a=%d s=%d c=%d err=%v wantValid=%v",k,d,a,s,c,err,want)}
					}
				}
			}
		}
	}
}

func TestEngineCommitRequiresExactReconciledPlan(t *testing.T){
	e,err:=NewEngine(9,"old",EngineOptions{});if err!=nil{t.Fatal(err)}
	l,p:=stage1Snapshots(9)
	if err:=e.Prepare(10,"new");err!=nil{t.Fatal(err)}
	plan,err:=e.Reconcile("new",l,p,"peer-boot");if err!=nil{t.Fatal(err)}
	mutated:=clonePlan(plan);mutated.Flows[0].LocalReplayFrom++
	if err:=e.Commit(10,"new",mutated);!errors.Is(err,ErrEnginePlanMismatch){t.Fatalf("mutated plan committed: %v",err)}
	if e.CurrentEpoch()!=9||e.Owner()!="old"{t.Fatal("failed commit changed authoritative state")}
	if err:=e.Commit(10,"new",plan);err!=nil{t.Fatal(err)}
}

func TestStage1DifferentialReferenceVsEngine(t *testing.T){
	cases:=[]struct{name string; mutate func(*Snapshot,*Snapshot); expectedBoot string}{
		{name:"baseline",expectedBoot:"peer-boot"},
		{name:"lost-ack",expectedBoot:"peer-boot",mutate:func(l,p *Snapshot){l.Flows[0].TxAcked=7}},
		{name:"boot-change",expectedBoot:"wrong"},
		{name:"A-greater-S",expectedBoot:"peer-boot",mutate:func(l,p *Snapshot){p.Flows[0].RxAccepted=l.Flows[0].TxNext+1;p.Flows[0].RxCredit=200}},
		{name:"K-greater-A",expectedBoot:"peer-boot",mutate:func(l,p *Snapshot){l.Flows[0].TxAcked=p.Flows[0].RxAccepted+1}},
		{name:"S-greater-C",expectedBoot:"peer-boot",mutate:func(l,p *Snapshot){p.Flows[0].RxCredit=l.Flows[0].TxNext-1}},
	}
	for _,tc:=range cases{
		t.Run(tc.name,func(t *testing.T){
			var ref ReconcileCommitEngine=newReferenceStage1Engine(9,"old")
			e,err:=NewEngine(9,"old",EngineOptions{});if err!=nil{t.Fatal(err)}
			var eng ReconcileCommitEngine=e
			if normalizeErr(ref.Prepare(10,"new"))!=normalizeErr(eng.Prepare(10,"new")){t.Fatal("prepare divergence")}
			l,p:=stage1Snapshots(9);if tc.mutate!=nil{tc.mutate(&l,&p)}
			rp,re:=ref.Reconcile("new",l,p,tc.expectedBoot)
			ep,ee:=eng.Reconcile("new",l,p,tc.expectedBoot)
			if normalizeErr(re)!=normalizeErr(ee){t.Fatalf("reconcile error divergence ref=%v engine=%v",re,ee)}
			if re!=nil{return}
			if !reflect.DeepEqual(rp,ep){t.Fatalf("plan divergence ref=%#v engine=%#v",rp,ep)}
			rc:=ref.Commit(10,"new",rp);ec:=eng.Commit(10,"new",ep)
			if normalizeErr(rc)!=normalizeErr(ec){t.Fatalf("commit divergence ref=%v engine=%v",rc,ec)}
			if ref.CurrentEpoch()!=eng.CurrentEpoch()||ref.Owner()!=eng.Owner(){t.Fatal("post-commit state divergence")}
			for _,q:=range []struct{epoch uint64;carrier string}{{9,"old"},{10,"new"},{10,"old"}}{
				if ref.Authorize(q.epoch,q.carrier)!=eng.Authorize(q.epoch,q.carrier){t.Fatalf("authorize divergence %s",fmt.Sprint(q))}
			}
		})
	}
}

func TestDurableSnapshotFlagDefaultsOff(t *testing.T){
	p,err:=NewReplayPolicy(EngineOptions{});if err!=nil{t.Fatal(err)}
	if p.DurableSnapshot(){t.Fatal("durable_snapshot must default off")}
	if got:=p.ReleaseThrough(17,9);got!=17{t.Fatalf("current mode release=%d want K=17",got)}
	if got:=p.EffectiveCredit(100,9);got!=100{t.Fatalf("current mode credit changed: %d",got)}
	if _,err:=NewReplayPolicy(EngineOptions{RReplay:64});!errors.Is(err,ErrDurableSnapshotConfig){t.Fatalf("r_replay escaped durable_snapshot flag: %v",err)}
}
func TestDurableSnapshotPolicyOnlyWhenExplicitlyEnabled(t *testing.T){
	p,err:=NewReplayPolicy(EngineOptions{DurableSnapshot:true,RReplay:64});if err!=nil{t.Fatal(err)}
	if !p.DurableSnapshot(){t.Fatal("durable flag not enabled")}
	if got:=p.ReleaseThrough(40,24);got!=24{t.Fatalf("durable release=%d want K_release=24",got)}
	if got:=p.EffectiveCredit(200,24);got!=88{t.Fatalf("credit_eff=%d want 88",got)}
	if err:=p.ValidateOutstanding(88,24);err!=nil{t.Fatal(err)}
	if err:=p.ValidateOutstanding(89,24);!errors.Is(err,ErrStateMismatch){t.Fatalf("replay cap not enforced: %v",err)}
}
func normalizeErr(err error)string{
	switch{
	case err==nil:return ""
	case errors.Is(err,ErrStaleEpoch):return "STALE_EPOCH"
	case errors.Is(err,ErrLeaseConflict):return "LEASE_CONFLICT"
	case errors.Is(err,ErrNotPrepared):return "NOT_PREPARED"
	case errors.Is(err,ErrPeerRestarted):return "PEER_RESTARTED"
	case errors.Is(err,ErrStateMismatch):return "STATE_MISMATCH"
	case errors.Is(err,ErrEnginePlanMismatch):return "PLAN_MISMATCH"
	default:return err.Error()
	}
}
