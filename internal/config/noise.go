package config

import (
	"errors"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

// Noise is an opt-in, pre-pinned single-peer mode. The existing mTLS mode
// remains the default. Pairing/enrollment is a separate administrative action.
type Noise struct {
	KeyFile       string             `json:"key_file"`
	PeerPublicKey string             `json:"peer_public_key"`
	RecordShaping recordshape.Config `json:"record_shaping"`
	CoverHTMLFile string             `json:"cover_html_file,omitempty"`
}

func validateNoise(c Config) error {
	if c.Noise == nil {
		return nil
	}
	if c.Noise.KeyFile == "" {
		return errors.New("noise.key_file is required")
	}
	if _, err := securityinternal.DecodePublicKey(c.Noise.PeerPublicKey); err != nil {
		return err
	}
	if _, err := recordshape.New(c.Noise.RecordShaping); err != nil {
		return err
	}
	if c.Node.Role == "listener" && (c.Server == nil || len(c.Server.AllowedPeerIdentities) != 1) {
		return errors.New("pinned Noise listener requires exactly one allowed peer identity")
	}
	return nil
}
