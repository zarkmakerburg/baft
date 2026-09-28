package node

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

func loadNoiseStatic(path string) (securityinternal.KeyPair, error) {
	if err := requirePrivateKeyPermissions(path); err != nil {
		return securityinternal.KeyPair{}, err
	}
	return securityinternal.LoadKeyPair(path)
}

func noiseConfig(cfg config.Config) (securityinternal.HandshakeConfig, error) {
	key, err := loadNoiseStatic(cfg.Noise.KeyFile)
	if err != nil {
		return securityinternal.HandshakeConfig{}, err
	}
	pub, err := securityinternal.DecodePublicKey(cfg.Noise.PeerPublicKey)
	if err != nil {
		return securityinternal.HandshakeConfig{}, err
	}
	return securityinternal.HandshakeConfig{Static: key, PeerStatic: pub, RecordShaping: cfg.Noise.RecordShaping}, nil
}

func noiseListenerOptions(cfg config.Config) (securityinternal.HandshakeConfig, map[string][]byte, string, error) {
	key, err := loadNoiseStatic(cfg.Noise.KeyFile)
	if err != nil {
		return securityinternal.HandshakeConfig{}, nil, "", err
	}
	hs := securityinternal.HandshakeConfig{Static:key, RecordShaping:cfg.Noise.RecordShaping}
	if len(cfg.Noise.AllowedPeerPublicKeys) == 0 {
		pub, err := securityinternal.DecodePublicKey(cfg.Noise.PeerPublicKey)
		if err != nil {
			return securityinternal.HandshakeConfig{}, nil, "", err
		}
		hs.PeerStatic = pub
		return hs, nil, cfg.Server.AllowedPeerIdentities[0], nil
	}
	allowed := make(map[string][]byte, len(cfg.Noise.AllowedPeerPublicKeys))
	for identity, encoded := range cfg.Noise.AllowedPeerPublicKeys {
		pub, err := securityinternal.DecodePublicKey(encoded)
		if err != nil {
			return securityinternal.HandshakeConfig{}, nil, "", fmt.Errorf("Noise peer %s: %w",identity,err)
		}
		allowed[identity] = pub
	}
	return hs, allowed, "", nil
}

func coverHandler(path string) (http.Handler, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || len(b) > 1<<20 {
		return nil, fmt.Errorf("cover HTML must be 1..1048576 bytes")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(b)
		}
	}), nil
}
