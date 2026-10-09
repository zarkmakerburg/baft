package bcc

import (
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "testing"
    "time"
)

func TestTunnelCreateFailedWriteDoesNotLeaveQueuedJob(t *testing.T) {
    store:=tunnelStore(t)
    realPath:=store.path
    startJob:=store.st.NextJob
    blocked:=filepath.Join(t.TempDir(),"state-directory")
    if err:=os.Mkdir(blocked,0700);err!=nil{t.Fatal(err)}
    store.path=blocked
    if _,err:=store.CreateTunnel(TunnelRequest{EXNode:"ex-1",IRNode:"ir-1"},time.Now().UTC());err==nil{
        t.Fatal("injected save failure unexpectedly succeeded")
    }
    if len(store.ListTunnels())!=0||len(store.ListJobs())!=0||store.st.NextJob!=startJob{
        t.Fatalf("failed tunnel leaked queued job: tunnels=%d jobs=%d next=%d",len(store.ListTunnels()),len(store.ListJobs()),store.st.NextJob)
    }
    store.path=realPath
    reopened,err:=OpenStore(realPath);if err!=nil{t.Fatal(err)}
    if len(reopened.ListTunnels())!=0||len(reopened.ListJobs())!=0||reopened.st.NextJob!=startJob{
        t.Fatal("failed tunnel reached disk")
    }
}

func TestTunnelCreateAuditAppendFailureReportsCommittedState(t *testing.T) {
    store:=tunnelStore(t)
    path:=store.path
    app,err:=NewServer(store,"admin");if err!=nil{t.Fatal(err)}
    breakAuditAppend(t,app.audit)
    rr:=httptest.NewRecorder()
    app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/tunnels","admin",map[string]any{
        "ex_node":"ex-1","ir_node":"ir-1",
    }))
    if rr.Code!=http.StatusAccepted||rr.Header().Get("X-BAFT-Audit-State")!="pending"{
        t.Fatalf("tunnel status=%d audit=%q body=%s",rr.Code,rr.Header().Get("X-BAFT-Audit-State"),rr.Body.String())
    }
    if len(store.ListTunnels())!=1||len(store.ListJobs())!=1||len(store.PendingSecurityAuditIntents())!=1{
        t.Fatal("state and intent were not committed together")
    }
    reopened,err:=OpenStore(path);if err!=nil{t.Fatal(err)}
    restarted,err:=NewServer(reopened,"admin");if err!=nil{t.Fatal(err)}
    entries,err:=restarted.audit.List(0);if err!=nil{t.Fatal(err)}
    if len(entries)!=1||entries[0].Action!="tunnel.create"||entries[0].IntentID==""{
        t.Fatalf("reconciled audit=%+v",entries)
    }
    if len(reopened.PendingSecurityAuditIntents())!=0{t.Fatal("audit intent not acknowledged")}
}
