package config

import (
	"errors"
	"fmt"

	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

// Noise supports either a pinned single peer or, on listeners, an explicit
// identity->public-key allowlist. Dialers remain pinned to one responder key.
type Noise struct {
	KeyFile               string             `json:"key_file"`
	PeerPublicKey         string             `json:"peer_public_key,omitempty"`
	AllowedPeerPublicKeys map[string]string  `json:"allowed_peer_public_keys,omitempty"`
	RecordShaping         recordshape.Config `json:"record_shaping"`
	CoverHTMLFile         string             `json:"cover_html_file,omitempty"`
}

func validateNoise(c Config) error {
	if c.Noise == nil {
		return nil
	}
	if c.Noise.KeyFile == "" {
		return errors.New("noise.key_file is required")
	}
	if _, err := recordshape.New(c.Noise.RecordShaping); err != nil {
		return err
	}

	switch c.Node.Role {
	case "dialer":
		if len(c.Noise.AllowedPeerPublicKeys) != 0 {
			return errors.New("Noise dialer must not define allowed_peer_public_keys")
		}
		if _, err := securityinternal.DecodePublicKey(c.Noise.PeerPublicKey); err != nil {
			return err
		}
	case "listener":
		if c.Server == nil || len(c.Server.AllowedPeerIdentities) == 0 {
			return errors.New("Noise listener requires at least one allowed peer identity")
		}
		if len(c.Noise.AllowedPeerPublicKeys) == 0 {
			if len(c.Server.AllowedPeerIdentities) != 1 {
				return errors.New("legacy pinned Noise listener requires exactly one allowed peer identity")
			}
			if _, err := securityinternal.DecodePublicKey(c.Noise.PeerPublicKey); err != nil {
				return err
			}
			return nil
		}
		if c.Noise.PeerPublicKey != "" {
			return errors.New("Noise listener must use either peer_public_key or allowed_peer_public_keys")
		}
		if len(c.Noise.AllowedPeerPublicKeys) != len(c.Server.AllowedPeerIdentities) {
			return errors.New("Noise allowlist must contain exactly one public key per allowed peer identity")
		}
		seen := map[string]struct{}{}
		for _, id := range c.Server.AllowedPeerIdentities {
			enc, ok := c.Noise.AllowedPeerPublicKeys[id]
			if !ok {
				return fmt.Errorf("Noise allowlist missing identity %s", id)
			}
			pub, err := securityinternal.DecodePublicKey(enc)
			if err != nil {
				return fmt.Errorf("Noise allowlist identity %s: %w", id, err)
			}
			key := string(pub)
			if _, dup := seen[key]; dup {
				return errors.New("Noise allowlist contains duplicate public keys")
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}
