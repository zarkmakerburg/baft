package bcc

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "path/filepath"
    "testing"
    "time"
)

func TestFinancePostReturnsNewVersionForBackdatedEffectiveRate(t *testing.T) {
    store,err:=OpenStore(filepath.Join(t.TempDir(),"state.db"))
    if err!=nil{t.Fatal(err)}
    if _,err:=store.UpsertNode(Node{ID:"n1",Address:"127.0.0.1:26000",Role:"foreign"},"");err!=nil{t.Fatal(err)}
    app,err:=NewServer(store,"admin");if err!=nil{t.Fatal(err)}
    later:=time.Date(2026,10,10,0,0,0,0,time.UTC)
    earlier:=later.Add(-24*time.Hour)
    submit:=func(effective time.Time,cost int64) FinancePolicy{
        t.Helper()
        rr:=httptest.NewRecorder()
        app.Handler().ServeHTTP(rr,authReq(http.MethodPost,"/api/finance","admin",map[string]any{
            "node_id":"n1","cost_micros_per_gib":cost,"revenue_micros_per_gib":cost*2,
            "currency":"USD","effective_from":effective.Format(time.RFC3339),
        }))
        if rr.Code!=http.StatusOK{t.Fatalf("post rate status=%d body=%s",rr.Code,rr.Body.String())}
        var out struct{Rate FinancePolicy `json:"rate"`}
        if err:=json.Unmarshal(rr.Body.Bytes(),&out);err!=nil{t.Fatal(err)}
        return out.Rate
    }
    first:=submit(later,100)
    second:=submit(earlier,300)
    if second.Version<=first.Version||second.CostMicrosPerGiB!=300||!second.EffectiveFrom.Equal(earlier){
        t.Fatalf("backdated POST returned another policy: first=%+v second=%+v",first,second)
    }
    h:=store.RateHistory("n1")
    if len(h)!=2||h[0].Version!=second.Version||h[1].Version!=first.Version{
        t.Fatalf("history ordering fixture invalid: %+v",h)
    }
}
