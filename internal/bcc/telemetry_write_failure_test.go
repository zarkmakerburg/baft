package bcc

import (
    "encoding/json"
    "os"
    "path/filepath"
    "reflect"
    "testing"
    "time"

    "github.com/zarkmakerburg/baft/internal/telemetry"
)

func TestFailedTelemetrySaveCanRetryBootSwitchWithoutLosingAccounting(t *testing.T) {
    dir := t.TempDir()
    dbPath := filepath.Join(dir,"state.db")
    store,err := OpenStore(dbPath)
    if err != nil { t.Fatal(err) }
    const token = "telemetry-secret"
    if _,err := store.UpsertNode(Node{ID:"node",Address:"127.0.0.1:23000",Role:"foreign"}, token);err!=nil{t.Fatal(err)}
    if err:=store.SetFinancePolicy("node",100,300);err!=nil{t.Fatal(err)}
    at:=time.Date(2026,10,8,0,0,0,0,time.UTC)
    send:=func(rep telemetry.Report)(bool,error){
        body,err:=json.Marshal(rep);if err!=nil{t.Fatal(err)}
        _,duplicate,err:=store.ApplyTelemetry(token,telemetry.Sign(token,body),body,rep)
        return duplicate,err
    }
    a:=telemetry.Report{NodeID:"node",BootID:"boot-A",Sequence:1,IngressBytes:1<<30,TimestampUnix:at.Unix()}
    if dup,err:=send(a);err!=nil||dup{t.Fatalf("initial report: duplicate=%v err=%v",dup,err)}
    beforeFinance:=store.FinanceSnapshot()
    beforeCursor,_:=store.TelemetrySnapshot("node")
    beforeLedger,beforeHistory:=ledgerHistoryCounts(store,"node")
    beforeID:=store.st.NextTelemetryIngestID
    b:=telemetry.Report{NodeID:"node",BootID:"boot-B",Sequence:1,IngressBytes:1<<30,TimestampUnix:at.Add(time.Minute).Unix()}
    blocked:=filepath.Join(dir,"state-directory")
    if err:=os.Mkdir(blocked,0700);err!=nil{t.Fatal(err)}
    store.path=blocked
    if dup,err:=send(b);err==nil||dup{t.Fatalf("failed write: duplicate=%v err=%v",dup,err)}
    cursor,_:=store.TelemetrySnapshot("node")
    ledger,history:=ledgerHistoryCounts(store,"node")
    if !reflect.DeepEqual(cursor,beforeCursor)||!reflect.DeepEqual(store.FinanceSnapshot(),beforeFinance)||
       ledger!=beforeLedger||history!=beforeHistory||store.st.NextTelemetryIngestID!=beforeID||
       len(store.RetiredBootIDs("node"))!=0{
        t.Fatalf("failed save changed RAM: cursor=%+v ledger=%d history=%d next=%d retired=%v",
            cursor,ledger,history,store.st.NextTelemetryIngestID,store.RetiredBootIDs("node"))
    }
    store.path=dbPath
    if dup,err:=send(b);err!=nil||dup{t.Fatalf("retry lost new boot report: duplicate=%v err=%v",dup,err)}
    if dup,err:=send(b);err!=nil||!dup{t.Fatalf("post-commit replay: duplicate=%v err=%v",dup,err)}
    reopened,err:=OpenStore(dbPath);if err!=nil{t.Fatal(err)}
    got:=reopened.FinanceSnapshot()[0]
    if got.IngressBytes!=2<<30||got.CostMicros!=200||got.RevenueMicros!=600{
        t.Fatalf("retried accounting: %+v",got)
    }
    ledger,history=ledgerHistoryCounts(reopened,"node")
    if ledger!=2||history!=2{t.Fatalf("retried ledger/history=%d/%d",ledger,history)}
    ids:=reopened.RetiredBootIDs("node")
    if len(ids)!=1||ids[0]!="boot-A"{t.Fatalf("retired boots=%v",ids)}
}
