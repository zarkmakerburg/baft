package bcc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

func TestAuditLogAdminTransactionsAndTamperDetection(t *testing.T){
	dir:=t.TempDir()
	store,err:=OpenStore(filepath.Join(dir,"state.json"));if err!=nil{t.Fatal(err)}
	app,err:=NewServer(store,"admin");if err!=nil{t.Fatal(err)}
	t.Setenv("NODE_A","token-a")
	t.Setenv("NODE_B","token-b")
	t.Setenv("NODE_A_NEW","token-a-new")

	do:=func(req *http.Request,want int){
		t.Helper();rr:=httptest.NewRecorder();app.Handler().ServeHTTP(rr,req)
		if rr.Code!=want{t.Fatalf("%s %s status=%d body=%s",req.Method,req.URL.Path,rr.Code,rr.Body.String())}
	}
	do(authReq(http.MethodPost,"/api/nodes","admin",map[string]any{"ID":"ex1","Alias":"EX1","Address":"127.0.0.1:28001","Role":"foreign","AgentTokenEnv":"NODE_A"}),http.StatusCreated)
	do(authReq(http.MethodPost,"/api/nodes","admin",map[string]any{"ID":"worker1","Alias":"Worker","Address":"127.0.0.1:28002","Role":"worker","PublicKey":"worker-pub","AgentTokenEnv":"NODE_B"}),http.StatusCreated)
	do(authReq(http.MethodPost,"/api/deploy","admin",map[string]any{"node_ids":[]string{"ex1"},"version":"v0.4.0"}),http.StatusAccepted)
	do(authReq(http.MethodPost,"/api/finance","admin",map[string]any{"node_id":"ex1","cost_micros_per_gib":100,"revenue_micros_per_gib":300,"currency":"IRR","effective_from":"2026-09-28T00:00:00Z"}),http.StatusOK)
	do(authReq(http.MethodPost,"/api/nodes/rotate-token","admin",map[string]any{"node_id":"ex1","agent_token_env":"NODE_A_NEW","grace_seconds":60}),http.StatusOK)
	do(authReq(http.MethodPost,"/api/nodes/revoke","admin",map[string]any{"node_id":"ex1","reason":"incident"}),http.StatusOK)

	entries,err:=app.audit.List(100);if err!=nil{t.Fatal(err)}
	want:=map[string]bool{"node.upsert":false,"deploy.create":false,"finance.rate.change":false,"node.token.rotate":false,"node.revoke":false}
	for _,e:=range entries{if _,ok:=want[e.Action];ok&&e.Outcome=="success"{want[e.Action]=true}}
	for action,ok:=range want{if !ok{t.Fatalf("missing audit action %s entries=%+v",action,entries)}}
	if err:=app.audit.Verify();err!=nil{t.Fatal(err)}

	req:=authReq(http.MethodDelete,"/api/audit","admin",nil);rr:=httptest.NewRecorder();app.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusMethodNotAllowed{t.Fatalf("audit DELETE status=%d",rr.Code)}

	path:=store.path+".audit.jsonl"
	b,err:=os.ReadFile(path);if err!=nil{t.Fatal(err)}
	i:=strings.Index(string(b),"\"action\"")
	if i<0{t.Fatal("audit content missing")}
	b[i]='X'
	if err:=os.WriteFile(path,b,0600);err!=nil{t.Fatal(err)}
	if _,err:=OpenAuditLog(path);err==nil{t.Fatal("tampered audit log was accepted")}
	t.Logf("PASS audit actions=%d tamper detected",len(entries))
}

func TestRateLimitAndBruteForceTemporaryBlock(t *testing.T){
	store,_:=OpenStore(filepath.Join(t.TempDir(),"state.json"))
	app,_:=NewServer(store,"admin")
	app.ConfigureSecurity(SecurityConfig{RequestsPerWindow:3,Window:time.Minute,MaxAuthFailures:3,AuthFailureWindow:time.Minute,BlockDuration:5*time.Minute})
	base:=time.Date(2026,9,28,12,0,0,0,time.UTC);now:=base
	app.now=func()time.Time{return now}

	for i:=0;i<3;i++{
		req:=authReq(http.MethodGet,"/api/jobs","wrong",nil)
		rr:=httptest.NewRecorder();app.Handler().ServeHTTP(rr,req)
		if rr.Code!=http.StatusUnauthorized{t.Fatalf("bad auth #%d status=%d",i+1,rr.Code)}
	}
	req:=authReq(http.MethodGet,"/api/jobs","admin",nil)
	rr:=httptest.NewRecorder();app.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusTooManyRequests{t.Fatalf("bruteforce block status=%d",rr.Code)}

	now=base.Add(6*time.Minute)
	req=authReq(http.MethodGet,"/api/jobs","admin",nil)
	rr=httptest.NewRecorder();app.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusOK{t.Fatalf("block did not expire status=%d body=%s",rr.Code,rr.Body.String())}

	app.ConfigureSecurity(SecurityConfig{RequestsPerWindow:2,Window:time.Minute,MaxAuthFailures:10,BlockDuration:time.Minute})
	now=base
	for i:=0;i<2;i++{
		req=authReq(http.MethodGet,"/api/nodes","admin",nil);rr=httptest.NewRecorder();app.Handler().ServeHTTP(rr,req)
		if rr.Code!=http.StatusOK{t.Fatalf("rate baseline #%d status=%d",i+1,rr.Code)}
	}
	req=authReq(http.MethodGet,"/api/nodes","admin",nil);rr=httptest.NewRecorder();app.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusTooManyRequests{t.Fatalf("rate limit status=%d",rr.Code)}
	t.Log("PASS rate limiting and temporary brute-force block")
}

func TestAgentTokenRotationGraceAndImmediateRevoke(t *testing.T){
	store,_:=OpenStore(filepath.Join(t.TempDir(),"state.json"))
	const oldToken="old-token"
	_,err:=store.UpsertNode(Node{ID:"n1",Alias:"N1",Address:"127.0.0.1:29001",Role:"foreign"},oldToken);if err!=nil{t.Fatal(err)}
	now:=time.Now().UTC()
	_,err=store.RotateAgentToken("n1","new-token",now,2*time.Minute);if err!=nil{t.Fatal(err)}
	store.mu.Lock()
	_,oldOK:=store.authorizedHashLocked("n1",oldToken,now.Add(time.Minute))
	_,newOK:=store.authorizedHashLocked("n1","new-token",now.Add(time.Minute))
	_,expiredOld:=store.authorizedHashLocked("n1",oldToken,now.Add(3*time.Minute))
	store.mu.Unlock()
	if !oldOK||!newOK||expiredOld{t.Fatalf("rotation old=%v new=%v expired_old=%v",oldOK,newOK,expiredOld)}

	_,err=store.RevokeNode("n1","kill switch",now.Add(90*time.Second));if err!=nil{t.Fatal(err)}
	store.mu.Lock()
	_,oldAfter:=store.authorizedHashLocked("n1",oldToken,now.Add(90*time.Second))
	_,newAfter:=store.authorizedHashLocked("n1","new-token",now.Add(90*time.Second))
	store.mu.Unlock()
	if oldAfter||newAfter{t.Fatalf("revoke left token valid old=%v new=%v",oldAfter,newAfter)}

	rep:=telemetry.Report{NodeID:"n1",BootID:"b",Sequence:1,TimestampUnix:now.Unix()}
	body,_:=json.Marshal(rep)
	if _,_,err:=store.ApplyTelemetry("new-token",telemetry.Sign("new-token",body),body,rep);err==nil{t.Fatal("revoked node telemetry accepted")}
	t.Log("PASS rotation grace and immediate kill switch")
}

func TestValidateListenAddressWhitelist(t *testing.T){
	for _,addr:=range []string{"127.0.0.1:8080","[::1]:8080","localhost:8080"}{
		if err:=ValidateListenAddress(addr,nil);err!=nil{t.Fatalf("loopback %s: %v",addr,err)}
	}
	if err:=ValidateListenAddress("10.10.10.5:8080",nil);err==nil{t.Fatal("non-loopback listen accepted without whitelist")}
	if err:=ValidateListenAddress("0.0.0.0:8080",[]string{"10.10.10.5"});err==nil{t.Fatal("wildcard listen accepted by unrelated whitelist")}
	if err:=ValidateListenAddress("10.10.10.5:8080",[]string{"10.10.10.5"});err!=nil{t.Fatalf("whitelisted IP rejected: %v",err)}
	t.Log("PASS localhost default and explicit non-loopback whitelist")
}

// The node list is the cluster topology (addresses, roles, public keys,
// health); only the dashboard reads it, and it must present the admin token.
func TestNodeListRequiresAdminAndOmitsTokenHashes(t *testing.T){
	store,_:=OpenStore(filepath.Join(t.TempDir(),"state.json"))
	if _,err:=store.UpsertNode(Node{ID:"n1",Alias:"N1",Address:"127.0.0.1:22001",Role:"worker",PublicKey:"pk"},"agent-secret");err!=nil{t.Fatal(err)}
	app,_:=NewServer(store,"admin")

	for _,token:=range []string{"","wrong"}{
		rr:=httptest.NewRecorder();app.Handler().ServeHTTP(rr,authReq(http.MethodGet,"/api/nodes",token,nil))
		if rr.Code!=http.StatusUnauthorized{t.Fatalf("GET /api/nodes with token %q: status=%d body=%s",token,rr.Code,rr.Body.String())}
		if strings.Contains(rr.Body.String(),"127.0.0.1:22001"){t.Fatalf("topology leaked without admin auth: %s",rr.Body.String())}
	}
	rr:=httptest.NewRecorder();app.Handler().ServeHTTP(rr,authReq(http.MethodGet,"/api/nodes","admin",nil))
	if rr.Code!=http.StatusOK||!strings.Contains(rr.Body.String(),"127.0.0.1:22001"){t.Fatalf("admin GET /api/nodes: status=%d body=%s",rr.Code,rr.Body.String())}
	if strings.Contains(rr.Body.String(),"token_hash"){t.Fatalf("node list exposes agent token hashes: %s",rr.Body.String())}
}

// Every client IP used to stay in the guard's map forever.
func TestIPGuardEvictsClientsWithNoLiveState(t *testing.T){
	g:=newIPGuard(SecurityConfig{RequestsPerWindow:3,Window:time.Minute,MaxAuthFailures:2,AuthFailureWindow:time.Minute,BlockDuration:5*time.Minute})
	base:=time.Date(2026,10,1,12,0,0,0,time.UTC)
	for i:=0;i<1000;i++{g.Allow("10.0."+strconv.Itoa(i/250)+"."+strconv.Itoa(i%250),base)}
	g.AuthFailure("10.9.9.9",base);g.AuthFailure("10.9.9.9",base)

	g.Allow("10.1.1.1",base.Add(2*time.Minute))
	g.mu.Lock();n:=len(g.clients);_,blockedKept:=g.clients["10.9.9.9"];g.mu.Unlock()
	if n!=2||!blockedKept{t.Fatalf("after the rate window: %d clients (blocked kept=%v), want the active one and the blocked one",n,blockedKept)}
	if ok,_:=g.Allow("10.9.9.9",base.Add(3*time.Minute));ok{t.Fatal("eviction lifted a brute-force block early")}

	g.Allow("10.1.1.1",base.Add(10*time.Minute))
	g.mu.Lock();_,blockedKept=g.clients["10.9.9.9"];g.mu.Unlock()
	if blockedKept{t.Fatal("expired block was never evicted")}
}
