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
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

func authReq(method,url,token string,body any) *http.Request {
	var b bytes.Buffer
	if body!=nil{_ = json.NewEncoder(&b).Encode(body)}
	r:=httptest.NewRequest(method,url,&b)
	if token!=""{r.Header.Set("Authorization","Bearer "+token)}
	r.Header.Set("Content-Type","application/json")
	return r
}

func signedTelemetryReq(t *testing.T,token string,rep telemetry.Report) *http.Request {
	t.Helper()
	body,err:=json.Marshal(rep);if err!=nil{t.Fatal(err)}
	r:=httptest.NewRequest(http.MethodPost,"/api/agent/traffic",bytes.NewReader(body))
	r.Header.Set("Authorization","Bearer "+token)
	r.Header.Set("Content-Type","application/json")
	r.Header.Set("X-BAFT-Signature",telemetry.Sign(token,body))
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


func TestFinancialTrafficSync(t *testing.T){
	store,err:=OpenStore(filepath.Join(t.TempDir(),"state.json"));if err!=nil{t.Fatal(err)}
	_,err=store.UpsertNode(Node{ID:"ex-fin",Alias:"EX Finance",Address:"127.0.0.1:25000",Role:"foreign"},"agent-fin")
	if err!=nil{t.Fatal(err)}
	app,_:=NewServer(store,"admin")

	rr:=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/finance","admin",map[string]any{
		"node_id":"ex-fin","cost_micros_per_gib":int64(2_000_000),"revenue_micros_per_gib":int64(5_000_000),
	}))
	if rr.Code!=http.StatusOK{t.Fatalf("finance policy status=%d body=%s",rr.Code,rr.Body.String())}

	rep:=telemetry.Report{NodeID:"ex-fin",BootID:"boot-a",Sequence:1,IngressBytes:1<<29,EgressBytes:1<<29,ActiveSessions:2,HandshakeErrors:3,TimestampUnix:1700000000}
	rr=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,signedTelemetryReq(t,"agent-fin",rep))
	if rr.Code!=http.StatusAccepted{t.Fatalf("traffic status=%d body=%s",rr.Code,rr.Body.String())}

	all:=store.FinanceSnapshot()
	if len(all)!=1||all[0].CostMicros!=2_000_000||all[0].RevenueMicros!=5_000_000||all[0].ProfitMicros!=3_000_000{t.Fatalf("finance=%+v",all)}
	cur,ok:=store.TelemetrySnapshot("ex-fin")
	if !ok||cur.Sequence!=1||cur.ActiveSessions!=2||cur.HandshakeErrors!=3{t.Fatalf("cursor=%+v ok=%v",cur,ok)}

	// Exact duplicate and out-of-order reports are acknowledged but never
	// counted twice.
	rr=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,signedTelemetryReq(t,"agent-fin",rep))
	if rr.Code!=http.StatusAccepted{t.Fatalf("duplicate status=%d",rr.Code)}
	older:=rep;older.Sequence=0
	body,_:=json.Marshal(older)
	bad:=httptest.NewRequest(http.MethodPost,"/api/agent/traffic",bytes.NewReader(body))
	bad.Header.Set("Authorization","Bearer agent-fin")
	bad.Header.Set("X-BAFT-Signature",telemetry.Sign("agent-fin",body))
	rr=httptest.NewRecorder();app.Handler().ServeHTTP(rr,bad)
	if rr.Code==http.StatusAccepted{t.Fatal("invalid sequence zero accepted")}

	all=store.FinanceSnapshot()
	if all[0].IngressBytes!=1<<29||all[0].EgressBytes!=1<<29{t.Fatalf("duplicate changed finance: %+v",all[0])}

	// Higher sequence uses cumulative counters; only the delta is applied.
	rep.Sequence=3;rep.IngressBytes+=(1<<20);rep.EgressBytes+=(2<<20)
	rr=httptest.NewRecorder();app.Handler().ServeHTTP(rr,signedTelemetryReq(t,"agent-fin",rep))
	if rr.Code!=http.StatusAccepted{t.Fatalf("delta status=%d body=%s",rr.Code,rr.Body.String())}
	all=store.FinanceSnapshot()
	if all[0].IngressBytes!=(1<<29)+(1<<20)||all[0].EgressBytes!=(1<<29)+(2<<20){t.Fatalf("delta mismatch: %+v",all[0])}

	rr=httptest.NewRecorder()
	app.Handler().ServeHTTP(rr,signedTelemetryReq(t,"wrong-token",rep))
	if rr.Code!=http.StatusUnauthorized{t.Fatalf("unauthorized traffic accepted status=%d",rr.Code)}
	t.Logf("PASS signed idempotent finance sync ingress=%d egress=%d",all[0].IngressBytes,all[0].EgressBytes)
}


func TestMonitoringAlertsAndSevenDayHistory(t *testing.T){
	alerts:=make(chan Alert,8)
	webhook:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		defer r.Body.Close()
		var a Alert
		if err:=json.NewDecoder(r.Body).Decode(&a);err!=nil{t.Error(err);http.Error(w,"bad json",400);return}
		alerts<-a
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()

	store,err:=OpenStore(filepath.Join(t.TempDir(),"state.json"));if err!=nil{t.Fatal(err)}
	const token="monitor-agent"
	_,err=store.UpsertNode(Node{ID:"n-monitor",Alias:"Monitor Node",Address:"127.0.0.1:25001",Role:"foreign"},token)
	if err!=nil{t.Fatal(err)}
	if err:=store.SetHealth("n-monitor","up",7,time.Now());err!=nil{t.Fatal(err)}
	app,_:=NewServer(store,"admin")
	if err:=app.ConfigureAlerts(AlertConfig{
		WebhookURL:webhook.URL,TelemetryStaleAfter:3*time.Minute,
		HandshakeErrorRateMilliPerMin:5000,Interval:time.Second,
	});err!=nil{t.Fatal(err)}

	now:=time.Now().UTC().Truncate(time.Second)
	first:=telemetry.Report{
		NodeID:"n-monitor",BootID:"boot-monitor",Sequence:1,
		IngressBytes:100,EgressBytes:200,ActiveSessions:1,HandshakeErrors:0,
		Routes:[]telemetry.RouteSnapshot{{RouteID:"route-a",Status:"up",LatencyMS:5,ProbeKind:"tcp"}},
		TimestampUnix:now.Add(-time.Minute).Unix(),
	}
	rr:=httptest.NewRecorder();app.Handler().ServeHTTP(rr,signedTelemetryReq(t,token,first))
	if rr.Code!=http.StatusAccepted{t.Fatalf("first telemetry status=%d body=%s",rr.Code,rr.Body.String())}

	second:=first
	second.Sequence=2
	second.IngressBytes=300
	second.EgressBytes=500
	second.HandshakeErrors=10
	second.Routes=[]telemetry.RouteSnapshot{{RouteID:"route-a",Status:"down",LatencyMS:-1,ErrorCount:1,ProbeKind:"tcp"}}
	second.TimestampUnix=now.Unix()
	rr=httptest.NewRecorder();app.Handler().ServeHTTP(rr,signedTelemetryReq(t,token,second))
	if rr.Code!=http.StatusAccepted{t.Fatalf("second telemetry status=%d body=%s",rr.Code,rr.Body.String())}

	if err:=app.EvaluateAlertsOnce(context.Background());err!=nil{t.Fatal(err)}
	gotTypes:=map[string]bool{}
	deadline:=time.After(2*time.Second)
	for len(gotTypes)<2{
		select{
		case a:=<-alerts:
			gotTypes[a.Type]=true
		case <-deadline:
			t.Fatalf("alerts=%v",gotTypes)
		}
	}
	if !gotTypes["handshake_error_rate"]||!gotTypes["route_down"]{t.Fatalf("unexpected alerts=%v",gotTypes)}

	_,err=store.UpsertNode(Node{ID:"n-stale",Alias:"Stale Node",Address:"127.0.0.1:25002",Role:"foreign"},"stale-agent")
	if err!=nil{t.Fatal(err)}
	if err:=store.SetHealth("n-stale","up",4,time.Now());err!=nil{t.Fatal(err)}
	staleRep:=telemetry.Report{
		NodeID:"n-stale",BootID:"stale-boot",Sequence:1,
		IngressBytes:10,EgressBytes:20,ActiveSessions:0,HandshakeErrors:0,
		Routes:[]telemetry.RouteSnapshot{{RouteID:"route-stale",Status:"up",LatencyMS:4,ProbeKind:"tcp"}},
		TimestampUnix:now.Add(-4*time.Minute).Unix(),
	}
	rr=httptest.NewRecorder();app.Handler().ServeHTTP(rr,signedTelemetryReq(t,"stale-agent",staleRep))
	if rr.Code!=http.StatusAccepted{t.Fatalf("stale telemetry status=%d body=%s",rr.Code,rr.Body.String())}
	if err:=app.EvaluateAlertsOnce(context.Background());err!=nil{t.Fatal(err)}
	select{
	case a:=<-alerts:
		if a.Type!="telemetry_stale"||a.NodeID!="n-stale"{t.Fatalf("unexpected stale alert=%+v",a)}
	case <-time.After(2*time.Second):
		t.Fatal("telemetry_stale webhook alert not emitted")
	}

	view:=store.MonitoringSnapshot(time.Now(),3*time.Minute)
	if len(view)!=1||view[0].Status!="up"||view[0].LatencyMS!=7||view[0].HandshakeErrorRateMilliMin<5000{
		t.Fatalf("monitoring view=%+v",view)
	}
	if len(view[0].Routes)!=1||view[0].Routes[0].Status!="down"{t.Fatalf("route view=%+v",view[0].Routes)}

	// A sample older than seven days must not survive retention.
	old:=second
	old.BootID="old-boot"
	old.Sequence=1
	old.TimestampUnix=now.Add(-8*24*time.Hour).Unix()
	old.IngressBytes=1
	old.EgressBytes=1
	rr=httptest.NewRecorder();app.Handler().ServeHTTP(rr,signedTelemetryReq(t,token,old))
	if rr.Code!=http.StatusAccepted{t.Fatalf("old telemetry status=%d body=%s",rr.Code,rr.Body.String())}

	fresh:=second
	fresh.BootID="fresh-boot"
	fresh.Sequence=1
	fresh.TimestampUnix=now.Unix()
	fresh.IngressBytes=10
	fresh.EgressBytes=20
	rr=httptest.NewRecorder();app.Handler().ServeHTTP(rr,signedTelemetryReq(t,token,fresh))
	if rr.Code!=http.StatusAccepted{t.Fatalf("fresh telemetry status=%d body=%s",rr.Code,rr.Body.String())}
	h:=store.History("n-monitor",now)
	for _,p:=range h{
		if p.Timestamp.Before(now.Add(-7*24*time.Hour)){t.Fatalf("expired history retained: %+v",p)}
	}
	if len(h)==0{t.Fatal("expected retained history")}
	t.Logf("PASS monitoring webhook alerts=%v history_points=%d",gotTypes,len(h))
}
