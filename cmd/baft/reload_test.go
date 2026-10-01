package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/node"
)

type lineWriter chan string

func (w lineWriter) Write(p []byte) (int, error) { w <- string(p); return len(p), nil }

func TestConfigValidateChecksRevocationFile(t *testing.T) {
	dir := t.TempDir()
	revoked := filepath.Join(dir, "revoked.yaml")
	cfg := filepath.Join(dir, "ex.yaml")
	b, err := os.ReadFile("../../configs/example-ex.yaml")
	if err != nil { t.Fatal(err) }
	b = append(b, []byte("revocation:\n  file: "+revoked+"\n")...)
	if err := os.WriteFile(cfg, b, 0o600); err != nil { t.Fatal(err) }

	for _, tc := range []struct{ list string; code int }{
		{"identities: [urn:baft:node:ir-02]\n", 0},
		{"identities: [ir-02]\n", 1},
	} {
		if err := os.WriteFile(revoked, []byte(tc.list), 0o600); err != nil { t.Fatal(err) }
		var out, errOut bytes.Buffer
		if code := run([]string{"config", "validate", "--file", cfg}, &out, &errOut); code != tc.code {
			t.Fatalf("list %q: code=%d want %d (stderr %q)", tc.list, code, tc.code, errOut.String())
		}
	}
}

// Without a SIGHUP handler the Go runtime terminates the process, so
// `systemctl reload` must always reach reloadOnHUP, even with no
// revocation.file configured.
func TestSIGHUPIsHandledWithoutRevocationFile(t *testing.T) {
	lines := make(lineWriter, 4)
	stop := reloadOnHUP(context.Background(), node.NewRuntime(), lines)
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-lines:
		if !strings.Contains(line, "no revocation.file configured") {
			t.Fatalf("unexpected reload output: %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SIGHUP was not handled")
	}
}
