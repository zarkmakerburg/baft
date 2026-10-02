package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	secretPair   = "BAFTPAIR1:eyJhZGRyZXNzIjoiMS4yLjMuNCJ9AAAAAAAA"
	secretReply  = "BAFTREPLY1:QUJDREVGR0hJSktMTU5PUFFSU1RVVldY"
	secretBearer = "Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MA"
	secretPEM    = "-----BEGIN PRIVATE KEY-----\nMC4CAQAwBQYDK2VwBCIEIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n-----END PRIVATE KEY-----"
)

func readBundle(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = string(b)
	}
	return files
}

func TestSupportBundleCollectsEverythingAndRedactsSecrets(t *testing.T) {
	h := newFakeHost(t)
	cfg, keyPath := writeDialerConfig(t, 0o600)
	os.WriteFile(keyPath, []byte(`{"private":"NOISE-PRIVATE-KEY-MATERIAL"}`), 0o600)
	// A config that (wrongly) carries inline secrets must still be scrubbed.
	data, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, append(data, []byte("admin_token: hunter2-hunter2\n")...), 0o600)
	rs := writeReleaseState(t, "v0.1.0")
	inner := h.env.output
	h.env.output = func(name string, args ...string) (string, error) {
		switch {
		case name == "systemctl" && args[0] == "cat":
			return "[Service]\nExecStart=/usr/local/bin/baft run --file /etc/baft/baft.yaml\nEnvironment=BAFT_TOKEN=supersecretvalue1\n", nil
		case name == "uname":
			return "Linux test 6.1 x86_64", nil
		}
		return inner(name, args...)
	}
	h.env.stream = func(name string, args []string, stdout, _ io.Writer) error {
		fmt.Fprintf(stdout, "2026-10-02T10:00:00+0000 baft: pairing %s reply %s\n", secretPair, secretReply)
		fmt.Fprintf(stdout, "2026-10-02T10:00:01+0000 baft: Authorization: Bearer %s\n%s\n", secretBearer, secretPEM)
		return nil
	}
	out := filepath.Join(t.TempDir(), "bundle.tar.gz")
	var so, se bytes.Buffer
	if code := runSupportBundle([]string{"--file", cfg, "--release-state", rs, "--out", out, "-n", "50"}, &so, &se, h.env, func() time.Time { return time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC) }); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	st, _ := os.Stat(out)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("bundle mode %o", st.Mode().Perm())
	}
	files := readBundle(t, out)
	for _, want := range []string{"manifest.json", "status.json", "doctor.json", "config.yaml", "release-state.json", "unit.txt", "service-show.txt", "journal.txt", "system.txt"} {
		if files[want] == "" {
			t.Errorf("bundle lacks %s", want)
		}
	}
	for name, content := range files {
		for _, secret := range []string{secretPair, secretReply, secretBearer, "MC4CAQAwBQYDK2VwBCIEI", "hunter2-hunter2", "supersecretvalue1", "NOISE-PRIVATE-KEY-MATERIAL"} {
			if strings.Contains(content, secret) {
				t.Errorf("%s still contains %q", name, secret)
			}
		}
	}
	if !strings.Contains(files["journal.txt"], "[redacted pairing code]") || !strings.Contains(files["journal.txt"], "Bearer [redacted]") || !strings.Contains(files["journal.txt"], "[redacted private key]") {
		t.Errorf("journal not redacted as expected:\n%s", files["journal.txt"])
	}
	// The key file itself is never read into the bundle.
	for name := range files {
		if strings.Contains(name, "key") && name != "manifest.json" {
			t.Errorf("unexpected key-like file %s", name)
		}
	}
	var m bundleManifest
	if err := json.Unmarshal([]byte(files["manifest.json"]), &m); err != nil {
		t.Fatal(err)
	}
	if m.Version != bundleVersion || m.Created != "2026-10-02T10:00:00Z" || m.Redactions["pairing-code"] != 2 || m.Redactions["pem-private-key"] != 1 {
		t.Fatalf("manifest %+v", m)
	}
	for _, f := range m.Files {
		sum := sha256.Sum256([]byte(files[f.Name]))
		if hex.EncodeToString(sum[:]) != f.SHA256 || len(files[f.Name]) != f.Bytes {
			t.Errorf("manifest entry for %s does not match", f.Name)
		}
	}
}

func TestSupportBundleToleratesMissingPiecesAndNeverOverwrites(t *testing.T) {
	h := newFakeHost(t)
	h.env.stream = func(string, []string, io.Writer, io.Writer) error { return errors.New("journalctl: not found") }
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	var so, se bytes.Buffer
	args := []string{"--file", filepath.Join(t.TempDir(), "absent.yaml"), "--release-state", filepath.Join(t.TempDir(), "absent.json"), "--out", out}
	if code := runSupportBundle(args, &so, &se, h.env, time.Now); code != 0 {
		t.Fatalf("a host with missing pieces must still produce a bundle: %d %s", code, se.String())
	}
	var m bundleManifest
	json.Unmarshal([]byte(readBundle(t, out)["manifest.json"]), &m)
	for _, name := range []string{"config.yaml", "release-state.json", "journal.txt", "unit.txt"} {
		if m.Unavailable[name] == "" {
			t.Errorf("%s not recorded as unavailable", name)
		}
	}
	before, _ := os.ReadFile(out)
	if code := runSupportBundle(args, &so, &se, h.env, time.Now); code == 0 {
		t.Fatal("an existing bundle was overwritten")
	}
	after, _ := os.ReadFile(out)
	if !bytes.Equal(before, after) {
		t.Fatal("the existing bundle changed")
	}
	if code := runSupportBundle([]string{"--json"}, &so, &se, h.env, time.Now); code != 2 {
		t.Fatalf("--json should be a usage error, got %d", code)
	}
}
