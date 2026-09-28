package bcc

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

func applyTelemetryReliability(t *testing.T,s *Store,token string,rep telemetry.Report)(NodeFinance,bool){
	t.Helper()
	body,err:=json.Marshal(rep);if err!=nil{t.Fatal(err)}
	f,dup,err:=s.ApplyTelemetry(token,telemetry.Sign(token,body),body,rep)
	if err!=nil{t.Fatal(err)}
	return f,dup
}

func ledgerHistoryCounts(s *Store,nodeID string)(int,int){
	s.mu.Lock();defer s.mu.Unlock()
	ledger:=0
	for _,e:=range s.st.FinanceLedger{if e.NodeID==nodeID{ledger++}}
	return ledger,len(s.st.History[nodeID])
}

func TestCrashAfterServerCommitBeforeLocalACKExactlyOnceFinance(t *testing.T){
	dir:=t.TempDir()
	store,err:=OpenStore(filepath.Join(dir,"state.json"));if err!=nil{t.Fatal(err)}
	const token="crash-ack-token"
	_,err=store.UpsertNode(Node{ID:"n1",Alias:"N1",Address:"127.0.0.1:34001",Role:"foreign"},token);if err!=nil{t.Fatal(err)}
	if err:=store.SetFinancePolicy("n1",100,300);err!=nil{t.Fatal(err)}
	app,err:=NewServer(store,"admin");if err!=nil{t.Fatal(err)}
	srv:=httptest.NewServer(app.Handler());defer srv.Close()

	spoolPath:=filepath.Join(dir,"telemetry.spool")
	e1,err:=telemetry.NewPersistent("n1",srv.URL,token,spoolPath,time.Second,10,func()telemetry.Snapshot{
		return telemetry.Snapshot{IngressBytes:1<<30,EgressBytes:0}
	});if err!=nil{t.Fatal(err)}
	crashed:=false
	e1.SetAfterSendBeforeACKForTest(func(telemetry.Report) error{
		if !crashed{crashed=true;return context.Canceled}
		return nil
	})
	if err:=e1.SendOnce(context.Background());err==nil{t.Fatal("expected injected crash-before-ACK")}
	if e1.QueueLength()!=1{t.Fatalf("pending after injected crash=%d",e1.QueueLength())}
	before:=store.FinanceSnapshot()[0]
	ledgerBefore,historyBefore:=ledgerHistoryCounts(store,"n1")
	if before.IngressBytes!=1<<30||before.CostMicros!=100||before.RevenueMicros!=300{t.Fatalf("first financial commit=%+v",before)}
	if ledgerBefore!=1||historyBefore!=1{t.Fatalf("first commit ledger=%d history=%d",ledgerBefore,historyBefore)}

	e2,err:=telemetry.NewPersistent("n1",srv.URL,token,spoolPath,time.Second,10,func()telemetry.Snapshot{return telemetry.Snapshot{}});if err!=nil{t.Fatal(err)}
	if err:=e2.FlushForTest(context.Background());err!=nil{t.Fatal(err)}
	if e2.QueueLength()!=0{t.Fatalf("queue not ACKed after replay=%d",e2.QueueLength())}
	after:=store.FinanceSnapshot()[0]
	ledgerAfter,historyAfter:=ledgerHistoryCounts(store,"n1")
	if !reflect.DeepEqual(before,after)||ledgerAfter!=ledgerBefore||historyAfter!=historyBefore{
		t.Fatalf("duplicate replay mutated financial state before=%+v after=%+v ledger %d->%d history %d->%d",before,after,ledgerBefore,ledgerAfter,historyBefore,historyAfter)
	}
	t.Log("PASS crash-before-ACK replay had exactly-once finance/ledger/history effect")
}

func TestRetiredBootIDReplayRejectedAcrossBCCRestart(t *testing.T){
	dir:=t.TempDir();path:=filepath.Join(dir,"state.json")
	store,err:=OpenStore(path);if err!=nil{t.Fatal(err)}
	const token="retired-token"
	_,_ = store.UpsertNode(Node{ID:"n1",Alias:"N1",Address:"127.0.0.1:34101",Role:"foreign"},token)
	_ = store.SetFinancePolicy("n1",10,30)
	base:=time.Date(2026,9,28,15,0,0,0,time.UTC)
	a1:=telemetry.Report{NodeID:"n1",BootID:"boot-A",Sequence:1,IngressBytes:100,EgressBytes:200,TimestampUnix:base.Unix()}
	_,dup:=applyTelemetryReliability(t,store,token,a1);if dup{t.Fatal("boot-A first sample duplicate")}
	b1:=telemetry.Report{NodeID:"n1",BootID:"boot-B",Sequence:1,IngressBytes:20,EgressBytes:40,TimestampUnix:base.Add(time.Minute).Unix()}
	_,dup=applyTelemetryReliability(t,store,token,b1);if dup{t.Fatal("boot-B first sample duplicate")}
	before:=store.FinanceSnapshot()[0];ledgerBefore,historyBefore:=ledgerHistoryCounts(store,"n1")
	delayedA:=a1;delayedA.Sequence=2;delayedA.IngressBytes=150;delayedA.EgressBytes=260
	_,dup=applyTelemetryReliability(t,store,token,delayedA)
	if !dup{t.Fatal("retired boot-A replay not marked stale/duplicate")}
	cur,_:=store.TelemetrySnapshot("n1")
	if cur.BootID!="boot-B"{t.Fatalf("cursor rolled back to %s",cur.BootID)}
	if !reflect.DeepEqual(before,store.FinanceSnapshot()[0]){t.Fatal("retired replay changed finance")}
	l,h:=ledgerHistoryCounts(store,"n1");if l!=ledgerBefore||h!=historyBefore{t.Fatalf("retired replay changed ledger/history %d/%d -> %d/%d",ledgerBefore,historyBefore,l,h)}

	restarted,err:=OpenStore(path);if err!=nil{t.Fatal(err)}
	beforeRestart:=restarted.FinanceSnapshot()[0]
	_,dup=applyTelemetryReliability(t,restarted,token,delayedA)
	if !dup{t.Fatal("retired boot-A accepted after BCC restart")}
	cur,_=restarted.TelemetrySnapshot("n1")
	if cur.BootID!="boot-B"||!reflect.DeepEqual(beforeRestart,restarted.FinanceSnapshot()[0]){t.Fatalf("restart replay mutated cursor/finance cur=%+v",cur)}
	ids:=restarted.RetiredBootIDs("n1")
	if len(ids)!=1||ids[0]!="boot-A"{t.Fatalf("retired ids=%v",ids)}
	t.Log("PASS retired boot replay rejected before and after BCC restart")
}

func TestRetiredBootIDSurvivesOldBackupRestore(t *testing.T){
	dir:=t.TempDir();path:=filepath.Join(dir,"state.json")
	store,_:=OpenStore(path)
	const token="retired-restore-token"
	_,_ = store.UpsertNode(Node{ID:"n1",Alias:"N1",Address:"127.0.0.1:34201",Role:"foreign"},token)
	_ = store.SetFinancePolicy("n1",10,30)
	app,_:=NewServer(store,"admin")
	base:=time.Date(2026,9,28,16,0,0,0,time.UTC)
	a1:=telemetry.Report{NodeID:"n1",BootID:"boot-A",Sequence:1,IngressBytes:100,EgressBytes:100,TimestampUnix:base.Unix()}
	applyTelemetryReliability(t,store,token,a1)
	backupPath:=filepath.Join(dir,"old.baftbak")
	if _,err:=app.BackupToFile(backupPath,backupTestKey(),base.Add(time.Minute));err!=nil{t.Fatal(err)}
	b1:=telemetry.Report{NodeID:"n1",BootID:"boot-B",Sequence:1,IngressBytes:25,EgressBytes:35,TimestampUnix:base.Add(2*time.Minute).Unix()}
	applyTelemetryReliability(t,store,token,b1)
	before:=store.FinanceSnapshot()[0];ledgerBefore,historyBefore:=ledgerHistoryCounts(store,"n1")
	if err:=app.RestoreFromFile(backupPath,backupTestKey());err!=nil{t.Fatal(err)}
	cur,_:=store.TelemetrySnapshot("n1")
	if cur.BootID!="boot-B"{t.Fatalf("restore rolled cursor to %s",cur.BootID)}
	ids:=store.RetiredBootIDs("n1")
	if len(ids)!=1||ids[0]!="boot-A"{t.Fatalf("retired state lost on restore: %v",ids)}
	delayed:=a1;delayed.Sequence=2;delayed.IngressBytes=200;delayed.EgressBytes=200
	_,dup:=applyTelemetryReliability(t,store,token,delayed)
	if !dup{t.Fatal("restored old boot-A became current again")}
	if !reflect.DeepEqual(before,store.FinanceSnapshot()[0]){t.Fatal("old epoch replay changed finance after restore")}
	l,h:=ledgerHistoryCounts(store,"n1");if l!=ledgerBefore||h!=historyBefore{t.Fatalf("ledger/history changed after stale replay %d/%d -> %d/%d",ledgerBefore,historyBefore,l,h)}
	t.Log("PASS retired boot state survived old-backup restore and prevented resurrection")
}
