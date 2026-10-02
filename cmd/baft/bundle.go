package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// baft support-bundle writes one owner-only archive an operator can attach to
// a report: status, doctor, the configuration, release state, service unit,
// recent logs and metrics. It never includes key files, pairing files or
// tokens, and every text file passes through redaction before it is stored.
// It only reads the host.

const bundleVersion = 1

var redactions = []struct {
	name string
	re   *regexp.Regexp
	to   string
}{
	{"pem-private-key", regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`), "[redacted private key]"},
	{"pairing-code", regexp.MustCompile(`BAFT(?:PAIR|REPLY)1:[A-Za-z0-9_-]+`), "[redacted pairing code]"},
	{"bearer-token", regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`), "${1}[redacted]"},
	{"secret-assignment", regexp.MustCompile(`(?i)((?:token|password|passphrase|secret|private[_-]?key|api[_-]?key)["']?\s*[:=]\s*["']?)[^\s"',;]{4,}`), "${1}[redacted]"},
}

// redact removes secrets from text and counts what it removed by rule.
func redact(b []byte, counts map[string]int) []byte {
	for _, r := range redactions {
		n := len(r.re.FindAll(b, -1))
		if n == 0 {
			continue
		}
		counts[r.name] += n
		b = r.re.ReplaceAll(b, []byte(r.to))
	}
	return b
}

type bundleFile struct {
	Name   string `json:"name"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type bundleManifest struct {
	Version     int               `json:"bundle_version"`
	Created     string            `json:"created_utc"`
	Binary      string            `json:"binary_version"`
	Service     string            `json:"service"`
	Files       []bundleFile      `json:"files"`
	Redactions  map[string]int    `json:"redactions"`
	Unavailable map[string]string `json:"unavailable,omitempty"`
	Note        string            `json:"note"`
}

type bundleBuilder struct {
	files       map[string][]byte
	unavailable map[string]string
	counts      map[string]int
}

func newBundleBuilder() *bundleBuilder {
	return &bundleBuilder{files: map[string][]byte{}, unavailable: map[string]string{}, counts: map[string]int{}}
}

// add stores a redacted copy: callers reuse their buffers, so the stored bytes
// must never alias them.
func (b *bundleBuilder) add(name string, data []byte) {
	b.files[name] = redact(append([]byte(nil), data...), b.counts)
}

func (b *bundleBuilder) miss(name string, err error) { b.unavailable[name] = err.Error() }

func runSupportBundle(args []string, stdout, stderr io.Writer, env opsEnv, now func() time.Time) int {
	var out string
	var lines int
	f, ok := parseOpsFlags("support-bundle", args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&out, "out", "", "output archive (default baft-support-<UTC time>.tar.gz in the current directory)")
		fs.IntVar(&lines, "n", 500, "journal lines to include")
	})
	if !ok || f.json || lines < 0 {
		fmt.Fprintln(stderr, "usage: baft support-bundle [--file baft.yaml] [--service baft] [--release-state path] [--out file.tar.gz] [-n 500]")
		return 2
	}
	t := now().UTC()
	if out == "" {
		out = "baft-support-" + t.Format("20060102T150405Z") + ".tar.gz"
	}
	b := newBundleBuilder()

	var buf, errBuf bytes.Buffer
	runStatus([]string{"--file", f.file, "--service", f.service, "--release-state", f.releaseState, "--json"}, &buf, &errBuf, env)
	b.add("status.json", buf.Bytes())
	buf.Reset()
	runDoctor([]string{"--file", f.file, "--service", f.service, "--release-state", f.releaseState, "--json"}, &buf, &errBuf, env)
	b.add("doctor.json", buf.Bytes())

	for name, path := range map[string]string{"config.yaml": f.file, "release-state.json": f.releaseState} {
		data, err := os.ReadFile(path)
		if err != nil {
			b.miss(name, err)
			continue
		}
		b.add(name, data)
	}
	if s, err := env.output("systemctl", "cat", f.service); err != nil {
		b.miss("unit.txt", err)
	} else {
		b.add("unit.txt", []byte(s))
	}
	if s, err := env.output("systemctl", "show", f.service, "-p", "ActiveState,SubState,NRestarts,MainPID,ExecMainStatus,ActiveEnterTimestamp"); err != nil {
		b.miss("service-show.txt", err)
	} else {
		b.add("service-show.txt", []byte(s))
	}
	buf.Reset()
	if err := env.stream("journalctl", []string{"-u", f.service, "-n", strconv.Itoa(lines), "--no-pager", "-o", "short-iso"}, &buf, &errBuf); err != nil {
		b.miss("journal.txt", err)
	} else {
		b.add("journal.txt", buf.Bytes())
	}
	if s, err := env.output("uname", "-a"); err != nil {
		b.miss("system.txt", err)
	} else {
		sys := s + "\n"
		if osr, err := os.ReadFile("/etc/os-release"); err == nil {
			sys += "\n" + string(osr)
		}
		b.add("system.txt", []byte(sys))
	}

	archive, err := b.archive(t, f.service)
	if err != nil {
		fmt.Fprintln(stderr, "baft support-bundle:", err)
		return 1
	}
	// O_EXCL: never overwrite an existing file; 0600: it may name hosts and identities.
	file, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		fmt.Fprintln(stderr, "baft support-bundle:", err)
		return 1
	}
	if _, err := file.Write(archive); err != nil {
		file.Close()
		os.Remove(out)
		fmt.Fprintln(stderr, "baft support-bundle:", err)
		return 1
	}
	if err := file.Close(); err != nil {
		fmt.Fprintln(stderr, "baft support-bundle:", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (%d files). Secrets are redacted; the bundle still names this host, addresses and node identities, so review it before sharing.\n", out, len(b.files)+1)
	return 0
}

func (b *bundleBuilder) archive(t time.Time, service string) ([]byte, error) {
	m := bundleManifest{
		Version: bundleVersion, Created: t.Format(time.RFC3339), Binary: version, Service: service,
		Redactions: b.counts, Unavailable: b.unavailable,
		Note: "Key files, pairing files and tokens are never collected; text is redacted before storing. Addresses, hostnames and node identities are not redacted.",
	}
	names := make([]string, 0, len(b.files))
	for n := range b.files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		sum := sha256.Sum256(b.files[n])
		m.Files = append(m.Files, bundleFile{Name: n, Bytes: len(b.files[n]), SHA256: hex.EncodeToString(sum[:])})
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	put := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: t, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err := put("manifest.json", append(manifest, '\n')); err != nil {
		return nil, err
	}
	for _, n := range names {
		if err := put(n, b.files[n]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return raw.Bytes(), nil
}
