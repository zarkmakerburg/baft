// Package ws implements a minimal RFC 6455 WebSocket transport used as a BAFT
// carrier. It is deliberately scoped to what the carrier needs: a reliable,
// ordered, bidirectional *byte stream* over WebSocket binary frames, with the
// inner Noise IK handshake and the BAFT framing riding inside. WebSocket is the
// transport Cloudflare's proxy (orange cloud) forwards natively for full-duplex
// streams, which a long-lived HTTP/2 POST body is not; this package is what
// lets a BAFT carrier run behind Cloudflare.
//
// The upper layers never depend on WebSocket message boundaries: Conn.Read
// exposes binary/continuation payload bytes as one ordered byte stream without
// assembling an entire fragmented message in memory, and Conn.Write emits one
// or more binary frames. Control frames (ping/pong/close) are handled
// transparently and never surface as payload bytes.
//
// Only the subset the carrier uses is implemented. Text frames are rejected (a
// BAFT carrier is always binary). Per RFC 6455 a client MUST mask every frame
// it sends and a server MUST NOT; each side rejects a violation of that rule.
package ws

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Opcodes (RFC 6455 §5.2).
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// maxReadFramePayload bounds a single inbound frame's payload. Legitimate BAFT
// carrier frames carry Noise records (<= 65535 bytes); anything larger is a
// protocol violation or a memory-exhaustion attempt and is refused.
const maxReadFramePayload = 1 << 20 // 1 MiB

// writeFramePayload caps the payload of each outbound binary frame. Larger
// writes are split across frames; smaller writes go out as a single frame.
const writeFramePayload = 32 * 1024

// ErrClosed is returned by Read/Write after the connection is closed.
var ErrClosed = errors.New("ws: connection closed")

// Conn is a WebSocket connection presented as an io.ReadWriteCloser byte stream.
// It is safe for one reader goroutine and one writer goroutine concurrently
// (the pattern securityinternal.Conn and the BAFT session use); Close is safe
// from any goroutine.
type Conn struct {
	nc       net.Conn
	isClient bool

	// read side (single reader goroutine)
	br         *bufio.Reader
	readBuf    []byte // leftover payload bytes from the last data frame
	fragmented bool   // a binary message is awaiting continuation frames

	// write side
	wmu sync.Mutex

	closeOnce sync.Once
	closed    chan struct{}
}

// newConn wraps an upgraded connection. br carries any bytes already buffered
// during the HTTP upgrade (it must be reused, not replaced, or those bytes are
// lost); when nil a fresh buffered reader is created.
func newConn(nc net.Conn, br *bufio.Reader, isClient bool) *Conn {
	if br == nil {
		br = bufio.NewReaderSize(nc, 16*1024)
	}
	return &Conn{
		nc:       nc,
		isClient: isClient,
		br:       br,
		closed:   make(chan struct{}),
	}
}

// Read returns payload bytes from the WebSocket data stream. It transparently
// answers pings and returns io.EOF after a close frame.
func (c *Conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(c.readBuf) == 0 {
		op, payload, err := c.readDataOrControl()
		if err != nil {
			return 0, err
		}
		switch op {
		case opBinary, opContinuation:
			c.readBuf = payload
		default:
			// control frame already handled; keep reading
		}
	}
	n := copy(p, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n, nil
}

// readDataOrControl reads one frame, handling control frames in place. It
// returns opBinary/opContinuation with the payload for data frames; for control
// frames it performs the side effect (pong, or EOF on close) and returns the
// opcode with a nil payload so the caller loops.
func (c *Conn) readDataOrControl() (byte, []byte, error) {
	fin, op, payload, err := c.readFrame()
	if err != nil {
		return 0, nil, err
	}
	switch op {
	case opBinary:
		if c.fragmented {
			return 0, nil, errors.New("ws: new binary frame before fragmented message completed")
		}
		c.fragmented = !fin
		return opBinary, payload, nil
	case opContinuation:
		if !c.fragmented {
			return 0, nil, errors.New("ws: continuation without fragmented message")
		}
		if fin {
			c.fragmented = false
		}
		return opContinuation, payload, nil
	case opPing:
		if err := c.writeControl(opPong, payload); err != nil {
			return 0, nil, err
		}
		return op, nil, nil
	case opPong:
		return op, nil, nil
	case opClose:
		_ = c.writeControl(opClose, nil)
		c.closeUnderlying()
		return 0, nil, io.EOF
	default:
		return 0, nil, fmt.Errorf("ws: unexpected opcode %#x", op)
	}
}

// readFrame reads one WebSocket frame header and payload.
func (c *Conn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(c.br, h[:]); err != nil {
		return false, 0, nil, err
	}
	fin = h[0]&0x80 != 0
	rsv := h[0] & 0x70
	if rsv != 0 {
		return false, 0, nil, errors.New("ws: RSV bits set without negotiated extension")
	}
	opcode = h[0] & 0x0F
	masked := h[1]&0x80 != 0
	// A server must receive masked frames; a client must receive unmasked
	// frames (RFC 6455 §5.1).
	if c.isClient && masked {
		return false, 0, nil, errors.New("ws: server frame must not be masked")
	}
	if !c.isClient && !masked {
		return false, 0, nil, errors.New("ws: client frame must be masked")
	}
	length := uint64(h[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
		if length&(1<<63) != 0 {
			return false, 0, nil, errors.New("ws: frame length high bit set")
		}
	}
	isControl := opcode&0x8 != 0
	if isControl {
		// Control frames must be <=125 bytes and not fragmented.
		if length > 125 || !fin {
			return false, 0, nil, errors.New("ws: invalid control frame")
		}
	} else if length > maxReadFramePayload {
		return false, 0, nil, fmt.Errorf("ws: frame payload %d exceeds limit", length)
	}
	if opcode == opText {
		return false, 0, nil, errors.New("ws: text frames not supported")
	}
	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, maskKey[:]); err != nil {
			return false, 0, nil, err
		}
	}
	if length > 0 {
		payload = make([]byte, length)
		if _, err = io.ReadFull(c.br, payload); err != nil {
			return false, 0, nil, err
		}
		if masked {
			for i := range payload {
				payload[i] ^= maskKey[i&3]
			}
		}
	}
	return fin, opcode, payload, nil
}

// Write sends p as one or more binary frames.
func (c *Conn) Write(p []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, ErrClosed
	default:
	}
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > writeFramePayload {
			n = writeFramePayload
		}
		if err := c.writeFrame(opBinary, p[:n]); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

func (c *Conn) writeControl(opcode byte, payload []byte) error {
	if len(payload) > 125 {
		payload = payload[:125]
	}
	return c.writeFrame(opcode, payload)
}

// writeFrame serializes and sends a single, unfragmented frame. Client frames
// are masked with a fresh key from crypto/rand, as RFC 6455 requires.
func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	var hdr [14]byte
	hdr[0] = 0x80 | opcode // FIN set; single-frame messages only
	n := 2
	length := len(payload)
	switch {
	case length < 126:
		hdr[1] = byte(length)
	case length < 1<<16:
		hdr[1] = 126
		binary.BigEndian.PutUint16(hdr[2:4], uint16(length))
		n = 4
	default:
		hdr[1] = 127
		binary.BigEndian.PutUint64(hdr[2:10], uint64(length))
		n = 10
	}
	var maskKey [4]byte
	if c.isClient {
		hdr[1] |= 0x80
		if _, err := rand.Read(maskKey[:]); err != nil {
			return err
		}
		copy(hdr[n:n+4], maskKey[:])
		n += 4
	}

	c.wmu.Lock()
	defer c.wmu.Unlock()
	select {
	case <-c.closed:
		return ErrClosed
	default:
	}
	// Keep the frame header and payload in one underlying write.
	// Mask into a scratch buffer; never mutate the caller's slice.
	frame := make([]byte, n+length)
	copy(frame, hdr[:n])
	if c.isClient {
		for i := 0; i < length; i++ {
			frame[n+i] = payload[i] ^ maskKey[i&3]
		}
	} else {
		copy(frame[n:], payload)
	}
	written, err := c.nc.Write(frame)
	if err == nil && written != len(frame) {
		return io.ErrShortWrite
	}
	return err
}

// Close sends a WebSocket close frame (best effort) and closes the underlying
// connection. It is idempotent.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		_ = c.writeControl(opClose, nil)
		close(c.closed)
	})
	return c.nc.Close()
}

func (c *Conn) closeUnderlying() {
	c.closeOnce.Do(func() { close(c.closed) })
	_ = c.nc.Close()
}

// SetDeadline, SetReadDeadline, SetWriteDeadline delegate to the underlying
// connection so the Noise handshake can be time-bounded by the caller.
func (c *Conn) SetDeadline(t time.Time) error      { return c.nc.SetDeadline(t) }
func (c *Conn) SetReadDeadline(t time.Time) error  { return c.nc.SetReadDeadline(t) }
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.nc.SetWriteDeadline(t) }

// LocalAddr and RemoteAddr expose the underlying connection's addresses.
func (c *Conn) LocalAddr() net.Addr  { return c.nc.LocalAddr() }
func (c *Conn) RemoteAddr() net.Addr { return c.nc.RemoteAddr() }
