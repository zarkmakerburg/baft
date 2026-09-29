package clustersync

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
)

func cloneManifestForIdentityTest(in Manifest) Manifest {
	out:=in
	out.Nodes=make([]NodeDescriptor,len(in.Nodes))
	for i:=range in.Nodes{
		out.Nodes[i]=in.Nodes[i]
		out.Nodes[i].Routes=append([]RouteDescriptor(nil),in.Nodes[i].Routes...)
	}
	return out
}

func refreshRevisionForIdentityTest(t *testing.T,m *Manifest){
	t.Helper()
	r,err:=semanticRevision(m.Nodes);if err!=nil{t.Fatal(err)};m.Revision=r
}

func permuteNodesForIdentityTest(in Manifest,p []int) Manifest{
	out:=cloneManifestForIdentityTest(in)
	out.Nodes=make([]NodeDescriptor,len(in.Nodes))
	for i,j:=range p{out.Nodes[i]=in.Nodes[j];out.Nodes[i].Routes=append([]RouteDescriptor(nil),in.Nodes[j].Routes...)}
	return out
}

func idsEqual(got,want []string) bool {
	a:=append([]string(nil),got...);b:=append([]string(nil),want...);sort.Strings(a);sort.Strings(b);return reflect.DeepEqual(a,b)
}

func nodeIDs(s Snapshot) []string{out:=make([]string,0,len(s.Routes));for _,r:=range s.Routes{out=append(out,r.NodeID)};return out}

func TestTopologyPermutationIsSemanticNoOp(t *testing.T){
	now:=time.Unix(1700000000,0)
	base,err:=ManifestFromConfigs("goldapp-baft",1,15*time.Minute,now,testConfigsN(t,8));if err!=nil{t.Fatal(err)}
	a:=snapshotFromManifest(base)
	r:=rand.New(rand.NewSource(20260930))
	for i:=0;i<100;i++{
		m:=permuteNodesForIdentityTest(base,r.Perm(len(base.Nodes)))
		refreshRevisionForIdentityTest(t,&m)
		b:=snapshotFromManifest(m);d:=DiffTopology(a,b)
		if len(d.AddedNodeIDs)!=0||len(d.RemovedNodeIDs)!=0||len(d.ChangedNodeIDs)!=0||len(d.UnchangedNodeIDs)!=8{
			t.Fatalf("iteration %d diff=%+v",i,d)
		}
		if !idsEqual(nodeIDs(a),nodeIDs(b)){t.Fatalf("iteration %d NodeID set changed",i)}
	}
}

func TestTopologyMutationDoesNotMutateUnrelatedNodes(t *testing.T){
	now:=time.Unix(1700000000,0)
	baseM,err:=ManifestFromConfigs("goldapp-baft",1,15*time.Minute,now,testConfigsN(t,8));if err!=nil{t.Fatal(err)}
	base:=snapshotFromManifest(baseM); ids:=nodeIDs(base)
	for iter:=0;iter<100;iter++{
		removed:=cloneSnapshot(base); removed.Routes=append(removed.Routes[:1:1],removed.Routes[2:]...)
		d:=DiffTopology(base,removed)
		if !idsEqual(d.RemovedNodeIDs,[]string{ids[1]})||len(d.AddedNodeIDs)!=0||len(d.ChangedNodeIDs)!=0{t.Fatalf("remove iter=%d diff=%+v",iter,d)}

		added:=cloneSnapshot(base)
		x:=added.Routes[0];x.NodeID=fmt.Sprintf("urn:baft:node:x-%03d",iter);x.Address=fmt.Sprintf("198.51.100.%d:443",100+(iter%100));x.ServerName=fmt.Sprintf("x-%03d.example",iter);x.AllowedIdentity=fmt.Sprintf("urn:baft:auth:x-%03d",iter)
		added.Routes=append(added.Routes,x)
		d=DiffTopology(base,added)
		if !idsEqual(d.AddedNodeIDs,[]string{x.NodeID})||len(d.RemovedNodeIDs)!=0||len(d.ChangedNodeIDs)!=0{t.Fatalf("add iter=%d diff=%+v",iter,d)}

		endpoint:=cloneSnapshot(base);endpoint.Routes[2].Address=fmt.Sprintf("203.0.113.%d:443",1+(iter%200))
		d=DiffTopology(base,endpoint);if !idsEqual(d.ChangedNodeIDs,[]string{ids[2]})||len(d.AddedNodeIDs)!=0||len(d.RemovedNodeIDs)!=0{t.Fatalf("endpoint iter=%d diff=%+v",iter,d)}

		reordered:=cloneSnapshot(base);rand.New(rand.NewSource(int64(1000+iter))).Shuffle(len(reordered.Routes),func(i,j int){reordered.Routes[i],reordered.Routes[j]=reordered.Routes[j],reordered.Routes[i]})
		d=DiffTopology(base,reordered);if len(d.AddedNodeIDs)!=0||len(d.RemovedNodeIDs)!=0||len(d.ChangedNodeIDs)!=0{t.Fatalf("reorder iter=%d diff=%+v",iter,d)}

		route:=cloneSnapshot(base);route.Routes[3].Routes[0].RemoteRoute=fmt.Sprintf("service-mutated-%03d",iter)
		d=DiffTopology(base,route);if !idsEqual(d.ChangedNodeIDs,[]string{ids[3]}){t.Fatalf("route iter=%d diff=%+v",iter,d)}
	}
}

func TestIdentityRotationSemantics(t *testing.T){
	now:=time.Unix(1700000000,0)
	m,err:=ManifestFromConfigs("goldapp-baft",1,15*time.Minute,now,testConfigsN(t,8));if err!=nil{t.Fatal(err)}
	base:=snapshotFromManifest(m);id:=base.Routes[2].NodeID
	for i:=0;i<100;i++{
		auth:=cloneSnapshot(base);auth.Routes[2].AllowedIdentity=fmt.Sprintf("urn:baft:rotated:%03d",i)
		d:=DiffTopology(base,auth);if !idsEqual(d.ChangedNodeIDs,[]string{id})||len(d.AddedNodeIDs)!=0||len(d.RemovedNodeIDs)!=0{t.Fatalf("auth rotation iter=%d diff=%+v",i,d)}
		repl:=cloneSnapshot(base);newID:=fmt.Sprintf("%s-r%03d",id,i);repl.Routes[2].NodeID=newID
		d=DiffTopology(base,repl);if !idsEqual(d.RemovedNodeIDs,[]string{id})||!idsEqual(d.AddedNodeIDs,[]string{newID})||len(d.ChangedNodeIDs)!=0{t.Fatalf("NodeID replacement iter=%d diff=%+v",i,d)}
	}
}

func TestManifestRevisionInvariantUnderPermutation(t *testing.T){
	now:=time.Unix(1700000000,0)
	base,err:=ManifestFromConfigs("goldapp-baft",1,15*time.Minute,now,testConfigsN(t,8));if err!=nil{t.Fatal(err)}
	rng:=rand.New(rand.NewSource(73))
	for i:=0;i<100;i++{m:=permuteNodesForIdentityTest(base,rng.Perm(len(base.Nodes)));r,err:=semanticRevision(m.Nodes);if err!=nil{t.Fatal(err)};if r!=base.Revision{t.Fatalf("permutation %d revision changed",i)}}
	mutations:=[]func(*Manifest){
		func(m *Manifest){m.Nodes[0].Address="203.0.113.9:443"},
		func(m *Manifest){m.Nodes[0].AllowedIdentity="urn:baft:rotated"},
		func(m *Manifest){m.Nodes[0].ID="urn:baft:node:replacement"},
		func(m *Manifest){m.Nodes[0].Routes[0].RemoteRoute="changed-route"},
		func(m *Manifest){m.Nodes[0].NoisePublicKey=m.Nodes[1].NoisePublicKey},
	}
	for i,mut:=range mutations{m:=cloneManifestForIdentityTest(base);mut(&m);r,err:=semanticRevision(m.Nodes);if err!=nil{t.Fatal(err)};if r==base.Revision{t.Fatalf("mutation %d did not change revision",i)}}
}

func TestTokenIdentityRoundTripAndValidation(t *testing.T){
	workerPriv,workerPub,err:=GenerateWorkerKeyPair();if err!=nil{t.Fatal(err)}
	signPub,signPriv,err:=GenerateSigningKeyPair();if err!=nil{t.Fatal(err)}
	now:=time.Unix(1700000000,0)
	m,err:=ManifestFromConfigs("goldapp-baft",1,15*time.Minute,now,testConfigsN(t,3));if err!=nil{t.Fatal(err)}
	for i:=range m.Nodes{m.Nodes[i].ID=fmt.Sprintf("node-%c",'A'+rune(i));m.Nodes[i].Routes[0].ID=fmt.Sprintf("route-%c",'A'+rune(i))}
	refreshRevisionForIdentityTest(t,&m)
	tok,err:=Seal(m,workerPub,signPriv);if err!=nil{t.Fatal(err)}
	got,err:=Open(tok,workerPriv,signPub,now.Add(time.Second));if err!=nil{t.Fatal(err)}
	for i:=range m.Nodes{
		if got.Nodes[i].ID!=m.Nodes[i].ID||got.Nodes[i].Address!=m.Nodes[i].Address||got.Nodes[i].AllowedIdentity!=m.Nodes[i].AllowedIdentity||got.Nodes[i].NoisePublicKey!=m.Nodes[i].NoisePublicKey{t.Fatalf("node %d roundtrip mismatch",i)}
		if len(got.Nodes[i].Routes)!=1||got.Nodes[i].Routes[0].ID!=m.Nodes[i].Routes[0].ID{t.Fatalf("node %d RouteID lost",i)}
	}
	snap:=snapshotFromManifest(got)
	for _,node:=range snap.Routes{if len(node.Routes)!=1||node.Routes[0].ID==""{t.Fatalf("snapshot lost RouteID for %s",node.NodeID)}}
	cases:=[]struct{name string;mut func(*Manifest)}{
		{"empty-node-id",func(x *Manifest){x.Nodes[1].ID=""}},
		{"duplicate-node-id",func(x *Manifest){x.Nodes[1].ID=x.Nodes[0].ID}},
		{"duplicate-address",func(x *Manifest){x.Nodes[1].Address=x.Nodes[0].Address}},
		{"duplicate-allowed-identity",func(x *Manifest){x.Nodes[1].AllowedIdentity=x.Nodes[0].AllowedIdentity}},
		{"empty-route-id",func(x *Manifest){x.Nodes[1].Routes[0].ID=""}},
		{"duplicate-route-id",func(x *Manifest){x.Nodes[0].Routes=append(x.Nodes[0].Routes,x.Nodes[0].Routes[0])}},
	}
	for _,tc:=range cases{t.Run(tc.name,func(t *testing.T){x:=cloneManifestForIdentityTest(m);tc.mut(&x);refreshRevisionForIdentityTest(t,&x);tok,err:=Seal(x,workerPub,signPriv);if err!=nil{t.Fatal(err)};if _,err:=Open(tok,workerPriv,signPub,now.Add(time.Second));err==nil{t.Fatal("invalid manifest accepted")}})}
}

func configsByNodeForIdentityTest(s Snapshot,cfgs []config.Config) map[string]config.Config{
	nodes:=append([]MirrorRoute(nil),s.Routes...);sort.Slice(nodes,func(i,j int)bool{return nodes[i].NodeID<nodes[j].NodeID})
	out:=make(map[string]config.Config,len(nodes));for i,n:=range nodes{out[n.NodeID]=cfgs[i]};return out
}

func TestBuildWorkerConfigsStableAcrossPermutation(t *testing.T){
	now:=time.Unix(1700000000,0);m,err:=ManifestFromConfigs("goldapp-baft",1,15*time.Minute,now,testConfigsN(t,8));if err!=nil{t.Fatal(err)}
	baseS:=snapshotFromManifest(m);baseCfg,err:=BuildWorkerConfigs(baseS,workerTemplateForTest());if err!=nil{t.Fatal(err)}
	rng:=rand.New(rand.NewSource(81))
	for i:=0;i<100;i++{p:=permuteNodesForIdentityTest(m,rng.Perm(len(m.Nodes)));refreshRevisionForIdentityTest(t,&p);s:=snapshotFromManifest(p);got,err:=BuildWorkerConfigs(s,workerTemplateForTest());if err!=nil{t.Fatal(err)};if !reflect.DeepEqual(baseCfg,got){t.Fatalf("permutation %d changed worker configs",i)}}
}

func TestWorkerResourceIdentityStableAcrossAddRemove(t *testing.T){
	now:=time.Unix(1700000000,0);m,err:=ManifestFromConfigs("goldapp-baft",1,15*time.Minute,now,testConfigsN(t,4));if err!=nil{t.Fatal(err)}
	baseS:=snapshotFromManifest(m);baseCfg,err:=BuildWorkerConfigs(baseS,workerTemplateForTest());if err!=nil{t.Fatal(err)};baseMap:=configsByNodeForIdentityTest(baseS,baseCfg)
	for i:=0;i<100;i++{
		s:=cloneSnapshot(baseS);removedID:=s.Routes[1].NodeID;s.Routes=append(s.Routes[:1:1],s.Routes[2:]...)
		x:=s.Routes[0];x.NodeID=fmt.Sprintf("node-X-%03d",i);x.Address=fmt.Sprintf("198.51.100.%d:443",100+i%100);x.ServerName=fmt.Sprintf("x-%03d.example",i);x.AllowedIdentity=fmt.Sprintf("urn:baft:auth:x-%03d",i);s.Routes=append(s.Routes,x)
		got,err:=BuildWorkerConfigs(s,workerTemplateForTest());if err!=nil{t.Fatal(err)};gm:=configsByNodeForIdentityTest(s,got)
		for id,want:=range baseMap{if id==removedID{continue};if !reflect.DeepEqual(want,gm[id]){t.Fatalf("iter=%d unrelated resource changed for %s",i,id)}}
	}
}

func TestNoAccidentalResourceCollision(t *testing.T){
	now:=time.Unix(1700000000,0)
	for _,n:=range []int{16,32}{t.Run(fmt.Sprintf("n=%d",n),func(t *testing.T){m,err:=ManifestFromConfigs("goldapp-baft",1,15*time.Minute,now,testConfigsN(t,n));if err!=nil{t.Fatal(err)};out,err:=BuildWorkerConfigs(snapshotFromManifest(m),workerTemplateForTest());if err!=nil{t.Fatal(err)}
		sockets:=map[string]struct{}{};ports:=map[string]struct{}{};routeIDs:=map[string]struct{}{}
		for _,c:=range out{if _,ok:=sockets[c.Management.UnixSocket];ok{t.Fatal("socket collision")};sockets[c.Management.UnixSocket]=struct{}{};if _,ok:=ports[c.Management.MetricsListen];ok{t.Fatal("metrics collision")};ports[c.Management.MetricsListen]=struct{}{};for _,r:=range c.Routes{if _,ok:=routeIDs[r.ID];ok{t.Fatal("route ID collision")};routeIDs[r.ID]=struct{}{};if _,ok:=ports[r.Listen];ok{t.Fatal("listener collision")};ports[r.Listen]=struct{}{}}}
	})}
}


func TestRoutePermutationIsSemanticNoOp(t *testing.T){
	now:=time.Unix(1700000000,0)
	m,err:=ManifestFromConfigs("goldapp-baft",7,15*time.Minute,now,testConfigsN(t,4));if err!=nil{t.Fatal(err)}
	for i:=range m.Nodes{
		r2:=m.Nodes[i].Routes[0]
		r2.ID=r2.ID+"-secondary"
		r2.RemoteRoute=r2.RemoteRoute+"-secondary"
		r2.MasterListen=fmt.Sprintf("127.0.0.1:%d",18000+i)
		m.Nodes[i].Routes=append(m.Nodes[i].Routes,r2)
	}
	refreshRevisionForIdentityTest(t,&m)
	baseRevision:=m.Revision
	baseSnapshot:=snapshotFromManifest(m)
	baseCfg,err:=BuildWorkerConfigs(baseSnapshot,workerTemplateForTest());if err!=nil{t.Fatal(err)}
	for i:=0;i<100;i++{
		p:=permuteNodesForIdentityTest(m,rand.New(rand.NewSource(int64(9000+i))).Perm(len(m.Nodes)))
		for j:=range p.Nodes{if (i+j)%2==0{sort.Slice(p.Nodes[j].Routes,func(a,b int)bool{return p.Nodes[j].Routes[a].ID>p.Nodes[j].Routes[b].ID})}}
		refreshRevisionForIdentityTest(t,&p)
		if p.Revision!=baseRevision{t.Fatalf("iteration %d route/node permutation changed revision",i)}
		s:=snapshotFromManifest(p);d:=DiffTopology(baseSnapshot,s)
		if len(d.AddedNodeIDs)!=0||len(d.RemovedNodeIDs)!=0||len(d.ChangedNodeIDs)!=0{t.Fatalf("iteration %d semantic diff=%+v",i,d)}
		cfg,err:=BuildWorkerConfigs(s,workerTemplateForTest());if err!=nil{t.Fatal(err)}
		if !reflect.DeepEqual(baseCfg,cfg){t.Fatalf("iteration %d resource mapping changed",i)}
	}
}

func TestEngineSameGenerationPermutationNoOp(t *testing.T){
	workerPriv,workerPub,err:=GenerateWorkerKeyPair();if err!=nil{t.Fatal(err)}
	signPub,signPriv,err:=GenerateSigningKeyPair();if err!=nil{t.Fatal(err)}
	now:=time.Unix(1700000000,0)
	m,err:=ManifestFromConfigs("goldapp-baft",11,15*time.Minute,now,testConfigsN(t,8));if err!=nil{t.Fatal(err)}
	engine,err:=NewEngine(workerPriv,signPub,"goldapp-baft");if err!=nil{t.Fatal(err)}
	tok,err:=Seal(m,workerPub,signPriv);if err!=nil{t.Fatal(err)}
	first,changed,err:=engine.Apply(tok,now.Add(time.Second));if err!=nil||!changed{t.Fatalf("first apply changed=%v err=%v",changed,err)}
	p:=permuteNodesForIdentityTest(m,[]int{3,7,0,4,1,6,2,5})
	refreshRevisionForIdentityTest(t,&p)
	if p.Revision!=m.Revision{t.Fatal("permutation revision mismatch")}
	tok2,err:=Seal(p,workerPub,signPriv);if err!=nil{t.Fatal(err)}
	second,changed,err:=engine.Apply(tok2,now.Add(2*time.Second));if err!=nil{t.Fatal(err)}
	if changed{t.Fatal("same-generation semantic permutation treated as change")}
	if !reflect.DeepEqual(first,second){t.Fatal("same-generation permutation changed current snapshot")}
}

func TestResourceCollisionFailsClosed(t *testing.T){
	used:=map[int]string{}
	if err:=reservePort(used,42424,"node:A");err!=nil{t.Fatal(err)}
	if err:=reservePort(used,42424,"node:B");err==nil{t.Fatal("duplicate identity-derived port was not rejected")}
}
