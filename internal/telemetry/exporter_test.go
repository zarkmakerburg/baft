package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestExporterQueuesRetriesAndPreservesSequence(t *testing.T){
	var calls atomic.Int32
	var mu sync.Mutex
	var accepted []uint64
	tokenHash:=hashTokenForTest("agent")
	srv:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		body,err:=io.ReadAll(r.Body);if err!=nil{t.Error(err);http.Error(w,"read",500);return}
		if !VerifyHashedToken(tokenHash,r.Header.Get("X-BAFT-Signature"),body){t.Error("signature verification failed");http.Error(w,"sig",401);return}
		var rep Report
		if err:=json.Unmarshal(body,&rep);err!=nil{t.Error(err);http.Error(w,"json",400);return}
		if calls.Add(1)==1{http.Error(w,"offline",http.StatusServiceUnavailable);return}
		mu.Lock();accepted=append(accepted,rep.Sequence);mu.Unlock()
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

	mu.Lock();defer mu.Unlock()
	if len(accepted)!=2||accepted[0]!=1||accepted[1]!=2{t.Fatalf("accepted sequences=%v",accepted)}
}

func hashTokenForTest(v string) string {
	sum:=sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}
