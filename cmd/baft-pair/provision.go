package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

const identityPrefix = "urn:baft:node:"

// exPending is what ex-code leaves for ex-accept: everything the EX needs to
// verify the IR's reply. It holds the one-time PSK and is deleted on accept.
type exPending struct {
	Version       int    `json:"version"`
	Identity      string `json:"identity"`
	ServerName    string `json:"server_name"`
	OneTimePSK    string `json:"one_time_psk"`
	ExpiresUnix   int64  `json:"expires_unix"`
	RecordShaping bool   `json:"record_shaping"`
}

// commonFlags are the parts of baft.yaml both roles share.
type commonFlags struct {
	configOut     *string
	routeID       *string
	metricsListen *string
	unixSocket    *string
	shards        *int
	transport     *string
	fallback      *string
	utls          *bool
}

func addCommonFlags(fs *flag.FlagSet, defaultSocket string) commonFlags {
	return commonFlags{
		configOut:     fs.String("config-out", "", "write a runnable baft.yaml here"),
		routeID:       fs.String("route-id", "service-main", "route id shared by both peers"),
		metricsListen: fs.String("metrics-listen", "127.0.0.1:9191", "loopback metrics listener"),
		unixSocket:    fs.String("unix-socket", defaultSocket, "management socket path (must be writable by the service)"),
		shards:        fs.Int("shards", 4, "carrier shards (dialer)"),
		transport:     fs.String("transport", "h2", "primary carrier transport: h2 (default) or ws"),
		fallback:      fs.String("fallback-transport", "", "optional same-process recovery fallback carrier (h2 or ws; must differ from primary)"),
		utls:          fs.Bool("utls", false, "present a browser-fidelity TLS ClientHello on ws carrier dials"),
	}
}

// baseConfig holds the defaults of configs/example-*.yaml.
func baseConfig(nodeID, role string, c commonFlags) config.Config {
	return config.Config{
		SchemaVersion: config.SchemaVersion,
		Node:          config.Node{ID: nodeID, Role: role},
		TLS:           config.TLS{MinVersion: "1.3"},
		Transport:     config.Transport{Primary: transportOrDefault(c.transport), Fallback: optionalString(c.fallback), Shards: *c.shards, Profile: "secure-fast", UTLS: c.utls != nil && *c.utls},
		Limits:        config.Limits{MaxFlows: 256, DataMemoryMiB: 256, ReceiveInitialKiB: 64, ReceiveMaxMiB: 16, ReplayMaxMiB: 16},
		Recovery:      config.Recovery{Enabled: optionalString(c.fallback) != "", Mode: func() string { if optionalString(c.fallback) != "" { return "same_process" }; return "" }(), RetentionSeconds: 30},
		Management:    config.Management{UnixSocket: *c.unixSocket, MetricsListen: *c.metricsListen},
		Logging:       config.Logging{Level: "info"},
	}
}

func transportOrDefault(t *string) string {
	if t == nil || *t == "" {
		return "h2"
	}
	return *t
}

func optionalString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func shaping(enabled bool) recordshape.Config {
	if enabled {
		return recordshape.DefaultConfig(true)
	}
	return recordshape.Config{}
}

// writeConfig validates cfg through the same decoder `baft run` uses before
// it touches the destination, so a written file always starts.
func writeConfig(path string, cfg config.Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if _, err := config.DecodeJSON(bytes.NewReader(b)); err != nil {
		return fmt.Errorf("generated config is invalid: %w", err)
	}
	return atomicWrite(path, append(b, '\n'), 0o640)
}

func nodeIDOf(identity string) (string, error) {
	id := strings.TrimPrefix(identity, identityPrefix)
	if id == identity || id == "" || len(id) > 64 {
		return "", fmt.Errorf("identity %q is not %s<node-id>", identity, identityPrefix)
	}
	return id, nil
}

func randomNodeID(prefix string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		die(err.Error())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

func writePending(path, identity, serverName string, psk []byte, expires int64, recordShaping bool) error {
	b, err := json.MarshalIndent(exPending{
		Version: 1, Identity: identity, ServerName: serverName,
		OneTimePSK: base64.RawURLEncoding.EncodeToString(psk), ExpiresUnix: expires, RecordShaping: recordShaping,
	}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(b, '\n'), 0o600)
}

func readPending(path string) (exPending, []byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return exPending{}, nil, err
	}
	var p exPending
	if err := json.Unmarshal(b, &p); err != nil {
		return exPending{}, nil, err
	}
	psk, err := base64.RawURLEncoding.DecodeString(p.OneTimePSK)
	if err != nil || len(psk) != 32 || p.Version != 1 || p.Identity == "" || p.ServerName == "" {
		return exPending{}, nil, errors.New("invalid pairing state")
	}
	return p, psk, nil
}

// irConfig builds the IR (dialer) side from a decoded pairing code. The IR
// presents no outer client certificate: Noise pins both static keys.
func irConfig(d securityinternal.PairingDescriptor, identity, caFile, keyFile, routeListen string, c commonFlags) (config.Config, error) {
	nodeID, err := nodeIDOf(identity)
	if err != nil {
		return config.Config{}, err
	}
	cfg := baseConfig(nodeID, "dialer", c)
	cfg.Peer = &config.Peer{Address: d.Address, ServerName: d.ServerName, AllowedIdentity: d.NodeIdentity}
	cfg.TLS.CAFile = caFile
	cfg.Noise = &config.Noise{KeyFile: keyFile, PeerPublicKey: d.ResponderKey, RecordShaping: shaping(d.RecordShaping)}
	cfg.Routes = []config.Route{{ID: *c.routeID, Listen: routeListen, RemoteRoute: *c.routeID, Direction: "outbound", TrafficClass: "interactive"}}
	return cfg, nil
}

// exConfig builds the EX (listener) side once the IR's reply is verified.
func exConfig(p exPending, irIdentity string, irPub []byte, listen, caFile, certFile, keyFile, noiseKey, target string, c commonFlags) (config.Config, error) {
	nodeID, err := nodeIDOf(p.Identity)
	if err != nil {
		return config.Config{}, err
	}
	if _, err := nodeIDOf(irIdentity); err != nil {
		return config.Config{}, err
	}
	pub, err := securityinternal.EncodePublicKey(irPub)
	if err != nil {
		return config.Config{}, err
	}
	cfg := baseConfig(nodeID, "listener", c)
	cfg.Server = &config.Server{Listen: listen, ServerName: p.ServerName, AllowedPeerIdentities: []string{irIdentity}}
	cfg.TLS.CAFile, cfg.TLS.CertFile, cfg.TLS.KeyFile = caFile, certFile, keyFile
	cfg.Noise = &config.Noise{KeyFile: noiseKey, PeerPublicKey: pub, RecordShaping: shaping(p.RecordShaping)}
	cfg.Routes = []config.Route{{ID: *c.routeID, Direction: "inbound", Target: target, AllowedPeers: []string{irIdentity}}}
	return cfg, nil
}

func exAccept(args []string) {
	fs := flag.NewFlagSet("ex-accept", flag.ExitOnError)
	reply := fs.String("reply", "", "BAFTREPLY1 code printed by the IR")
	pendingPath := fs.String("pending", "", "pairing state written by ex-code --pending-out")
	pskPath := fs.String("psk-file", "", "one-time PSK file from ex-code --psk-out (removed on success)")
	keyPath := fs.String("key", "", "responder Noise key file")
	listen := fs.String("listen", "0.0.0.0:8443", "public listen address")
	caFile := fs.String("ca-file", "", "outer TLS CA PEM")
	certFile := fs.String("cert-file", "", "outer TLS server certificate")
	certKey := fs.String("cert-key-file", "", "outer TLS server private key")
	target := fs.String("target", "127.0.0.1:2443", "fixed IP:port the route exits to")
	c := addCommonFlags(fs, "/var/lib/baft/admin.sock")
	_ = fs.Parse(args)
	if *reply == "" || *pendingPath == "" || *keyPath == "" || *caFile == "" || *certFile == "" || *certKey == "" || *c.configOut == "" {
		die("missing required flag")
	}
	p, psk, err := readPending(*pendingPath)
	if err != nil {
		die(err.Error())
	}
	k, err := securityinternal.LoadKeyPair(*keyPath)
	if err != nil {
		die(err.Error())
	}
	irIdentity, irPub, err := securityinternal.DecodePairingReply(*reply, psk, k.Public, time.Now())
	if err != nil {
		die(err.Error())
	}
	cfg, err := exConfig(p, irIdentity, irPub, *listen, *caFile, *certFile, *certKey, *keyPath, *target, c)
	if err != nil {
		die(err.Error())
	}
	if err := writeConfig(*c.configOut, cfg); err != nil {
		die(err.Error())
	}
	// The PSK is one-time: once the reply is accepted it has no further use.
	_ = os.Remove(*pendingPath)
	if *pskPath != "" {
		_ = os.Remove(*pskPath)
	}
	fmt.Println(irIdentity)
}
