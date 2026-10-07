package sshmigrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fake struct {
	failTest bool
	reloads  int
}

func (f *fake) Run(_ context.Context, _ string, _ ...string) (string, error) {
	if f.failTest {
		return "bad config", errors.New("exit 1")
	}
	return "", nil
}
func (f *fake) Systemctl(_ context.Context, _ ...string) (string, error) { f.reloads++; return "", nil }
func cfg(t *testing.T) Config {
	d := t.TempDir()
	m := filepath.Join(d, "sshd_config")
	os.WriteFile(m, []byte("Include "+filepath.Join(d, "d", "*.conf")+"\n"), 0644)
	return Config{m, filepath.Join(d, "d"), "sshd", "ssh"}
}
func TestStageCommitRollback(t *testing.T) {
	c := cfg(t)
	s := &fake{}
	if err := Stage(context.Background(), c, s, 22, 2222); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dropPath(c))
	if !strings.Contains(string(b), "Port 22") || !strings.Contains(string(b), "Port 2222") {
		t.Fatal(string(b))
	}
	if err := Commit(context.Background(), c, s, 2222); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(dropPath(c))
	if strings.Contains(string(b), "Port 22\n") || !strings.Contains(string(b), "Port 2222") {
		t.Fatal(string(b))
	}
	if err := Rollback(context.Background(), c, s); err != nil {
		t.Fatal(err)
	}
}
func TestStageValidationFailureRollsBack(t *testing.T) {
	c := cfg(t)
	s := &fake{failTest: true}
	if err := Stage(context.Background(), c, s, 22, 2222); err == nil {
		t.Fatal("want failure")
	}
	if _, err := os.Stat(dropPath(c)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("drop-in survived failed validation")
	}
}
func TestPreflightRefusesUnmanagedPort(t *testing.T) {
	c := cfg(t)
	os.WriteFile(c.MainConfig, []byte("Port 22\n"), 0644)
	if err := Preflight(c, 2222); err == nil {
		t.Fatal("want refusal")
	}
}
