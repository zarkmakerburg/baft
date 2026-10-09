package bcc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicDoesNotFollowStaleTemporarySymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "backup.baftbak")
	victim := filepath.Join(dir, "unrelated")
	if err := os.WriteFile(victim, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	// The old fixed temporary path would truncate the symlink target.
	if err := os.Symlink(victim, target+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(target, []byte("encrypted"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != "preserve" {
		t.Fatalf("stale symlink target changed: %q, %v", got, err)
	}
	got, err = os.ReadFile(target)
	if err != nil || string(got) != "encrypted" {
		t.Fatalf("atomic target = %q, %v", got, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("backup mode = %v, %v", info, err)
	}
}
