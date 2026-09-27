package recordshape

import (
	"encoding/binary"
	"errors"
	"io"
)

var (
	ErrFrameTooLarge = errors.New("recordshape: frame too large")
	ErrNoBucket       = errors.New("recordshape: no bucket can contain frame")
)

type Config struct {
	Enabled bool
	Buckets []uint32
}

type Codec struct {
	enabled bool
	buckets []uint32
	maxBody uint32
}

func DefaultConfig(enabled bool) Config {
	return Config{
		Enabled: enabled,
		Buckets: []uint32{256, 512, 1024, 2048, 4096, 8192, 12288, 16384, 20480},
	}
}

func New(cfg Config) (Codec, error) {
	if !cfg.Enabled {
		return Codec{}, nil
	}
	if len(cfg.Buckets) == 0 {
		cfg = DefaultConfig(true)
	}
	b := append([]uint32(nil), cfg.Buckets...)
	var prev uint32
	for i, v := range b {
		if v < 8 || (i > 0 && v <= prev) {
			return Codec{}, errors.New("recordshape: buckets must be strictly increasing and >= 8")
		}
		prev = v
	}
	return Codec{enabled: true, buckets: b, maxBody: b[len(b)-1]}, nil
}

func (c Codec) Enabled() bool { return c.enabled }

func (c Codec) WireSize(payloadLen int) (int, error) {
	if payloadLen < 0 {
		return 0, ErrFrameTooLarge
	}
	if !c.enabled {
		return 4 + payloadLen, nil
	}
	need := uint32(4 + payloadLen)
	for _, b := range c.buckets {
		if need <= b {
			return 4 + int(b), nil
		}
	}
	return 0, ErrNoBucket
}

func (c Codec) WriteFrame(w io.Writer, payload []byte, maxPayload int) error {
	if len(payload) == 0 || len(payload) > maxPayload {
		return ErrFrameTooLarge
	}
	if !c.enabled {
		return writeLengthPrefixed(w, payload)
	}

	need := uint32(4 + len(payload))
	var bucket uint32
	for _, b := range c.buckets {
		if need <= b {
			bucket = b
			break
		}
	}
	if bucket == 0 {
		return ErrNoBucket
	}

	body := make([]byte, bucket)
	binary.BigEndian.PutUint32(body[:4], uint32(len(payload)))
	copy(body[4:], payload)

	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], bucket)
	if err := writeFull(w, hdr[:]); err != nil {
		return err
	}
	return writeFull(w, body)
}

func (c Codec) ReadFrame(r io.Reader, maxPayload int) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return nil, ErrFrameTooLarge
	}
	if !c.enabled {
		if n > uint32(maxPayload) {
			return nil, ErrFrameTooLarge
		}
		out := make([]byte, n)
		if _, err := io.ReadFull(r, out); err != nil {
			return nil, err
		}
		return out, nil
	}
	if n > c.maxBody || n < 4 {
		return nil, ErrFrameTooLarge
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	payloadLen := binary.BigEndian.Uint32(body[:4])
	if payloadLen == 0 || payloadLen > uint32(maxPayload) || payloadLen > n-4 {
		return nil, ErrFrameTooLarge
	}
	out := make([]byte, payloadLen)
	copy(out, body[4:4+payloadLen])
	return out, nil
}

func writeLengthPrefixed(w io.Writer, payload []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if err := writeFull(w, hdr[:]); err != nil {
		return err
	}
	return writeFull(w, payload)
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
