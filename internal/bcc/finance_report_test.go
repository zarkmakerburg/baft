package bcc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

func applyFinanceReport(t *testing.T,s *Store,nodeID,token,boot string,seq uint64,ts time.Time,in,out uint64){
	t.Helper()
	rep:=telemetry.Report{
		NodeID:nodeID,BootID:boot,Sequence:seq,
		IngressBytes:in,EgressBytes:out,TimestampUnix:ts.Unix(),
	}
	body,err:=json.Marshal(rep);if err!=nil{t.Fatal(err)}
	if _,_,err:=s.ApplyTelemetry(token,telemetry.Sign(token,body),body,rep);err!=nil{t.Fatal(err)}
}

func sumRows(rows []FinanceReportRow,scope,node,currency string) FinanceReportRow {
	var out FinanceReportRow
	out.Scope=scope;out.NodeID=node;out.Currency=currency
	for _,r:=range rows{
		if r.Scope!=scope||r.Currency!=currency{continue}
		if scope=="node"&&r.NodeID!=node{continue}
		out.IngressBytes+=r.IngressBytes
		out.EgressBytes+=r.EgressBytes
		out.CostMicros=satAdd(out.CostMicros,r.CostMicros)
		out.RevenueMicros=satAdd(out.RevenueMicros,r.RevenueMicros)
		out.ProfitMicros=satAdd(out.ProfitMicros,r.ProfitMicros)
	}
	return out
}

func TestFinanceRateChangeMidDayUsesEffectiveVersion(t *testing.T){
	s,err:=OpenStore(t.TempDir()+"/state.json");if err!=nil{t.Fatal(err)}
	const token="rate-agent"
	_,_ = s.UpsertNode(Node{ID:"n1",Alias:"N1",Address:"127.0.0.1:22001",Role:"foreign"},token)
	loc,err:=time.LoadLocation("Asia/Tehran");if err!=nil{t.Fatal(err)}
	day:=time.Date(2026,9,28,0,0,0,0,loc)
	noon:=time.Date(2026,9,28,12,0,0,0,loc)
	if err:=s.SetFinancePolicyAt("n1",100,300,"IRR",day);err!=nil{t.Fatal(err)}
	if err:=s.SetFinancePolicyAt("n1",200,500,"IRR",noon);err!=nil{t.Fatal(err)}

	const gib uint64=1<<30
	applyFinanceReport(t,s,"n1",token,"boot-a",1,time.Date(2026,9,28,10,0,0,0,loc),gib,0)
	applyFinanceReport(t,s,"n1",token,"boot-b",1,time.Date(2026,9,28,14,0,0,0,loc),gib,0)

	rows,err:=s.FinanceReport("daily","2026-09-28","2026-09-28","Asia/Tehran");if err!=nil{t.Fatal(err)}
	n:=sumRows(rows,"node","n1","IRR")
	if n.IngressBytes!=2*gib||n.CostMicros!=300||n.RevenueMicros!=800||n.ProfitMicros!=500{
		t.Fatalf("mid-day version report=%+v",n)
	}
	h:=s.RateHistory("n1")
	if len(h)!=2||h[0].Version==h[1].Version||!h[1].EffectiveFrom.Equal(noon.UTC()){t.Fatalf("rate history=%+v",h)}
	t.Logf("PASS mid-day versioned rates cost=%d revenue=%d profit=%d",n.CostMicros,n.RevenueMicros,n.ProfitMicros)
}

func TestFinanceDailyMonthlyAndClusterSumsMatch(t *testing.T){
	s,err:=OpenStore(t.TempDir()+"/state.json");if err!=nil{t.Fatal(err)}
	const gib uint64=1<<30
	for _,x:=range []struct{id,token string}{{"n1","a1"},{"n2","a2"}}{
		_,_ = s.UpsertNode(Node{ID:x.id,Alias:x.id,Address:"127.0.0.1:23001",Role:"foreign"},x.token)
		if err:=s.SetFinancePolicyAt(x.id,10,30,"IRR",time.Unix(0,0));err!=nil{t.Fatal(err)}
	}
	loc,_:=time.LoadLocation("Asia/Tehran")
	applyFinanceReport(t,s,"n1","a1","n1-a",1,time.Date(2026,9,27,12,0,0,0,loc),gib,0)
	applyFinanceReport(t,s,"n1","a1","n1-b",1,time.Date(2026,9,28,12,0,0,0,loc),0,gib)
	applyFinanceReport(t,s,"n2","a2","n2-a",1,time.Date(2026,9,27,13,0,0,0,loc),gib,gib)
	applyFinanceReport(t,s,"n2","a2","n2-b",1,time.Date(2026,9,29,13,0,0,0,loc),gib,0)

	daily,err:=s.FinanceReport("daily","2026-09-27","2026-09-30","Asia/Tehran");if err!=nil{t.Fatal(err)}
	monthly,err:=s.FinanceReport("monthly","2026-09-27","2026-09-30","Asia/Tehran");if err!=nil{t.Fatal(err)}
	for _,node:=range []string{"n1","n2"}{
		d:=sumRows(daily,"node",node,"IRR")
		m:=sumRows(monthly,"node",node,"IRR")
		if d.IngressBytes!=m.IngressBytes||d.EgressBytes!=m.EgressBytes||d.CostMicros!=m.CostMicros||d.RevenueMicros!=m.RevenueMicros||d.ProfitMicros!=m.ProfitMicros{
			t.Fatalf("daily/monthly mismatch node=%s daily=%+v monthly=%+v",node,d,m)
		}
	}
	cluster:=sumRows(monthly,"cluster","","IRR")
	n1:=sumRows(monthly,"node","n1","IRR")
	n2:=sumRows(monthly,"node","n2","IRR")
	if cluster.IngressBytes!=n1.IngressBytes+n2.IngressBytes||
		cluster.EgressBytes!=n1.EgressBytes+n2.EgressBytes||
		cluster.CostMicros!=n1.CostMicros+n2.CostMicros||
		cluster.RevenueMicros!=n1.RevenueMicros+n2.RevenueMicros||
		cluster.ProfitMicros!=n1.ProfitMicros+n2.ProfitMicros{
		t.Fatalf("cluster mismatch cluster=%+v n1=%+v n2=%+v",cluster,n1,n2)
	}
	t.Logf("PASS daily=monthly and node sums=cluster traffic=%d/%d",cluster.IngressBytes,cluster.EgressBytes)
}

func TestFinanceTehranDayBoundary(t *testing.T){
	s,err:=OpenStore(t.TempDir()+"/state.json");if err!=nil{t.Fatal(err)}
	const token="tz-agent"
	_,_ = s.UpsertNode(Node{ID:"tz",Alias:"TZ",Address:"127.0.0.1:24001",Role:"foreign"},token)
	_ = s.SetFinancePolicyAt("tz",1,2,"IRR",time.Unix(0,0))
	const gib uint64=1<<30
	// Tehran is UTC+03:30 in 2026: these are 23:59 and 00:01 local.
	applyFinanceReport(t,s,"tz",token,"tz-a",1,time.Date(2026,9,27,20,29,0,0,time.UTC),gib,0)
	applyFinanceReport(t,s,"tz",token,"tz-b",1,time.Date(2026,9,27,20,31,0,0,time.UTC),gib,0)
	rows,err:=s.FinanceReport("daily","2026-09-27","2026-09-28","Asia/Tehran");if err!=nil{t.Fatal(err)}
	seen:=map[string]uint64{}
	for _,r:=range rows{if r.Scope=="node"&&r.NodeID=="tz"{seen[r.Period]+=r.IngressBytes}}
	if seen["2026-09-27"]!=gib||seen["2026-09-28"]!=gib{t.Fatalf("Tehran boundary=%v",seen)}
	t.Logf("PASS Tehran boundary split=%v",seen)
}

func TestFinanceDuplicateAndOutOfOrderDoNotChangeReports(t *testing.T){
	s,err:=OpenStore(t.TempDir()+"/state.json");if err!=nil{t.Fatal(err)}
	const token="dup-agent"
	_,_ = s.UpsertNode(Node{ID:"dup",Alias:"Dup",Address:"127.0.0.1:25001",Role:"foreign"},token)
	_ = s.SetFinancePolicyAt("dup",10,20,"IRR",time.Unix(0,0))
	ts:=time.Date(2026,9,28,12,0,0,0,time.UTC)
	rep1:=telemetry.Report{NodeID:"dup",BootID:"same",Sequence:1,IngressBytes:1000,EgressBytes:2000,TimestampUnix:ts.Unix()}
	body1,_:=json.Marshal(rep1)
	if _,_,err:=s.ApplyTelemetry(token,telemetry.Sign(token,body1),body1,rep1);err!=nil{t.Fatal(err)}
	rep2:=rep1;rep2.Sequence=2;rep2.IngressBytes=3000;rep2.EgressBytes=4000;rep2.TimestampUnix=ts.Add(time.Minute).Unix()
	body2,_:=json.Marshal(rep2)
	if _,_,err:=s.ApplyTelemetry(token,telemetry.Sign(token,body2),body2,rep2);err!=nil{t.Fatal(err)}
	before,err:=s.FinanceReport("daily","2026-09-28","2026-09-28","Asia/Tehran");if err!=nil{t.Fatal(err)}

	if _,dup,err:=s.ApplyTelemetry(token,telemetry.Sign(token,body2),body2,rep2);err!=nil||!dup{t.Fatalf("duplicate result dup=%v err=%v",dup,err)}
	if _,dup,err:=s.ApplyTelemetry(token,telemetry.Sign(token,body1),body1,rep1);err!=nil||!dup{t.Fatalf("out-of-order result dup=%v err=%v",dup,err)}
	after,err:=s.FinanceReport("daily","2026-09-28","2026-09-28","Asia/Tehran");if err!=nil{t.Fatal(err)}
	if !reflect.DeepEqual(before,after){t.Fatalf("report changed after duplicate/out-of-order\nbefore=%+v\nafter=%+v",before,after)}

	csvBytes,err:=FinanceReportCSV(after);if err!=nil{t.Fatal(err)}
	csvText:=string(csvBytes)
	if !strings.Contains(csvText,"currency")||!strings.Contains(csvText,",IRR")||strings.Contains(csvText,"1,000"){
		t.Fatalf("CSV format unexpected: %s",csvText)
	}
	t.Log("PASS duplicate/out-of-order left finance report unchanged")
}


func TestFinanceReportAPIAndCSV(t *testing.T){
	s,err:=OpenStore(t.TempDir()+"/state.json");if err!=nil{t.Fatal(err)}
	const token="api-agent"
	_,_ = s.UpsertNode(Node{ID:"api-node",Alias:"API",Address:"127.0.0.1:26001",Role:"foreign"},token)
	_ = s.SetFinancePolicyAt("api-node",100,250,"IRR",time.Unix(0,0))
	applyFinanceReport(t,s,"api-node",token,"api-boot",1,time.Date(2026,9,28,12,0,0,0,time.UTC),1<<30,0)
	app,_:=NewServer(s,"admin")

	req:=httptest.NewRequest(http.MethodGet,"/api/finance/report?period=daily&from=2026-09-28&to=2026-09-28&tz=Asia%2FTehran",nil)
	req.Header.Set("Authorization","Bearer admin")
	rr:=httptest.NewRecorder();app.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusOK{t.Fatalf("JSON report status=%d body=%s",rr.Code,rr.Body.String())}
	if !strings.Contains(rr.Body.String(),"\"scope\":\"cluster\"")||!strings.Contains(rr.Body.String(),"\"currency\":\"IRR\""){t.Fatalf("JSON report=%s",rr.Body.String())}

	req=httptest.NewRequest(http.MethodGet,"/api/finance/report?period=monthly&from=2026-09-01&to=2026-09-30&tz=Asia%2FTehran&format=csv",nil)
	req.Header.Set("Authorization","Bearer admin")
	rr=httptest.NewRecorder();app.Handler().ServeHTTP(rr,req)
	if rr.Code!=http.StatusOK{t.Fatalf("CSV report status=%d body=%s",rr.Code,rr.Body.String())}
	if ct:=rr.Header().Get("Content-Type");!strings.HasPrefix(ct,"text/csv"){t.Fatalf("content-type=%s",ct)}
	if !strings.Contains(rr.Body.String(),"period,scope,node_id,ingress_bytes,egress_bytes,cost_micros,revenue_micros,profit_micros,currency"){
		t.Fatalf("CSV header=%s",rr.Body.String())
	}
	t.Log("PASS finance JSON+CSV API")
}
