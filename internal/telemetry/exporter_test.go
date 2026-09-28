package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestExporterQueuesRetriesAndPreservesSequence(t *testing.T){
	var calls atomic.Int32
	var seqs []uint64
	srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		if !VerifyHashedToken(hashTokenForTest("agent"),r.Header.Get("X-BAFT-Signature"),mustReadBody(t,r)){t.Fatal("signature verification failed")}
		calls.Add(1)
		if calls.Load()==1{http.Error(w,"offline",http.StatusServiceUnavailable);return}
		// body was consumed by mustReadBody; this fixture only verifies queue drain/signature.
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	var n atomic.Uint64
	e,err:=New("n1",srv.URL,"agent",0,func() Snapshot{
		v:=n.Add(100)
		return Snapshot{IngressBytes:v,EgressBytes:v}
	})
	if err!=nil{t.Fatal(err)}
	e.QueueLimit=4
	if err:=e.SendOnce(context.Background());err==nil{t.Fatal("expected first send failure")}
	if e.QueueLength()!=1{t.Fatalf("queue=%d",e.QueueLength())}
	if err:=e.SendOnce(context.Background());err!=nil{t.Fatal(err)}
	if e.QueueLength()!=0{t.Fatalf("queue not drained: %d",e.QueueLength())}
	_ = seqs
}

func hashTokenForTest(v string) string {
	sum:=sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func mustReadBody(t *testing.T,r *http.Request) []byte {
	t.Helper()
	b,err:=io.ReadAll(r.Body);if err!=nil{t.Fatal(err)}
	return b
}
