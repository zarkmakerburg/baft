package clustersync

import (
    "crypto/sha256"
    "encoding/binary"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"
    "net"
    "path/filepath"
    "sort"

    "github.com/zarkmakerburg/baft/internal/config"
    "github.com/zarkmakerburg/baft/internal/recordshape"
)

type WorkerTemplate struct {
    NodeID string
    NoiseKeyFile string
    TLS config.TLS
    Limits config.Limits
    Recovery config.Recovery
    RouteBasePort int
    MetricsBasePort int
    StateDir string
}

type ManagedNodeConfig struct {
    NodeID string
    Config config.Config
    Digest string
}

func (t WorkerTemplate) validate() error {
    if t.NodeID == "" { return errors.New("worker node id is required") }
    if t.NoiseKeyFile == "" { return errors.New("worker Noise key file is required") }
    if t.TLS.MinVersion != "1.3" || t.TLS.CAFile == "" || t.TLS.CertFile == "" || t.TLS.KeyFile == "" {
        return errors.New("worker TLS material is incomplete")
    }
    if t.RouteBasePort < 1024 || t.RouteBasePort > 65000 { return errors.New("route base port out of range") }
    if t.MetricsBasePort < 1024 || t.MetricsBasePort > 65000 { return errors.New("metrics base port out of range") }
    if t.StateDir == "" { return errors.New("state dir is required") }
    return nil
}

func BuildWorkerConfigs(s Snapshot, t WorkerTemplate) ([]config.Config, error) {
    managed,err:=BuildManagedWorkerConfigs(s,t)
    if err!=nil{return nil,err}
    out:=make([]config.Config,0,len(managed))
    for _,m:=range managed{out=append(out,m.Config)}
    return out,nil
}

// BuildManagedWorkerConfigs preserves the stable remote NodeID needed by the
// live runtime owner while retaining deterministic identity-derived resources.
func BuildManagedWorkerConfigs(s Snapshot, t WorkerTemplate) ([]ManagedNodeConfig, error) {
    if err := t.validate(); err != nil { return nil, err }
    if err := validateSnapshotIdentity(s); err != nil { return nil, err }
    if len(s.Routes) < MinNodes { return nil, fmt.Errorf("worker requires at least %d mirrored node", MinNodes) }

    recovery:=t.Recovery
    if recovery==(config.Recovery{}) { recovery=config.Recovery{Enabled:false,RetentionSeconds:30} }

    nodes:=append([]MirrorRoute(nil),s.Routes...)
    sort.Slice(nodes,func(i,j int)bool{return nodes[i].NodeID<nodes[j].NodeID})
    usedPorts:=map[int]string{}
    usedSockets:=map[string]string{}
    usedRouteIDs:=map[string]string{}
    out := make([]ManagedNodeConfig, 0, len(nodes))
    for _, r := range nodes {
        owner:="node:"+r.NodeID
        metricsPort,err:=identityPort(t.MetricsBasePort,"metrics",r.NodeID)
        if err!=nil{return nil,err}
        if err:=reservePort(usedPorts,metricsPort,owner+"/metrics");err!=nil{return nil,err}
        socketName:="worker-"+stableShortID("socket",r.NodeID)+".sock"
        if prev,ok:=usedSockets[socketName];ok{return nil,fmt.Errorf("unix socket identity collision: %s and %s",prev,owner)}
        usedSockets[socketName]=owner

        routeDescriptors:=append([]MirrorRouteDescriptor(nil),r.Routes...)
        sort.Slice(routeDescriptors,func(i,j int)bool{return routeDescriptors[i].ID<routeDescriptors[j].ID})
        routes := make([]config.Route, 0, len(routeDescriptors))
        for _, remote := range routeDescriptors {
            identity:=r.NodeID+"\x00"+remote.ID
            localID:="mirror-"+stableShortID("route",identity)
            if prev,ok:=usedRouteIDs[localID];ok{return nil,fmt.Errorf("local route identity collision: %s and %s",prev,identity)}
            usedRouteIDs[localID]=identity
            listenerPort,err:=identityPort(t.RouteBasePort,"listener",identity)
            if err!=nil{return nil,err}
            if err:=reservePort(usedPorts,listenerPort,owner+"/route:"+remote.ID);err!=nil{return nil,err}
            routes = append(routes, config.Route{
                ID: localID,
                Listen: net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", listenerPort)),
                RemoteRoute: remote.RemoteRoute,
                Direction: "outbound",
            })
        }

        cfg := config.Config{
            SchemaVersion: config.SchemaVersion,
            Noise: &config.Noise{KeyFile:t.NoiseKeyFile, PeerPublicKey:r.NoisePublicKey, RecordShaping:recordshape.Config{}},
            Node: config.Node{ID:t.NodeID, Role:"dialer"},
            Peer: &config.Peer{Address:r.Address, ServerName:r.ServerName, AllowedIdentity:r.AllowedIdentity},
            TLS:t.TLS,
            Transport:config.Transport{Primary:"h2", H3Enabled:false, Shards:r.Shards, Profile:r.TransportProfile},
            Limits:t.Limits,
            Recovery:recovery,
            Routes:routes,
            Management:config.Management{
                UnixSocket:filepath.Join(t.StateDir,socketName),
                MetricsListen:net.JoinHostPort("127.0.0.1",fmt.Sprintf("%d",metricsPort)),
            },
            Logging:config.Logging{Level:"info",Payload:false},
        }
        if err := config.Validate(cfg); err != nil { return nil, fmt.Errorf("worker config for NodeID %q: %w",r.NodeID,err) }
        raw,err:=json.Marshal(cfg);if err!=nil{return nil,err}
        sum:=sha256.Sum256(raw)
        out=append(out,ManagedNodeConfig{NodeID:r.NodeID,Config:cfg,Digest:hex.EncodeToString(sum[:])})
    }
    return out,nil
}

func validateSnapshotIdentity(s Snapshot) error {
    seenNodeID:=map[string]struct{}{}
    seenAddress:=map[string]struct{}{}
    seenIdentity:=map[string]struct{}{}
    for _,r:=range s.Routes{
        if r.NodeID==""||r.Address==""||r.ServerName==""||r.AllowedIdentity==""||r.NoisePublicKey==""||len(r.Routes)==0{
            return fmt.Errorf("snapshot node %q is incomplete",r.NodeID)
        }
        if r.NodeID!=r.AllowedIdentity{return fmt.Errorf("schema v1 NodeID %q must equal AllowedIdentity %q",r.NodeID,r.AllowedIdentity)}
        if _,ok:=seenNodeID[r.NodeID];ok{return fmt.Errorf("duplicate snapshot NodeID %q",r.NodeID)};seenNodeID[r.NodeID]=struct{}{}
        if _,ok:=seenAddress[r.Address];ok{return fmt.Errorf("duplicate peer address %s",r.Address)};seenAddress[r.Address]=struct{}{}
        if _,ok:=seenIdentity[r.AllowedIdentity];ok{return fmt.Errorf("duplicate AllowedIdentity %q",r.AllowedIdentity)};seenIdentity[r.AllowedIdentity]=struct{}{}
        seenRoute:=map[string]struct{}{}
        for _,rr:=range r.Routes{
            if rr.ID==""||rr.RemoteRoute==""{return fmt.Errorf("snapshot node %q has incomplete route",r.NodeID)}
            if _,ok:=seenRoute[rr.ID];ok{return fmt.Errorf("snapshot node %q has duplicate RouteID %q",r.NodeID,rr.ID)};seenRoute[rr.ID]=struct{}{}
        }
    }
    return nil
}

func stableShortID(namespace, identity string) string {
    sum:=sha256.Sum256([]byte(namespace+"\x00"+identity))
    return fmt.Sprintf("%x",sum[:6])
}

func identityPort(base int, namespace, identity string) (int,error) {
    if base<1024||base>65000{return 0,fmt.Errorf("%s base port out of range",namespace)}
    span:=65536-base
    if span<=0{return 0,fmt.Errorf("%s port range exhausted",namespace)}
    sum:=sha256.Sum256([]byte(namespace+"\x00"+identity))
    offset:=binary.BigEndian.Uint64(sum[:8])%uint64(span)
    return base+int(offset),nil
}

func reservePort(used map[int]string, port int, owner string) error {
    if prev,ok:=used[port];ok{return fmt.Errorf("identity-derived port collision on %d: %s and %s",port,prev,owner)}
    used[port]=owner
    return nil
}
