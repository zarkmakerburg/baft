package recordshape

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/rand"
	"sync"
	"testing"
	"time"
)

func stats(x []int) (mean, sd, entropy, maxcorr float64, unique int) {
	counts := map[int]int{}
	for _, v := range x {
		mean += float64(v)
		counts[v]++
	}
	mean /= float64(len(x))
	for _, v := range x {
		sd += math.Pow(float64(v)-mean, 2)
	}
	variance := sd / float64(len(x))
	sd = math.Sqrt(variance)
	for _, n := range counts {
		p := float64(n) / float64(len(x))
		entropy -= p * math.Log2(p)
	}
	if variance > 0 {
		for lag := 1; lag <= 16; lag++ {
			var cov float64
			for i := lag; i < len(x); i++ {
				cov += (float64(x[i]) - mean) * (float64(x[i-lag]) - mean)
			}
			corr := math.Abs(cov / float64(len(x)-lag) / variance)
			if corr > maxcorr {
				maxcorr = corr
			}
		}
	}
	return mean, sd, entropy, maxcorr, len(counts)
}
func healthy(x []int) bool { _, sd, h, c, u := stats(x); return sd > 50 && h > 7 && c < 0.2 && u > 150 }
func TestMorphingStatistics1000(t *testing.T) {
	for _, dist := range []string{"normal", "laplace"} {
		t.Run(dist, func(t *testing.T) {
			cfg := DefaultConfig(true)
			cfg.Distribution = dist
			cfg.JitterMinUS = 0
			cfg.JitterMaxUS = 0
			c, err := newCodec(cfg, rand.New(rand.NewSource(441)))
			if err != nil {
				t.Fatal(err)
			}
			p := bytes.Repeat([]byte{42}, 1024)
			lengths := make([]int, 1000)
			padding := make([]int, 0, 200000)
			for i := range lengths {
				var b bytes.Buffer
				if err := c.WriteFrame(&b, p, 16400); err != nil {
					t.Fatal(err)
				}
				lengths[i] = b.Len()
				for _, v := range b.Bytes()[8+len(p):] {
					padding = append(padding, int(v))
				}
				got, err := c.ReadFrame(&b, 16400)
				if err != nil || !bytes.Equal(got, p) {
					t.Fatalf("round trip: %v", err)
				}
			}
			mean, sd, h, corr, u := stats(lengths)
			_, _, byteH, _, _ := stats(padding)
			t.Logf("n=1000 mean=%.2f sd=%.2f length_entropy=%.3f unique=%d max_abs_autocorrelation=%.3f padding_byte_entropy=%.3f", mean, sd, h, u, corr, byteH)
			if !healthy(lengths) || byteH < 7.9 {
				t.Fatal("statistical regression / repeated length signature")
			}
			// Discrete KS distance against the configured truncated distribution.
			cdf := func(x float64) float64 {
				z := (x - cfg.MeanBytes) / cfg.StdDevBytes
				if dist == "normal" {
					return .5 * math.Erfc(-z/math.Sqrt2)
				}
				if z < 0 {
					return .5 * math.Exp(z*math.Sqrt2)
				}
				return 1 - .5*math.Exp(-z*math.Sqrt2)
			}
			a, b := cdf(-.5), cdf(float64(cfg.MaxPaddingBytes)+.5)
			var ks float64
			for k := 0; k <= cfg.MaxPaddingBytes; k++ {
				count := 0
				for _, v := range lengths {
					if v-1032 <= k {
						count++
					}
				}
				d := math.Abs(float64(count)/1000 - (cdf(float64(k)+.5)-a)/(b-a))
				ks = math.Max(ks, d)
			}
			t.Logf("discrete KS distance=%.4f", ks)
			if ks > .07 {
				t.Fatal("sampler deviates from configured CDF")
			}
		})
	}
}
func TestStatisticalGateRejectsFixedBucketAndPeriodicMutants(t *testing.T) {
	for _, pattern := range [][]int{{2048}, {1024, 2048}, {256, 512, 1024, 2048, 4096, 8192}} {
		x := make([]int, 1000)
		for i := range x {
			x[i] = pattern[i%len(pattern)]
		}
		if healthy(x) {
			t.Fatalf("gate accepted %v", pattern)
		}
	}
	x := make([]int, 1000)
	for i := range x {
		x[i] = i % 256
	}
	if healthy(x) {
		t.Fatal("gate accepted correlated sawtooth with high entropy")
	}
}
func TestMorphingRealEntropyAndConcurrency(t *testing.T) {
	cfg := DefaultConfig(true)
	cfg.JitterMaxUS = 0
	cfg.JitterMinUS = 0
	c, _ := New(cfg)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seen := map[int]bool{}
			for j := 0; j < 100; j++ {
				var b bytes.Buffer
				if err := c.WriteFrame(&b, make([]byte, 1024), 2048); err != nil {
					t.Error(err)
					return
				}
				seen[b.Len()] = true
			}
			if len(seen) < 40 {
				t.Error("entropy source stuck")
			}
		}()
	}
	wg.Wait()
}
func TestMorphingBoundsAndMalformedFrames(t *testing.T) {
	cfg := DefaultConfig(true)
	cfg.JitterMinUS = 0
	cfg.JitterMaxUS = 0
	c, _ := New(cfg)
	for _, n := range []int{1, 200, 1000, 16400, MaxBodySize - 4} {
		var b bytes.Buffer
		p := bytes.Repeat([]byte{9}, n)
		if err := c.WriteFrame(&b, p, n); err != nil {
			t.Fatal(err)
		}
		max, err := c.WireSize(n)
		if err != nil || b.Len() > max || b.Len() < n+8 {
			t.Fatal("bounds")
		}
		out, err := c.ReadFrame(&b, n)
		if err != nil || !bytes.Equal(p, out) {
			t.Fatal("round trip", err)
		}
	}
	for _, hdr := range [][2]uint32{{0, 0}, {4, 0}, {MaxBodySize + 1, 1}, {65542, 1}, {10, 20}, {10, 0}} {
		var b bytes.Buffer
		_ = binary.Write(&b, binary.BigEndian, hdr)
		if _, err := c.ReadFrame(&b, 1024); !errors.Is(err, ErrFrameTooLarge) {
			t.Fatalf("accepted %v: %v", hdr, err)
		}
	}
	for _, n := range []int{-1, 0, MaxBodySize} {
		if _, err := c.WireSize(n); err == nil {
			t.Fatal("invalid size accepted")
		}
	}
	var b bytes.Buffer
	_ = c.WriteFrame(&b, []byte("payload"), 1024)
	raw := b.Bytes()
	for i := 0; i < len(raw); i++ {
		if _, err := c.ReadFrame(bytes.NewReader(raw[:i]), 1024); err == nil {
			t.Fatalf("accepted truncation at %d", i)
		}
	}
}
func TestDisabledPreservesLengthPrefixedBehavior(t *testing.T) {
	c, _ := New(Config{})
	var b bytes.Buffer
	p := []byte("abc")
	if err := c.WriteFrame(&b, p, 10); err != nil {
		t.Fatal(err)
	}
	if b.Len() != 7 {
		t.Fatal("legacy framing")
	}
	out, err := c.ReadFrame(&b, 10)
	if err != nil || !bytes.Equal(p, out) {
		t.Fatal(err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type shortWriter struct{ bytes.Buffer }

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > 3 {
		p = p[:3]
	}
	return w.Buffer.Write(p)
}
func TestMorphingFailureAndShortWrites(t *testing.T) {
	cfg := DefaultConfig(true)
	cfg.JitterMinUS = 0
	cfg.JitterMaxUS = 0
	c, _ := newCodec(cfg, errorReader{})
	var b bytes.Buffer
	if err := c.WriteFrame(&b, make([]byte, 100), 1024); err == nil || b.Len() != 0 {
		t.Fatal("random failure must write nothing")
	}
	c, _ = New(cfg)
	w := &shortWriter{}
	if err := c.WriteFrame(w, []byte("hello"), 10); err != nil {
		t.Fatal(err)
	}
	out, err := c.ReadFrame(&w.Buffer, 10)
	if err != nil || string(out) != "hello" {
		t.Fatal(err)
	}
}
func TestJitterDistributionAndCancellation(t *testing.T) {
	cfg := DefaultConfig(true)
	c, _ := newCodec(cfg, rand.New(rand.NewSource(56)))
	x := make([]int, 1000)
	for i := range x {
		d, err := c.SampleJitter()
		if err != nil || d < 25*time.Microsecond || d > 250*time.Microsecond {
			t.Fatal(d, err)
		}
		x[i] = int(d / time.Microsecond)
	}
	_, sd, h, corr, _ := stats(x)
	if sd < 50 || h < 7 || corr > .2 {
		t.Fatal("jitter distribution collapsed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var b bytes.Buffer
	if err := c.WriteFrameContext(ctx, &b, []byte("x"), 2); !errors.Is(err, context.Canceled) || b.Len() != 0 {
		t.Fatal("cancel did not prevent write")
	}
}
func TestInvalidMorphingConfig(t *testing.T) {
	for _, mutate := range []func(*Config){func(c *Config) { c.StdDevBytes = math.NaN() }, func(c *Config) { c.MeanBytes = math.Inf(1) }, func(c *Config) { c.MaxPaddingRatio = -1 }, func(c *Config) { c.JitterMaxUS = -1 }, func(c *Config) { c.Distribution = "buckets" }, func(c *Config) { c.MaxPaddingBytes = 65537 }} {
		c := DefaultConfig(true)
		mutate(&c)
		if _, err := New(c); err == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
}
func FuzzMorphingReadFrame(f *testing.F) {
	f.Add([]byte{0, 0, 0, 5, 0, 0, 0, 1, 42})
	c, _ := New(DefaultConfig(true))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = c.ReadFrame(bytes.NewReader(b), 16400) })
}

// This measures codec cost only, not HTTPS/TCP goodput or detectability.
func BenchmarkMorphing(b *testing.B) {
	for _, mode := range []string{"disabled", "padding-only", "padding-and-jitter"} {
		b.Run(mode, func(b *testing.B) {
			cfg := DefaultConfig(mode != "disabled")
			if mode == "padding-only" {
				cfg.JitterMinUS = 0
				cfg.JitterMaxUS = 0
			}
			c, err := New(cfg)
			if err != nil {
				b.Fatal(err)
			}
			p := make([]byte, 16400)
			b.SetBytes(int64(len(p)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := c.WriteFrame(io.Discard, p, 16400); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
