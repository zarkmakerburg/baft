package session

import (
	"bytes"
	"context"
	"net"
	"testing"

	"github.com/zarkmakerburg/baft/internal/resources"
)

func TestConservationSnapshotTracksTWRL(t *testing.T) {
	a,err:=resources.NewAllocator(resources.Limits{Total:256*1024,Receive:128*1024,Replay:128*1024,PerFlowReceive:128*1024,PerFlowReplay:128*1024});if err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01",Resources:a});if err!=nil{t.Fatal(err)}
	f:=newFlow(1,"main","00112233445566778899aabbccddeeff",nil,a);f.openOK=true
	p.mu.Lock();p.flows[1]=f;p.mu.Unlock()
	if _,err:=p.reserveReceiveWindow(f);err!=nil{t.Fatal(err)}
	if _,_,err:=f.acceptData(0,make([]byte,32*1024));err!=nil{t.Fatal(err)}

	s:=p.ConservationSnapshot()
	if s.Violations!=0||len(s.Flows)!=1{t.Fatalf("snapshot=%#v",s)}
	fs:=s.Flows[0]
	if !fs.TWRLValid||fs.Accepted!=32*1024||fs.Delivered!=0||fs.Credit!=64*1024||fs.RingBytes!=32*1024{
		t.Fatalf("flow snapshot=%#v",fs)
	}
	f.close();p.removeFlow(1)
}

func TestSharedAllocatorBoundsReceiveAcrossShardPeers(t *testing.T) {
	a,err:=resources.NewAllocator(resources.Limits{Total:256*1024,Receive:128*1024,Replay:128*1024,PerFlowReceive:64*1024,PerFlowReplay:64*1024});if err!=nil{t.Fatal(err)}
	mkPeer:=func(shard uint8)*Peer{
		var out bytes.Buffer
		p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01",ShardID:shard,Resources:a})
		if err!=nil{t.Fatal(err)}
		return p
	}
	p0,p1,p2:=mkPeer(0),mkPeer(1),mkPeer(2)
	f0:=newFlow(1,"main","00112233445566778899aabbccddeeff",nil,a);f0.openOK=true
	f1:=newFlow(3,"main","11112222333344445555666677778888",nil,a);f1.openOK=true
	f2:=newFlow(5,"main","9999aaaabbbbccccddddeeeeffff0000",nil,a);f2.openOK=true

	if _,err:=p0.reserveReceiveWindow(f0);err!=nil{t.Fatal(err)}
	if _,err:=p1.reserveReceiveWindow(f1);err!=nil{t.Fatal(err)}
	if _,err:=p2.reserveReceiveWindow(f2);err==nil{t.Fatal("third shard must not exceed shared 128 KiB receive pool")}
	if got:=a.Snapshot().ReceiveUsed;got!=128*1024{t.Fatalf("receive used=%d",got)}

	f0.close()
	if got:=a.Snapshot().ReceiveUsed;got!=64*1024{t.Fatalf("receive after release=%d",got)}
	if _,err:=p2.reserveReceiveWindow(f2);err!=nil{t.Fatalf("released capacity was not reusable: %v",err)}
	if got:=a.Snapshot().ReceiveUsed;got!=128*1024{t.Fatalf("receive after reuse=%d",got)}
	f1.close();f2.close()
}

func TestConservationSnapshotCanRunWhileTargetPumpActive(t *testing.T) {
	a,err:=resources.NewAllocator(resources.Limits{Total:256*1024,Receive:128*1024,Replay:128*1024,PerFlowReceive:128*1024,PerFlowReplay:128*1024});if err!=nil{t.Fatal(err)}
	var out bytes.Buffer
	p,err:=New(Dialer,Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01",Resources:a});if err!=nil{t.Fatal(err)}
	local,remote:=net.Pipe();defer remote.Close()
	f:=newFlow(1,"main","00112233445566778899aabbccddeeff",local,a);f.openOK=true
	p.mu.Lock();p.flows[1]=f;p.mu.Unlock()
	if _,err:=p.reserveReceiveWindow(f);err!=nil{t.Fatal(err)}
	ctx,cancel:=context.WithCancel(context.Background());defer cancel()
	p.startTargetPump(ctx,f)
	if _,_,err:=f.acceptData(0,make([]byte,32*1024));err!=nil{t.Fatal(err)}
	s:=p.ConservationSnapshot()
	if s.Violations!=0{t.Fatalf("invariant violation during blocked target write: %#v",s)}
	cancel();f.close();p.removeFlow(1);p.wg.Wait()
}
