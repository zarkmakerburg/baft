package node

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	carrierh2 "github.com/zarkmakerburg/baft/internal/carrier/h2"
	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/identity"
	baftmetrics "github.com/zarkmakerburg/baft/internal/metrics"
	"github.com/zarkmakerburg/baft/internal/resources"
	"github.com/zarkmakerburg/baft/internal/routes"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
	"github.com/zarkmakerburg/baft/internal/session"
)

type Runtime struct {
	Revocations *identity.RevocationSet
	Resources   *resources.Allocator
	peerMu      sync.Mutex
	peers       map[*session.Peer]struct{}
}

func NewRuntime() *Runtime {
	return &Runtime{Revocations: identity.NewRevocationSet(), peers: map[*session.Peer]struct{}{}}
}

func (r *Runtime) Run(ctx context.Context, cfg config.Config) error {
	if err := config.Validate(cfg); err != nil {
		return err
	}
	if err := requirePrivateKeyPermissions(cfg.TLS.KeyFile); err != nil {
		return err
	}
	if r.Resources == nil {
		a, err := allocatorFromConfig(cfg)
		if err != nil {
			return err
		}
		r.Resources = a
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	metricsDone, stopMetrics, err := r.startMetrics(runCtx, cfg.Management.MetricsListen)
	if err != nil {
		return err
	}
	defer stopMetrics()

	roleDone := make(chan error, 1)
	go func() {
		switch cfg.Node.Role {
		case "listener":
			roleDone <- r.runListener(runCtx, cfg)
		case "dialer":
			roleDone <- r.runDialer(runCtx, cfg)
		default:
			roleDone <- fmt.Errorf("unsupported node role %q", cfg.Node.Role)
		}
	}()

	select {
	case err := <-roleDone:
		cancel()
		return err
	case err := <-metricsDone:
		cancel()
		if err == nil {
			err = errors.New("metrics server stopped unexpectedly")
		}
		return fmt.Errorf("metrics: %w", err)
	case <-ctx.Done():
		cancel()
		<-roleDone
		return nil
	}
}

func allocatorFromConfig(cfg config.Config) (*resources.Allocator, error) {
	total := int64(cfg.Limits.DataMemoryMiB) * resources.MiB
	receive := total / 2
	replay := total - receive
	return resources.NewAllocator(resources.Limits{
		Total:          total,
		Receive:        receive,
		Replay:         replay,
		PerFlowReceive: int64(cfg.Limits.ReceiveMaxMiB) * resources.MiB,
		PerFlowReplay:  int64(cfg.Limits.ReplayMaxMiB) * resources.MiB,
	})
}

func requirePrivateKeyPermissions(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("private key: %w", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("private key must not be readable or writable by group/other")
	}
	return nil
}

func loadTLSMaterial(c config.TLS) (*identityMaterial, error) {
	ca, err := identity.LoadCertPool(c.CAFile)
	if err != nil {
		return nil, fmt.Errorf("load CA: %w", err)
	}
	cert, err := identity.LoadKeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load certificate/key: %w", err)
	}
	return &identityMaterial{ca: ca, cert: cert}, nil
}

type identityMaterial struct {
	ca   *x509.CertPool
	cert tls.Certificate
}

func (r *Runtime) runListener(ctx context.Context, cfg config.Config) error {
	mat, err := loadTLSMaterial(cfg.TLS)
	if err != nil {
		return err
	}
	allowed := make(map[string]struct{}, len(cfg.Server.AllowedPeerIdentities))
	for _, id := range cfg.Server.AllowedPeerIdentities {
		allowed[id] = struct{}{}
	}
	tlsCfg, err := identity.ServerTLS(mat.ca, mat.cert, allowed)
	if err != nil {
		return err
	}

	var defs []routes.Route
	for _, cr := range cfg.Routes {
		if cr.Direction != "inbound" {
			continue
		}
		peers := make(map[string]struct{}, len(cr.AllowedPeers))
		for _, id := range cr.AllowedPeers {
			peers[id] = struct{}{}
		}
		defs = append(defs, routes.Route{ID: cr.ID, Target: cr.Target, AllowedPeers: peers})
	}
	if len(defs) == 0 {
		return errors.New("listener requires at least one inbound route")
	}
	table, err := routes.New(defs)
	if err != nil {
		return err
	}

	stream := func(hctx context.Context, in io.Reader, out io.Writer, peer carrierh2.PeerInfo) error {
		expected, err := nodeIDFromIdentity(peer.Identity)
		if err != nil {
			return err
		}
		p, err := session.New(session.Listener, session.Carrier{In: in, Out: out}, peer.Identity, table, session.Options{
			NodeID: cfg.Node.ID, ExpectedPeerNodeID: expected,
			ProfileID: cfg.Transport.Profile, ProfileVersion: 1, ConfigRevision: "config-v1",
			Resources: r.Resources,
		})
		if err != nil {
			return err
		}
		r.registerPeer(p)
		defer r.unregisterPeer(p)
		return p.Run(hctx)
	}
	var handler http.Handler
	if cfg.Noise != nil {
		nc, err := noiseConfig(cfg)
		if err != nil {
			return err
		}
		cover, err := coverHandler(cfg.Noise.CoverHTMLFile)
		if err != nil {
			return err
		}
		handler, err = carrierh2.HandlerWithNoise(stream, carrierh2.NoiseOptions{Handshake: nc, PeerIdentity: cfg.Server.AllowedPeerIdentities[0], Cover: cover, Revocations: r.Revocations})
		if err != nil {
			return err
		}
		// Public website TLS; Noise authenticates the pinned peer before session.New.
		tlsCfg.ClientAuth = tls.NoClientCert
		tlsCfg.ClientCAs = nil
		tlsCfg.VerifyConnection = nil
		tlsCfg.NextProtos = []string{"h2", "http/1.1"}
	} else {
		handler = carrierh2.HandlerWithRevocation(stream, r.Revocations)
	}

	srv := &http.Server{Handler: handler, TLSConfig: tlsCfg, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10}
	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.Server.Listen, err)
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		err := srv.ServeTLS(ln, "", "")
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-done:
		return err
	}
}

type dialerShard struct {
	peer   *session.Peer
	client *carrierh2.Client
	pw     *io.PipeWriter
	body   io.ReadCloser
}

func (s *dialerShard) close() {
	_ = s.pw.Close()
	_ = s.body.Close()
	s.client.CloseIdleConnections()
}

func (r *Runtime) runDialer(ctx context.Context, cfg config.Config) error {
	mat, err := loadTLSMaterial(cfg.TLS)
	if err != nil {
		return err
	}
	tlsCfg, err := identity.ClientTLS(mat.ca, mat.cert, cfg.Peer.ServerName)
	if err != nil {
		return err
	}
	if cfg.Noise != nil {
		tlsCfg.Certificates = nil
	}
	expectedPeerNode, err := nodeIDFromIdentity(cfg.Peer.AllowedIdentity)
	if err != nil {
		return err
	}

	shards := make([]*dialerShard, 0, cfg.Transport.Shards)
	runErr := make(chan error, cfg.Transport.Shards+len(cfg.Routes)+1)
	for i := 0; i < cfg.Transport.Shards; i++ {
		client, err := carrierh2.NewClient("https://"+cfg.Peer.Address, tlsCfg)
		if err != nil {
			r.closeShards(shards)
			return err
		}
		var pw *io.PipeWriter
		var resp *http.Response
		var secure *securityinternal.Conn
		if cfg.Noise != nil {
			nc, e := noiseConfig(cfg)
			if e != nil {
				r.closeShards(shards)
				client.CloseIdleConnections()
				return e
			}
			resp, pw, secure, err = client.OpenNoise(ctx, nc)
		} else {
			pr, writer := io.Pipe()
			pw = writer
			resp, err = client.Open(ctx, pr)
		}
		if err != nil {
			if pw != nil {
				_ = pw.Close()
			}
			client.CloseIdleConnections()
			r.closeShards(shards)
			return fmt.Errorf("open shard %d: %w", i, err)
		}
		carrier := session.Carrier{In: resp.Body, Out: pw}
		if secure != nil {
			carrier = session.Carrier{In: secure, Out: secure}
		}
		p, err := session.New(session.Dialer, carrier, cfg.Peer.AllowedIdentity, nil, session.Options{
			NodeID: cfg.Node.ID, ExpectedPeerNodeID: expectedPeerNode, ShardID: uint8(i),
			ProfileID: cfg.Transport.Profile, ProfileVersion: 1, ConfigRevision: "config-v1",
			Resources: r.Resources,
		})
		if err != nil {
			_ = pw.Close()
			_ = resp.Body.Close()
			client.CloseIdleConnections()
			r.closeShards(shards)
			return err
		}
		sh := &dialerShard{peer: p, client: client, pw: pw, body: resp.Body}
		r.registerPeer(p)
		shards = append(shards, sh)
		go func(index int, sh *dialerShard) {
			err := sh.peer.Run(ctx)
			if ctx.Err() == nil && err != nil {
				runErr <- fmt.Errorf("shard %d: %w", index, err)
			}
		}(i, sh)
	}
	defer r.closeShards(shards)

	var listeners []net.Listener
	defer func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}()

	var rr atomic.Uint64
	var wg sync.WaitGroup
	for _, cr := range cfg.Routes {
		if cr.Direction != "outbound" {
			continue
		}
		ln, err := net.Listen("tcp", cr.Listen)
		if err != nil {
			return fmt.Errorf("route %s listen: %w", cr.ID, err)
		}
		listeners = append(listeners, ln)
		route := cr
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				conn, err := ln.Accept()
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					runErr <- fmt.Errorf("route %s accept: %w", route.ID, err)
					return
				}
				idx := int((rr.Add(1) - 1) % uint64(len(shards)))
				go func(c net.Conn, sh *dialerShard) {
					if err := sh.peer.OpenFlow(ctx, route.RemoteRoute, c); err != nil {
						_ = c.Close()
					}
				}(conn, shards[idx])
			}
		}()
	}
	if len(listeners) == 0 {
		return errors.New("dialer requires at least one outbound route")
	}

	select {
	case <-ctx.Done():
		for _, ln := range listeners {
			_ = ln.Close()
		}
		wg.Wait()
		return nil
	case err := <-runErr:
		for _, ln := range listeners {
			_ = ln.Close()
		}
		wg.Wait()
		return err
	}
}

func (r *Runtime) closeShards(shards []*dialerShard) {
	for _, sh := range shards {
		r.unregisterPeer(sh.peer)
		sh.close()
	}
}

func (r *Runtime) registerPeer(p *session.Peer) {
	if p == nil {
		return
	}
	r.peerMu.Lock()
	if r.peers == nil {
		r.peers = map[*session.Peer]struct{}{}
	}
	r.peers[p] = struct{}{}
	r.peerMu.Unlock()
}

func (r *Runtime) unregisterPeer(p *session.Peer) {
	if p == nil {
		return
	}
	r.peerMu.Lock()
	delete(r.peers, p)
	r.peerMu.Unlock()
}

func (r *Runtime) metricsSnapshot() baftmetrics.Snapshot {
	r.peerMu.Lock()
	peers := make([]*session.Peer, 0, len(r.peers))
	for p := range r.peers {
		peers = append(peers, p)
	}
	r.peerMu.Unlock()

	out := baftmetrics.Snapshot{}
	if r.Resources != nil {
		rs := r.Resources.Snapshot()
		out.ReceiveUsedBytes = rs.ReceiveUsed
		out.ReplayUsedBytes = rs.ReplayUsed
		out.TotalUsedBytes = rs.TotalUsed
	}
	for _, p := range peers {
		s := p.ConservationSnapshot()
		out.ActiveFlows += len(s.Flows)
		out.InvariantViolations += s.Violations
		for _, f := range s.Flows {
			if f.Accepted >= f.Delivered {
				out.AcceptedBacklogBytes += f.Accepted - f.Delivered
			}
			if f.Credit >= f.Delivered {
				out.CreditExposureBytes += f.Credit - f.Delivered
			}
			out.ReplayOutstandingBytes += f.ReplayOutstanding
		}
	}
	return out
}

func nodeIDFromIdentity(identity string) (string, error) {
	const prefix = "urn:baft:node:"
	if !strings.HasPrefix(identity, prefix) {
		return "", fmt.Errorf("unsupported peer identity format")
	}
	id := strings.TrimPrefix(identity, prefix)
	if id == "" || len(id) > 64 {
		return "", fmt.Errorf("invalid peer node identity")
	}
	return id, nil
}
