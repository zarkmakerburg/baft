//go:build linux

package session

import (
	"net"
	"syscall"
	"testing"
)

func sockoptInt(t *testing.T, c *net.TCPConn, opt int) int {
	t.Helper()
	raw, err := c.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var v int
	var gerr error
	if err := raw.Control(func(fd uintptr) { v, gerr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, opt) }); err != nil {
		t.Fatal(err)
	}
	if gerr != nil {
		t.Fatal(gerr)
	}
	return v
}

// The kernel reports SOCK_RCVBUF_LOCK only indirectly: an explicitly set
// buffer reads back as (min(requested, rmem_max) * 2), and it no longer grows.
// Assert newFlow applied an explicit buffer to both directions.
func TestNewFlowFixesAppConnSocketBuffers(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); accepted <- c }()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	peer := <-accepted
	defer peer.Close()

	tc := c.(*net.TCPConn)
	beforeRcv := sockoptInt(t, tc, syscall.SO_RCVBUF)
	_ = newFlow(1, "r", "n", c)
	afterRcv := sockoptInt(t, tc, syscall.SO_RCVBUF)
	afterSnd := sockoptInt(t, tc, syscall.SO_SNDBUF)

	ref, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer ref.Close()
	rt := ref.(*net.TCPConn)
	if err := rt.SetReadBuffer(appConnSocketBuffer); err != nil {
		t.Fatal(err)
	}
	if err := rt.SetWriteBuffer(appConnSocketBuffer); err != nil {
		t.Fatal(err)
	}
	wantRcv := sockoptInt(t, rt, syscall.SO_RCVBUF)
	wantSnd := sockoptInt(t, rt, syscall.SO_SNDBUF)
	if afterRcv != wantRcv || afterSnd != wantSnd {
		t.Fatalf("app conn buffers not fixed: rcv before=%d after=%d want=%d snd after=%d want=%d", beforeRcv, afterRcv, wantRcv, afterSnd, wantSnd)
	}
}

func TestTuneAppConnIgnoresNonTCP(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	tuneAppConn(a)
	tuneAppConn(nil)
}
