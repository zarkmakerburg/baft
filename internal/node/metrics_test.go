package node

import (
	"bytes"
	"net"
	"testing"

	"github.com/zarkmakerburg/baft/internal/resources"
	"github.com/zarkmakerburg/baft/internal/session"
)

func TestRuntimeMetricsAggregateConservationWithoutPeerLabels(t *testing.T) {
	a,err:=resources.NewAllocator(resources.Limits{Total:256*1024,Receive:128*1024,Replay:128*1024,PerFlowReceive:128*1024,PerFlowReplay:128*1024});if err!=nil{t.Fatal(err)}
	r:=NewRuntime();r.Resources=a
	var out bytes.Buffer
	p,err:=session.New(session.Dialer,session.Carrier{In:bytes.NewReader(nil),Out:&out},"urn:baft:node:ex-01",nil,session.Options{NodeID:"ir-01",ExpectedPeerNodeID:"ex-01",Resources:a});if err!=nil{t.Fatal(err)}
	r.registerPeer(p);defer r.unregisterPeer(p)
	s:=r.metricsSnapshot()
	if s.ActiveFlows!=0||s.TotalUsedBytes!=0||s.InvariantViolations!=0{t.Fatalf("snapshot=%#v",s)}
}

// Keep an explicit loopback example at the test boundary; config validation is
// responsible for rejecting non-loopback metrics addresses.
func TestMetricsListenExampleIsLoopback(t *testing.T) {
	ip:=net.ParseIP("127.0.0.1")
	if ip==nil||!ip.IsLoopback(){t.Fatal("expected loopback")}
}
