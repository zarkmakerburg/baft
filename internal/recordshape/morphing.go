// Package recordshape frames already-encrypted Noise records inside an HTTP
// body. It does not control TLS record boundaries or TCP packet boundaries.
package recordshape

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sync"
	"time"
)

const MaxBodySize = 1 << 20

var ErrFrameTooLarge = errors.New("recordshape: frame too large")

// Config describes a truncated, discretized padding distribution. MeanBytes
// and StdDevBytes describe the underlying distribution, not the truncated one.
// MaxPaddingRatio limits padding/payload; zero disables this additional cap.
// Both peers must explicitly enable shaping. There is no unauthenticated
// auto-detection or downgrade. Jitter is a requested delay, not a wire guarantee.
type Config struct {
	Enabled         bool    `json:"enabled"`
	Distribution    string  `json:"distribution"`
	MeanBytes       float64 `json:"mean_bytes"`
	StdDevBytes     float64 `json:"stddev_bytes"`
	MaxPaddingBytes int     `json:"max_padding_bytes"`
	MaxPaddingRatio float64 `json:"max_padding_ratio"`
	JitterMinUS     int     `json:"jitter_min_us"`
	JitterMaxUS     int     `json:"jitter_max_us"`
}

func DefaultConfig(enabled bool) Config {
	return Config{Enabled: enabled, Distribution: "normal", MeanBytes: 192,
		StdDevBytes: 96, MaxPaddingBytes: 512, MaxPaddingRatio: 0.5,
		JitterMinUS: 25, JitterMaxUS: 250}
}

type randomSource struct {
	mu sync.Mutex
	r  io.Reader
}
type Codec struct {
	cfg Config
	rng *randomSource
}

func New(cfg Config) (Codec, error) { return newCodec(cfg, rand.Reader) }
func newCodec(cfg Config, r io.Reader) (Codec, error) {
	if !cfg.Enabled {
		return Codec{}, nil
	}
	if cfg == (Config{Enabled: true}) {
		cfg = DefaultConfig(true)
	}
	if cfg.Distribution != "normal" && cfg.Distribution != "laplace" {
		return Codec{}, errors.New("recordshape: distribution must be normal or laplace")
	}
	finite := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	if cfg.MaxPaddingBytes < 1 || cfg.MaxPaddingBytes > 65536 || !finite(cfg.MeanBytes) || cfg.MeanBytes < 0 || cfg.MeanBytes > float64(cfg.MaxPaddingBytes) || !finite(cfg.StdDevBytes) || cfg.StdDevBytes < 1 || cfg.StdDevBytes > 65536 || cfg.MeanBytes > 8*cfg.StdDevBytes || !finite(cfg.MaxPaddingRatio) || cfg.MaxPaddingRatio < 0 || cfg.MaxPaddingRatio > 16 {
		return Codec{}, errors.New("recordshape: invalid padding parameters")
	}
	if cfg.JitterMinUS < 0 || cfg.JitterMaxUS < cfg.JitterMinUS || cfg.JitterMaxUS > 100000 {
		return Codec{}, errors.New("recordshape: jitter must be within 0..100000 microseconds")
	}
	return Codec{cfg: cfg, rng: &randomSource{r: r}}, nil
}
func (c Codec) Enabled() bool { return c.cfg.Enabled }
func (c Codec) paddingCap(n int) int {
	cap := c.cfg.MaxPaddingBytes
	if r := c.cfg.MaxPaddingRatio; r > 0 && float64(n)*r < float64(cap) {
		cap = int(float64(n) * r)
	}
	if left := MaxBodySize - 4 - n; cap > left {
		cap = left
	}
	return cap
}

// WireSize is a conservative maximum, never a separately sampled prediction.
func (c Codec) WireSize(n int) (int, error) {
	if n < 1 || n > MaxBodySize-4 {
		return 0, ErrFrameTooLarge
	}
	if !c.Enabled() {
		return n + 4, nil
	}
	return n + 8 + c.paddingCap(n), nil
}

func (c Codec) uniform() (float64, error) {
	var b [8]byte
	c.rng.mu.Lock()
	_, err := io.ReadFull(c.rng.r, b[:])
	c.rng.mu.Unlock()
	if err != nil {
		return 0, err
	}
	// 52 bits plus a half-unit gives an open interval (0,1).
	return (float64(binary.LittleEndian.Uint64(b[:])>>12) + 0.5) / (1 << 52), nil
}

// samplePadding inverts a truncated CDF and discretizes to integer lengths.
// No clipping creates endpoint point masses; sampling work is bounded.
func (c Codec) samplePadding(n int) (int, error) {
	cap := c.paddingCap(n)
	if cap <= 0 {
		return 0, nil
	}
	u, err := c.uniform()
	if err != nil {
		return 0, err
	}
	mean, sd := c.cfg.MeanBytes, c.cfg.StdDevBytes
	lo, hi := -0.5, float64(cap)+0.5
	// Erfc retains precision in the lower normal tail.
	cdf := func(x float64) float64 {
		z := (x - mean) / sd
		if c.cfg.Distribution == "normal" {
			return 0.5 * math.Erfc(-z/math.Sqrt2)
		}
		if z < 0 {
			return 0.5 * math.Exp(z*math.Sqrt2)
		}
		return 1 - 0.5*math.Exp(-z*math.Sqrt2)
	}
	a, b := cdf(lo), cdf(hi)
	if b <= a || b == 0 {
		return 0, errors.New("recordshape: distribution has negligible mass within padding budget")
	}
	target := a + u*(b-a)
	for i := 0; i < 48; i++ {
		mid := (lo + hi) / 2
		if cdf(mid) < target {
			lo = mid
		} else {
			hi = mid
		}
	}
	v := int(math.Floor((lo+hi)/2 + 0.5))
	if v < 0 || v > cap {
		return 0, errors.New("recordshape: padding sampler out of bounds")
	}
	return v, nil
}

func (c Codec) SampleJitter() (time.Duration, error) {
	if !c.Enabled() || c.cfg.JitterMaxUS == 0 {
		return 0, nil
	}
	u, err := c.uniform()
	if err != nil {
		return 0, err
	}
	lo, hi := float64(c.cfg.JitterMinUS)*1000, float64(c.cfg.JitterMaxUS)*1000
	return time.Duration(lo + u*(hi-lo)), nil
}
func (c Codec) Wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d, err := c.SampleJitter()
	if err != nil {
		return err
	}
	if d == 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
func (c Codec) WriteFrame(w io.Writer, p []byte, maxPayload int) error {
	return c.WriteFrameContext(context.Background(), w, p, maxPayload)
}
func (c Codec) WriteFrameContext(ctx context.Context, w io.Writer, p []byte, maxPayload int) error {
	if len(p) == 0 || maxPayload < 1 || len(p) > maxPayload || len(p) > MaxBodySize-4 {
		return ErrFrameTooLarge
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var wire []byte
	if !c.Enabled() {
		wire = make([]byte, 4+len(p))
		binary.BigEndian.PutUint32(wire, uint32(len(p)))
		copy(wire[4:], p)
	} else {
		pad, err := c.samplePadding(len(p))
		if err != nil {
			return err
		}
		wire = make([]byte, 8+len(p)+pad)
		binary.BigEndian.PutUint32(wire, uint32(len(wire)-4))
		binary.BigEndian.PutUint32(wire[4:], uint32(len(p)))
		copy(wire[8:], p)
		c.rng.mu.Lock()
		_, err = io.ReadFull(c.rng.r, wire[8+len(p):])
		c.rng.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if err := c.Wait(ctx); err != nil {
		return err
	}
	// One write avoids forcing a separate HTTP/2 flush for the length prefix.
	return writeFull(w, wire)
}
func (c Codec) ReadFrame(r io.Reader, maxPayload int) ([]byte, error) {
	if maxPayload < 1 || maxPayload > MaxBodySize-4 {
		return nil, ErrFrameTooLarge
	}
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := uint64(binary.BigEndian.Uint32(hdr[:]))
	if !c.Enabled() {
		if n == 0 || n > uint64(maxPayload) {
			return nil, ErrFrameTooLarge
		}
		out := make([]byte, int(n))
		_, err := io.ReadFull(r, out)
		return out, err
	}
	// The format-wide receive cap is independent of the local send budget.
	if n < 5 || n > MaxBodySize || n > uint64(maxPayload)+4+65536 {
		return nil, ErrFrameTooLarge
	}
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	size := uint64(binary.BigEndian.Uint32(hdr[:]))
	if size == 0 || size > uint64(maxPayload) || size > n-4 || n-4-size > 65536 {
		return nil, ErrFrameTooLarge
	}
	out := make([]byte, int(size))
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	_, err := io.CopyN(io.Discard, r, int64(n-4-size))
	return out, err
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
