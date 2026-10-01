package bcc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAuditAnchorsDetectFullyRewrittenChainAndFireOnSecurityChanges(t *testing.T){
	var mu sync.Mutex
	var anchors []AuditAnchor
	hook:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		defer r.Body.Close()
		var a AuditAnchor
		if err:=json.NewDecoder(r.Body).Decode(&a);err!=nil{t.Error(err);http.Error(w,"bad",400);return}
		mu.Lock();anchors=append(anchors,a);mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hook.Close()

	store,_:=OpenStore(filepath.Join(t.TempDir(),"state.json"))
	app,_:=NewServer(store,"admin")
	if err:=app.ConfigureAuditAnchoring(hook.URL,time.Hour);err!=nil{t.Fatal(err)}
	t.Setenv("ANCHOR_NODE_TOKEN","token-a")
	t.Setenv("ANCHOR_NODE_TOKEN_NEW","token-b")

	rr:=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes","admin",map[string]any{
		"ID":"n1","Alias":"N1","Address":"127.0.0.1:31001","Role":"foreign","AgentTokenEnv":"ANCHOR_NODE_TOKEN",
	}))
	if rr.Code!=http.StatusCreated{t.Fatalf("register status=%d body=%s",rr.Code,rr.Body.String())}
	if err:=app.SendAuditAnchor(context.Background());err!=nil{t.Fatal(err)}
	baseAnchor:=app.audit.CurrentAnchor()

	rr=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes/rotate-token","admin",map[string]any{
		"node_id":"n1","agent_token_env":"ANCHOR_NODE_TOKEN_NEW","grace_seconds":0,
	}))
	if rr.Code!=http.StatusOK{t.Fatalf("rotate status=%d body=%s",rr.Code,rr.Body.String())}

	rr=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes/revoke","admin",map[string]any{
		"node_id":"n1","reason":"anchor-test",
	}))
	if rr.Code!=http.StatusOK{t.Fatalf("revoke status=%d body=%s",rr.Code,rr.Body.String())}

	mu.Lock()
	got:=append([]AuditAnchor(nil),anchors...)
	mu.Unlock()
	if len(got)<3{t.Fatalf("expected manual + rotation + revoke anchors, got=%d",len(got))}
	if got[len(got)-1].Sequence<=baseAnchor.Sequence{t.Fatalf("anchor sequence did not advance: %+v",got)}

	entries,err:=app.audit.List(0);if err!=nil{t.Fatal(err)}
	rewrittenPath:=filepath.Join(t.TempDir(),"rewritten.audit.jsonl")
	rewritten,err:=OpenAuditLog(rewrittenPath);if err!=nil{t.Fatal(err)}
	for _,e:=range entries{
		e.Action="rewritten."+e.Action
		e.Sequence=0;e.PrevHash="";e.Hash=""
		if _,err:=rewritten.Append(e);err!=nil{t.Fatal(err)}
	}
	if err:=rewritten.Verify();err!=nil{t.Fatalf("rewritten chain should be internally valid: %v",err)}
	if err:=rewritten.VerifyAgainstAnchors([]AuditAnchor{baseAnchor});err==nil{
		t.Fatal("fully rewritten self-consistent audit chain matched external anchor")
	}
	t.Logf("PASS external anchors=%d detect full hash-chain rewrite",len(got))
}

func TestTrustedProxyXForwardedForAcceptedOnlyFromConfiguredProxy(t *testing.T){
	store,_:=OpenStore(filepath.Join(t.TempDir(),"state.json"))
	app,_:=NewServer(store,"admin")
	if err:=app.ConfigureTrustedProxies([]string{"10.0.0.1","2001:db8::1"});err!=nil{t.Fatal(err)}

	trusted:=httptest.NewRequest(http.MethodGet,"/api/nodes",nil)
	trusted.RemoteAddr="10.0.0.1:12345"
	trusted.Header.Set("X-Forwarded-For","198.51.100.77, 10.0.0.1")
	if got:=app.clientIP(trusted);got!="198.51.100.77"{t.Fatalf("trusted proxy client ip=%q",got)}

	untrusted:=httptest.NewRequest(http.MethodGet,"/api/nodes",nil)
	untrusted.RemoteAddr="10.0.0.2:12345"
	untrusted.Header.Set("X-Forwarded-For","198.51.100.88")
	if got:=app.clientIP(untrusted);got!="10.0.0.2"{t.Fatalf("untrusted XFF was trusted, got=%q",got)}

	invalid:=httptest.NewRequest(http.MethodGet,"/api/nodes",nil)
	invalid.RemoteAddr="10.0.0.1:12345"
	invalid.Header.Set("X-Forwarded-For","not-an-ip")
	if got:=app.clientIP(invalid);got!="10.0.0.1"{t.Fatalf("invalid XFF should fall back to proxy ip, got=%q",got)}
	t.Log("PASS trusted proxy XFF accepted; untrusted/invalid XFF ignored")
}
