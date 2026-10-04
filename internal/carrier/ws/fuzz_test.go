package ws

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// fakeConn adapts a byte slice (the inbound stream) to net.Conn for the read
// path; writes (pongs, close frames) are discarded.
type fakeConn struct {
	r *bytes.Reader
}

func (f *fakeConn) Read(p []byte) (int, error)         { return f.r.Read(p) }
func (f *fakeConn) Write(p []byte) (int, error)        { return len(p), nil }
func (f *fakeConn) Close() error                       { return nil }
func (f *fakeConn) LocalAddr() net.Addr                { return nil }
func (f *fakeConn) RemoteAddr() net.Addr               { return nil }
func (f *fakeConn) SetDeadline(t time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(t time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(t time.Time) error { return nil }

// FuzzReadFrame feeds arbitrary bytes to the server-side frame reader. The
// parser must never panic, hang, or allocate unboundedly: it either returns
// payload bytes or an error. The inbound stream is treated as client→server, so
// frames are expected to be masked.
func FuzzReadFrame(f *testing.F) {
	// A well-formed masked binary frame carrying "hi".
	f.Add([]byte{0x82, 0x82, 0x01, 0x02, 0x03, 0x04, 'h' ^ 0x01, 'i' ^ 0x02})
	// Fragmented data frame + continuation.
	f.Add([]byte{0x02, 0x81, 0, 0, 0, 0, 'a', 0x80, 0x81, 0, 0, 0, 0, 'b'})
	// Ping then binary.
	f.Add([]byte{0x89, 0x80, 0, 0, 0, 0, 0x82, 0x81, 0, 0, 0, 0, 'z'})
	// 16-bit length header (short-read).
	f.Add([]byte{0x82, 0xFE, 0x10, 0x00})
	// 64-bit length header with high bit set (must be rejected, not OOM).
	f.Add([]byte{0x82, 0xFF, 0x80, 0, 0, 0, 0, 0, 0, 0})

	f.Fuzz(func(t *testing.T, data []byte) {
		c := &Conn{
			nc:       &fakeConn{r: bytes.NewReader(data)},
			isClient: false,
			br:       bufio.NewReaderSize(bytes.NewReader(data), 16*1024),
			closed:   make(chan struct{}),
		}
		// Bound total work so a crafted stream of tiny control frames cannot
		// spin forever within one fuzz execution.
		buf := make([]byte, 4096)
		for i := 0; i < 1000; i++ {
			_, err := c.Read(buf)
			if err != nil {
				if err == io.EOF || err == ErrClosed {
					return
				}
				return // any parse error is acceptable; must not panic
			}
		}
	})
}
