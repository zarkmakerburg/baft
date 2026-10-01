package integration_test

import (
	"bytes"
	"io"
	"net"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/bcc"
	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/telemetry"
)

func TestRecoveryTelemetryFinanceRemainExact(t *testing.T){
	dir:=t.TempDir()
	store,err:=bcc.OpenStore(filepath.Join(dir,"bcc.json"));if err!=nil{t.Fatal(err)}
	// startRuntimePair names the dialer ir-recovery; that is the billed node.
	const nodeID="ir-recovery"
	const token="recovery-finance-token"
	if _,err:=store.UpsertNode(bcc.Node{ID:nodeID,Alias:"IR Recovery Finance",Address:"127.0.0.1:29970",Role:"worker"},token);err!=nil{t.Fatal(err)}
	const costRate int64=1<<28
	const revenueRate int64=1<<29
	if err:=store.SetFinancePolicy(nodeID,costRate,revenueRate);err!=nil{t.Fatal(err)}
	app,err:=bcc.NewServer(store,"admin");if err!=nil{t.Fatal(err)}
	bccHTTP:=httptest.NewServer(app.Handler());defer bccHTTP.Close()
	t.Setenv("BAFT_AGENT_TOKEN",token)

	// The pair owns every listener it hands to the runtimes, so a port cannot
	// be taken between reservation and bind, and a failed start is reported.
	spoolPath:=filepath.Join(dir,"telemetry.spool")
	p:=startRuntimePair(t,1,true,func(_,ir *config.Config){
		ir.Telemetry=config.Telemetry{
			Enabled:true,BCCURL:bccHTTP.URL,AgentTokenEnv:"BAFT_AGENT_TOKEN",
			IntervalSeconds:1,RouteProbeIntervalSeconds:1,SpoolPath:spoolPath,SpoolMaxPending:64,
		}
	})
	defer p.close(t)

	c,err:=net.DialTimeout("tcp",p.ir.Routes[0].Listen,time.Second);if err!=nil{t.Fatal(err)}
	defer c.Close();_ = c.SetDeadline(time.Now().Add(20*time.Second))

	sendEcho:=func(payload []byte){
		t.Helper()
		if _,err:=c.Write(payload);err!=nil{t.Fatal(err)}
		got:=make([]byte,len(payload))
		if _,err:=io.ReadFull(c,got);err!=nil{t.Fatal(err)}
		if !bytes.Equal(got,payload){t.Fatal("echo payload mismatch")}
	}
	first:=bytes.Repeat([]byte{0x31},384*1024+17)
	second:=bytes.Repeat([]byte{0xa7},512*1024+29)
	sendEcho(first)

	var telemetryBoot string
	deadline:=time.Now().Add(5*time.Second)
	for{
		sp,err:=telemetry.OpenSpool(spoolPath,nodeID,64)
		if err==nil&&sp.BootID()!=""{telemetryBoot=sp.BootID();break}
		if time.Now().After(deadline){t.Fatalf("telemetry spool boot unavailable: %v",err)}
		time.Sleep(50*time.Millisecond)
	}

	// Let BCC book the first payload on its own, so the two payloads always
	// arrive in separate telemetry reports. Both byte counts are odd, which is
	// the split that used to lose one micro of cost to per-report rounding.
	deadline=time.Now().Add(6*time.Second)
	for{
		fs:=store.FinanceSnapshot()
		if len(fs)==1&&fs[0].IngressBytes==uint64(len(first))&&fs[0].EgressBytes==uint64(len(first)){break}
		if time.Now().After(deadline){t.Fatalf("first payload was not booked on its own: %+v",fs)}
		time.Sleep(50*time.Millisecond)
	}

	// Count target sockets immediately before the cut rather than relative to
	// the fixture baseline: the first payload was booked at least one telemetry
	// tick after startup, so every earlier target socket has been accepted.
	targetBeforeCut:=p.targetAccepts.Load()
	p.proxy.CutAll()
	time.Sleep(350*time.Millisecond)
	sendEcho(second)

	wantBytes:=uint64(len(first)+len(second))
	deadline=time.Now().Add(8*time.Second)
	for{
		fs:=store.FinanceSnapshot()
		cur,ok:=store.TelemetrySnapshot(nodeID)
		sp,spErr:=telemetry.OpenSpool(spoolPath,nodeID,64)
		if len(fs)==1&&ok&&spErr==nil&&fs[0].IngressBytes==wantBytes&&fs[0].EgressBytes==wantBytes{
			if cur.BootID!=telemetryBoot||sp.BootID()!=telemetryBoot{
				t.Fatalf("telemetry BootID changed during same-process ECRL recovery cursor=%s spool=%s want=%s",cur.BootID,sp.BootID(),telemetryBoot)
			}
			total:=wantBytes*2
			wantCost:=int64((total*uint64(costRate))/(1<<30))
			wantRevenue:=int64((total*uint64(revenueRate))/(1<<30))
			if fs[0].CostMicros!=wantCost||fs[0].RevenueMicros!=wantRevenue||fs[0].ProfitMicros!=wantRevenue-wantCost{
				t.Fatalf("finance mismatch got=%+v want cost=%d revenue=%d",fs[0],wantCost,wantRevenue)
			}
			time.Sleep(1200*time.Millisecond)
			again:=store.FinanceSnapshot()[0]
			if again.IngressBytes!=wantBytes||again.EgressBytes!=wantBytes||again.CostMicros!=wantCost||again.RevenueMicros!=wantRevenue{
				t.Fatalf("post-replacement telemetry double-counted: %+v",again)
			}
			if now:=p.targetAccepts.Load();now!=targetBeforeCut{
				t.Fatalf("target socket reopened during recovery before_cut=%d after=%d",targetBeforeCut,now)
			}
			// The whole test must also have used exactly one target socket.
			if n:=p.targetAccepts.Load()-p.targetBaseline;n!=1{
				t.Fatalf("target socket count since fixture baseline test_accepts=%d baseline=%d total=%d",n,p.targetBaseline,p.targetAccepts.Load())
			}
			t.Logf("PASS recovery telemetry/finance exact boot=%s ingress=%d egress=%d seq=%d",telemetryBoot,again.IngressBytes,again.EgressBytes,cur.Sequence)
			break
		}
		if time.Now().After(deadline){t.Fatalf("finance recovery deadline finance=%+v cursor=%+v ok=%v spoolErr=%v",fs,cur,ok,spErr)}
		time.Sleep(100*time.Millisecond)
	}
}
