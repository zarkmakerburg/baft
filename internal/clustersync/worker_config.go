package clustersync

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/recordshape"
)

// WorkerTemplate contains only local Worker material. Secrets are deliberately
// not carried by the cluster token.
type WorkerTemplate struct {
	NodeID string
	NoiseKeyFile string
	TLS config.TLS
	Limits config.Limits
	RouteBasePort int
	MetricsBasePort int
	StateDir string
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

// BuildWorkerConfigs converts a verified Snapshot into six dialer configs.
// Record shaping is intentionally disabled here: the sync layer mirrors
// connectivity identity and routes without distributing traffic-obfuscation
// policy or Master secrets.
func BuildWorkerConfigs(s Snapshot, t WorkerTemplate) ([]config.Config, error) {
	if err := t.validate(); err != nil { return nil, err }
	if len(s.Routes) != RequiredNodes { return nil, fmt.Errorf("worker requires exactly %d mirrored nodes", RequiredNodes) }

	out := make([]config.Config, 0, RequiredNodes)
	nextRoutePort := t.RouteBasePort
	seenAddr := map[string]struct{}{}
	for i, r := range s.Routes {
		if r.Address == "" || r.ServerName == "" || r.AllowedIdentity == "" || r.NoisePublicKey == "" || len(r.RemoteRoutes) == 0 {
			return nil, fmt.Errorf("route %d is incomplete", i+1)
		}
		if _, ok := seenAddr[r.Address]; ok { return nil, fmt.Errorf("duplicate peer address %s", r.Address) }
		seenAddr[r.Address] = struct{}{}

		routes := make([]config.Route, 0, len(r.RemoteRoutes))
		for j, remote := range r.RemoteRoutes {
			if remote == "" { return nil, fmt.Errorf("route %d/%d remote route is empty", i+1, j+1) }
			if nextRoutePort > 65535 { return nil, errors.New("route listener port range exhausted") }
			routes = append(routes, config.Route{
				ID: fmt.Sprintf("mirror-%02d-%02d", i+1, j+1),
				Listen: net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", nextRoutePort)),
				RemoteRoute: remote,
				Direction: "outbound",
			})
			nextRoutePort++
		}
		metricsPort := t.MetricsBasePort + i
		if metricsPort > 65535 { return nil, errors.New("metrics port range exhausted") }

		cfg := config.Config{
			SchemaVersion: config.SchemaVersion,
			Noise: &config.Noise{
				KeyFile: t.NoiseKeyFile,
				PeerPublicKey: r.NoisePublicKey,
				RecordShaping: recordshape.Config{},
			},
			Node: config.Node{ID:t.NodeID, Role:"dialer"},
			Peer: &config.Peer{
				Address:r.Address,
				ServerName:r.ServerName,
				AllowedIdentity:r.AllowedIdentity,
			},
			TLS:t.TLS,
			Transport:config.Transport{
				Primary:"h2",
				H3Enabled:false,
				Shards:r.Shards,
				Profile:r.TransportProfile,
			},
			Limits:t.Limits,
			Recovery:config.Recovery{Enabled:false,RetentionSeconds:30},
			Routes:routes,
			Management:config.Management{
				UnixSocket:filepath.Join(t.StateDir,fmt.Sprintf("worker-%02d.sock",i+1)),
				MetricsListen:net.JoinHostPort("127.0.0.1",fmt.Sprintf("%d",metricsPort)),
			},
			Logging:config.Logging{Level:"info",Payload:false},
		}
		if err := config.Validate(cfg); err != nil { return nil, fmt.Errorf("worker config %d: %w",i+1,err) }
		out=append(out,cfg)
	}
	return out,nil
}

func ChangedRouteIndexes(a,b Snapshot) []int {
	max:=len(a.Routes); if len(b.Routes)>max { max=len(b.Routes) }
	out:=make([]int,0)
	for i:=0;i<max;i++ {
		if i>=len(a.Routes)||i>=len(b.Routes) { out=append(out,i); continue }
		x,y:=a.Routes[i],b.Routes[i]
		if x.NodeID!=y.NodeID||x.Address!=y.Address||x.ServerName!=y.ServerName||
			x.AllowedIdentity!=y.AllowedIdentity||x.NoisePublicKey!=y.NoisePublicKey||
			x.TransportProfile!=y.TransportProfile||x.Shards!=y.Shards||
			!sameStrings(x.RemoteRoutes,y.RemoteRoutes) {
			out=append(out,i)
		}
	}
	return out
}

func sameStrings(a,b []string) bool {
	if len(a)!=len(b){return false}
	for i:=range a { if a[i]!=b[i]{return false} }
	return true
}
