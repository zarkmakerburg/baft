package node

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

func noiseConfig(cfg config.Config) (securityinternal.HandshakeConfig, error) {
	if err := requirePrivateKeyPermissions(cfg.Noise.KeyFile); err != nil {
		return securityinternal.HandshakeConfig{}, err
	}
	key, err := securityinternal.LoadKeyPair(cfg.Noise.KeyFile)
	if err != nil {
		return securityinternal.HandshakeConfig{}, err
	}
	pub, err := securityinternal.DecodePublicKey(cfg.Noise.PeerPublicKey)
	if err != nil {
		return securityinternal.HandshakeConfig{}, err
	}
	return securityinternal.HandshakeConfig{Static: key, PeerStatic: pub, RecordShaping: cfg.Noise.RecordShaping}, nil
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
