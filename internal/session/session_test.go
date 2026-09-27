package session

import (
	"bytes"
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/routes"
)

func TestFlowRejectsAckPastTxNext(t *testing.T) {
	f := newFlow(1, "main", "00112233445566778899aabbccddeeff", nil)
	f.txNext = 10
	if err := f.onAck(11); err == nil { t.Fatal("expected invalid ACK") }
	if err := f.onAck(7); err != nil { t.Fatal(err) }
	if f.txAcked != 7 { t.Fatalf("txAcked=%d", f.txAcked) }
	if err := f.onAck(5); err != nil { t.Fatal(err) }
	if f.txAcked != 7 { t.Fatalf("old ACK moved state backwards: %d", f.txAcked) }
}

func TestFlowDuplicateDataIsNotReturnedForRewrite(t *testing.T) {
	f := newFlow(1, "main", "00112233445566778899aabbccddeeff", nil); f.rxMax = 100
	data,ack,dup,err:=f.acceptData(0,[]byte("abc")); if err!=nil||dup||string(data)!="abc"||ack!=3{t.Fatalf("first: data=%q ack=%d dup=%v err=%v",data,ack,dup,err)}
	data,ack,dup,err=f.acceptData(0,[]byte("abc")); if err!=nil||!dup||len(data)!=0||ack!=3{t.Fatalf("duplicate: data=%q ack=%d dup=%v err=%v",data,ack,dup,err)}
	data,ack,dup,err=f.acceptData(1,[]byte("bcXYZ")); if err!=nil||dup||string(data)!="XYZ"||ack!=6{t.Fatalf("overlap: data=%q ack=%d dup=%v err=%v",data,ack,dup,err)}
}

func TestFlowRejectsWindowRegression(t *testing.T){f:=newFlow(1,"main","00112233445566778899aabbccddeeff",nil);if err:=f.onWindow(100);err!=nil{t.Fatal(err)};if err:=f.onWindow(99);err==nil{t.Fatal("expected backwards WINDOW rejection")}}

func TestOpenIsIdempotentAndDoesNotRedial(t *testing.T){tbl,err:=routes.New([]routes.Route{{ID:"main",Target:"127.0.0.1:2443",AllowedPeers:map[string]struct{}{"urn:baft:node:ir-01":{}}}});if err!=nil{t.Fatal(err)};var out bytes.Buffer;p,err:=New(Listener,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ir-01",tbl,Options{NodeID:"ex-01",ExpectedPeerNodeID:"ir-01",ProfileID:"secure-fast",ProfileVersion:1,ConfigRevision:"test"});if err!=nil{t.Fatal(err)};var dials atomic.Int32;var remote net.Conn;p.dial=func(context.Context,string,string)(net.Conn,error){dials.Add(1);a,b:=net.Pipe();remote=b;return a,nil};req,_:=protocol.EncodeControl(protocol.OpenRequest{RouteID:"main",OpenNonce:"00112233445566778899aabbccddeeff"});fr:=protocol.Frame{Type:protocol.TypeOpen,StreamID:1,Payload:req};if err:=p.handleOpen(context.Background(),fr);err!=nil{t.Fatal(err)};if err:=p.handleOpen(context.Background(),fr);err!=nil{t.Fatal(err)};if got:=dials.Load();got!=1{t.Fatalf("dial count=%d",got)};if remote!=nil{_ = remote.Close()};p.closeAll();p.wg.Wait()}
