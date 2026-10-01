package securityinternal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/recordshape"
)

func secureMorphingPair(t *testing.T) (*Conn, *Conn) {
	t.Helper()
	ik, _ := GenerateKeyPair()
	rk, _ := GenerateKeyPair()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	_ = a.SetDeadline(time.Now().Add(3 * time.Second))
	_ = b.SetDeadline(time.Now().Add(3 * time.Second))
	shape := recordshape.DefaultConfig(true)
	shape.JitterMinUS = 0
	shape.JitterMaxUS = 0
	type result struct {
		conn *Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		c, _, e := Responder(b, b, HandshakeConfig{Static: rk, PeerStatic: ik.Public, RecordShaping: shape})
		done <- result{c, e}
	}()
	client, err := Initiator(a, a, HandshakeConfig{Static: ik, PeerStatic: rk.Public, RecordShaping: shape})
	if err != nil {
		t.Fatal(err)
	}
	server := <-done
	if server.err != nil {
		t.Fatal(server.err)
	}
	return client, server.conn
}

type corruptCiphertext struct{ io.Writer }

func (w corruptCiphertext) Write(p []byte) (int, error) {
	p = append([]byte(nil), p...)
	p[8] ^= 1
	return w.Writer.Write(p)
}
func TestMorphingPreservesNoiseAuthentication(t *testing.T) {
	c, s := secureMorphingPair(t)
	c.w = corruptCiphertext{c.w}
	done := make(chan error, 1)
	go func() { _, err := c.Write([]byte("authenticated payload")); done <- err }()
	b := make([]byte, 100)
	if _, err := s.Read(b); err == nil {
		t.Fatal("tampered Noise ciphertext accepted")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(b); err == nil {
		t.Fatal("read error not sticky")
	}
}

type failWriter struct{ calls int }

func (w *failWriter) Write(p []byte) (int, error) { w.calls++; return 1, io.ErrClosedPipe }
func TestMorphingWriteFailurePoisonsConnection(t *testing.T) {
	c, _ := secureMorphingPair(t)
	w := &failWriter{}
	c.w = w
	for i := 0; i < 2; i++ {
		if _, err := c.Write([]byte("hello")); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal(err)
		}
	}
	if w.calls != 1 {
		t.Fatal("retried after partial ciphertext write; Noise nonce desynchronized")
	}
}
func TestMorphingCancelledWriteIsSticky(t *testing.T) {
	c, _ := secureMorphingPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.ctx = ctx
	var out bytes.Buffer
	c.w = &out
	if _, err := c.Write([]byte("hello")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("cancel wrote ciphertext")
	}
}
func TestMorphingModeMismatchFailsAtNoiseHandshake(t *testing.T) {
	ik, _ := GenerateKeyPair()
	rk, _ := GenerateKeyPair()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_ = a.SetDeadline(time.Now().Add(time.Second))
	_ = b.SetDeadline(time.Now().Add(time.Second))
	done := make(chan error, 1)
	go func() {
		_, _, err := Responder(b, b, HandshakeConfig{Static: rk, PeerStatic: ik.Public})
		b.Close()
		done <- err
	}()
	_, err := Initiator(a, a, HandshakeConfig{Static: ik, PeerStatic: rk.Public, RecordShaping: recordshape.DefaultConfig(true)})
	if err == nil {
		t.Fatal("mode mismatch accepted")
	}
	if err := <-done; err == nil {
		t.Fatal("responder accepted mismatched mode")
	}
}
