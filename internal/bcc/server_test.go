package bcc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func authReq(method,url,token string,body any) *http.Request {
	var b bytes.Buffer
	if body!=nil{_ = json.NewEncoder(&b).Encode(body)}
	r:=httptest.NewRequest(method,url,&b)
	if token!=""{r.Header.Set("Authorization","Bearer "+token)}
	r.Header.Set("Content-Type","application/json")
	return r
}

func TestDynamicRegistryAndEnrollmentJobs(t *testing.T){
	path:=filepath.Join(t.TempDir(),"state.json")
	store,err:=OpenStore(path);if err!=nil{t.Fatal(err)}
	for i:=0;i<50;i++{
		id:=fmt.Sprintf("ex-%02d",i+1)
		_,err:=store.UpsertNode(Node{ID:id,Alias:"Foreign "+id,Address:fmt.Sprintf("127.0.0.1:%d",20000+i),Role:"foreign"},"agent-"+id)
		if err!=nil{t.Fatal(err)}
	}
	_,err=store.UpsertNode(Node{ID:"worker-1",Alias:"IR Worker",Address:"127.0.0.1:29999",Role:"worker",PublicKey:"worker-public"},"worker-agent")
	if err!=nil{t.Fatal(err)}
	app,err:=NewServer(store,"admin-secret");if err!=nil{t.Fatal(err)}

	rr:=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,httptest.NewRequest(http.MethodGet,"/api/nodes",nil))
	if rr.Code!=200{t.Fatalf("nodes status=%d",rr.Code)}
	var nodes []Node
	if err:=json.Unmarshal(rr.Body.Bytes(),&nodes);err!=nil{t.Fatal(err)}
	if len(nodes)!=51{t.Fatalf("nodes=%d",len(nodes))}

	rr=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/enroll","admin-secret",map[string]string{"worker_id":"worker-1","public_key":"worker-public"}))
	if rr.Code!=http.StatusAccepted{t.Fatalf("enroll status=%d body=%s",rr.Code,rr.Body.String())}
	var jobs []Job
	if err:=json.Unmarshal(rr.Body.Bytes(),&jobs);err!=nil{t.Fatal(err)}
	if len(jobs)!=50{t.Fatalf("enrollment jobs=%d",len(jobs))}

	pulled,err:=store.PullJobs("ex-01","agent-ex-01")
	if err!=nil{t.Fatal(err)}
	if len(pulled)!=1||pulled[0].Type!=JobEnrollPeer{t.Fatalf("pulled=%+v",pulled)}
	if err:=store.AckJob("ex-01","agent-ex-01",pulled[0].ID,"succeeded","peer staged");err!=nil{t.Fatal(err)}

	reopened,err:=OpenStore(path);if err!=nil{t.Fatal(err)}
	if len(reopened.ListNodes())!=51{t.Fatal("persistent registry lost nodes")}
	t.Logf("PASS dynamic dashboard registry nodes=%d enrollment_jobs=%d",len(nodes),len(jobs))
}

func TestOneClickDeployCreatesOnlyTypedJobs(t *testing.T){
	store,err:=OpenStore(filepath.Join(t.TempDir(),"state.json"));if err!=nil{t.Fatal(err)}
	for i:=1;i<=3;i++{_,_ = store.UpsertNode(Node{ID:fmt.Sprintf("n%d",i),Alias:fmt.Sprintf("N%d",i),Address:fmt.Sprintf("127.0.0.1:%d",21000+i),Role:"foreign"},fmt.Sprintf("a%d",i))}
	app,_:=NewServer(store,"admin")
	rr:=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/deploy","admin",map[string]any{"node_ids":[]string{"n1","n3"},"version":"v0.3.0"}))
	if rr.Code!=http.StatusAccepted{t.Fatalf("deploy status=%d body=%s",rr.Code,rr.Body.String())}
	var jobs []Job;_ = json.Unmarshal(rr.Body.Bytes(),&jobs)
	if len(jobs)!=2||jobs[0].Type!=JobDeployBAFT||jobs[1].Type!=JobDeployBAFT{t.Fatalf("jobs=%+v",jobs)}

	rr=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/deploy","admin",map[string]any{"node_ids":[]string{"n1"},"version":"../../shell"}))
	if rr.Code!=http.StatusBadRequest{t.Fatalf("unsafe version accepted status=%d",rr.Code)}
}

func TestHealthProbeAndDashboard(t *testing.T){
	ln,err:=net.Listen("tcp","127.0.0.1:0");if err!=nil{t.Fatal(err)};defer ln.Close()
	go func(){for{c,e:=ln.Accept();if e!=nil{return};_ = c.Close()}}()
	store,_:=OpenStore(filepath.Join(t.TempDir(),"state.json"))
	_,_ = store.UpsertNode(Node{ID:"live",Alias:"Live Node",Address:ln.Addr().String(),Role:"foreign"},"agent")
	app,_:=NewServer(store,"admin")
	app.ProbeOnce(context.Background())
	nodes:=store.ListNodes()
	if len(nodes)!=1||nodes[0].Health!="up"{t.Fatalf("health=%+v",nodes)}

	rr:=httptest.NewRecorder();app.Handler().ServeHTTP(rr,httptest.NewRequest(http.MethodGet,"/",nil))
	if rr.Code!=200||!strings.Contains(rr.Body.String(),"BAFT Command Center"){t.Fatal("dashboard missing")}
}
