package clustersync

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
)

const (
	TokenPrefix = "BAFT1."
	TokenVersion = 1
	MinNodes = 1
)

var (
	ErrInvalidToken = errors.New("invalid cluster token")
	ErrExpiredToken = errors.New("cluster token expired")
	ErrSignature = errors.New("cluster token signature invalid")
	ErrRollback = errors.New("cluster token generation rollback")
)

type NodeDescriptor struct {
	ID string
	Address string
	ServerName string
	AllowedIdentity string
	NoisePublicKey string
	TransportProfile string
	Shards int
	Routes []RouteDescriptor
}

type RouteDescriptor struct {
	ID string
	RemoteRoute string
	MasterListen string
}

type Manifest struct {
	Version int
	ClusterID string
	Generation uint64
	IssuedAt int64
	ExpiresAt int64
	Revision string
	Nodes []NodeDescriptor
}

type envelope struct {
	Version int
	EphemeralPublic string
	Nonce string
	Ciphertext string
	Signature string
}

type unsignedEnvelope struct {
	Version int
	EphemeralPublic string
	Nonce string
	Ciphertext string
}

func ManifestFromConfigs(clusterID string, generation uint64, ttl time.Duration, now time.Time, cfgs []config.Config) (Manifest, error) {
	if strings.TrimSpace(clusterID) == "" { return Manifest{}, errors.New("cluster id is required") }
	if generation == 0 { return Manifest{}, errors.New("generation must be positive") }
	if ttl <= 0 || ttl > 24*time.Hour { return Manifest{}, errors.New("ttl must be >0 and <=24h") }
	if len(cfgs) < MinNodes { return Manifest{}, fmt.Errorf("manifest requires at least %d node", MinNodes) }

	nodes := make([]NodeDescriptor, 0, len(cfgs))
	for i, cfg := range cfgs {
		if err := config.Validate(cfg); err != nil { return Manifest{}, fmt.Errorf("config %d: %w", i+1, err) }
		if cfg.Node.Role != "dialer" || cfg.Peer == nil || cfg.Noise == nil { return Manifest{}, fmt.Errorf("config %d is not a Noise dialer", i+1) }
		d := NodeDescriptor{
			// Compatibility: existing configs do not carry a separate remote NodeID.
			// Keep deriving it here, but all downstream semantics use this explicit ID.
			ID: cfg.Peer.AllowedIdentity,
			Address: cfg.Peer.Address,
			ServerName: cfg.Peer.ServerName,
			AllowedIdentity: cfg.Peer.AllowedIdentity,
			NoisePublicKey: cfg.Noise.PeerPublicKey,
			TransportProfile: cfg.Transport.Profile,
			Shards: cfg.Transport.Shards,
		}
		for _, r := range cfg.Routes {
			if r.Direction == "outbound" {
				d.Routes = append(d.Routes, RouteDescriptor{ID:r.ID, RemoteRoute:r.RemoteRoute, MasterListen:r.Listen})
			}
		}
		if len(d.Routes) == 0 { return Manifest{}, fmt.Errorf("config %d has no outbound route", i+1) }
		nodes = append(nodes, d)
	}
	if err := validateNodeDescriptors(nodes); err != nil { return Manifest{}, err }
	revision, err := semanticRevision(nodes)
	if err != nil { return Manifest{}, err }
	return Manifest{
		Version: TokenVersion,
		ClusterID: clusterID,
		Generation: generation,
		IssuedAt: now.Unix(),
		ExpiresAt: now.Add(ttl).Unix(),
		Revision: revision,
		Nodes: nodes,
	}, nil
}

func Seal(manifest Manifest, workerPublic *ecdh.PublicKey, signingPrivate ed25519.PrivateKey) (string, error) {
	if workerPublic == nil { return "", errors.New("worker public key is required") }
	if len(signingPrivate) != ed25519.PrivateKeySize { return "", errors.New("invalid signing private key") }
	plain, err := json.Marshal(manifest)
	if err != nil { return "", err }

	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil { return "", err }
	shared, err := ephemeral.ECDH(workerPublic)
	if err != nil { return "", err }
	key := deriveKey(shared, ephemeral.PublicKey().Bytes(), workerPublic.Bytes())

	block, err := aes.NewCipher(key)
	if err != nil { return "", err }
	aead, err := cipher.NewGCM(block)
	if err != nil { return "", err }
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil { return "", err }
	ct := aead.Seal(nil, nonce, plain, []byte("BAFT_CLUSTER_TOKEN_V1"))

	u := unsignedEnvelope{
		Version: TokenVersion,
		EphemeralPublic: base64.RawURLEncoding.EncodeToString(ephemeral.PublicKey().Bytes()),
		Nonce: base64.RawURLEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(ct),
	}
	signBytes, _ := json.Marshal(u)
	env := envelope{
		Version:u.Version, EphemeralPublic:u.EphemeralPublic, Nonce:u.Nonce, Ciphertext:u.Ciphertext,
		Signature:base64.RawURLEncoding.EncodeToString(ed25519.Sign(signingPrivate, signBytes)),
	}
	raw, err := json.Marshal(env)
	if err != nil { return "", err }
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func Open(token string, workerPrivate *ecdh.PrivateKey, signingPublic ed25519.PublicKey, now time.Time) (Manifest, error) {
	if workerPrivate == nil { return Manifest{}, errors.New("worker private key is required") }
	if len(signingPublic) != ed25519.PublicKeySize { return Manifest{}, errors.New("invalid signing public key") }
	if !strings.HasPrefix(token, TokenPrefix) { return Manifest{}, ErrInvalidToken }

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, TokenPrefix))
	if err != nil { return Manifest{}, fmt.Errorf("%w: envelope encoding", ErrInvalidToken) }
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Version != TokenVersion { return Manifest{}, ErrInvalidToken }

	u := unsignedEnvelope{Version:env.Version, EphemeralPublic:env.EphemeralPublic, Nonce:env.Nonce, Ciphertext:env.Ciphertext}
	signBytes, _ := json.Marshal(u)
	sig, err := base64.RawURLEncoding.DecodeString(env.Signature)
	if err != nil || !ed25519.Verify(signingPublic, signBytes, sig) { return Manifest{}, ErrSignature }

	ephBytes, err := base64.RawURLEncoding.DecodeString(env.EphemeralPublic)
	if err != nil { return Manifest{}, ErrInvalidToken }
	eph, err := ecdh.X25519().NewPublicKey(ephBytes)
	if err != nil { return Manifest{}, ErrInvalidToken }
	shared, err := workerPrivate.ECDH(eph)
	if err != nil { return Manifest{}, ErrInvalidToken }
	key := deriveKey(shared, eph.Bytes(), workerPrivate.PublicKey().Bytes())

	block, err := aes.NewCipher(key)
	if err != nil { return Manifest{}, err }
	aead, err := cipher.NewGCM(block)
	if err != nil { return Manifest{}, err }
	nonce, err := base64.RawURLEncoding.DecodeString(env.Nonce)
	if err != nil { return Manifest{}, ErrInvalidToken }
	ct, err := base64.RawURLEncoding.DecodeString(env.Ciphertext)
	if err != nil { return Manifest{}, ErrInvalidToken }
	plain, err := aead.Open(nil, nonce, ct, []byte("BAFT_CLUSTER_TOKEN_V1"))
	if err != nil { return Manifest{}, fmt.Errorf("%w: decrypt", ErrInvalidToken) }

	var manifest Manifest
	if err := json.Unmarshal(plain, &manifest); err != nil { return Manifest{}, ErrInvalidToken }
	if err := validateManifest(manifest, now); err != nil { return Manifest{}, err }
	return manifest, nil
}

func validateManifest(m Manifest, now time.Time) error {
	if m.Version != TokenVersion || m.ClusterID == "" || m.Generation == 0 || len(m.Nodes) < MinNodes { return ErrInvalidToken }
	if m.ExpiresAt <= m.IssuedAt || now.Unix() > m.ExpiresAt { return ErrExpiredToken }
	if err := validateNodeDescriptors(m.Nodes); err != nil { return ErrInvalidToken }
	revision, err := semanticRevision(m.Nodes)
	if err != nil || m.Revision != revision { return ErrInvalidToken }
	return nil
}

func validateNodeDescriptors(nodes []NodeDescriptor) error {
	if len(nodes) < MinNodes { return fmt.Errorf("manifest requires at least %d node", MinNodes) }
	seenNodeID := make(map[string]struct{}, len(nodes))
	seenAddress := make(map[string]struct{}, len(nodes))
	seenIdentity := make(map[string]struct{}, len(nodes))
	for i, n := range nodes {
		if strings.TrimSpace(n.ID) == "" { return fmt.Errorf("node %d has empty NodeID", i+1) }
		if n.Address == "" || n.ServerName == "" || n.NoisePublicKey == "" || n.AllowedIdentity == "" || len(n.Routes) == 0 {
			return fmt.Errorf("node %q is incomplete", n.ID)
		}
		if _, ok := seenNodeID[n.ID]; ok { return fmt.Errorf("duplicate NodeID %q", n.ID) }
		seenNodeID[n.ID] = struct{}{}
		if _, ok := seenAddress[n.Address]; ok { return fmt.Errorf("duplicate node address %q", n.Address) }
		seenAddress[n.Address] = struct{}{}
		if _, ok := seenIdentity[n.AllowedIdentity]; ok { return fmt.Errorf("duplicate AllowedIdentity %q", n.AllowedIdentity) }
		seenIdentity[n.AllowedIdentity] = struct{}{}
		seenRouteID := make(map[string]struct{}, len(n.Routes))
		for j, r := range n.Routes {
			if strings.TrimSpace(r.ID) == "" { return fmt.Errorf("node %q route %d has empty RouteID", n.ID, j+1) }
			if r.RemoteRoute == "" { return fmt.Errorf("node %q route %q has empty remote route", n.ID, r.ID) }
			if _, ok := seenRouteID[r.ID]; ok { return fmt.Errorf("node %q has duplicate RouteID %q", n.ID, r.ID) }
			seenRouteID[r.ID] = struct{}{}
		}
	}
	return nil
}

func canonicalNodes(nodes []NodeDescriptor) []NodeDescriptor {
	out := make([]NodeDescriptor, len(nodes))
	for i := range nodes {
		out[i] = nodes[i]
		out[i].Routes = append([]RouteDescriptor(nil), nodes[i].Routes...)
		sort.Slice(out[i].Routes, func(a, b int) bool {
			if out[i].Routes[a].ID != out[i].Routes[b].ID { return out[i].Routes[a].ID < out[i].Routes[b].ID }
			if out[i].Routes[a].RemoteRoute != out[i].Routes[b].RemoteRoute { return out[i].Routes[a].RemoteRoute < out[i].Routes[b].RemoteRoute }
			return out[i].Routes[a].MasterListen < out[i].Routes[b].MasterListen
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID { return out[i].ID < out[j].ID }
		return out[i].Address < out[j].Address
	})
	return out
}

func semanticRevision(nodes []NodeDescriptor) (string, error) {
	canonical := canonicalNodes(nodes)
	raw, err := json.Marshal(canonical)
	if err != nil { return "", err }
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func deriveKey(shared, ephemeralPublic, recipientPublic []byte) []byte {
	salt := sha256.Sum256([]byte("BAFT_CLUSTER_TOKEN_KDF_V1"))
	prkMac := hmac.New(sha256.New, salt[:])
	_, _ = prkMac.Write(shared)
	prk := prkMac.Sum(nil)
	info := append([]byte("cluster-token|"), ephemeralPublic...)
	info = append(info, '|')
	info = append(info, recipientPublic...)
	mac := hmac.New(sha256.New, prk)
	_, _ = mac.Write(info)
	_, _ = mac.Write([]byte{1})
	return mac.Sum(nil)
}
