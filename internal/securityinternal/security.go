package securityinternal

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/flynn/noise"
	"github.com/zarkmakerburg/baft/internal/recordshape"
)

const (
	DefaultPrologue       = "BAFT-SecurityInternal-v1"
	MaxPlaintextRecord    = 16 * 1024
	MaxCiphertextRecord   = MaxPlaintextRecord + 16
	maxHandshakeFrameSize = 4096
)

var (
	ErrPeerStaticMismatch = errors.New("securityinternal: peer static key mismatch")
	ErrPairingRequired    = errors.New("securityinternal: responder requires a pinned peer or one-time pairing PSK")
	ErrInvalidPairingPSK  = errors.New("securityinternal: pairing PSK must be 32 bytes")
	ErrFrameTooLarge      = errors.New("securityinternal: frame too large")
)

type KeyPair struct {
	Private []byte
	Public  []byte
}

func GenerateKeyPair() (KeyPair, error) {
	k, err := noise.DH25519.GenerateKeypair(rand.Reader)
	if err != nil {
		return KeyPair{}, err
	}
	return KeyPair{
		Private: append([]byte(nil), k.Private...),
		Public:  append([]byte(nil), k.Public...),
	}, nil
}

func (k KeyPair) Valid() bool {
	return len(k.Private) == 32 && len(k.Public) == 32
}

func (k KeyPair) noiseKey() (noise.DHKey, error) {
	if !k.Valid() {
		return noise.DHKey{}, errors.New("securityinternal: invalid X25519 keypair")
	}
	return noise.DHKey{
		Private: append([]byte(nil), k.Private...),
		Public:  append([]byte(nil), k.Public...),
	}, nil
}

func EncodePublicKey(pub []byte) (string, error) {
	if len(pub) != 32 {
		return "", errors.New("securityinternal: public key must be 32 bytes")
	}
	return base64.RawURLEncoding.EncodeToString(pub), nil
}

func DecodePublicKey(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, errors.New("securityinternal: public key must decode to 32 bytes")
	}
	return b, nil
}

type HandshakeConfig struct {
	Context            context.Context
	Static             KeyPair
	PeerStatic         []byte
	AllowedPeerStatics [][]byte
	OneTimePairingPSK  []byte
	Prologue           []byte
	RecordShaping      recordshape.Config
}

func (c HandshakeConfig) prologue() []byte {
	p := c.Prologue
	if len(p) == 0 {
		p = []byte(DefaultPrologue)
	}
	if c.RecordShaping.Enabled {
		return append(append([]byte(nil), p...), []byte("/probabilistic-records-v2")...)
	}
	return p
}

func (c HandshakeConfig) validatePairingPSK() error {
	if len(c.OneTimePairingPSK) != 0 && len(c.OneTimePairingPSK) != 32 {
		return ErrInvalidPairingPSK
	}
	return nil
}

type Conn struct {
	r io.Reader
	w io.Writer

	send *noise.CipherState
	recv *noise.CipherState

	readMu   sync.Mutex
	writeMu  sync.Mutex
	readBuf  []byte
	shape    recordshape.Codec
	ctx      context.Context
	writeErr error
	readErr  error
}

func Initiator(r io.Reader, w io.Writer, cfg HandshakeConfig) (*Conn, error) {
	if cfg.Context == nil {
		cfg.Context = context.Background()
	}
	if err := cfg.validatePairingPSK(); err != nil {
		return nil, err
	}
	local, err := cfg.Static.noiseKey()
	if err != nil {
		return nil, err
	}
	shape, err := recordshape.New(cfg.RecordShaping)
	if err != nil {
		return nil, err
	}
	if len(cfg.PeerStatic) != 32 {
		return nil, errors.New("securityinternal: IK initiator requires responder static public key")
	}

	ncfg := noise.Config{
		CipherSuite:   noise.NewCipherSuite(noise.DH25519, noise.CipherAESGCM, noise.HashSHA256),
		Pattern:       noise.HandshakeIK,
		Initiator:     true,
		Prologue:      cfg.prologue(),
		StaticKeypair: local,
		PeerStatic:    append([]byte(nil), cfg.PeerStatic...),
	}
	if len(cfg.OneTimePairingPSK) != 0 {
		ncfg.PresharedKey = append([]byte(nil), cfg.OneTimePairingPSK...)
		ncfg.PresharedKeyPlacement = 0
	}

	hs, err := noise.NewHandshakeState(ncfg)
	if err != nil {
		return nil, err
	}
	msg1, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("securityinternal: write IK message 1: %w", err)
	}
	if err := shape.Wait(cfg.Context); err != nil {
		return nil, err
	}
	if err := writeHandshakeFrame(w, msg1); err != nil {
		return nil, err
	}

	msg2, err := readHandshakeFrame(r)
	if err != nil {
		return nil, err
	}
	_, cs1, cs2, err := hs.ReadMessage(nil, msg2)
	if err != nil {
		return nil, fmt.Errorf("securityinternal: read IK message 2: %w", err)
	}
	if cs1 == nil || cs2 == nil {
		return nil, errors.New("securityinternal: IK handshake did not enter transport mode")
	}
	return &Conn{r: r, w: w, send: cs1, recv: cs2, shape: shape, ctx: cfg.Context}, nil
}

// Responder performs IK or IKpsk0. If PeerStatic is empty,
// OneTimePairingPSK must be present. The returned peerStatic is the
// authenticated initiator static public key and can be pinned atomically by
// the caller before the one-time PSK is deleted.
func Responder(r io.Reader, w io.Writer, cfg HandshakeConfig) (*Conn, []byte, error) {
	if cfg.Context == nil {
		cfg.Context = context.Background()
	}
	if err := cfg.validatePairingPSK(); err != nil {
		return nil, nil, err
	}
	local, err := cfg.Static.noiseKey()
	if err != nil {
		return nil, nil, err
	}
	shape, err := recordshape.New(cfg.RecordShaping)
	if err != nil {
		return nil, nil, err
	}
	if len(cfg.PeerStatic) != 0 && len(cfg.AllowedPeerStatics) != 0 {
		return nil, nil, errors.New("securityinternal: configure either one pinned peer or an allowlist, not both")
	}
	if len(cfg.PeerStatic) == 0 && len(cfg.AllowedPeerStatics) == 0 && len(cfg.OneTimePairingPSK) == 0 {
		return nil, nil, ErrPairingRequired
	}
	if len(cfg.PeerStatic) != 0 && len(cfg.PeerStatic) != 32 {
		return nil, nil, errors.New("securityinternal: expected initiator static key must be 32 bytes")
	}
	for _, pub := range cfg.AllowedPeerStatics {
		if len(pub) != 32 {
			return nil, nil, errors.New("securityinternal: allowlisted initiator static key must be 32 bytes")
		}
	}

	ncfg := noise.Config{
		CipherSuite:   noise.NewCipherSuite(noise.DH25519, noise.CipherAESGCM, noise.HashSHA256),
		Pattern:       noise.HandshakeIK,
		Initiator:     false,
		Prologue:      cfg.prologue(),
		StaticKeypair: local,
	}
	if len(cfg.OneTimePairingPSK) != 0 {
		ncfg.PresharedKey = append([]byte(nil), cfg.OneTimePairingPSK...)
		ncfg.PresharedKeyPlacement = 0
	}

	hs, err := noise.NewHandshakeState(ncfg)
	if err != nil {
		return nil, nil, err
	}
	msg1, err := readHandshakeFrame(r)
	if err != nil {
		return nil, nil, err
	}
	if _, _, _, err := hs.ReadMessage(nil, msg1); err != nil {
		return nil, nil, fmt.Errorf("securityinternal: read IK message 1: %w", err)
	}

	peerStatic := append([]byte(nil), hs.PeerStatic()...)
	if len(peerStatic) != 32 {
		return nil, nil, errors.New("securityinternal: responder did not obtain initiator static key")
	}
	if len(cfg.PeerStatic) != 0 && !bytes.Equal(peerStatic, cfg.PeerStatic) {
		return nil, nil, ErrPeerStaticMismatch
	}
	if len(cfg.PeerStatic) == 0 && len(cfg.AllowedPeerStatics) != 0 {
		allowed := false
		for _, pub := range cfg.AllowedPeerStatics {
			if bytes.Equal(peerStatic, pub) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, nil, ErrPeerStaticMismatch
		}
	}

	msg2, cs1, cs2, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("securityinternal: write IK message 2: %w", err)
	}
	if cs1 == nil || cs2 == nil {
		return nil, nil, errors.New("securityinternal: IK handshake did not enter transport mode")
	}
	if err := shape.Wait(cfg.Context); err != nil {
		return nil, nil, err
	}
	if err := writeHandshakeFrame(w, msg2); err != nil {
		return nil, nil, err
	}

	// Noise Split returns initiator->responder first and
	// responder->initiator second. The responder therefore sends with cs2 and
	// receives with cs1.
	return &Conn{r: r, w: w, send: cs2, recv: cs1, shape: shape, ctx: cfg.Context}, peerStatic, nil
}

func (c *Conn) Write(p []byte) (total int, retErr error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if c.writeErr != nil {
		return 0, c.writeErr
	}
	defer func() {
		if retErr != nil {
			c.writeErr = retErr
		}
	}()
	for len(p) > 0 {
		n := len(p)
		if n > MaxPlaintextRecord {
			n = MaxPlaintextRecord
		}
		ciphertext, err := c.send.Encrypt(nil, nil, p[:n])
		if err != nil {
			return total, err
		}
		if len(ciphertext) > MaxCiphertextRecord {
			return total, ErrFrameTooLarge
		}
		if err := c.shape.WriteFrameContext(c.ctx, c.w, ciphertext, MaxCiphertextRecord); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

func (c *Conn) Read(p []byte) (n int, retErr error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	if len(p) == 0 {
		return 0, nil
	}
	if c.readErr != nil {
		return 0, c.readErr
	}
	defer func() {
		if retErr != nil {
			c.readErr = retErr
		}
	}()
	if len(c.readBuf) == 0 {
		ciphertext, err := c.shape.ReadFrame(c.r, MaxCiphertextRecord)
		if err != nil {
			return 0, err
		}
		plain, err := c.recv.Decrypt(nil, nil, ciphertext)
		if err != nil {
			return 0, fmt.Errorf("securityinternal: decrypt record: %w", err)
		}
		c.readBuf = plain
	}
	n = copy(p, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n, nil
}

func writeHandshakeFrame(w io.Writer, msg []byte) error {
	if len(msg) == 0 || len(msg) > maxHandshakeFrameSize {
		return ErrFrameTooLarge
	}
	wire := make([]byte, 2+len(msg))
	binary.BigEndian.PutUint16(wire, uint16(len(msg)))
	copy(wire[2:], msg)
	return writeFull(w, wire)
}

func readHandshakeFrame(r io.Reader) ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n == 0 || n > maxHandshakeFrameSize {
		return nil, ErrFrameTooLarge
	}
	msg := make([]byte, n)
	if _, err := io.ReadFull(r, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n < 0 || n > len(p) {
			return io.ErrShortWrite
		}
		p = p[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
