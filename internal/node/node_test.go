package node

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNodeIDFromIdentity(t *testing.T) {
	got, err := nodeIDFromIdentity("urn:baft:node:ir-01")
	if err != nil { t.Fatal(err) }
	if got != "ir-01" { t.Fatalf("got %q", got) }
	if _, err := nodeIDFromIdentity("spiffe://example/ir-01"); err == nil {
		t.Fatal("expected unsupported identity format")
	}
}

func TestPrivateKeyPermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "node.key")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil { t.Fatal(err) }
	if err := requirePrivateKeyPermissions(p); err != nil { t.Fatal(err) }
	if err := os.Chmod(p, 0o644); err != nil { t.Fatal(err) }
	if err := requirePrivateKeyPermissions(p); err == nil {
		t.Fatal("expected world-readable key to be rejected")
	}
}
