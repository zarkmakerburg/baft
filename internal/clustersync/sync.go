package clustersync

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"time"
)

type MirrorRoute struct {
	NodeID string
	Address string
	ServerName string
	AllowedIdentity string
	NoisePublicKey string
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
	out := Snapshot{ClusterID:m.ClusterID, Generation:m.Generation, Revision:m.Revision, Routes:make([]MirrorRoute,0,len(m.Nodes))}
	for _, n := range m.Nodes {
		r := MirrorRoute{
			NodeID:n.ID, Address:n.Address, ServerName:n.ServerName,
			AllowedIdentity:n.AllowedIdentity, NoisePublicKey:n.NoisePublicKey,
			TransportProfile:n.TransportProfile, Shards:n.Shards,
		}
		for _, rr := range n.Routes { r.RemoteRoutes = append(r.RemoteRoutes, rr.RemoteRoute) }
		out.Routes = append(out.Routes, r)
	}
	return out
}

func cloneSnapshot(in Snapshot) Snapshot {
	out := in
	out.Routes = make([]MirrorRoute, len(in.Routes))
	for i := range in.Routes {
		out.Routes[i] = in.Routes[i]
		out.Routes[i].RemoteRoutes = append([]string(nil), in.Routes[i].RemoteRoutes...)
	}
	return out
}
