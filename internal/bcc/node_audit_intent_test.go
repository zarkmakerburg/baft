package bcc

import (
    "net/http"
    "net/http/httptest"
    "path/filepath"
    "testing"
)

func TestNodeUpsertKeepsDurableAuditIntentWhenAuditAppendFails(t *testing.T) {
    statePath:=filepath.Join(t.TempDir(),"state.db")
    store,err:=OpenStore(statePath);if err!=nil{t.Fatal(err)}
    app,err:=NewServer(store,"admin");if err!=nil{t.Fatal(err)}
    breakAuditAppend(t,app.audit)
    rr:=httptest.NewRecorder()
    app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/nodes","admin",map[string]any{
        "ID":"node","Address":"127.0.0.1:24000","Role":"foreign",
    }))
    if rr.Code!=http.StatusCreated{t.Fatalf("committed upsert status=%d body=%s",rr.Code,rr.Body.String())}
    if rr.Header().Get("X-BAFT-Audit-State")!="pending"{t.Fatalf("missing pending audit header: %+v",rr.Header())}
    if _,ok:=store.GetNode("node");!ok{t.Fatal("node not committed")}
    pending:=store.PendingSecurityAuditIntents()
    if len(pending)!=1||pending[0].Action!="node.upsert"||pending[0].Target!="node"{
        t.Fatalf("missing durable success intent: %+v",pending)
    }
    reopened,err:=OpenStore(statePath);if err!=nil{t.Fatal(err)}
    if _,ok:=reopened.GetNode("node");!ok{t.Fatal("node missing after restart")}
    if len(reopened.PendingSecurityAuditIntents())!=1{t.Fatal("audit intent missing after restart")}
    restarted,err:=NewServer(reopened,"admin");if err!=nil{t.Fatal(err)}
    entries,err:=restarted.audit.List(0);if err!=nil{t.Fatal(err)}
    if len(entries)!=1||entries[0].Action!="node.upsert"||entries[0].IntentID==""{
        t.Fatalf("recovered audit=%+v",entries)
    }
    if len(reopened.PendingSecurityAuditIntents())!=0{t.Fatal("delivered intent not acknowledged")}
    if err:=restarted.audit.Verify();err!=nil{t.Fatal(err)}
}
