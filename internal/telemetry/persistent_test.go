package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newToggleTelemetryServer(t *testing.T,token string)(*httptest.Server,*atomic.Bool,*sync.Mutex,*[]Report){
	t.Helper()
	var available atomic.Bool
	var mu sync.Mutex
	var accepted []Report
	hash:=sha256.Sum256([]byte(token))
	tokenHash:=hex.EncodeToString(hash[:])
	srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		if !available.Load(){http.Error(w,"offline",http.StatusServiceUnavailable);return}
		body,err:=io.ReadAll(r.Body);if err!=nil{t.Error(err);http.Error(w,"read",500);return}
		if r.Header.Get("Authorization")!="Bearer "+token{t.Errorf("authorization mismatch");http.Error(w,"auth",401);return}
		if !VerifyHashedToken(tokenHash,r.Header.Get("X-BAFT-Signature"),body){t.Errorf("signature mismatch");http.Error(w,"sig",401);return}
		var rep Report
		if err:=json.Unmarshal(body,&rep);err!=nil{t.Error(err);http.Error(w,"json",400);return}
		mu.Lock();accepted=append(accepted,rep);mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	return srv,&available,&mu,&accepted
}

func TestPersistentQueueSurvivesRestartAndDeliversInOrder(t *testing.T){
	dir:=t.TempDir();path:=filepath.Join(dir,"telemetry.spool")
	srv,available,mu,accepted:=newToggleTelemetryServer(t,"agent");defer srv.Close()
	var raw atomic.Uint64
	source:=func()Snapshot{v:=raw.Add(10);return Snapshot{IngressBytes:v,EgressBytes:v*2}}

	e1,err:=NewPersistent("n1",srv.URL,"agent",path,time.Second,10,source);if err!=nil{t.Fatal(err)}
	boot:=e1.BootID
	for i:=0;i<3;i++{if err:=e1.SendOnce(context.Background());err==nil{t.Fatal("expected outage error")}}
	if e1.QueueLength()!=3{t.Fatalf("pending=%d",e1.QueueLength())}

	var restartedRaw atomic.Uint64
	e2,err:=NewPersistent("n1",srv.URL,"agent",path,time.Second,10,func()Snapshot{
		v:=restartedRaw.Load()
		return Snapshot{IngressBytes:v,EgressBytes:v*2}
	});if err!=nil{t.Fatal(err)}
	if e2.BootID!=boot{t.Fatalf("boot id changed %s -> %s",boot,e2.BootID)}
	st:=e2.Spool.Snapshot()
	if st.NextSequence!=4||len(st.Pending)!=3{t.Fatalf("reopened spool=%+v",st)}
	for i,r:=range st.Pending{if r.Sequence!=uint64(i+1){t.Fatalf("pending order=%+v",st.Pending)}}

	available.Store(true)
	if err:=e2.flush(context.Background());err!=nil{t.Fatal(err)}
	if e2.QueueLength()!=0{t.Fatalf("queue not drained=%d",e2.QueueLength())}
	mu.Lock();got:=append([]Report(nil),(*accepted)...);mu.Unlock()
	if len(got)!=3||got[0].Sequence!=1||got[1].Sequence!=2||got[2].Sequence!=3{t.Fatalf("delivery order=%+v",got)}
	t.Logf("PASS durable queue restart boot=%s sequences=1,2,3",boot)
}

func TestMultipleRestartsDuringOutagePreserveOrder(t *testing.T){
	dir:=t.TempDir();path:=filepath.Join(dir,"multi.spool")
	srv,available,mu,accepted:=newToggleTelemetryServer(t,"agent");defer srv.Close()
	var boot string
	for i:=0;i<3;i++{
		var raw atomic.Uint64
		e,err:=NewPersistent("n1",srv.URL,"agent",path,time.Second,10,func()Snapshot{
			v:=raw.Add(uint64(i+1))
			return Snapshot{IngressBytes:v,EgressBytes:v}
		});if err!=nil{t.Fatal(err)}
		if i==0{boot=e.BootID}else if e.BootID!=boot{t.Fatalf("boot changed at restart %d",i)}
		if err:=e.SendOnce(context.Background());err==nil{t.Fatal("expected outage")}
	}
	e,err:=NewPersistent("n1",srv.URL,"agent",path,time.Second,10,func()Snapshot{return Snapshot{}});if err!=nil{t.Fatal(err)}
	available.Store(true)
	if err:=e.flush(context.Background());err!=nil{t.Fatal(err)}
	mu.Lock();got:=append([]Report(nil),(*accepted)...);mu.Unlock()
	if len(got)!=3{t.Fatalf("delivered=%d reports=%+v",len(got),got)}
	for i,r:=range got{if r.Sequence!=uint64(i+1)||r.BootID!=boot{t.Fatalf("delivery[%d]=%+v",i,r)}}
	t.Log("PASS three exporter restarts during outage preserved boot and ordered sequences")
}

func validSpoolWithTwoReports(t *testing.T,path string) spoolEnvelope {
	t.Helper()
	s,err:=OpenSpool(path,"n1",10);if err!=nil{t.Fatal(err)}
	boot:=s.BootID()
	for i:=uint64(1);i<=2;i++{
		if err:=s.Enqueue(Report{NodeID:"n1",BootID:boot,Sequence:i,IngressBytes:i*10,EgressBytes:i*20});err!=nil{t.Fatal(err)}
	}
	raw,err:=os.ReadFile(path);if err!=nil{t.Fatal(err)}
	var env spoolEnvelope
	if err:=json.Unmarshal(raw,&env);err!=nil{t.Fatal(err)}
	return env
}

func writeEnvelope(t *testing.T,path string,env spoolEnvelope){
	t.Helper()
	raw,err:=json.MarshalIndent(env,"","  ");if err!=nil{t.Fatal(err)}
	if err:=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)}
}

func TestCorruptedSpoolFailsClosed(t *testing.T){
	tests:=[]struct{
		name string
		mutate func(t *testing.T,path string,env spoolEnvelope)
	}{
		{"truncated_tail",func(t *testing.T,path string,env spoolEnvelope){
			raw,_:=os.ReadFile(path);if len(raw)<8{t.Fatal("short spool")}
			if err:=os.WriteFile(path,raw[:len(raw)-7],0600);err!=nil{t.Fatal(err)}
		}},
		{"bad_checksum",func(t *testing.T,path string,env spoolEnvelope){env.Checksum=strings.Repeat("0",64);writeEnvelope(t,path,env)}},
		{"bad_schema",func(t *testing.T,path string,env spoolEnvelope){
			env.State.SchemaVersion=99;sum,_:=spoolChecksum(env.State);env.Checksum=sum;writeEnvelope(t,path,env)
		}},
		{"mid_file_corruption",func(t *testing.T,path string,env spoolEnvelope){
			raw,_:=os.ReadFile(path)
			idx:=bytes.Index(raw,[]byte("\"ingress_bytes\": 10"))
			if idx<0{idx=len(raw)/2}
			raw[idx]^=1
			if err:=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)}
		}},
		{"invalid_sequence_order",func(t *testing.T,path string,env spoolEnvelope){
			env.State.Pending[1].Sequence=7
			sum,_:=spoolChecksum(env.State);env.Checksum=sum;writeEnvelope(t,path,env)
		}},
	}
	for _,tc:=range tests{
		t.Run(tc.name,func(t *testing.T){
			path:=filepath.Join(t.TempDir(),"spool.json")
			env:=validSpoolWithTwoReports(t,path)
			before,_:=os.ReadFile(path)
			tc.mutate(t,path,env)
			corrupt,_:=os.ReadFile(path)
			_,err:=OpenSpool(path,"n1",10)
			if err==nil{t.Fatalf("corrupt spool accepted")}
			after,_:=os.ReadFile(path)
			if !bytes.Equal(corrupt,after){t.Fatal("failed open silently rewrote/reset spool")}
			if bytes.Equal(before,after)&&tc.name!="invalid_sequence_order"{t.Fatal("mutation did not alter fixture")}
		})
	}
	path:=filepath.Join(t.TempDir(),"binding.json")
	_ = validSpoolWithTwoReports(t,path)
	if _,err:=OpenSpool(path,"other-node",10);err==nil{t.Fatal("invalid node binding accepted")}
	t.Log("PASS truncated/checksum/schema/mid-file/order/binding corruption fail closed")
}

func TestSpoolCapacityNoSilentEviction(t *testing.T){
	path:=filepath.Join(t.TempDir(),"capacity.json")
	s,err:=OpenSpool(path,"n1",2);if err!=nil{t.Fatal(err)}
	boot:=s.BootID()
	if err:=s.Enqueue(Report{NodeID:"n1",BootID:boot,Sequence:1,IngressBytes:10});err!=nil{t.Fatal(err)}
	if err:=s.Enqueue(Report{NodeID:"n1",BootID:boot,Sequence:2,IngressBytes:20});err!=nil{t.Fatal(err)}
	if err:=s.Enqueue(Report{NodeID:"n1",BootID:boot,Sequence:3,IngressBytes:30});!errors.Is(err,ErrSpoolFull){t.Fatalf("capacity err=%v",err)}
	st:=s.Snapshot()
	if len(st.Pending)!=2||st.Pending[0].Sequence!=1||st.Pending[1].Sequence!=2||st.NextSequence!=3{
		t.Fatalf("capacity silently evicted/advanced: %+v",st)
	}
	t.Log("PASS queue full explicit and oldest pending preserved")
}

func TestPendingReportsUseCurrentTokenAfterRotationAndSpoolHasNoSecret(t *testing.T){
	dir:=t.TempDir();path:=filepath.Join(dir,"token.spool")
	var available atomic.Bool
	const oldToken="old-agent-secret"
	const newToken="new-agent-secret"
	newHash:=sha256.Sum256([]byte(newToken))
	var accepted Report
	srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		if !available.Load(){http.Error(w,"down",503);return}
		body,_:=io.ReadAll(r.Body)
		if r.Header.Get("Authorization")!="Bearer "+newToken{http.Error(w,"wrong token",401);return}
		if !VerifyHashedToken(hex.EncodeToString(newHash[:]),r.Header.Get("X-BAFT-Signature"),body){http.Error(w,"bad sig",401);return}
		if err:=json.Unmarshal(body,&accepted);err!=nil{http.Error(w,"bad json",400);return}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	e1,err:=NewPersistent("n1",srv.URL,oldToken,path,time.Second,10,func()Snapshot{return Snapshot{IngressBytes:123,EgressBytes:456}});if err!=nil{t.Fatal(err)}
	if err:=e1.SendOnce(context.Background());err==nil{t.Fatal("expected outage")}
	raw,err:=os.ReadFile(path);if err!=nil{t.Fatal(err)}
	if bytes.Contains(raw,[]byte(oldToken))||bytes.Contains(raw,[]byte(newToken))||bytes.Contains(raw,[]byte("Authorization")){
		t.Fatal("credential material leaked into spool")
	}
	pending:=e1.Spool.Snapshot().Pending
	if len(pending)!=1{t.Fatalf("pending=%+v",pending)}
	available.Store(true)
	e2,err:=NewPersistent("n1",srv.URL,newToken,path,time.Second,10,func()Snapshot{return Snapshot{}});if err!=nil{t.Fatal(err)}
	if err:=e2.flush(context.Background());err!=nil{t.Fatal(err)}
	if e2.QueueLength()!=0{t.Fatal("pending report not acked after token rotation")}
	if !reflect.DeepEqual(accepted,pending[0]){t.Fatalf("payload mutated across token rotation want=%+v got=%+v",pending[0],accepted)}
	t.Log("PASS queued payload delivered with current token and no secret persisted")
}

func TestConcurrentSampleFlushAndSpoolOpen(t *testing.T){
	dir:=t.TempDir();path:=filepath.Join(dir,"race.spool")
	srv,available,_,_:=newToggleTelemetryServer(t,"agent");defer srv.Close();available.Store(true)
	var n atomic.Uint64
	e,err:=NewPersistent("n1",srv.URL,"agent",path,time.Millisecond,500,func()Snapshot{
		v:=n.Add(1)
		return Snapshot{IngressBytes:v,EgressBytes:v*2,HandshakeErrors:v/7}
	});if err!=nil{t.Fatal(err)}
	var wg sync.WaitGroup
	errCh:=make(chan error,3)
	wg.Add(3)
	go func(){defer wg.Done();for i:=0;i<100;i++{if err:=e.sample(time.Now());err!=nil{errCh<-err;return}}}()
	go func(){defer wg.Done();for i:=0;i<100;i++{if err:=e.flush(context.Background());err!=nil{errCh<-err;return}}}()
	go func(){defer wg.Done();for i:=0;i<100;i++{s,err:=OpenSpool(path,"n1",500);if err!=nil{errCh<-err;return};_ = s.PendingCount()}}()
	wg.Wait();close(errCh)
	for err:=range errCh{if err!=nil{t.Fatal(err)}}
	if err:=e.flush(context.Background());err!=nil{t.Fatal(err)}
	st:=e.Status()
	if st.PendingCount!=0||st.SpoolHealth!="ok"||st.LatestSequence==0{t.Fatalf("status=%+v",st)}
	info,err:=os.Stat(path);if err!=nil{t.Fatal(err)}
	if info.Mode().Perm()!=0600{t.Fatalf("spool mode=%o",info.Mode().Perm())}
	dinfo,err:=os.Stat(filepath.Dir(path));if err!=nil{t.Fatal(err)}
	if dinfo.Mode().Perm()!=0700{t.Fatalf("spool dir mode=%o",dinfo.Mode().Perm())}
	t.Logf("PASS concurrent sample/flush/open latest=%d",st.LatestSequence)
}
