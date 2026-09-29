package clustersync

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

type MirrorRouteDescriptor struct {
	ID string
	RemoteRoute string
	MasterListen string
}

type MirrorRoute struct {
	NodeID string
	Address string
	ServerName string
	AllowedIdentity string
	NoisePublicKey string
	Routes []MirrorRouteDescriptor
	// RemoteRoutes is retained for caller compatibility only. Semantic topology
	// decisions must use Routes because it preserves stable RouteID.
	RemoteRoutes []string
	TransportProfile string
	Shards int
}

type Snapshot struct {
	ClusterID string
	Generation uint64
	Revision string
	Routes []MirrorRoute
}

type RouteTopologyDiff struct {
	AddedRouteIDs []string
	RemovedRouteIDs []string
	ChangedRouteIDs []string
	UnchangedRouteIDs []string
}

type TopologyDiff struct {
	AddedNodeIDs []string
	RemovedNodeIDs []string
	ChangedNodeIDs []string
	UnchangedNodeIDs []string
	Routes map[string]RouteTopologyDiff
}

type Engine struct {
	mu sync.Mutex
	workerPrivate *ecdh.PrivateKey
	signingPublic ed25519.PublicKey
	clusterID string
	current Snapshot
	hasCurrent bool
}

func NewEngine(workerPrivate *ecdh.PrivateKey, signingPublic ed25519.PublicKey, clusterID string) (*Engine, error) {
	if workerPrivate == nil { return nil, errors.New("worker private key is required") }
	if len(signingPublic) != ed25519.PublicKeySize { return nil, errors.New("signing public key is required") }
	if clusterID == "" { return nil, errors.New("cluster id is required") }
	return &Engine{workerPrivate:workerPrivate, signingPublic:append(ed25519.PublicKey(nil), signingPublic...), clusterID:clusterID}, nil
}

func (e *Engine) Apply(token string, now time.Time) (Snapshot, bool, error) {
	manifest, err := Open(token, e.workerPrivate, e.signingPublic, now)
	if err != nil { return Snapshot{}, false, err }
	if manifest.ClusterID != e.clusterID { return Snapshot{}, false, fmt.Errorf("%w: cluster id mismatch", ErrInvalidToken) }
	next := snapshotFromManifest(manifest)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.hasCurrent {
		if next.Generation < e.current.Generation { return Snapshot{}, false, ErrRollback }
		if next.Generation == e.current.Generation {
			if next.Revision != e.current.Revision {
				return Snapshot{}, false, fmt.Errorf("%w: generation reused with different revision", ErrInvalidToken)
			}
			return cloneSnapshot(e.current), false, nil
		}
	}
	e.current = cloneSnapshot(next)
	e.hasCurrent = true
	return cloneSnapshot(e.current), true, nil
}

func (e *Engine) Current() (Snapshot, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.hasCurrent { return Snapshot{}, false }
	return cloneSnapshot(e.current), true
}

func snapshotFromManifest(m Manifest) Snapshot {
	nodes := canonicalNodes(m.Nodes)
	out := Snapshot{ClusterID:m.ClusterID, Generation:m.Generation, Revision:m.Revision, Routes:make([]MirrorRoute,0,len(nodes))}
	for _, n := range nodes {
		r := MirrorRoute{
			NodeID:n.ID, Address:n.Address, ServerName:n.ServerName,
			AllowedIdentity:n.AllowedIdentity, NoisePublicKey:n.NoisePublicKey,
			TransportProfile:n.TransportProfile, Shards:n.Shards,
			Routes:make([]MirrorRouteDescriptor,0,len(n.Routes)),
			RemoteRoutes:make([]string,0,len(n.Routes)),
		}
		for _, rr := range n.Routes {
			r.Routes = append(r.Routes, MirrorRouteDescriptor{ID:rr.ID, RemoteRoute:rr.RemoteRoute, MasterListen:rr.MasterListen})
			r.RemoteRoutes = append(r.RemoteRoutes, rr.RemoteRoute)
		}
		out.Routes = append(out.Routes, r)
	}
	return out
}

func cloneSnapshot(in Snapshot) Snapshot {
	out := in
	out.Routes = make([]MirrorRoute, len(in.Routes))
	for i := range in.Routes {
		out.Routes[i] = in.Routes[i]
		out.Routes[i].Routes = append([]MirrorRouteDescriptor(nil), in.Routes[i].Routes...)
		out.Routes[i].RemoteRoutes = append([]string(nil), in.Routes[i].RemoteRoutes...)
	}
	return out
}

func DiffTopology(a, b Snapshot) TopologyDiff {
	out := TopologyDiff{Routes:make(map[string]RouteTopologyDiff)}
	am := mirrorRouteByNodeID(a.Routes)
	bm := mirrorRouteByNodeID(b.Routes)
	ids := make(map[string]struct{}, len(am)+len(bm))
	for id := range am { ids[id]=struct{}{} }
	for id := range bm { ids[id]=struct{}{} }
	ordered := make([]string,0,len(ids))
	for id := range ids { ordered=append(ordered,id) }
	sort.Strings(ordered)
	for _, id := range ordered {
		x, aok := am[id]
		y, bok := bm[id]
		switch {
		case !aok:
			out.AddedNodeIDs=append(out.AddedNodeIDs,id)
		case !bok:
			out.RemovedNodeIDs=append(out.RemovedNodeIDs,id)
		default:
			rd := diffRoutes(x.Routes,y.Routes)
			if hasRouteChanges(rd) { out.Routes[id]=rd }
			if x.Address!=y.Address || x.ServerName!=y.ServerName || x.AllowedIdentity!=y.AllowedIdentity ||
				x.NoisePublicKey!=y.NoisePublicKey || x.TransportProfile!=y.TransportProfile || x.Shards!=y.Shards ||
				hasRouteChanges(rd) {
				out.ChangedNodeIDs=append(out.ChangedNodeIDs,id)
			} else {
				out.UnchangedNodeIDs=append(out.UnchangedNodeIDs,id)
			}
		}
	}
	return out
}

func mirrorRouteByNodeID(in []MirrorRoute) map[string]MirrorRoute {
	out:=make(map[string]MirrorRoute,len(in))
	for _, r:=range in { out[r.NodeID]=r }
	return out
}

func diffRoutes(a,b []MirrorRouteDescriptor) RouteTopologyDiff {
	out:=RouteTopologyDiff{}
	am:=make(map[string]MirrorRouteDescriptor,len(a)); bm:=make(map[string]MirrorRouteDescriptor,len(b))
	for _,r:=range a{am[r.ID]=r}; for _,r:=range b{bm[r.ID]=r}
	ids:=make(map[string]struct{},len(am)+len(bm)); for id:=range am{ids[id]=struct{}{}}; for id:=range bm{ids[id]=struct{}{}}
	ordered:=make([]string,0,len(ids)); for id:=range ids{ordered=append(ordered,id)}; sort.Strings(ordered)
	for _,id:=range ordered{
		x,aok:=am[id]; y,bok:=bm[id]
		switch{
		case !aok: out.AddedRouteIDs=append(out.AddedRouteIDs,id)
		case !bok: out.RemovedRouteIDs=append(out.RemovedRouteIDs,id)
		case x.RemoteRoute!=y.RemoteRoute || x.MasterListen!=y.MasterListen: out.ChangedRouteIDs=append(out.ChangedRouteIDs,id)
		default: out.UnchangedRouteIDs=append(out.UnchangedRouteIDs,id)
		}
	}
	return out
}

func hasRouteChanges(d RouteTopologyDiff) bool {
	return len(d.AddedRouteIDs)>0 || len(d.RemovedRouteIDs)>0 || len(d.ChangedRouteIDs)>0
}

// ChangedRouteIndexes is retained for compatibility. It is derived from the
// identity-keyed diff and therefore treats pure permutation as a no-op.
func ChangedRouteIndexes(a,b Snapshot) []int {
	d:=DiffTopology(a,b)
	changed:=make(map[string]struct{},len(d.AddedNodeIDs)+len(d.RemovedNodeIDs)+len(d.ChangedNodeIDs))
	for _,id:=range d.AddedNodeIDs{changed[id]=struct{}{}}
	for _,id:=range d.RemovedNodeIDs{changed[id]=struct{}{}}
	for _,id:=range d.ChangedNodeIDs{changed[id]=struct{}{}}
	idx:=make(map[int]struct{})
	for i,r:=range b.Routes{if _,ok:=changed[r.NodeID];ok{idx[i]=struct{}{}}}
	for i,r:=range a.Routes{if _,ok:=changed[r.NodeID];ok{if _,exists:=mirrorRouteByNodeID(b.Routes)[r.NodeID];!exists{idx[i]=struct{}{}}}}
	out:=make([]int,0,len(idx));for i:=range idx{out=append(out,i)};sort.Ints(out)
	return out
}
