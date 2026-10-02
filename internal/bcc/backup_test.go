package bcc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

func backupTestKey() []byte { return bytes.Repeat([]byte{0x42},32) }

func stateSnapshotForTest(t *testing.T,s *Store) state {
	t.Helper()
	st,err:=s.snapshotState();if err!=nil{t.Fatal(err)}
	return st
}

// diskStateJSONForTest reads the state database itself, not the store's RAM.
func diskStateJSONForTest(t *testing.T,s *Store) []byte {
	t.Helper()
	st,err:=readStateDB(s.path);if err!=nil{t.Fatal(err)}
	return canonicalStateJSON(st)
}

func stateJSONForTest(t *testing.T,s *Store) []byte {
	t.Helper()
	st:=stateSnapshotForTest(t,s)
	b,err:=json.Marshal(st);if err!=nil{t.Fatal(err)}
	return b
}

func applyTelemetryDirect(t *testing.T,s *Store,token string,rep telemetry.Report){
	t.Helper()
	body,err:=json.Marshal(rep);if err!=nil{t.Fatal(err)}
	if _,_,err:=s.ApplyTelemetry(token,telemetry.Sign(token,body),body,rep);err!=nil{t.Fatal(err)}
}

func TestEncryptedBackupFreshRestoreFullStateAndAudit(t *testing.T){
	dir:=t.TempDir()
	store,_:=OpenStore(filepath.Join(dir,"source.json"))
	app,_:=NewServer(store,"admin")
	t.Setenv("BACKUP_NODE_A","backup-token-a")
	t.Setenv("BACKUP_NODE_B","backup-token-b")

	for _,body:=range []map[string]any{
		{"ID":"n1","Alias":"Node 1","Address":"127.0.0.1:32001","Role":"foreign","AgentTokenEnv":"BACKUP_NODE_A"},
		{"ID":"n2","Alias":"Node 2","Address":"127.0.0.1:32002","Role":"worker","PublicKey":"pub2","AgentTokenEnv":"BACKUP_NODE_B"},
	}{
		rr:=httptest.NewRecorder();app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes","admin",body))
		if rr.Code!=http.StatusCreated{t.Fatalf("node create status=%d body=%s",rr.Code,rr.Body.String())}
	}
	rr:=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/finance","admin",map[string]any{
		"node_id":"n1","cost_micros_per_gib":100,"revenue_micros_per_gib":250,"currency":"IRR","effective_from":"2026-09-01T00:00:00Z",
	}))
	if rr.Code!=http.StatusOK{t.Fatalf("finance status=%d body=%s",rr.Code,rr.Body.String())}
	rr=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/deploy","admin",map[string]any{"node_ids":[]string{"n1","n2"},"version":"v0.5.5"}))
	if rr.Code!=http.StatusAccepted{t.Fatalf("deploy status=%d body=%s",rr.Code,rr.Body.String())}

	rep:=telemetry.Report{NodeID:"n1",BootID:"boot1",Sequence:1,IngressBytes:1234,EgressBytes:5678,ActiveSessions:2,TimestampUnix:time.Date(2026,9,28,10,0,0,0,time.UTC).Unix()}
	applyTelemetryDirect(t,store,"backup-token-a",rep)
	app.alertMu.Lock()
	app.activeAlerts["route_down:n1:r1"]=Alert{Type:"route_down",Status:"firing",NodeID:"n1",NodeAlias:"Node 1",RouteID:"r1",Timestamp:time.Now().UTC()}
	alertSnapshot:=cloneAlerts(app.activeAlerts)
	app.alertMu.Unlock()
	if err:=store.SetActiveAlerts(alertSnapshot);err!=nil{t.Fatal(err)}

	sourceState:=stateSnapshotForTest(t,store)
	sourceAudit,err:=app.audit.List(0);if err!=nil{t.Fatal(err)}
	backupPath:=filepath.Join(dir,"full.baftbak")
	header,err:=app.BackupToFile(backupPath,backupTestKey(),time.Date(2026,9,28,11,0,0,0,time.UTC))
	if err!=nil{t.Fatal(err)}
	if header.SchemaVersion!=BackupSchemaVersion||header.PayloadSHA256==""||header.AuditHash==""{t.Fatalf("header=%+v",header)}
	raw,err:=os.ReadFile(backupPath);if err!=nil{t.Fatal(err)}
	if bytes.Contains(raw,backupTestKey()){t.Fatal("raw backup key appears in backup file")}

	freshStore,_:=OpenStore(filepath.Join(dir,"fresh.json"))
	fresh,_:=NewServer(freshStore,"admin")
	if err:=fresh.RestoreFromFile(backupPath,backupTestKey());err!=nil{t.Fatal(err)}
	restoredState:=stateSnapshotForTest(t,freshStore)
	if !reflect.DeepEqual(sourceState,restoredState){t.Fatalf("restored state differs\nsource=%+v\nrestored=%+v",sourceState,restoredState)}
	// The committed database is what a restart would load.
	if !bytes.Equal(diskStateJSONForTest(t,freshStore),canonicalStateJSON(restoredState)){t.Fatal("restored state database differs from the restored RAM state")}
	if reopened,err:=OpenStore(freshStore.path);err!=nil||!bytes.Equal(canonicalStateJSON(reopened.st),canonicalStateJSON(restoredState)){t.Fatalf("restored state does not survive a restart: %v",err)}
	fresh.alertMu.Lock()
	if !reflect.DeepEqual(app.activeAlerts,fresh.activeAlerts){t.Fatalf("active alerts differ source=%+v restored=%+v",app.activeAlerts,fresh.activeAlerts)}
	fresh.alertMu.Unlock()

	restoredAudit,err:=fresh.audit.List(0);if err!=nil{t.Fatal(err)}
	if len(restoredAudit)!=len(sourceAudit)+1{t.Fatalf("audit len=%d want=%d",len(restoredAudit),len(sourceAudit)+1)}
	for i:=range sourceAudit{if !reflect.DeepEqual(sourceAudit[i],restoredAudit[i]){t.Fatalf("audit prefix differs at %d",i)}}
	if restoredAudit[len(restoredAudit)-1].Action!="backup.restore"{t.Fatalf("restore audit=%+v",restoredAudit[len(restoredAudit)-1])}
	if err:=fresh.audit.Verify();err!=nil{t.Fatal(err)}
	t.Logf("PASS full encrypted restore nodes=%d ledger=%d history=%d audit=%d",len(restoredState.Nodes),len(restoredState.FinanceLedger),len(restoredState.History["n1"]),len(restoredAudit))
}

func TestBackupRejectsWrongKeyTamperAndSchemaWithoutChangingState(t *testing.T){
	dir:=t.TempDir()
	srcStore,_:=OpenStore(filepath.Join(dir,"src.json"))
	_,_ = srcStore.UpsertNode(Node{ID:"src",Alias:"Src",Address:"127.0.0.1:32100",Role:"foreign"},"src-token")
	src,_:=NewServer(srcStore,"admin")
	backupPath:=filepath.Join(dir,"valid.baftbak")
	if _,err:=src.BackupToFile(backupPath,backupTestKey(),time.Now());err!=nil{t.Fatal(err)}

	targetStore,_:=OpenStore(filepath.Join(dir,"target.json"))
	_,_ = targetStore.UpsertNode(Node{ID:"sentinel",Alias:"Sentinel",Address:"127.0.0.1:32101",Role:"foreign"},"sentinel-token")
	target,_:=NewServer(targetStore,"admin")
	before:=stateJSONForTest(t,targetStore)

	wrong:=bytes.Repeat([]byte{0x24},32)
	if err:=target.RestoreFromFile(backupPath,wrong);err==nil{t.Fatal("wrong backup key accepted")}
	if !bytes.Equal(before,stateJSONForTest(t,targetStore)){t.Fatal("wrong-key restore mutated state")}

	raw,_:=os.ReadFile(backupPath)
	var env encryptedBackup
	if err:=json.Unmarshal(raw,&env);err!=nil{t.Fatal(err)}
	if len(env.Ciphertext)<2{t.Fatal("ciphertext too short")}
	if env.Ciphertext[0]=='A'{env.Ciphertext="B"+env.Ciphertext[1:]}else{env.Ciphertext="A"+env.Ciphertext[1:]}
	tampered,_:=json.Marshal(env)
	tamperedPath:=filepath.Join(dir,"tampered.baftbak")
	_ = os.WriteFile(tamperedPath,tampered,0600)
	if err:=target.RestoreFromFile(tamperedPath,backupTestKey());err==nil{t.Fatal("tampered backup accepted")}
	if !bytes.Equal(before,stateJSONForTest(t,targetStore)){t.Fatal("tampered restore mutated state")}

	if err:=json.Unmarshal(raw,&env);err!=nil{t.Fatal(err)}
	env.Header.SchemaVersion=BackupSchemaVersion+1
	incompatible,_:=json.Marshal(env)
	schemaPath:=filepath.Join(dir,"schema.baftbak")
	_ = os.WriteFile(schemaPath,incompatible,0600)
	if err:=target.RestoreFromFile(schemaPath,backupTestKey());err==nil{t.Fatal("incompatible schema accepted")}
	if !bytes.Equal(before,stateJSONForTest(t,targetStore)){t.Fatal("schema rejection mutated state")}
	t.Log("PASS wrong key, tamper and incompatible schema rejected without state mutation")
}

func TestRestorePreservesPostBackupRotationAndRevoke(t *testing.T){
	dir:=t.TempDir()
	store,_:=OpenStore(filepath.Join(dir,"state.json"))
	app,_:=NewServer(store,"admin")
	t.Setenv("SEC_N1_OLD","old1")
	t.Setenv("SEC_N1_NEW","new1")
	t.Setenv("SEC_N2","token2")
	for _,body:=range []map[string]any{
		{"ID":"n1","Alias":"N1","Address":"127.0.0.1:32201","Role":"foreign","AgentTokenEnv":"SEC_N1_OLD"},
		{"ID":"n2","Alias":"N2","Address":"127.0.0.1:32202","Role":"foreign","AgentTokenEnv":"SEC_N2"},
	}{
		rr:=httptest.NewRecorder();app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes","admin",body))
		if rr.Code!=http.StatusCreated{t.Fatalf("register status=%d",rr.Code)}
	}
	path:=filepath.Join(dir,"pre-security.baftbak")
	if _,err:=app.BackupToFile(path,backupTestKey(),time.Now().UTC());err!=nil{t.Fatal(err)}

	rr:=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes/rotate-token","admin",map[string]any{"node_id":"n1","agent_token_env":"SEC_N1_NEW","grace_seconds":0}))
	if rr.Code!=http.StatusOK{t.Fatalf("rotate status=%d body=%s",rr.Code,rr.Body.String())}
	rr=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes/revoke","admin",map[string]any{"node_id":"n2","reason":"post-backup revoke"}))
	if rr.Code!=http.StatusOK{t.Fatalf("revoke status=%d body=%s",rr.Code,rr.Body.String())}

	if err:=app.RestoreFromFile(path,backupTestKey());err!=nil{t.Fatal(err)}
	store.mu.Lock()
	_,oldOK:=store.authorizedHashLocked("n1","old1",time.Now())
	_,newOK:=store.authorizedHashLocked("n1","new1",time.Now())
	_,revokedOK:=store.authorizedHashLocked("n2","token2",time.Now())
	n2:=store.st.Nodes["n2"]
	store.mu.Unlock()
	if oldOK||!newOK||revokedOK||!n2.Revoked{t.Fatalf("anti rollback old=%v new=%v revokedToken=%v n2=%+v",oldOK,newOK,revokedOK,n2)}
	if err:=app.audit.Verify();err!=nil{t.Fatal(err)}
	t.Log("PASS old backup could not roll back later token rotation or revoke")
}

func TestRestorePreservesTelemetryHighWatermarkAndFinanceIdempotency(t *testing.T){
	dir:=t.TempDir()
	store,_:=OpenStore(filepath.Join(dir,"state.json"))
	const token="telemetry-restore-token"
	_,_ = store.UpsertNode(Node{ID:"n1",Alias:"N1",Address:"127.0.0.1:32301",Role:"foreign"},token)
	_ = store.SetFinancePolicyAt("n1",100,300,"IRR",time.Unix(0,0))
	app,_:=NewServer(store,"admin")
	base:=time.Date(2026,9,28,12,0,0,0,time.UTC)
	rep1:=telemetry.Report{NodeID:"n1",BootID:"boot",Sequence:1,IngressBytes:100,EgressBytes:200,TimestampUnix:base.Unix()}
	applyTelemetryDirect(t,store,token,rep1)
	path:=filepath.Join(dir,"telemetry.baftbak")
	if _,err:=app.BackupToFile(path,backupTestKey(),base.Add(time.Minute));err!=nil{t.Fatal(err)}

	rep2:=rep1;rep2.Sequence=2;rep2.IngressBytes=300;rep2.EgressBytes=500;rep2.TimestampUnix=base.Add(2*time.Minute).Unix()
	applyTelemetryDirect(t,store,token,rep2)
	before:=store.FinanceSnapshot()[0]
	if err:=app.RestoreFromFile(path,backupTestKey());err!=nil{t.Fatal(err)}
	afterRestore:=store.FinanceSnapshot()[0]
	if !reflect.DeepEqual(before,afterRestore){t.Fatalf("finance rolled back before=%+v after=%+v",before,afterRestore)}

	body2,_:=json.Marshal(rep2)
	if _,dup,err:=store.ApplyTelemetry(token,telemetry.Sign(token,body2),body2,rep2);err!=nil||!dup{t.Fatalf("replayed telemetry dup=%v err=%v",dup,err)}
	afterDup:=store.FinanceSnapshot()[0]
	if !reflect.DeepEqual(afterRestore,afterDup){t.Fatalf("duplicate changed finance before=%+v after=%+v",afterRestore,afterDup)}

	rep3:=rep2;rep3.Sequence=3;rep3.IngressBytes=400;rep3.EgressBytes=700;rep3.TimestampUnix=base.Add(3*time.Minute).Unix()
	applyTelemetryDirect(t,store,token,rep3)
	final:=store.FinanceSnapshot()[0]
	if final.IngressBytes!=400||final.EgressBytes!=700{t.Fatalf("final finance counters=%+v",final)}
	t.Logf("PASS restore high-watermark duplicate-safe ingress=%d egress=%d",final.IngressBytes,final.EgressBytes)
}

func TestBackupSnapshotRemainsInternallyConsistentDuringTelemetry(t *testing.T){
	dir:=t.TempDir()
	store,_:=OpenStore(filepath.Join(dir,"state.json"))
	const token="concurrent-token"
	_,_ = store.UpsertNode(Node{ID:"n1",Alias:"N1",Address:"127.0.0.1:32401",Role:"foreign"},token)
	app,_:=NewServer(store,"admin")
	base:=time.Now().UTC()
	applyTelemetryDirect(t,store,token,telemetry.Report{NodeID:"n1",BootID:"b",Sequence:1,IngressBytes:100,EgressBytes:200,TimestampUnix:base.Unix()})

	var wg sync.WaitGroup
	wg.Add(1)
	go func(){
		defer wg.Done()
		for i:=uint64(2);i<=40;i++{
			rep:=telemetry.Report{NodeID:"n1",BootID:"b",Sequence:i,IngressBytes:i*100,EgressBytes:i*200,TimestampUnix:base.Add(time.Duration(i)*time.Second).Unix()}
			body,_:=json.Marshal(rep)
			_,_,_ = store.ApplyTelemetry(token,telemetry.Sign(token,body),body,rep)
			time.Sleep(time.Millisecond)
		}
	}()

	for i:=0;i<12;i++{
		data,_,err:=app.CreateBackupBytes(backupTestKey(),time.Now().UTC());if err!=nil{t.Fatal(err)}
		payload,_,err:=decodeBackup(data,backupTestKey());if err!=nil{t.Fatal(err)}
		cur:=payload.State.Telemetry["n1"]
		f:=payload.State.Finance["n1"]
		if f.IngressBytes!=cur.IngressBytes||f.EgressBytes!=cur.EgressBytes{
			t.Fatalf("inconsistent snapshot cursor=%+v finance=%+v",cur,f)
		}
		time.Sleep(time.Millisecond)
	}
	wg.Wait()
	t.Log("PASS concurrent telemetry snapshots were point-in-time consistent")
}

func TestScheduledBackupRetentionDefaultsSevenDailyFourWeekly(t *testing.T){
	dir:=t.TempDir()
	store,_:=OpenStore(filepath.Join(dir,"state.json"))
	app,_:=NewServer(store,"admin")
	start:=time.Date(2026,1,5,0,0,0,0,time.UTC) // Monday
	for i:=0;i<12;i++{
		if _,err:=app.ScheduledBackup(dir,backupTestKey(),start.AddDate(0,0,7*i),BackupRetention{});err!=nil{t.Fatal(err)}
	}
	entries,err:=os.ReadDir(dir);if err!=nil{t.Fatal(err)}
	var daily,weekly []string
	for _,e:=range entries{
		if strings.HasPrefix(e.Name(),"daily-"){daily=append(daily,e.Name())}
		if strings.HasPrefix(e.Name(),"weekly-"){weekly=append(weekly,e.Name())}
	}
	sort.Strings(daily);sort.Strings(weekly)
	if len(daily)!=7||len(weekly)!=4{t.Fatalf("retention daily=%d weekly=%d files=%v",len(daily),len(weekly),entries)}
	t.Logf("PASS retention daily=%d weekly=%d",len(daily),len(weekly))
}

func TestBackupKeyComesOnlyFromEnvironment(t *testing.T){
	key:=backupTestKey()
	encoded:=base64.StdEncoding.EncodeToString(key)
	t.Setenv("BAFT_BCC_BACKUP_KEY",encoded)
	got,err:=BackupKeyFromEnv();if err!=nil{t.Fatal(err)}
	if !bytes.Equal(got,key){t.Fatal("decoded backup key mismatch")}
	t.Setenv("BAFT_BCC_BACKUP_KEY","not-base64")
	if _,err:=BackupKeyFromEnv();err==nil{t.Fatal("invalid env backup key accepted")}
}


func TestTelemetryBootIDAntiRollbackEqualTimestampUsesIngestID(t *testing.T){
	dir:=t.TempDir()
	store,_:=OpenStore(filepath.Join(dir,"state.json"))
	const token="boot-equal-token"
	_,_ = store.UpsertNode(Node{ID:"n1",Alias:"N1",Address:"127.0.0.1:32501",Role:"foreign"},token)
	_ = store.SetFinancePolicyAt("n1",100,300,"IRR",time.Unix(0,0))
	app,_:=NewServer(store,"admin")
	base:=time.Date(2026,9,28,12,0,0,0,time.UTC)

	first:=telemetry.Report{NodeID:"n1",BootID:"boot-a",Sequence:1,IngressBytes:100,EgressBytes:200,TimestampUnix:base.Unix()}
	applyTelemetryDirect(t,store,token,first)
	path:=filepath.Join(dir,"equal.baftbak")
	if _,err:=app.BackupToFile(path,backupTestKey(),base.Add(time.Minute));err!=nil{t.Fatal(err)}

	second:=telemetry.Report{NodeID:"n1",BootID:"boot-b",Sequence:1,IngressBytes:40,EgressBytes:60,TimestampUnix:base.Unix()}
	applyTelemetryDirect(t,store,token,second)
	before:=store.FinanceSnapshot()[0]
	curBefore,_:=store.TelemetrySnapshot("n1")
	if curBefore.BootID!="boot-b"||curBefore.IngestID<2{t.Fatalf("missing server ingestion marker: %+v",curBefore)}

	if err:=app.RestoreFromFile(path,backupTestKey());err!=nil{t.Fatal(err)}
	curAfter,_:=store.TelemetrySnapshot("n1")
	if curAfter.BootID!="boot-b"||curAfter.IngestID!=curBefore.IngestID{t.Fatalf("cursor rolled back before=%+v after=%+v",curBefore,curAfter)}
	afterRestore:=store.FinanceSnapshot()[0]
	if !reflect.DeepEqual(before,afterRestore){t.Fatalf("finance rolled back before=%+v after=%+v",before,afterRestore)}

	body,_:=json.Marshal(second)
	if _,dup,err:=store.ApplyTelemetry(token,telemetry.Sign(token,body),body,second);err!=nil||!dup{t.Fatalf("replay duplicate=%v err=%v",dup,err)}
	afterReplay:=store.FinanceSnapshot()[0]
	if !reflect.DeepEqual(afterRestore,afterReplay){t.Fatalf("equal-timestamp replay changed finance before=%+v after=%+v",afterRestore,afterReplay)}
	t.Logf("PASS boot-ID anti-rollback equal timestamp ingest_id=%d duplicate=true",curAfter.IngestID)
}

func TestTelemetryBootIDAntiRollbackClockSkewUsesIngestID(t *testing.T){
	dir:=t.TempDir()
	store,_:=OpenStore(filepath.Join(dir,"state.json"))
	const token="boot-skew-token"
	_,_ = store.UpsertNode(Node{ID:"n1",Alias:"N1",Address:"127.0.0.1:32502",Role:"foreign"},token)
	_ = store.SetFinancePolicyAt("n1",100,300,"IRR",time.Unix(0,0))
	app,_:=NewServer(store,"admin")
	base:=time.Date(2026,9,28,12,0,0,0,time.UTC)

	first:=telemetry.Report{NodeID:"n1",BootID:"boot-a",Sequence:1,IngressBytes:100,EgressBytes:200,TimestampUnix:base.Unix()}
	applyTelemetryDirect(t,store,token,first)
	path:=filepath.Join(dir,"skew.baftbak")
	if _,err:=app.BackupToFile(path,backupTestKey(),base.Add(time.Minute));err!=nil{t.Fatal(err)}

	second:=telemetry.Report{NodeID:"n1",BootID:"boot-b",Sequence:1,IngressBytes:50,EgressBytes:70,TimestampUnix:base.Add(-2*time.Hour).Unix()}
	applyTelemetryDirect(t,store,token,second)
	curBefore,_:=store.TelemetrySnapshot("n1")
	before:=store.FinanceSnapshot()[0]
	if err:=app.RestoreFromFile(path,backupTestKey());err!=nil{t.Fatal(err)}
	curAfter,_:=store.TelemetrySnapshot("n1")
	if curAfter.IngestID!=curBefore.IngestID||curAfter.BootID!="boot-b"{t.Fatalf("clock-skew cursor rolled back before=%+v after=%+v",curBefore,curAfter)}
	if !reflect.DeepEqual(before,store.FinanceSnapshot()[0]){t.Fatal("clock-skew restore changed finance")}

	body,_:=json.Marshal(second)
	if _,dup,err:=store.ApplyTelemetry(token,telemetry.Sign(token,body),body,second);err!=nil||!dup{t.Fatalf("skew replay duplicate=%v err=%v",dup,err)}
	beforeNew:=store.FinanceSnapshot()[0]
	third:=second;third.Sequence=2;third.IngressBytes=80;third.EgressBytes=110;third.TimestampUnix=base.Add(-3*time.Hour).Unix()
	applyTelemetryDirect(t,store,token,third)
	final:=store.FinanceSnapshot()[0]
	if final.IngressBytes!=beforeNew.IngressBytes+30||final.EgressBytes!=beforeNew.EgressBytes+40{
		t.Fatalf("new telemetry did not add exact delta before=%+v final=%+v",beforeNew,final)
	}
	t.Logf("PASS boot-ID anti-rollback clock skew ingest_id=%d new_delta=30/40",curAfter.IngestID)
}

func TestRestoreFaultInjectionLeavesStateAndAuditUnchanged(t *testing.T){
	stages:=[]string{"state_stage_write","audit_stage_write","audit_verify","before_commit","after_state_commit"}
	for _,stage:=range stages{
		t.Run(stage,func(t *testing.T){
			dir:=t.TempDir()
			store,_:=OpenStore(filepath.Join(dir,"state.json"))
			app,_:=NewServer(store,"admin")
			t.Setenv("RESTORE_FAULT_TOKEN_"+strings.ToUpper(strings.ReplaceAll(stage,"_","X")),"tok")
			envName:="RESTORE_FAULT_TOKEN_"+strings.ToUpper(strings.ReplaceAll(stage,"_","X"))
			rr:=httptest.NewRecorder()
			app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes","admin",map[string]any{
				"ID":"n1","Alias":"N1","Address":"127.0.0.1:32601","Role":"foreign","AgentTokenEnv":envName,
			}))
			if rr.Code!=http.StatusCreated{t.Fatalf("register status=%d body=%s",rr.Code,rr.Body.String())}
			path:=filepath.Join(dir,"backup.baftbak")
			if _,err:=app.BackupToFile(path,backupTestKey(),time.Now().UTC());err!=nil{t.Fatal(err)}
			beforeState:=stateJSONForTest(t,store)
			beforeDisk:=diskStateJSONForTest(t,store)
			beforeAudit,err:=os.ReadFile(app.audit.path);if err!=nil{t.Fatal(err)}

			app.restoreFault=func(got string) error {
				if got==stage{return errors.New("injected "+stage)}
				return nil
			}
			if err:=app.RestoreFromFile(path,backupTestKey());err==nil{t.Fatalf("restore unexpectedly succeeded at %s",stage)}
			app.restoreFault=nil

			afterState:=stateJSONForTest(t,store)
			afterAudit,err:=os.ReadFile(app.audit.path);if err!=nil{t.Fatal(err)}
			if !bytes.Equal(beforeState,afterState){t.Fatalf("state changed after %s",stage)}
			if !bytes.Equal(beforeDisk,diskStateJSONForTest(t,store)){t.Fatalf("state database changed after %s",stage)}
			if !bytes.Equal(beforeAudit,afterAudit){t.Fatalf("audit changed after %s",stage)}
			if err:=app.audit.Verify();err!=nil{t.Fatalf("audit invalid after %s: %v",stage,err)}

			req:=authReq(http.MethodGet,"/api/nodes","admin",nil)
			resp:=httptest.NewRecorder();app.Handler().ServeHTTP(resp,req)
			if resp.Code!=http.StatusOK{t.Fatalf("BCC not operational after %s status=%d",stage,resp.Code)}
		})
	}
	t.Log("PASS restore fault injection left existing state/audit unchanged and BCC operational")
}

func TestRestoreRejectsCorruptCurrentAuditBeforeMutation(t *testing.T){
	dir:=t.TempDir()
	store,_:=OpenStore(filepath.Join(dir,"state.json"))
	app,_:=NewServer(store,"admin")
	t.Setenv("CORRUPT_AUDIT_NODE","token")
	rr:=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes","admin",map[string]any{
		"ID":"n1","Alias":"N1","Address":"127.0.0.1:32701","Role":"foreign","AgentTokenEnv":"CORRUPT_AUDIT_NODE",
	}))
	if rr.Code!=http.StatusCreated{t.Fatalf("register status=%d",rr.Code)}
	path:=filepath.Join(dir,"backup.baftbak")
	if _,err:=app.BackupToFile(path,backupTestKey(),time.Now().UTC());err!=nil{t.Fatal(err)}

	rr=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/finance","admin",map[string]any{
		"node_id":"n1","cost_micros_per_gib":10,"revenue_micros_per_gib":20,"currency":"IRR",
	}))
	if rr.Code!=http.StatusOK{t.Fatalf("finance status=%d body=%s",rr.Code,rr.Body.String())}
	beforeState:=stateJSONForTest(t,store)
	raw,err:=os.ReadFile(app.audit.path);if err!=nil{t.Fatal(err)}
	corrupt:=bytes.Replace(raw,[]byte("finance.rate.change"),[]byte("finance.rate.changf"),1)
	if bytes.Equal(raw,corrupt){t.Fatal("failed to corrupt current audit")}
	if err:=os.WriteFile(app.audit.path,corrupt,0600);err!=nil{t.Fatal(err)}

	if err:=app.RestoreFromFile(path,backupTestKey());err==nil{t.Fatal("restore accepted corrupt current audit")}
	if !bytes.Equal(beforeState,stateJSONForTest(t,store)){t.Fatal("restore mutated state despite corrupt current audit")}
	afterAudit,err:=os.ReadFile(app.audit.path);if err!=nil{t.Fatal(err)}
	if !bytes.Equal(corrupt,afterAudit){t.Fatal("restore rewrote corrupt current audit before rejection")}
	t.Log("PASS corrupt current audit rejected before any restore mutation")
}
