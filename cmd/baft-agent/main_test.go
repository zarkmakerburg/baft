package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlagsAndSecrets(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"--node-id", "ex-1"}, &out); code != 2 {
		t.Fatalf("missing --bcc-url = %d", code)
	}
	out.Reset()
	if code := run(context.Background(), []string{"--bcc-url", "http://bcc", "--node-id", "ex-1"}, &out); code != 2 || !strings.Contains(out.String(), "https") {
		t.Fatalf("plain HTTP accepted: %d %s", code, out.String())
	}
	dir := t.TempDir()
	tok := filepath.Join(dir, "token")
	os.WriteFile(tok, []byte("secret\n"), 0o644)
	if _, err := readSecret(tok); err == nil {
		t.Fatal("group-readable token accepted")
	}
	os.Chmod(tok, 0o600)
	if v, err := readSecret(tok); err != nil || v != "secret" {
		t.Fatalf("token = %q, %v", v, err)
	}
}
