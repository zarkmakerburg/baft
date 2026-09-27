package recovery

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"math/rand"
	"testing"
)

// Deterministic falsification reference model only.
// This is not the Stage-D resume/snapshot/replay engine.

type ecrlMarks struct {
	F uint64
	K uint64
	D uint64
	A uint64
	S uint64
	C uint64
}

func validateIntegratedMarks(m ecrlMarks) error {
	if m.F > m.D {
		return errors.New("F>D")
	}
	if m.D > m.A {
		return errors.New("D>A")
	}
	if !mutantActive("f09_skip_k_le_a") && m.K > m.A {
		return errors.New("K>A")
	}
	if m.A > m.S && !mutantActive("f08_accept_a_gt_s") {
		return errors.New("A>S")
	}
	if m.S > m.C {
		return errors.New("S>C")
	}
	return nil
}

type ownerReference struct {
	lease *EpochLease
	owner map[uint64]string
}

func newOwnerReference(epoch uint64, owner string) *ownerReference {
	l, _ := NewEpochLease(epoch)
	return &ownerReference{lease:l, owner:map[uint64]string{epoch:owner}}
}

func (m *ownerReference) prepare(next uint64, candidate string) error {
	if mutantActive("f02_accept_both_candidates") && next == m.lease.Current()+1 {
		return nil
	}
	return m.lease.Prepare(next,candidate)
}

func (m *ownerReference) commit(next uint64, candidate string) error {
	if mutantActive("f02_accept_both_candidates") {
		m.owner[next]=candidate
		return nil
	}
	if err:=m.lease.Commit(next,candidate);err!=nil{return err}
	m.owner[next]=candidate
	return nil
}

func (m *ownerReference) authorized(epoch uint64, carrier string) bool {
	if mutantActive("f01_accept_old_epoch") && epoch < m.lease.Current() {
		return true
	}
	if !m.lease.Authorize(epoch){return false}
	return m.owner[epoch]==carrier
}

type tombstoneReference struct{ closedAt map[uint64]uint64 }

func newTombstoneReference()*tombstoneReference{
	return &tombstoneReference{closedAt:map[uint64]uint64{}}
}
func(t *tombstoneReference)close(streamID,epoch uint64){t.closedAt[streamID]=epoch}
func(t *tombstoneReference)canReopen(streamID,epoch uint64)bool{
	if mutantActive("f05_accept_tombstone_open"){return true}
	closedAt,ok:=t.closedAt[streamID]
	if !ok{return true}
	return epoch<closedAt
}

func replayFrontier(k,a uint64)uint64{
	if mutantActive("f03_replay_from_k"){return k}
	return a
}

func partitionHandoff(source []byte,m ecrlMarks)(prefix,ring,replay,combined []byte,err error){
	if err=validateIntegratedMarks(m);err!=nil{return nil,nil,nil,nil,err}
	if m.S>uint64(len(source)){return nil,nil,nil,nil,errors.New("S exceeds source")}
	ringEnd:=m.A
	replayStart:=m.A
	if mutantActive("f04_overlap_ring_replay"){
		replayStart=m.D
	}
	if mutantActive("f10_replay_from_a_minus_1") && m.A>0 {
		replayStart=m.A-1
	}
	if mutantActive("f10_replay_from_a_plus_1") && m.A<m.S {
		replayStart=m.A+1
	}
	prefix=append([]byte(nil),source[:m.D]...)
	ring=append([]byte(nil),source[m.D:ringEnd]...)
	replay=append([]byte(nil),source[replayStart:m.S]...)
	combined=append(combined,prefix...)
	combined=append(combined,ring...)
	combined=append(combined,replay...)
	return
}

type finReference struct{
	received bool
	closed bool
	sideEffects int
}
func(f *finReference)applyFIN(){
	if f.received && !mutantActive("f06_apply_fin_twice"){return}
	f.received=true
	f.sideEffects++
}
func(f *finReference)reconcileLostFIN(finSent bool){
	if !finSent||f.received{return}
	if mutantActive("f06_lost_fin_never_closes"){return}
	f.applyFIN()
	f.closed=true
}

func resumeAllowed(expectedBoot,observedBoot string)bool{
	if mutantActive("f07_resume_after_boot_change"){return true}
	return expectedBoot!=""&&expectedBoot==observedBoot
}

func validBaseSnapshotsForHarness()(Snapshot,Snapshot){
	local:=Snapshot{SessionID:"s",BootID:"local-boot",Epoch:9,Flows:[]FlowSnapshot{{
		StreamID:1,OpenNonce:"n",TxNext:100,TxAcked:40,RxAccepted:80,RxDelivered:64,RxCredit:128,
	}}}
	peer:=Snapshot{SessionID:"s",BootID:"peer-boot",Epoch:9,Flows:[]FlowSnapshot{{
		StreamID:1,OpenNonce:"n",TxNext:120,TxAcked:64,RxAccepted:72,RxDelivered:70,RxCredit:134,
	}}}
	return local,peer
}

func TestECRLF01ZombieCarrier(t *testing.T){
	m:=newOwnerReference(7,"carrier-old")
	if err:=m.prepare(8,"carrier-new");err!=nil{t.Fatal(err)}
	if err:=m.commit(8,"carrier-new");err!=nil{t.Fatal(err)}
	target:=[]byte("prefix")
	before:=append([]byte(nil),target...)
	if m.authorized(7,"carrier-old"){target=append(target,[]byte("ZOMBIE")...)}
	if !bytes.Equal(target,before){t.Fatal("F01: zombie carrier changed target bytes after commit")}
	if !m.authorized(8,"carrier-new"){t.Fatal("F01: committed carrier was not authorized")}
}

func TestECRLF02DualCandidate(t *testing.T){
	m:=newOwnerReference(1,"carrier-a")
	if err:=m.prepare(2,"candidate-a");err!=nil{t.Fatal(err)}
	if err:=m.prepare(2,"candidate-b");!errors.Is(err,ErrLeaseConflict){
		t.Fatalf("F02: second candidate was not rejected: %v",err)
	}
	if err:=m.commit(2,"candidate-a");err!=nil{t.Fatal(err)}
	if !m.authorized(2,"candidate-a"){t.Fatal("F02: winner lost ownership")}
	if m.authorized(2,"candidate-b"){t.Fatal("F02: losing candidate obtained ownership")}
}

func TestECRLF03LostACK(t *testing.T){
	local,peer:=validBaseSnapshotsForHarness()
	plan,err:=Reconcile(local,peer,"peer-boot");if err!=nil{t.Fatal(err)}
	if got:=plan.Flows[0].LocalReplayFrom;got!=72{t.Fatalf("F03: production-model replay=%d want 72",got)}
	if got:=replayFrontier(local.Flows[0].TxAcked,peer.Flows[0].RxAccepted);got!=peer.Flows[0].RxAccepted{
		t.Fatalf("F03: reference replay frontier=%d want A=%d",got,peer.Flows[0].RxAccepted)
	}
}

func TestECRLF04AcceptedNotDeliveredPartition(t *testing.T){
	source:=[]byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	m:=ecrlMarks{F:4,K:8,D:10,A:18,S:30,C:36}
	prefix,ring,replay,combined,err:=partitionHandoff(source,m);if err!=nil{t.Fatal(err)}
	if len(prefix)!=10||len(ring)!=8||len(replay)!=12{
		t.Fatalf("F04: wrong partition sizes prefix=%d ring=%d replay=%d",len(prefix),len(ring),len(replay))
	}
	if !bytes.Equal(combined,source[:m.S]){
		t.Fatalf("F04: handoff produced gap/overlap got=%q want=%q",combined,source[:m.S])
	}
}

func TestECRLKReleaseNotRequiredForCurrentInMemoryResume(t *testing.T){
	// Current threat model: unchanged boot_id means the receiver Process and
	// its volatile receive ring survive Carrier replacement.
	source:=[]byte("abcdefghijklmnopqrstuvwxyz0123456789")
	m:=ecrlMarks{F:4,K:14,D:8,A:20,S:30,C:36}
	if err:=validateIntegratedMarks(m);err!=nil{t.Fatal(err)}
	if !(m.D<m.K&&m.K<=m.A){t.Fatal("test requires D<K<=A")}

	// Sender frees replay through K. The receiver still owns [D,A) in RAM,
	// therefore [D,K) has not lost its last copy.
	senderReplay:=append([]byte(nil),source[m.K:m.S]...)
	receiverRing:=append([]byte(nil),source[m.D:m.A]...)
	if len(receiverRing)==0||len(senderReplay)==0{t.Fatal("trace setup failed")}

	// Carrier dies, but boot_id does not change: same ring remains. Drain ring
	// first, then replay only the not-yet-accepted tail [A,S).
	target:=append([]byte(nil),source[:m.D]...)
	target=append(target,receiverRing...)
	target=append(target,senderReplay[m.A-m.K:]...)
	if !bytes.Equal(target,source[:m.S]){
		t.Fatalf("current in-memory model lost/duplicated data: got=%q want=%q",target,source[:m.S])
	}
}

func TestECRLF05TombstoneResurrection(t *testing.T){
	ts:=newTombstoneReference();ts.close(11,9)
	for _,epoch:=range []uint64{9,10,11,100}{
		if ts.canReopen(11,epoch){t.Fatalf("F05: tombstoned stream reopened at epoch %d",epoch)}
	}
	if !ts.canReopen(13,10){t.Fatal("F05: unrelated stream incorrectly blocked")}
}

func TestECRLF06LostFIN(t *testing.T){
	var f finReference
	f.reconcileLostFIN(true)
	if !f.received||!f.closed||f.sideEffects!=1{
		t.Fatalf("F06 LostFIN: recovered state=%+v",f)
	}
}

func TestECRLF06LostFINACK(t *testing.T){
	local,peer:=validBaseSnapshotsForHarness()
	local.Flows[0].FinSent=true
	peer.Flows[0].FinRecv=true
	peer.Flows[0].FinAckSent=true
	plan,err:=Reconcile(local,peer,"peer-boot");if err!=nil{t.Fatal(err)}
	if !plan.Flows[0].LocalFinAckCanAdvance{t.Fatal("F06: lost FIN_ACK was not recoverable")}
}

func TestECRLF06DuplicateFINIdempotence(t *testing.T){
	var f finReference
	f.applyFIN()
	f.applyFIN()
	if f.sideEffects!=1{t.Fatalf("F06: FIN side effect count=%d want 1",f.sideEffects)}
}

func TestECRLF07PeerRestart(t *testing.T){
	local,peer:=validBaseSnapshotsForHarness()
	if resumeAllowed("peer-boot","different-peer-boot"){
		t.Fatal("F07: reference model allowed resume after boot change")
	}
	if _,err:=Reconcile(local,peer,"different-peer-boot");!errors.Is(err,ErrPeerRestarted){
		t.Fatalf("F07: production model did not reject restart: %v",err)
	}
}

func TestECRLF08InconsistentSnapshot(t *testing.T){
	invalid:=[]ecrlMarks{
		{F:0,K:11,D:2,A:10,S:12,C:20},
		{F:0,K:2,D:11,A:10,S:12,C:20},
		{F:0,K:2,D:3,A:13,S:12,C:20},
		{F:0,K:2,D:3,A:10,S:21,C:20},
	}
	for i,m:=range invalid{
		if err:=validateIntegratedMarks(m);err==nil{t.Fatalf("F08: invalid set %d accepted: %#v",i,m)}
	}
	local,peer:=validBaseSnapshotsForHarness()
	peer.Flows[0].RxAccepted=local.Flows[0].TxNext+1
	if _,err:=Reconcile(local,peer,"peer-boot");!errors.Is(err,ErrStateMismatch){
		t.Fatalf("F08: production model accepted A>S: %v",err)
	}
}

func TestECRLF09DeterministicPropertySweep(t *testing.T){
	for k:=uint64(0);k<=5;k++{
		for d:=uint64(0);d<=5;d++{
			for a:=uint64(0);a<=5;a++{
				for s:=uint64(0);s<=5;s++{
					for c:=uint64(0);c<=5;c++{
						for f:=uint64(0);f<=d;f++{
							m:=ecrlMarks{F:f,K:k,D:d,A:a,S:s,C:c}
							want:=f<=d&&d<=a&&k<=a&&a<=s&&s<=c
							err:=validateIntegratedMarks(m)
							if (err==nil)!=want{
								t.Fatalf("F09: classifier mismatch %#v err=%v want=%v",m,err,want)
							}
							if !want{continue}
							source:=[]byte("abcdef")
							_,ring,replay,combined,err:=partitionHandoff(source,m);if err!=nil{t.Fatal(err)}
							if uint64(len(ring)+len(replay))!=s-d{t.Fatalf("F09: conservation length failed %#v",m)}
							if !bytes.Equal(combined,source[:s]){t.Fatalf("F09: conservation bytes failed %#v",m)}
						}
					}
				}
			}
		}
	}
}

func TestECRLF10ExactByteStream(t *testing.T){
	const n=256*1024
	rng:=rand.New(rand.NewSource(0xEC12))
	source:=make([]byte,n);if _,err:=rng.Read(source);err!=nil{t.Fatal(err)}
	wantHash:=sha256.Sum256(source)
	for i:=0;i<512;i++{
		s:=uint64(1+rng.Intn(n))
		a:=uint64(rng.Intn(int(s+1)))
		d:=uint64(rng.Intn(int(a+1)))
		k:=uint64(rng.Intn(int(a+1)))
		f:=uint64(rng.Intn(int(d+1)))
		c:=s+uint64(rng.Intn(4096))
		m:=ecrlMarks{F:f,K:k,D:d,A:a,S:s,C:c}
		prefix,ring,replay,combined,err:=partitionHandoff(source,m);if err!=nil{t.Fatalf("F10 case %d: %v",i,err)}
		if !bytes.Equal(combined,source[:s]){t.Fatalf("F10 case %d: duplicate/missing byte",i)}
		if len(prefix)+len(ring)+len(replay)!=int(s){t.Fatalf("F10 case %d: byte-count mismatch",i)}
	}
	full:=ecrlMarks{F:n/4,K:n/3,D:n/2,A:3*n/4,S:n,C:n}
	_,_,_,got,err:=partitionHandoff(source,full);if err!=nil{t.Fatal(err)}
	gotHash:=sha256.Sum256(got)
	if len(got)!=len(source)||gotHash!=wantHash{
		t.Fatalf("F10: exact stream mismatch len=%d/%d hash=%x/%x",len(got),len(source),gotHash,wantHash)
	}
}

func TestECRLFutureDurableSlowTargetReplayBoundNoDeadlock(t *testing.T){
	// Future durable-snapshot mode only. K_release is a delivery watermark
	// retained by the sender so it can preserve bytes across receiver restart.
	const (
		total=uint64(256)
		rReplay=uint64(64)
		chunk=uint64(16)
	)
	var s,k,kRelease uint64
	c:=total
	steps:=0
	for kRelease<total{
		steps++
		if steps>1000{t.Fatal("future durable model deadlocked")}
		progress:=false
		creditEff:=c
		if lim:=kRelease+rReplay;lim<creditEff{creditEff=lim}

		if s<total&&s+chunk<=creditEff{
			s+=chunk
			k=s // receiver accepts quickly; target delivery intentionally lags.
			progress=true
		}
		if s-kRelease>rReplay{
			t.Fatalf("replay cap exceeded: S=%d K_release=%d cap=%d",s,kRelease,rReplay)
		}

		// Slow target: deliver only when sender hits the effective cap, or
		// periodically. Delivery control is outside PADL DATA eligibility.
		if s>kRelease&&(s==creditEff||steps%4==0){
			kRelease+=chunk
			if kRelease>s{kRelease=s}
			progress=true
		}
		if k<s{t.Fatal("acceptance watermark regressed")}
		if !progress{t.Fatal("no sender or target progress")}
	}
	if s!=total||kRelease!=total{t.Fatalf("incomplete progress S=%d K_release=%d",s,kRelease)}
}
