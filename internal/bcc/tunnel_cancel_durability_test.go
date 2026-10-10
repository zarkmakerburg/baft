package bcc

import (
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "testing"
    "time"
)

func TestCancelTunnelFailedSaveRestoresTunnelAndJobs(t *testing.T) {
    store:=tunnelStore(t)
    tn:=newTunnel(t,store,time.Now().UTC())
    before:=store.st.Jobs[tn.JobID]
    next:=store.st.NextJob
    path:=store.path
    blocked:=filepath.Join(t.TempDir(),"state-directory")
    if err:=os.Mkdir(blocked,0700);err!=nil{t.Fatal(err)}
    store.path=blocked
    if _,err:=store.CancelTunnel(tn.ID,"test",time.Now().UTC());err==nil{
        t.Fatal("injected save failure unexpectedly succeeded")
    }
    got,_:=store.GetTunnel(tn.ID)
    job:=store.st.Jobs[tn.JobID]
    if got.Phase!=tn.Phase||got.JobID!=tn.JobID||job.Status!=before.Status||store.st.NextJob!=next{
        t.Fatalf("failed cancel changed RAM: tunnel=%+v job=%+v next=%d",got,job,store.st.NextJob)
    }
    store.path=path
    reopened,err:=OpenStore(path);if err!=nil{t.Fatal(err)}
    got,_=reopened.GetTunnel(tn.ID)
    job=reopened.st.Jobs[tn.JobID]
    if got.Phase!=tn.Phase||job.Status!=before.Status||reopened.st.NextJob!=next{
        t.Fatal("failed cancel reached disk")
    }
}

func TestCancelTunnelAuditFailureReportsCommittedRollback(t *testing.T) {
    store:=tunnelStore(t)
    tn:=newTunnel(t,store,time.Now().UTC())
    path:=store.path
    app,err:=NewServer(store,"admin");if err!=nil{t.Fatal(err)}
    breakAuditAppend(t,app.audit)
    rr:=httptest.NewRecorder()
    app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/tunnels/cancel","admin",map[string]any{
        "id":tn.ID,"reason":"operator test",
    }))
    if rr.Code!=http.StatusOK||rr.Header().Get("X-BAFT-Audit-State")!="pending"{
        t.Fatalf("cancel status=%d audit=%q body=%s",rr.Code,rr.Header().Get("X-BAFT-Audit-State"),rr.Body.String())
    }
    got,_:=store.GetTunnel(tn.ID)
    if got.Phase!=TunnelRolledBack||len(store.PendingSecurityAuditIntents())!=1{
        t.Fatalf("rollback or intent missing: %+v",got)
    }
    reopened,err:=OpenStore(path);if err!=nil{t.Fatal(err)}
    restarted,err:=NewServer(reopened,"admin");if err!=nil{t.Fatal(err)}
    entries,err:=restarted.audit.List(0);if err!=nil{t.Fatal(err)}
    if len(entries)!=1||entries[0].Action!="tunnel.cancel"||entries[0].IntentID==""{
        t.Fatalf("reconciled audit=%+v",entries)
    }
}
