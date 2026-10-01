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

var ErrPreparedStale = errors.New("prepared topology update is stale")

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

type PreparedUpdate struct {
    engine *Engine
    baseHadCurrent bool
    baseGeneration uint64
    baseRevision string
    next Snapshot
    diff TopologyDiff
    changed bool
}

func (p PreparedUpdate) Snapshot() Snapshot { return cloneSnapshot(p.next) }
func (p PreparedUpdate) Diff() TopologyDiff { return cloneTopologyDiff(p.diff) }
func (p PreparedUpdate) Changed() bool { return p.changed }

func NewEngine(workerPrivate *ecdh.PrivateKey, signingPublic ed25519.PublicKey, clusterID string) (*Engine, error) {
    if workerPrivate == nil { return nil, errors.New("worker private key is required") }
    if len(signingPublic) != ed25519.PublicKeySize { return nil, errors.New("signing public key is required") }
    if clusterID == "" { return nil, errors.New("cluster id is required") }
    return &Engine{workerPrivate:workerPrivate, signingPublic:append(ed25519.PublicKey(nil), signingPublic...), clusterID:clusterID}, nil
}

// PrepareToken verifies and evaluates a signed topology update without
// publishing it. Runtime reconciliation must complete before CommitPrepared.
func (e *Engine) PrepareToken(token string, now time.Time) (PreparedUpdate, error) {
    manifest, err := Open(token, e.workerPrivate, e.signingPublic, now)
    if err != nil { return PreparedUpdate{}, err }
    if manifest.ClusterID != e.clusterID { return PreparedUpdate{}, fmt.Errorf("%w: cluster id mismatch", ErrInvalidToken) }
    next := snapshotFromManifest(manifest)

    e.mu.Lock()
    defer e.mu.Unlock()

    p := PreparedUpdate{engine:e, next:cloneSnapshot(next), baseHadCurrent:e.hasCurrent}
    if e.hasCurrent {
        p.baseGeneration=e.current.Generation
        p.baseRevision=e.current.Revision
        if next.Generation < e.current.Generation { return PreparedUpdate{}, ErrRollback }
        if next.Generation == e.current.Generation {
            if next.Revision != e.current.Revision {
                return PreparedUpdate{}, fmt.Errorf("%w: generation reused with different revision", ErrInvalidToken)
            }
            p.next=cloneSnapshot(e.current)
            p.diff=DiffTopology(e.current,e.current)
            p.changed=false
            return p,nil
        }
        p.diff=DiffTopology(e.current,next)
    } else {
        p.diff=DiffTopology(Snapshot{},next)
    }
    p.changed=true
    return p,nil
}

// CommitPrepared publishes a previously verified update only if the committed
// base has not changed since PrepareToken. No runtime work belongs here.
func (e *Engine) CommitPrepared(p PreparedUpdate) (Snapshot, bool, error) {
    if p.engine != e { return Snapshot{}, false, ErrPreparedStale }
    e.mu.Lock()
    defer e.mu.Unlock()

    if e.hasCurrent != p.baseHadCurrent { return Snapshot{}, false, ErrPreparedStale }
    if p.baseHadCurrent && (e.current.Generation != p.baseGeneration || e.current.Revision != p.baseRevision) {
        return Snapshot{}, false, ErrPreparedStale
    }
    if !p.changed {
        if !e.hasCurrent { return Snapshot{}, false, ErrPreparedStale }
        return cloneSnapshot(e.current),false,nil
    }
    e.current=cloneSnapshot(p.next)
    e.hasCurrent=true
    return cloneSnapshot(e.current),true,nil
}

// Apply preserves the legacy control-only API. Production live reconciliation
// uses PrepareToken -> runtime reconcile -> CommitPrepared.
func (e *Engine) Apply(token string, now time.Time) (Snapshot, bool, error) {
    p,err:=e.PrepareToken(token,now)
    if err!=nil{return Snapshot{},false,err}
    return e.CommitPrepared(p)
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

func cloneTopologyDiff(in TopologyDiff) TopologyDiff {
    out:=TopologyDiff{
        AddedNodeIDs:append([]string(nil),in.AddedNodeIDs...),
        RemovedNodeIDs:append([]string(nil),in.RemovedNodeIDs...),
        ChangedNodeIDs:append([]string(nil),in.ChangedNodeIDs...),
        UnchangedNodeIDs:append([]string(nil),in.UnchangedNodeIDs...),
        Routes:make(map[string]RouteTopologyDiff,len(in.Routes)),
    }
    for id,d:=range in.Routes{
        out.Routes[id]=RouteTopologyDiff{
            AddedRouteIDs:append([]string(nil),d.AddedRouteIDs...),
            RemovedRouteIDs:append([]string(nil),d.RemovedRouteIDs...),
            ChangedRouteIDs:append([]string(nil),d.ChangedRouteIDs...),
            UnchangedRouteIDs:append([]string(nil),d.UnchangedRouteIDs...),
        }
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
    bm:=mirrorRouteByNodeID(b.Routes)
    for i,r:=range a.Routes{if _,ok:=changed[r.NodeID];ok{if _,exists:=bm[r.NodeID];!exists{idx[i]=struct{}{}}}}
    out:=make([]int,0,len(idx));for i:=range idx{out=append(out,i)};sort.Ints(out)
    return out
}
