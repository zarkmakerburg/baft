package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFileRejectsUnknownExtension(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.txt")
	if err := os.WriteFile(p, []byte("schema_version: 1"), 0o600); err != nil { t.Fatal(err) }
	if _, err := LoadFile(p); err == nil { t.Fatal("expected extension rejection") }
}

func TestLoadFileRejectsOversize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	f, err := os.Create(p); if err != nil { t.Fatal(err) }
	if err := f.Truncate(maxConfigFileBytes+1); err != nil { t.Fatal(err) }
	_ = f.Close()
	if _, err := LoadFile(p); err == nil { t.Fatal("expected size rejection") }
}
