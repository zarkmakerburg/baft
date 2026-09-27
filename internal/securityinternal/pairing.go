package securityinternal

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

const PairingPrefix = "BAFTPAIR1:"

type PairingDescriptor struct {
	Version       int    `json:"v"`
	Address       string `json:"address"`
	ServerName    string `json:"server_name"`
	NodeIdentity  string `json:"node_identity"`
	ResponderKey  string `json:"noise_responder_key"`
	OneTimePSK    string `json:"one_time_psk"`
	CAPEM         string `json:"ca_pem"`
	ExpiresUnix   int64  `json:"expires_unix"`
	RecordShaping bool   `json:"record_shaping"`
}

func NewPairingDescriptor(address, serverName, identity string, responderPub, caPEM []byte, ttl time.Duration) (PairingDescriptor, []byte, error) {
	if address == "" || serverName == "" || identity == "" {
		return PairingDescriptor{}, nil, errors.New("securityinternal: address, server name, and identity are required")
	}
	if len(responderPub) != 32 || len(caPEM) == 0 {
		return PairingDescriptor{}, nil, errors.New("securityinternal: invalid responder key or CA")
	}
	if ttl <= 0 || ttl > 24*time.Hour {
		return PairingDescriptor{}, nil, errors.New("securityinternal: pairing TTL must be in (0,24h]")
	}
	psk := make([]byte, 32)
	if _, err := rand.Read(psk); err != nil {
		return PairingDescriptor{}, nil, err
	}
	d := PairingDescriptor{
		Version:      1,
		Address:      address,
		ServerName:   serverName,
		NodeIdentity: identity,
		ResponderKey: base64.RawURLEncoding.EncodeToString(responderPub),
		OneTimePSK:   base64.RawURLEncoding.EncodeToString(psk),
		CAPEM:        base64.RawURLEncoding.EncodeToString(caPEM),
		ExpiresUnix:  time.Now().Add(ttl).Unix(),
	}
	return d, psk, nil
}

func (d PairingDescriptor) Encode() (string, error) {
	b, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	return PairingPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func DecodePairingDescriptor(s string, now time.Time) (PairingDescriptor, []byte, []byte, []byte, error) {
	if len(s) <= len(PairingPrefix) || s[:len(PairingPrefix)] != PairingPrefix {
		return PairingDescriptor{}, nil, nil, nil, errors.New("securityinternal: invalid pairing prefix")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s[len(PairingPrefix):])
	if err != nil {
		return PairingDescriptor{}, nil, nil, nil, err
	}
	var d PairingDescriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		return PairingDescriptor{}, nil, nil, nil, err
	}
	if d.Version != 1 || d.Address == "" || d.ServerName == "" || d.NodeIdentity == "" {
		return PairingDescriptor{}, nil, nil, nil, errors.New("securityinternal: invalid pairing descriptor")
	}
	if now.Unix() >= d.ExpiresUnix {
		return PairingDescriptor{}, nil, nil, nil, errors.New("securityinternal: pairing descriptor expired")
	}
	pub, err := base64.RawURLEncoding.DecodeString(d.ResponderKey)
	if err != nil || len(pub) != 32 {
		return PairingDescriptor{}, nil, nil, nil, errors.New("securityinternal: invalid responder key")
	}
	psk, err := base64.RawURLEncoding.DecodeString(d.OneTimePSK)
	if err != nil || len(psk) != 32 {
		return PairingDescriptor{}, nil, nil, nil, errors.New("securityinternal: invalid pairing PSK")
	}
	ca, err := base64.RawURLEncoding.DecodeString(d.CAPEM)
	if err != nil || len(ca) == 0 {
		return PairingDescriptor{}, nil, nil, nil, errors.New("securityinternal: invalid CA")
	}
	return d, pub, psk, ca, nil
}
