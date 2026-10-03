package bcc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

func TestForcedStaleAlertFiresOnceAndResolvesOnce(t *testing.T){
	var mu sync.Mutex
	var got []Alert
	webhook:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		defer r.Body.Close()
		var a Alert
		if err:=json.NewDecoder(r.Body).Decode(&a);err!=nil{t.Error(err);http.Error(w,"bad json",400);return}
		mu.Lock();got=append(got,a);mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()

	store,err:=OpenStore(filepath.Join(t.TempDir(),"state.json"));if err!=nil{t.Fatal(err)}
	const token="stale-agent"
	_,err=store.UpsertNode(Node{ID:"n-stale",Alias:"نود تهران",Address:"127.0.0.1:25011",Role:"foreign"},token)
	if err!=nil{t.Fatal(err)}
	if err:=store.SetHealth("n-stale","up",3,time.Now());err!=nil{t.Fatal(err)}

	app,err:=NewServer(store,"admin");if err!=nil{t.Fatal(err)}
	if err:=app.ConfigureAlerts(AlertConfig{
		WebhookURL:webhook.URL,TelemetryStaleAfter:3*time.Minute,
		HandshakeErrorRateMilliPerMin:5000,Interval:time.Second,
	});err!=nil{t.Fatal(err)}

	base:=time.Date(2026,9,28,9,0,0,0,time.UTC)
	app.now=func() time.Time{return base}
	rep:=telemetry.Report{
		NodeID:"n-stale",BootID:"boot-stale",Sequence:1,
		IngressBytes:100,EgressBytes:200,TimestampUnix:base.Unix(),
	}
	body,_:=json.Marshal(rep)
	if _,_,err:=store.ApplyTelemetry(token,telemetry.Sign(token,body),body,rep);err!=nil{t.Fatal(err)}

	// Fresh telemetry: samples are OK, no alert.
	tick:=func(at time.Time){
		app.now=func() time.Time{return at}
		app.evaluateHealthAt(at)
		if err:=app.evaluateAlertsAt(context.Background(),at);err!=nil{t.Fatal(err)}
	}
	for i:=0;i<3;i++{tick(base.Add(time.Duration(i)*10*time.Second))}
	mu.Lock();if len(got)!=0{t.Fatalf("unexpected alert before threshold: %+v",got)};mu.Unlock()

	// The node goes silent. The first stale sample is only an observation; the
	// alert opens once the layer is confirmed DEGRADED (two stale samples), and
	// only once however long the silence lasts.
	fakeNow:=base.Add(3*time.Minute)
	tick(fakeNow)
	mu.Lock();if len(got)!=0{t.Fatalf("one stale sample alerted: %+v",got)};mu.Unlock()
	tick(fakeNow.Add(10*time.Second))
	tick(fakeNow.Add(20*time.Second))
	fakeNow=fakeNow.Add(20*time.Second)
	mu.Lock()
	if len(got)!=1{t.Fatalf("stale alert count=%d want=1 alerts=%+v",len(got),got)}
	if got[0].Type!="telemetry_stale"||got[0].Status!="firing"{t.Fatalf("unexpected firing alert=%+v",got[0])}
	if !strings.Contains(got[0].Message,"نود تهران")||!strings.Contains(got[0].Message,"زمان UTC")||!strings.Contains(got[0].Message,"زمان تهران"){
		t.Fatalf("Persian alert missing required fields: %q",got[0].Message)
	}
	mu.Unlock()

	fresh:=rep
	fresh.Sequence=2
	fresh.TimestampUnix=fakeNow.Add(5*time.Second).Unix()
	fresh.IngressBytes=150
	fresh.EgressBytes=250
	freshBody,_:=json.Marshal(fresh)
	if _,_,err:=store.ApplyTelemetry(token,telemetry.Sign(token,freshBody),freshBody,fresh);err!=nil{t.Fatal(err)}

	// Telemetry is back: the layer must prove itself with consecutive OK samples
	// (DEGRADED -> UP needs three) before the alert resolves, and then once.
	tick(fakeNow.Add(10*time.Second))
	mu.Lock()
	if len(got)!=1{t.Fatalf("resolved after one OK sample: %+v",got)}
	mu.Unlock()
	tick(fakeNow.Add(20*time.Second))
	tick(fakeNow.Add(30*time.Second))
	tick(fakeNow.Add(40*time.Second))
	mu.Lock()
	if len(got)!=2{t.Fatalf("resolved alert count=%d want=2 alerts=%+v",len(got),got)}
	if got[1].Status!="resolved"||!strings.Contains(got[1].Message,"برطرف شد"){
		t.Fatalf("missing resolved Persian alert: %+v",got[1])
	}
	t.Logf("PASS stale firing/resolution exactly once: firing=%s resolved=%s",got[0].Status,got[1].Status)
	mu.Unlock()
}

func TestWebhookSecretNeverAppearsInAPIResponses(t *testing.T){
	store,err:=OpenStore(filepath.Join(t.TempDir(),"state.json"));if err!=nil{t.Fatal(err)}
	_,_ = store.UpsertNode(Node{ID:"n-mask",Alias:"Mask",Address:"127.0.0.1:25012",Role:"foreign"},"agent-secret-value")
	app,_:=NewServer(store,"admin-secret-value")
	const secretURL="https://example.invalid/hook/very-secret-token"
	if err:=app.ConfigureAlerts(AlertConfig{WebhookURL:secretURL});err!=nil{t.Fatal(err)}

	for _,path:=range []string{"/api/nodes","/api/monitoring","/api/jobs","/api/finance"}{
		req:=httptest.NewRequest(http.MethodGet,path,nil)
		if path!="/api/nodes"{req.Header.Set("Authorization","Bearer admin-secret-value")}
		rr:=httptest.NewRecorder()
		app.Handler().ServeHTTP(rr,req)
		payload:=rr.Body.String()
		for _,secret:=range []string{"very-secret-token","agent-secret-value","admin-secret-value",secretURL}{
			if strings.Contains(payload,secret){t.Fatalf("secret leaked on %s: %s",path,payload)}
		}
	}
	// Also verify unauthorized responses never echo the supplied bearer value.
	req:=httptest.NewRequest(http.MethodGet,"/api/jobs",bytes.NewBuffer(nil))
	req.Header.Set("Authorization","Bearer attacker-visible-secret")
	rr:=httptest.NewRecorder();app.Handler().ServeHTTP(rr,req)
	if strings.Contains(rr.Body.String(),"attacker-visible-secret"){t.Fatal("bearer token echoed in error response")}
}
