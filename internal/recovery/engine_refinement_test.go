package recovery

import (
	"errors"
	"math/rand"
	"reflect"
	"sync"
	"testing"
)

const generatedDifferentialSeed int64 = 3964471334
const generatedDifferentialCount = 10000

type referenceStage1Engine struct{lease *EpochLease;owner,pendingCandidate string;pendingEpoch uint64;pending Plan;hasPending bool;activeFlows,frozenFlows map[uint64]string}
func newReferenceStage1Engine(epoch uint64,owner string)*referenceStage1Engine{l,_:=NewEpochLease(epoch);return &referenceStage1Engine{lease:l,owner:owner,activeFlows:map[uint64]string{}}}
func(r *referenceStage1Engine)SetActiveFlows(flows []FlowIdentity)error{if r.pendingEpoch!=0{return ErrResumeFrozen};m,e:=identitiesToMap(flows);if e!=nil{return e};r.activeFlows=m;return nil}
func(r *referenceStage1Engine)Prepare(next uint64,c string)error{if e:=r.lease.Prepare(next,c);e!=nil{return e};r.pendingEpoch=next;r.pendingCandidate=c;r.pending=Plan{};r.hasPending=false;r.frozenFlows=cloneIdentityMap(r.activeFlows);return nil}
func(r *referenceStage1Engine)Reconcile(c string,l,p Snapshot,b string)(Plan,error){if r.pendingCandidate!=c||c==""{return Plan{},ErrNotPrepared};if !snapshotMatchesFrozen(l,r.frozenFlows)||!snapshotMatchesFrozen(p,r.frozenFlows){return Plan{},ErrStateMismatch};x,e:=Reconcile(l,p,b);if e!=nil{return Plan{},e};r.pending=clonePlan(x);r.hasPending=true;return x,nil}
func(r *referenceStage1Engine)Commit(next uint64,c string,p Plan)error{if !r.hasPending||r.pendingEpoch!=next||r.pendingCandidate!=c{return ErrNotPrepared};if !plansEqual(r.pending,p){return ErrEnginePlanMismatch};if e:=r.lease.Commit(next,c);e!=nil{return e};r.owner=c;r.pendingEpoch=0;r.pendingCandidate="";r.pending=Plan{};r.hasPending=false;r.frozenFlows=nil;return nil}
func(r *referenceStage1Engine)Abort(next uint64,c string){r.lease.Abort(next,c);if r.pendingEpoch==next&&r.pendingCandidate==c{r.pendingEpoch=0;r.pendingCandidate="";r.hasPending=false;r.frozenFlows=nil}}
func(r *referenceStage1Engine)Authorize(ep uint64,c string)bool{return r.lease.Authorize(ep)&&r.owner==c}
func(r *referenceStage1Engine)CurrentEpoch()uint64{return r.lease.Current()}
func(r *referenceStage1Engine)Owner()string{return r.owner}
func(r *referenceStage1Engine)TransitionAllowed(k TransitionKind,id uint64,n string)error{if r.pendingEpoch!=0{return ErrResumeFrozen};if id==0{return ErrStateMismatch};if k==TransitionOpen&&n==""{return ErrStateMismatch};if k!=TransitionOpen&&k!=TransitionData&&k!=TransitionFIN{return ErrStateMismatch};return nil}

func engineForMutation(t *testing.T,epoch uint64,owner string,s EngineScheduler)*Engine{t.Helper();e,er:=NewEngine(epoch,owner,EngineOptions{Scheduler:s});if er!=nil{t.Fatal(er)};switch engineMutantName(){case"f01_accept_old_epoch":e.faults.acceptOldEpoch=true;case"f02_accept_both_candidates":e.faults.acceptBothCandidates=true;case"f03_replay_from_k":e.faults.replayFromK=true;case"f07_resume_after_boot_change":e.faults.resumeAfterBootChange=true;case"f08_accept_a_gt_s":e.faults.acceptAGreaterThanS=true;case"f09_skip_k_le_a":e.faults.skipKLessEqualA=true;case"commit_two_stage":e.faults.twoStageCommit=true};return e}
func stage1Snapshots(ep uint64)(Snapshot,Snapshot){l,p:=validBaseSnapshotsForHarness();l.Epoch=ep;p.Epoch=ep;l.Flows[0].RxCredit=160;p.Flows[0].RxCredit=160;return l,p}
func armFlows(t *testing.T,e ReconcileCommitEngine,s Snapshot){t.Helper();if er:=e.SetActiveFlows(flowIdentities(s));er!=nil{t.Fatal(er)}}

func TestEngineECRLF01ZombieCarrier(t *testing.T){e:=engineForMutation(t,7,"old",nil);l,p:=stage1Snapshots(7);armFlows(t,e,l);if er:=e.Prepare(8,"new");er!=nil{t.Fatal(er)};pl,er:=e.Reconcile("new",l,p,"peer-boot");if er!=nil{t.Fatal(er)};if er=e.Commit(8,"new",pl);er!=nil{t.Fatal(er)};if e.Authorize(7,"old"){t.Fatal("old authorized")};if !e.Authorize(8,"new"){t.Fatal("new denied")}}
func TestEngineECRLF02DualCandidate(t *testing.T){e:=engineForMutation(t,1,"old",nil);l,_:=stage1Snapshots(1);armFlows(t,e,l);if er:=e.Prepare(2,"a");er!=nil{t.Fatal(er)};if er:=e.Prepare(2,"b");!errors.Is(er,ErrLeaseConflict){t.Fatalf("err=%v",er)}}
func TestEngineECRLF03LostACK(t *testing.T){e:=engineForMutation(t,9,"old",nil);l,p:=stage1Snapshots(9);armFlows(t,e,l);if er:=e.Prepare(10,"new");er!=nil{t.Fatal(er)};pl,er:=e.Reconcile("new",l,p,"peer-boot");if er!=nil{t.Fatal(er)};if pl.Flows[0].LocalReplayFrom!=p.Flows[0].RxAccepted{t.Fatal("frontier != A")};if pl.Flows[0].LocalReleaseThrough!=l.Flows[0].TxAcked{t.Fatal("release != K")}}
func TestEngineECRLF07PeerRestart(t *testing.T){e:=engineForMutation(t,9,"old",nil);l,p:=stage1Snapshots(9);armFlows(t,e,l);_ = e.Prepare(10,"new");if _,er:=e.Reconcile("new",l,p,"wrong");!errors.Is(er,ErrPeerRestarted){t.Fatalf("err=%v",er)}}
func TestEngineECRLF08InconsistentSnapshot(t *testing.T){e:=engineForMutation(t,9,"old",nil);l,p:=stage1Snapshots(9);armFlows(t,e,l);p.Flows[0].RxAccepted=l.Flows[0].TxNext+1;p.Flows[0].RxCredit=p.Flows[0].RxAccepted+10;_ = e.Prepare(10,"new");if _,er:=e.Reconcile("new",l,p,"peer-boot");!errors.Is(er,ErrStateMismatch){t.Fatalf("err=%v",er)}}
func TestEngineECRLF09DeterministicSnapshotSweep(t *testing.T){for k:=uint64(0);k<4;k++{for d:=uint64(0);d<4;d++{for a:=uint64(0);a<4;a++{for s:=uint64(0);s<4;s++{for c:=uint64(0);c<4;c++{e:=engineForMutation(t,1,"old",nil);l:=Snapshot{SessionID:"s",BootID:"l",Epoch:1,Flows:[]FlowSnapshot{{StreamID:1,OpenNonce:"n",TxNext:s,TxAcked:k,RxCredit:8}}};p:=Snapshot{SessionID:"s",BootID:"p",Epoch:1,Flows:[]FlowSnapshot{{StreamID:1,OpenNonce:"n",RxAccepted:a,RxDelivered:d,RxCredit:c}}};want:=d<=a&&k<=a&&a<=s&&s<=c;armFlows(t,e,l);_ = e.Prepare(2,"new");_,er:=e.Reconcile("new",l,p,"p");if (er==nil)!=want{t.Fatalf("k=%d d=%d a=%d s=%d c=%d err=%v want=%v",k,d,a,s,c,er,want)}}}}}}}

func TestEngineAIdentityVectorFrozenAcrossPrepareCommit(t *testing.T){e:=engineForMutation(t,9,"old",nil);l,p:=stage1Snapshots(9);armFlows(t,e,l);_ = e.Prepare(10,"new");for _,x:=range []struct{k TransitionKind;id uint64;n string}{{TransitionOpen,2,"x"},{TransitionData,1,""},{TransitionFIN,1,""}}{if er:=e.TransitionAllowed(x.k,x.id,x.n);!errors.Is(er,ErrResumeFrozen){t.Fatalf("%s escaped freeze: %v",x.k,er)}};extra:=l;extra.Flows=append(append([]FlowSnapshot{},l.Flows...),FlowSnapshot{StreamID:2,OpenNonce:"x",RxCredit:1});if _,er:=e.Reconcile("new",extra,p,"peer-boot");!errors.Is(er,ErrStateMismatch){t.Fatalf("extra OPEN accepted: %v",er)};pl,er:=e.Reconcile("new",l,p,"peer-boot");if er!=nil{t.Fatal(er)};if er=e.Commit(10,"new",pl);er!=nil{t.Fatal(er)};if er=e.TransitionAllowed(TransitionOpen,2,"x");er!=nil{t.Fatal(er)}}

type diffTrace struct{K,D,A,S,C uint64;BootOK bool}
type diffOutcome struct{ReconcileErr string;Plan Plan;CommitErr string;Epoch uint64;Owner string;OldAuth,NewAuth bool}
func runDiffSide(e ReconcileCommitEngine,tr diffTrace)diffOutcome{
	l:=Snapshot{SessionID:"s",BootID:"l",Epoch:1,Flows:[]FlowSnapshot{{StreamID:1,OpenNonce:"n",TxNext:tr.S,TxAcked:tr.K,RxCredit:8}}}
	p:=Snapshot{SessionID:"s",BootID:"p",Epoch:1,Flows:[]FlowSnapshot{{StreamID:1,OpenNonce:"n",RxAccepted:tr.A,RxDelivered:tr.D,RxCredit:tr.C}}}
	b:="p";if !tr.BootOK{b="wrong"}
	if er:=e.SetActiveFlows(flowIdentities(l));er!=nil{return diffOutcome{ReconcileErr:normalizeErr(er)}}
	if er:=e.Prepare(2,"new");er!=nil{return diffOutcome{ReconcileErr:normalizeErr(er)}}
	pl,er:=e.Reconcile("new",l,p,b)
	o:=diffOutcome{ReconcileErr:normalizeErr(er),Plan:pl,Epoch:e.CurrentEpoch(),Owner:e.Owner()}
	if er!=nil{return o}
	o.CommitErr=normalizeErr(e.Commit(2,"new",pl))
	o.Epoch=e.CurrentEpoch();o.Owner=e.Owner();o.OldAuth=e.Authorize(1,"old");o.NewAuth=e.Authorize(2,"new")
	return o
}
func runDiff(tr diffTrace)(diffOutcome,diffOutcome){return runDiffSide(newReferenceStage1Engine(1,"old"),tr),func()diffOutcome{e,_:=NewEngine(1,"old",EngineOptions{});return runDiffSide(e,tr)}()}
func diverges(tr diffTrace)bool{a,b:=runDiff(tr);return !reflect.DeepEqual(a,b)}
func minimizeTrace(tr diffTrace)diffTrace{
	sets:=[]func(*diffTrace,uint64){func(x *diffTrace,v uint64){x.K=v},func(x *diffTrace,v uint64){x.D=v},func(x *diffTrace,v uint64){x.A=v},func(x *diffTrace,v uint64){x.S=v},func(x *diffTrace,v uint64){x.C=v}}
	for _,set:=range sets{c:=tr;set(&c,0);if diverges(c){tr=c}}
	if !tr.BootOK{c:=tr;c.BootOK=true;if diverges(c){tr=c}}
	return tr
}
func assertNoDiff(t *testing.T,tr diffTrace,seed int64,i int){t.Helper();a,b:=runDiff(tr);if !reflect.DeepEqual(a,b){t.Fatalf("DIFF seed=%d index=%d trace=%+v minimized=%+v ref=%#v engine=%#v",seed,i,tr,minimizeTrace(tr),a,b)}}
func TestStage1DifferentialGenerated(t *testing.T){
	ex:=0
	for _,boot:=range []bool{false,true}{for k:=uint64(0);k<4;k++{for d:=uint64(0);d<4;d++{for a:=uint64(0);a<4;a++{for s:=uint64(0);s<4;s++{for c:=uint64(0);c<4;c++{assertNoDiff(t,diffTrace{k,d,a,s,c,boot},generatedDifferentialSeed,-1);ex++}}}}}}
	r:=rand.New(rand.NewSource(generatedDifferentialSeed))
	for i:=0;i<generatedDifferentialCount;i++{assertNoDiff(t,diffTrace{uint64(r.Intn(33)),uint64(r.Intn(33)),uint64(r.Intn(33)),uint64(r.Intn(33)),uint64(r.Intn(33)),r.Intn(8)!=0},generatedDifferentialSeed,i)}
	t.Logf("DIFFERENTIAL seed=%d exhaustive=%d generated=%d total=%d",generatedDifferentialSeed,ex,generatedDifferentialCount,ex+generatedDifferentialCount)
}

type blockingScheduler struct{target string;reached,release chan struct{};once sync.Once}
func newBlockingScheduler(x string)*blockingScheduler{return &blockingScheduler{x,make(chan struct{}),make(chan struct{}),sync.Once{}}}
func(s *blockingScheduler)Point(n string){if n!=s.target{return};s.once.Do(func(){close(s.reached)});<-s.release}
func TestEngineCommitAtomicLateAuthorize(t *testing.T){s:=newBlockingScheduler("commit_locked_before_publish");e:=engineForMutation(t,9,"old",s);l,p:=stage1Snapshots(9);armFlows(t,e,l);_ = e.Prepare(10,"new");pl,er:=e.Reconcile("new",l,p,"peer-boot");if er!=nil{t.Fatal(er)};cd:=make(chan error,1);go func(){cd<-e.Commit(10,"new",pl)}();<-s.reached;started:=make(chan struct{});ad:=make(chan bool,1);go func(){close(started);ad<-e.Authorize(9,"old")}();<-started;select{case <-ad:t.Fatal("Authorize observed intermediate commit");default:};close(s.release);if er:=<-cd;er!=nil{t.Fatal(er)};if <-ad{t.Fatal("late old auth succeeded")};if !e.Authorize(10,"new"){t.Fatal("new auth failed")}}

type inspectScheduler struct{e *Engine;mixed bool}
func(s *inspectScheduler)Point(n string){if n=="commit_nonatomic_mid"&&s.e!=nil&&s.e.Authorize(9,"new"){s.mixed=true}}
func TestEngineCommitAtomicNoMixedAuthority(t *testing.T){s:=&inspectScheduler{};e:=engineForMutation(t,9,"old",s);s.e=e;l,p:=stage1Snapshots(9);armFlows(t,e,l);_ = e.Prepare(10,"new");pl,er:=e.Reconcile("new",l,p,"peer-boot");if er!=nil{t.Fatal(er)};if er=e.Commit(10,"new",pl);er!=nil{t.Fatal(er)};if s.mixed{t.Fatal("mixed authority observed: (old_epoch,new_owner)")}}

func TestEngineConcurrentPrepareCommitInterleavings(t *testing.T){
	for _,winner:=range []string{"a","b"}{
		for _,loserTiming:=range []string{"before-commit","after-commit"}{
			t.Run(winner+"-"+loserTiming,func(t *testing.T){
				loser:="b";if winner=="b"{loser="a"}
				s:=newBlockingScheduler("prepare_before_lock:"+loser)
				e:=engineForMutation(t,1,"old",s);l,p:=stage1Snapshots(1);armFlows(t,e,l)
				loserDone:=make(chan error,1);go func(){loserDone<-e.Prepare(2,loser)}();<-s.reached
				if er:=e.Prepare(2,winner);er!=nil{t.Fatal(er)}
				pl,er:=e.Reconcile(winner,l,p,"peer-boot");if er!=nil{t.Fatal(er)}
				if loserTiming=="before-commit"{
					close(s.release)
					if er:=<-loserDone;!errors.Is(er,ErrLeaseConflict){t.Fatalf("loser before commit err=%v",er)}
					if er=e.Commit(2,winner,pl);er!=nil{t.Fatal(er)}
				}else{
					if er=e.Commit(2,winner,pl);er!=nil{t.Fatal(er)}
					close(s.release)
					if er:=<-loserDone;!errors.Is(er,ErrStaleEpoch){t.Fatalf("loser after commit err=%v",er)}
				}
				if e.Owner()!=winner||e.CurrentEpoch()!=2{t.Fatalf("wrong winner owner=%s epoch=%d",e.Owner(),e.CurrentEpoch())}
				if e.Authorize(1,"old")||!e.Authorize(2,winner){t.Fatal("authority overlap or missing new authority")}
			})
		}
	}
}

func TestDurableSnapshotFlagDefaultsOff(t *testing.T){p,er:=NewReplayPolicy(EngineOptions{});if er!=nil{t.Fatal(er)};if p.DurableSnapshot(){t.Fatal("default on")};if p.ReleaseThrough(17,9)!=17{t.Fatal("release not K")};if _,er=NewReplayPolicy(EngineOptions{RReplay:64});!errors.Is(er,ErrDurableSnapshotConfig){t.Fatal("RReplay escaped flag")}}
func TestDurableSnapshotPolicyOnlyWhenExplicitlyEnabled(t *testing.T){p,er:=NewReplayPolicy(EngineOptions{DurableSnapshot:true,RReplay:64});if er!=nil{t.Fatal(er)};if p.ReleaseThrough(40,24)!=24{t.Fatal("release not K_release")};if p.EffectiveCredit(200,24)!=88{t.Fatal("credit")}}
func normalizeErr(er error)string{switch{case er==nil:return"";case errors.Is(er,ErrStaleEpoch):return"STALE_EPOCH";case errors.Is(er,ErrLeaseConflict):return"LEASE_CONFLICT";case errors.Is(er,ErrNotPrepared):return"NOT_PREPARED";case errors.Is(er,ErrPeerRestarted):return"PEER_RESTARTED";case errors.Is(er,ErrStateMismatch):return"STATE_MISMATCH";case errors.Is(er,ErrEnginePlanMismatch):return"PLAN_MISMATCH";case errors.Is(er,ErrResumeFrozen):return"RESUME_FROZEN";default:return er.Error()}}
