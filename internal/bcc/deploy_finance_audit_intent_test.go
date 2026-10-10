package bcc

import (
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "testing"
    "time"
)

func TestDeployAndFinanceCommitDurableAuditIntents(t *testing.T) {
    statePath:=filepath.Join(t.TempDir(),"state.db")
    store,err:=OpenStore(statePath);if err!=nil{t.Fatal(err)}
    if _,err:=store.UpsertNode(Node{ID:"node",Address:"127.0.0.1:25000",Role:"foreign"},"");err!=nil{t.Fatal(err)}
    app,err:=NewServer(store,"admin");if err!=nil{t.Fatal(err)}
    breakAuditAppend(t,app.audit)
    for _,tc:=range []struct{path string;body map[string]any;status int}{
        {"/api/deploy",map[string]any{"node_ids":[]string{"node"},"version":"v1.0.0"},http.StatusAccepted},
        {"/api/finance",map[string]any{"node_id":"node","cost_micros_per_gib":100,"revenue_micros_per_gib":200,"currency":"USD"},http.StatusOK},
    }{
        rr:=httptest.NewRecorder()
        app.Handler().ServeHTTP(rr,authReq(http.MethodPost,tc.path,"admin",tc.body))
        if rr.Code!=tc.status||rr.Header().Get("X-BAFT-Audit-State")!="pending"{
            t.Fatalf("%s status=%d audit=%q body=%s",tc.path,rr.Code,rr.Header().Get("X-BAFT-Audit-State"),rr.Body.String())
        }
    }
    if len(store.ListJobs())!=1||len(store.RateHistory("node"))!=1{t.Fatal("committed state missing")}
    if got:=store.PendingSecurityAuditIntents();len(got)!=2{t.Fatalf("pending=%+v",got)}
    reopened,err:=OpenStore(statePath);if err!=nil{t.Fatal(err)}
    if len(reopened.PendingSecurityAuditIntents())!=2{t.Fatal("intents not durable")}
    restarted,err:=NewServer(reopened,"admin");if err!=nil{t.Fatal(err)}
    entries,err:=restarted.audit.List(0);if err!=nil{t.Fatal(err)}
    actions:=map[string]int{}
    for _,e:=range entries{if e.IntentID==""{t.Fatalf("untracked audit=%+v",e)};actions[e.Action]++}
    if actions["deploy.create"]!=1||actions["finance.rate.change"]!=1||len(entries)!=2{
        t.Fatalf("reconciled audit=%+v",entries)
    }
    if len(reopened.PendingSecurityAuditIntents())!=0{t.Fatal("intent acknowledgement missing")}
    if err:=restarted.audit.Verify();err!=nil{t.Fatal(err)}
}

func TestDeployAndFinanceRejectFailedStateWriteWithoutOrphanIntent(t *testing.T) {
    dir:=t.TempDir()
    dbPath:=filepath.Join(dir,"state.db")
    store,err:=OpenStore(dbPath);if err!=nil{t.Fatal(err)}
    if _,err:=store.UpsertNode(Node{ID:"node",Address:"127.0.0.1:25001",Role:"foreign"},"");err!=nil{t.Fatal(err)}
    blocked:=filepath.Join(dir,"state-directory")
    if err:=os.Mkdir(blocked,0700);err!=nil{t.Fatal(err)}
    store.path=blocked
    audit:=AuditEntry{Timestamp:time.Now().UTC(),Actor:"admin"}
    if _,err:=store.CreateDeployJobs([]string{"node"},"v1.0.0",audit);err==nil{t.Fatal("deploy save unexpectedly succeeded")}
    if err:=store.SetFinancePolicyAt("node",100,200,"USD",time.Now().UTC(),audit);err==nil{
        t.Fatal("finance save unexpectedly succeeded")
    }
    if len(store.ListJobs())!=0||len(store.RateHistory("node"))!=0||len(store.PendingSecurityAuditIntents())!=0{
        t.Fatal("failed writes left jobs, rates, or audit intents in RAM")
    }
    store.path=dbPath
    reopened,err:=OpenStore(dbPath);if err!=nil{t.Fatal(err)}
    if len(reopened.ListJobs())!=0||len(reopened.RateHistory("node"))!=0||len(reopened.PendingSecurityAuditIntents())!=0{
        t.Fatal("failed writes reached disk")
    }
}
