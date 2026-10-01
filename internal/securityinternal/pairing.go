package securityinternal

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
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

const PairingReplyPrefix = "BAFTREPLY1:"

// PairingReply carries the IR's identity and Noise static key back to the EX.
// The MAC is keyed with the descriptor's one-time PSK, which only the EX and
// the holder of the pairing code know, so a third party cannot substitute its
// own key; it also binds the reply to that EX's responder key.
type PairingReply struct {
	Version      int    `json:"v"`
	NodeIdentity string `json:"node_identity"`
	PublicKey    string `json:"noise_public_key"`
	ExpiresUnix  int64  `json:"expires_unix"`
	MAC          string `json:"mac"`
}

func pairingReplyMAC(psk, responderPub []byte, r PairingReply) []byte {
	m := hmac.New(sha256.New, psk)
	for _, part := range []string{PairingReplyPrefix, r.NodeIdentity, r.PublicKey, strconv.FormatInt(r.ExpiresUnix, 10), base64.RawURLEncoding.EncodeToString(responderPub)} {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(part)))
		m.Write(n[:])
		m.Write([]byte(part))
	}
	return m.Sum(nil)
}

// NewPairingReply answers descriptor d (already decoded with its PSK and
// responder key) with the IR's identity and public key. The reply expires
// with the descriptor.
func NewPairingReply(d PairingDescriptor, responderPub, psk []byte, identity string, localPub []byte) (string, error) {
	if identity == "" || len(localPub) != 32 || len(psk) != 32 || len(responderPub) != 32 {
		return "", errors.New("securityinternal: invalid pairing reply input")
	}
	r := PairingReply{Version: 1, NodeIdentity: identity, PublicKey: base64.RawURLEncoding.EncodeToString(localPub), ExpiresUnix: d.ExpiresUnix}
	r.MAC = base64.RawURLEncoding.EncodeToString(pairingReplyMAC(psk, responderPub, r))
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return PairingReplyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// DecodePairingReply verifies a reply against the EX's PSK and responder key
// and returns the IR identity and its Noise public key.
func DecodePairingReply(s string, psk, responderPub []byte, now time.Time) (string, []byte, error) {
	if !strings.HasPrefix(s, PairingReplyPrefix) {
		return "", nil, errors.New("securityinternal: invalid pairing reply prefix")
	}
	raw, err := base64.RawURLEncoding.DecodeString(s[len(PairingReplyPrefix):])
	if err != nil {
		return "", nil, err
	}
	var r PairingReply
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", nil, err
	}
	mac, err := base64.RawURLEncoding.DecodeString(r.MAC)
	if err != nil || r.Version != 1 || !hmac.Equal(mac, pairingReplyMAC(psk, responderPub, r)) {
		return "", nil, errors.New("securityinternal: pairing reply is not authentic")
	}
	if now.Unix() >= r.ExpiresUnix {
		return "", nil, errors.New("securityinternal: pairing reply expired")
	}
	pub, err := base64.RawURLEncoding.DecodeString(r.PublicKey)
	if err != nil || len(pub) != 32 || r.NodeIdentity == "" {
		return "", nil, errors.New("securityinternal: invalid pairing reply")
	}
	return r.NodeIdentity, pub, nil
}
