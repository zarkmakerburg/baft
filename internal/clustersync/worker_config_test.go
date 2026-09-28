package clustersync

import (
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
)

func workerTemplateForTest() WorkerTemplate {
	return WorkerTemplate{
		NodeID:"ir-worker",
		NoiseKeyFile:"/tmp/worker-noise.key",
		TLS:config.TLS{MinVersion:"1.3",CAFile:"/tmp/ca",CertFile:"/tmp/cert",KeyFile:"/tmp/tls.key",SessionTickets:false},
		Limits:config.Limits{MaxFlows:64,DataMemoryMiB:64,ReceiveInitialKiB:64,ReceiveMaxMiB:8,ReplayMaxMiB:8},
		RouteBasePort:16000,
		MetricsBasePort:9400,
		StateDir:"/tmp/baft-worker",
	}
}

func TestBuildWorkerConfigsMirrorsExactlySixRoutes(t *testing.T) {
	cfgs:=testConfigs(t)
	now:=time.Unix(1700000000,0)
	m,err:=ManifestFromConfigs("goldapp-baft",1,15*time.Minute,now,cfgs)
	if err!=nil{t.Fatal(err)}
	s:=snapshotFromManifest(m)
	out,err:=BuildWorkerConfigs(s,workerTemplateForTest())
	if err!=nil{t.Fatal(err)}
	if len(out)!=6{t.Fatalf("configs=%d",len(out))}
	for i,c:=range out{
		if c.Peer.Address!=cfgs[i].Peer.Address{t.Fatalf("node %d address mismatch",i+1)}
		if c.Peer.AllowedIdentity!=cfgs[i].Peer.AllowedIdentity{t.Fatalf("node %d identity mismatch",i+1)}
		if c.Noise.PeerPublicKey!=cfgs[i].Noise.PeerPublicKey{t.Fatalf("node %d peer key mismatch",i+1)}
		if len(c.Routes)!=1||c.Routes[0].RemoteRoute!=cfgs[i].Routes[0].RemoteRoute{t.Fatalf("node %d route mismatch",i+1)}
		if c.Node.ID!="ir-worker"{t.Fatalf("node %d worker id mismatch",i+1)}
	}
	t.Log("PASS worker config builder mirrored exactly six peer identities, addresses, Noise public keys, and remote routes")
}

func TestChangedRouteIndexesPinpointsSingleNodeUpdate(t *testing.T) {
	now:=time.Unix(1700000000,0)
	aManifest,err:=ManifestFromConfigs("goldapp-baft",1,15*time.Minute,now,testConfigs(t))
	if err!=nil{t.Fatal(err)}
	cfgs:=testConfigs(t)
	cfgs[4].Peer.Address="198.51.100.99:443"
	bManifest,err:=ManifestFromConfigs("goldapp-baft",2,15*time.Minute,now,cfgs)
	if err!=nil{t.Fatal(err)}
	a:=snapshotFromManifest(aManifest)
	b:=snapshotFromManifest(bManifest)
	got:=ChangedRouteIndexes(a,b)
	if len(got)!=1||got[0]!=4{t.Fatalf("changed=%v",got)}
	t.Logf("PASS changed-node detection index=%d address=%s",got[0]+1,b.Routes[got[0]].Address)
}
