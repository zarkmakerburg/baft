package bcc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type anchorReceiver struct {
	available atomic.Bool
	mu sync.Mutex
	delivered []AuditAnchor
	keys []string
}

func (r *anchorReceiver) handler(w http.ResponseWriter,req *http.Request){
	if !r.available.Load(){http.Error(w,"down",http.StatusServiceUnavailable);return}
	defer req.Body.Close()
	var a AuditAnchor
	if err:=json.NewDecoder(req.Body).Decode(&a);err!=nil{http.Error(w,"bad",400);return}
	r.mu.Lock()
	r.delivered=append(r.delivered,a)
	r.keys=append(r.keys,req.Header.Get("Idempotency-Key"))
	r.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (r *anchorReceiver) snapshot()([]AuditAnchor,[]string){
	r.mu.Lock();defer r.mu.Unlock()
	return append([]AuditAnchor(nil),r.delivered...),append([]string(nil),r.keys...)
}

func waitPendingEmpty(t *testing.T,s *Server,timeout time.Duration){
	t.Helper()
	deadline:=time.Now().Add(timeout)
	for{
		if len(s.anchorOutbox.Pending())==0{return}
		if time.Now().After(deadline){t.Fatalf("anchor outbox did not drain: %+v",s.anchorOutbox.Pending())}
		time.Sleep(5*time.Millisecond)
	}
}

func restartAnchorServer(t *testing.T,statePath,webhook string)(*Store,*Server,context.CancelFunc){
	t.Helper()
	store,err:=OpenStore(statePath);if err!=nil{t.Fatal(err)}
	app,err:=NewServer(store,"admin");if err!=nil{t.Fatal(err)}
	if err:=app.ConfigureAuditAnchoring(webhook,time.Hour);err!=nil{t.Fatal(err)}
	app.auditAnchorRetryBase=5*time.Millisecond
	app.auditAnchorRetryMax=20*time.Millisecond
	ctx,cancel:=context.WithCancel(context.Background())
	go app.StartAuditAnchorLoop(ctx)
	return store,app,cancel
}

func TestDurableRevokeAnchorRetryAfterRestart(t *testing.T){
	dir:=t.TempDir();statePath:=filepath.Join(dir,"state.json")
	recv:=&anchorReceiver{}
	hook:=httptest.NewServer(http.HandlerFunc(recv.handler));defer hook.Close()

	store,_:=OpenStore(statePath);app,_:=NewServer(store,"admin")
	t.Setenv("OUTBOX_REVOKE_TOKEN","token-r")
	rr:=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes","admin",map[string]any{
		"ID":"n1","Alias":"N1","Address":"127.0.0.1:33001","Role":"foreign","AgentTokenEnv":"OUTBOX_REVOKE_TOKEN",
	}))
	if rr.Code!=http.StatusCreated{t.Fatalf("register=%d body=%s",rr.Code,rr.Body.String())}
	if err:=app.ConfigureAuditAnchoring(hook.URL,time.Hour);err!=nil{t.Fatal(err)}

	rr=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes/revoke","admin",map[string]any{"node_id":"n1","reason":"outage"}))
	if rr.Code!=http.StatusOK{t.Fatalf("revoke=%d body=%s",rr.Code,rr.Body.String())}
	pending:=app.anchorOutbox.Pending()
	if len(pending)!=1{t.Fatalf("pending revoke anchors=%+v",pending)}
	revokeAnchor:=pending[0]

	recv.available.Store(true)
	_,restarted,cancel:=restartAnchorServer(t,statePath,hook.URL);defer cancel()
	waitPendingEmpty(t,restarted,2*time.Second)
	got,_:=recv.snapshot()
	if len(got)!=1||got[0].Sequence!=revokeAnchor.Sequence||got[0].Hash!=revokeAnchor.Hash{
		t.Fatalf("revoke-era anchor mismatch want=%+v got=%+v",revokeAnchor,got)
	}
	t.Logf("PASS revoke anchor survived outage+restart sequence=%d",revokeAnchor.Sequence)
}

func TestDurableRotationAnchorRetryAfterRestart(t *testing.T){
	dir:=t.TempDir();statePath:=filepath.Join(dir,"state.json")
	recv:=&anchorReceiver{}
	hook:=httptest.NewServer(http.HandlerFunc(recv.handler));defer hook.Close()

	store,_:=OpenStore(statePath);app,_:=NewServer(store,"admin")
	t.Setenv("OUTBOX_ROTATE_OLD","old")
	t.Setenv("OUTBOX_ROTATE_NEW","new")
	rr:=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes","admin",map[string]any{
		"ID":"n1","Alias":"N1","Address":"127.0.0.1:33002","Role":"foreign","AgentTokenEnv":"OUTBOX_ROTATE_OLD",
	}))
	if rr.Code!=http.StatusCreated{t.Fatalf("register=%d",rr.Code)}
	if err:=app.ConfigureAuditAnchoring(hook.URL,time.Hour);err!=nil{t.Fatal(err)}

	rr=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes/rotate-token","admin",map[string]any{
		"node_id":"n1","agent_token_env":"OUTBOX_ROTATE_NEW","grace_seconds":0,
	}))
	if rr.Code!=http.StatusOK{t.Fatalf("rotate=%d body=%s",rr.Code,rr.Body.String())}
	pending:=app.anchorOutbox.Pending()
	if len(pending)!=1{t.Fatalf("pending rotation anchors=%+v",pending)}
	rotationAnchor:=pending[0]

	recv.available.Store(true)
	_,restarted,cancel:=restartAnchorServer(t,statePath,hook.URL);defer cancel()
	waitPendingEmpty(t,restarted,2*time.Second)
	got,_:=recv.snapshot()
	if len(got)!=1||got[0].Sequence!=rotationAnchor.Sequence||got[0].Hash!=rotationAnchor.Hash{
		t.Fatalf("rotation-era anchor mismatch want=%+v got=%+v",rotationAnchor,got)
	}
	t.Logf("PASS rotation anchor survived outage+restart sequence=%d",rotationAnchor.Sequence)
}

func TestMultiplePendingAuditAnchorsPreserveOrderAndNoLoss(t *testing.T){
	dir:=t.TempDir();statePath:=filepath.Join(dir,"state.json")
	recv:=&anchorReceiver{}
	hook:=httptest.NewServer(http.HandlerFunc(recv.handler));defer hook.Close()
	store,_:=OpenStore(statePath);app,_:=NewServer(store,"admin")

	envs:=[]string{"OUTBOX_MULTI_1","OUTBOX_MULTI_2","OUTBOX_MULTI_3"}
	for i,env:=range envs{
		t.Setenv(env,"token-"+env)
		rr:=httptest.NewRecorder()
		app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes","admin",map[string]any{
			"ID":string(rune('a'+i)),"Alias":"N","Address":"127.0.0.1:3310"+string(rune('1'+i)),"Role":"foreign","AgentTokenEnv":env,
		}))
		if rr.Code!=http.StatusCreated{t.Fatalf("register %d status=%d body=%s",i,rr.Code,rr.Body.String())}
	}
	t.Setenv("OUTBOX_MULTI_NEW_A","new-a")
	t.Setenv("OUTBOX_MULTI_NEW_C","new-c")
	if err:=app.ConfigureAuditAnchoring(hook.URL,time.Hour);err!=nil{t.Fatal(err)}

	actions:=[]*http.Request{
		authReq(http.MethodPost,"/api/nodes/rotate-token","admin",map[string]any{"node_id":"a","agent_token_env":"OUTBOX_MULTI_NEW_A","grace_seconds":0}),
		authReq(http.MethodPost,"/api/nodes/revoke","admin",map[string]any{"node_id":"b","reason":"multi"}),
		authReq(http.MethodPost,"/api/nodes/rotate-token","admin",map[string]any{"node_id":"c","agent_token_env":"OUTBOX_MULTI_NEW_C","grace_seconds":0}),
	}
	for i,req:=range actions{
		rr:=httptest.NewRecorder();app.Handler().ServeHTTP(rr,req)
		if rr.Code!=http.StatusOK{t.Fatalf("action %d status=%d body=%s",i,rr.Code,rr.Body.String())}
	}
	pending:=app.anchorOutbox.Pending()
	if len(pending)!=3{t.Fatalf("pending anchors=%+v",pending)}
	for i:=1;i<len(pending);i++{if pending[i].Sequence<=pending[i-1].Sequence{t.Fatalf("pending order=%+v",pending)}}

	recv.available.Store(true)
	_,restarted,cancel:=restartAnchorServer(t,statePath,hook.URL);defer cancel()
	waitPendingEmpty(t,restarted,2*time.Second)
	got,_:=recv.snapshot()
	if len(got)!=3{t.Fatalf("delivered=%+v",got)}
	for i:=range pending{
		if got[i].Sequence!=pending[i].Sequence||got[i].Hash!=pending[i].Hash{t.Fatalf("anchor %d want=%+v got=%+v",i,pending[i],got[i])}
	}
	t.Logf("PASS three pending anchors delivered in order sequences=%d,%d,%d",got[0].Sequence,got[1].Sequence,got[2].Sequence)
}

func TestAuditAnchorACKRemovesPendingAndProvidesIdempotencyKey(t *testing.T){
	dir:=t.TempDir();statePath:=filepath.Join(dir,"state.json")
	recv:=&anchorReceiver{};recv.available.Store(true)
	hook:=httptest.NewServer(http.HandlerFunc(recv.handler));defer hook.Close()
	store,_:=OpenStore(statePath);app,_:=NewServer(store,"admin")
	t.Setenv("OUTBOX_ACK_OLD","old");t.Setenv("OUTBOX_ACK_NEW","new")
	rr:=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes","admin",map[string]any{
		"ID":"n1","Alias":"N1","Address":"127.0.0.1:33201","Role":"foreign","AgentTokenEnv":"OUTBOX_ACK_OLD",
	}))
	if rr.Code!=http.StatusCreated{t.Fatalf("register=%d",rr.Code)}
	if err:=app.ConfigureAuditAnchoring(hook.URL,time.Hour);err!=nil{t.Fatal(err)}
	rr=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes/rotate-token","admin",map[string]any{
		"node_id":"n1","agent_token_env":"OUTBOX_ACK_NEW","grace_seconds":0,
	}))
	if rr.Code!=http.StatusOK{t.Fatalf("rotate=%d body=%s",rr.Code,rr.Body.String())}
	if len(app.anchorOutbox.Pending())!=0{t.Fatalf("2xx did not ACK pending=%+v",app.anchorOutbox.Pending())}
	got,keys:=recv.snapshot()
	if len(got)!=1||len(keys)!=1||keys[0]==""{t.Fatalf("receiver anchors=%+v keys=%v",got,keys)}
	wantKey:="baft-audit:"+json.Number(string(rune(0))).String()
	_ = wantKey
	expected:="baft-audit:"+formatUint(got[0].Sequence)+":"+got[0].Hash
	if keys[0]!=expected{t.Fatalf("idempotency key=%q want=%q",keys[0],expected)}
	if app.anchorOutbox.AckedThrough()<got[0].Sequence{t.Fatalf("acked through=%d anchor=%d",app.anchorOutbox.AckedThrough(),got[0].Sequence)}
	t.Logf("PASS 2xx ACK removed pending idempotency_key=%s",keys[0])
}

func formatUint(v uint64) string {
	if v==0{return "0"}
	var buf [20]byte
	i:=len(buf)
	for v>0{i--;buf[i]=byte('0'+v%10);v/=10}
	return string(buf[i:])
}
