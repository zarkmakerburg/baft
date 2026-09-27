package recordshape

import (
	"bytes"
	"testing"
)

func TestBucketedRoundTripAndBoundedWireSizes(t *testing.T) {
	c, err := New(DefaultConfig(true))
	if err != nil {
		t.Fatal(err)
	}

	payloads := [][]byte{
		bytes.Repeat([]byte{1}, 1),
		bytes.Repeat([]byte{2}, 200),
		bytes.Repeat([]byte{3}, 500),
		bytes.Repeat([]byte{4}, 1000),
		bytes.Repeat([]byte{5}, 3000),
		bytes.Repeat([]byte{6}, 9000),
		bytes.Repeat([]byte{7}, 16000),
	}

	allowed := map[int]bool{260:true,516:true,1028:true,2052:true,4100:true,8196:true,12292:true,16388:true,20484:true}
	seen := map[int]bool{}

	for _, p := range payloads {
		var buf bytes.Buffer
		if err := c.WriteFrame(&buf, p, 16*1024+16); err != nil {
			t.Fatal(err)
		}
		if !allowed[buf.Len()] {
			t.Fatalf("wire size %d not in bounded bucket set", buf.Len())
		}
		seen[buf.Len()] = true
		got, err := c.ReadFrame(&buf, 16*1024+16)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, p) {
			t.Fatalf("round trip mismatch len=%d", len(p))
		}
	}

	if len(seen) < 4 {
		t.Fatalf("expected multiple deterministic buckets, saw %d", len(seen))
	}
}

func TestDisabledPreservesLengthPrefixedBehavior(t *testing.T) {
	c, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	p := bytes.Repeat([]byte{9}, 123)
	var buf bytes.Buffer
	if err := c.WriteFrame(&buf, p, 1024); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.Len(), 4+len(p); got != want {
		t.Fatalf("wire size=%d want=%d", got, want)
	}
	out, err := c.ReadFrame(&buf, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, p) {
		t.Fatal("round trip mismatch")
	}
}
