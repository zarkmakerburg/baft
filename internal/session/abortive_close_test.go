package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/protocol"
)

// tcpPair returns the two ends of a loopback TCP connection: local is what
// a Flow holds, remote stands in for the application on the other side.
func tcpPair(t *testing.T) (local, remote net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	remote, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	local = <-accepted
	if local == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() { local.Close(); remote.Close() })
	return local, remote
}

func readAfterClose(t *testing.T, c net.Conn) error {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := io.ReadAll(c)
	if err == nil {
		return io.EOF // ReadAll maps a clean EOF to nil
	}
	return err
}

func TestFlowCloseBeforeFinResetsLocalConn(t *testing.T) {
	local, remote := tcpPair(t)
	fl := newFlow(1, "route", "nonce", local)
	fl.close()
	err := readAfterClose(t, remote)
	if !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("truncated Flow ended with %v, want connection reset", err)
	}
}

func TestFlowCloseAfterFinIsGraceful(t *testing.T) {
	local, remote := tcpPair(t)
	fl := newFlow(1, "route", "nonce", local)
	if _, err := local.Write([]byte("whole stream")); err != nil {
		t.Fatal(err)
	}
	if err := local.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	fl.mu.Lock()
	fl.writeClosed = true
	fl.mu.Unlock()
	fl.close()
	_ = remote.SetReadDeadline(time.Now().Add(2 * time.Second))
	got, err := io.ReadAll(remote)
	if err != nil || string(got) != "whole stream" {
		t.Fatalf("finished Flow read %q, %v; want the whole stream and a clean EOF", got, err)
	}
}

func peerWithOpenFlow(t *testing.T) (*Peer, net.Conn) {
	t.Helper()
	var out bytes.Buffer
	p, err := New(Dialer, Carrier{In: bytes.NewReader(nil), Out: &out}, "urn:baft:node:ex-01", nil, Options{
		NodeID: "ir-01", ExpectedPeerNodeID: "ex-01", ProfileID: "secure-fast", ProfileVersion: 1, ConfigRevision: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	local, remote := tcpPair(t)
	fl := newFlow(1, "main", "00112233445566778899aabbccddeeff", local, p.allocator)
	fl.openOK = true
	p.mu.Lock()
	p.flows[1] = fl
	p.localReady, p.peerReady = true, true
	p.markReadyLocked()
	p.mu.Unlock()
	return p, remote
}

func TestPeerResetAbortsLocalConn(t *testing.T) {
	p, remote := peerWithOpenFlow(t)
	payload, err := protocol.EncodeControl(protocol.Reset{Code: protocol.ErrorAdminDrain})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.handleFrame(context.Background(), protocol.Frame{Type: protocol.TypeReset, StreamID: 1, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := readAfterClose(t, remote); !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("peer RESET reached the local application as %v, want connection reset", err)
	}
}

func TestSessionTeardownAbortsUnfinishedLocalConns(t *testing.T) {
	p, remote := peerWithOpenFlow(t)
	p.closeAll()
	if err := readAfterClose(t, remote); !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("session teardown reached the local application as %v, want connection reset", err)
	}
}
