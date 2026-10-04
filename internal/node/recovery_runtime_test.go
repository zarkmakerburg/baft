package node

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/carrier/utlsdial"
	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/recovery"
	"github.com/zarkmakerburg/baft/internal/session"
)

func recoveryTestPeer(t *testing.T)(*session.Peer,*bytes.Buffer){
	t.Helper()
	var out bytes.Buffer
	p,err:=session.New(session.Dialer,session.Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:peer",nil,session.Options{
		NodeID:"local",ExpectedPeerNodeID:"peer",RecoveryEnabled:true,RecoveryRetention:time.Second,CarrierID:"carrier-1",
	})
	if err!=nil{t.Fatal(err)}
	return p,&out
}

func initializeRecoveryPeerBoot(t *testing.T,p *session.Peer,peerBoot string){
	t.Helper()
	ack:=protocol.HelloAck{
		SelectedProtocol:1,SessionID:p.SessionID(),Epoch:"1",PeerBootID:peerBoot,
		AcceptedProfile:protocol.AcceptedProfile{ID:"secure-fast",Version:1},
		NegotiatedLimits:protocol.NegotiatedLimits{MaxFramePayloadBytes:protocol.MaxPayloadSize,MaxFlowsPerShard:64,ReceiveInitialBytes:65536,ReceiveMaxBytes:1<<20,RetentionMS:30000},
	}
	payload,err:=protocol.EncodeControl(ack);if err!=nil{t.Fatal(err)}
	if err:=p.HandleCarrierFrame(context.Background(),1,"carrier-1",protocol.Frame{Type:protocol.TypeHelloAck,Payload:payload});err!=nil{t.Fatal(err)}
	readyPayload,err:=protocol.EncodeControl(protocol.Ready{SnapshotID:nil});if err!=nil{t.Fatal(err)}
	if err:=p.HandleCarrierFrame(context.Background(),1,"carrier-1",protocol.Frame{Type:protocol.TypeReady,Payload:readyPayload});err!=nil{t.Fatal(err)}
}

func assertOldRecoveryOwner(t *testing.T,p *session.Peer){
	t.Helper()
	if p.RecoveryEpoch()!=1||p.RecoveryOwner()!="carrier-1"{t.Fatalf("authority changed epoch=%d owner=%s",p.RecoveryEpoch(),p.RecoveryOwner())}
	offer,err:=p.BeginRecovery("probe-after-abort")
	if err!=nil{t.Fatalf("session remained frozen after abort: %v",err)}
	if offer.NextEpoch!=2{t.Fatalf("next epoch=%d",offer.NextEpoch)}
	p.AbortRecovery("probe-after-abort")
}

func TestNextRecoveryTransportPreservesLegacyPrimary(t *testing.T) {
	cfg := config.Config{Transport: config.Transport{Primary: "h2"}}
	if got := nextRecoveryTransport(cfg, "h2", ""); got != "h2" {
		t.Fatalf("without fallback recovery transport=%q want h2", got)
	}
}

func TestNextRecoveryTransportAlternatesConfiguredCarriers(t *testing.T) {
	cfg := config.Config{Transport: config.Transport{Primary: "h2", Fallback: "ws"}}
	if got := nextRecoveryTransport(cfg, "h2", ""); got != "ws" {
		t.Fatalf("from primary got %q want ws", got)
	}
	if got := nextRecoveryTransport(cfg, "ws", ""); got != "h2" {
		t.Fatalf("from fallback got %q want h2", got)
	}
	if got := nextRecoveryTransport(cfg, "", ""); got != "ws" {
		t.Fatalf("unknown current transport should treat initial carrier as primary; got %q", got)
	}
}

func TestRecoveryTransportRetriesAlternateAfterCandidateFailure(t *testing.T) {
	cfg := config.Config{Transport: config.Transport{Primary: "h2", Fallback: "ws"}}
	sh := &dialerShard{transport: "h2"}
	if got := sh.recoveryTransport(cfg); got != "ws" {
		t.Fatalf("first recovery attempt=%q want ws", got)
	}
	if got := sh.recoveryTransport(cfg); got != "h2" {
		t.Fatalf("second recovery attempt=%q want h2", got)
	}
	sh.replaceCarrier(&openedRuntimeCarrier{transport: "ws"})
	if got := sh.recoveryTransport(cfg); got != "h2" {
		t.Fatalf("after committing ws, next failure should try h2 first; got %q", got)
	}
}

func TestRuntimeCandidateSetupFailureKeepsOldOwner(t *testing.T){
	p,_:=recoveryTestPeer(t)
	r:=NewRuntime()
	injected:=errors.New("candidate unavailable")
	r.recoveryFault=func(stage string)error{if stage=="candidate_setup"{return injected};return nil}
	cfg:=config.Config{Peer:&config.Peer{Address:"127.0.0.1:1"}}
	err:=r.recoverDialerShard(context.Background(),cfg,&tls.Config{},0,&dialerShard{peer:p})
	if err==nil||!strings.Contains(err.Error(),"candidate setup"){t.Fatalf("err=%v",err)}
	assertOldRecoveryOwner(t,p)
}

func TestRuntimeSnapshotExchangeFailureKeepsOldOwner(t *testing.T){
	p,_:=recoveryTestPeer(t)
	ts:=httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		w.Header().Set("Content-Type","application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
	}))
	ts.EnableHTTP2=true;ts.StartTLS();defer ts.Close()

	rt:=NewRuntime()
	injected:=errors.New("snapshot exchange injected")
	rt.recoveryFault=func(stage string)error{if stage=="snapshot_exchange"{return injected};return nil}
	addr:=strings.TrimPrefix(ts.URL,"https://")
	cfg:=config.Config{Peer:&config.Peer{Address:addr}}
	tlsCfg:=&tls.Config{InsecureSkipVerify:true,NextProtos:[]string{"h2"}} // test-only server
	err:=rt.recoverDialerShard(context.Background(),cfg,tlsCfg,0,&dialerShard{peer:p})
	if err==nil||!strings.Contains(err.Error(),"snapshot exchange"){t.Fatalf("err=%v",err)}
	assertOldRecoveryOwner(t,p)
}

func TestRuntimeFailureImmediatelyBeforeCommitKeepsOldOwner(t *testing.T){
	p,_:=recoveryTestPeer(t)
	const peerBoot="22222222222222222222222222222222"
	initializeRecoveryPeerBoot(t,p,peerBoot)

	ts:=httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		w.Header().Set("Content-Type","application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		fr,err:=protocol.Decode(r.Body);if err!=nil{return}
		local,err:=session.DecodeRecoveryOffer(fr);if err!=nil{return}
		remote:=session.RecoveryOffer{
			CandidateID:local.CandidateID,NextEpoch:local.NextEpoch,
			Snapshot:recovery.Snapshot{SessionID:local.Snapshot.SessionID,BootID:peerBoot,Epoch:local.Snapshot.Epoch,Flows:[]recovery.FlowSnapshot{}},
			Routes:map[uint64]string{},
		}
		if err:=session.EncodeRecoveryOffer(w,remote);err!=nil{return}
		_ = http.NewResponseController(w).Flush()
		fr,err=protocol.Decode(r.Body);if err!=nil{return}
		prepared,err:=session.DecodeRecoveryControl(fr);if err!=nil{return}
		if prepared.Phase!=session.RecoveryPhasePrepared{return}
		if err:=session.EncodeRecoveryControl(w,prepared);err!=nil{return}
		_ = http.NewResponseController(w).Flush()
		fr,err=protocol.Decode(r.Body);if err!=nil{return}
		ready,err:=session.DecodeRecoveryControl(fr);if err!=nil{return}
		if ready.Phase!=session.RecoveryPhaseCommitReady{return}
		if err:=session.EncodeRecoveryControl(w,ready);err!=nil{return}
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
	}))
	ts.EnableHTTP2=true;ts.StartTLS();defer ts.Close()

	rt:=NewRuntime()
	injected:=errors.New("before commit injected")
	rt.recoveryFault=func(stage string)error{if stage=="before_commit"{return injected};return nil}
	cfg:=config.Config{Peer:&config.Peer{Address:strings.TrimPrefix(ts.URL,"https://")}}
	tlsCfg:=&tls.Config{InsecureSkipVerify:true,NextProtos:[]string{"h2"}} // test-only server
	err:=rt.recoverDialerShard(context.Background(),cfg,tlsCfg,0,&dialerShard{peer:p})
	if err==nil||!strings.Contains(err.Error(),"before commit"){t.Fatalf("err=%v",err)}
	assertOldRecoveryOwner(t,p)
}


func TestWSCarrierHeadersMatchDefaultUTLSChromeMajor(t *testing.T) {
	ua:=wsCarrierHeaders("example.com").Get("User-Agent")
	want:="Chrome/"+utlsdial.DefaultChromeMajor+"."
	if !strings.Contains(ua,want){t.Fatalf("User-Agent %q does not match default uTLS Chrome major %s",ua,utlsdial.DefaultChromeMajor)}
}
