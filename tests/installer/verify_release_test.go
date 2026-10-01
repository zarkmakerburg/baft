// Package installer_test checks install.sh's release verifier (python3 and
// the openssl CLI) against releases signed by internal/release, so the two
// implementations of the verification rules cannot drift apart.
package installer_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/release"
)

const commitA = "4545a784cb6c043ac1f97f88c810c422144d7823"

var commitB = strings.Repeat("b", 40)

var artifacts = []string{"baft-linux-amd64", "baft-linux-arm64", "baft-pair-linux-amd64", "baft-pair-linux-arm64"}

type rig struct {
	t               *testing.T
	rootPub         ed25519.PublicKey
	rootKey, relKey ed25519.PrivateKey
	relPub          ed25519.PublicKey
	cert            release.Envelope
	revFile, state  string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	for _, tool := range []string{"bash", "python3", "openssl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	r := &rig{t: t, state: filepath.Join(t.TempDir(), "release-state.json")}
	var err error
	if r.rootPub, r.rootKey, err = release.GenerateKey(); err != nil {
		t.Fatal(err)
	}
	if r.relPub, r.relKey, err = release.GenerateKey(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if r.cert, err = release.Certify(r.rootKey, r.relPub, now.Add(-time.Hour), now.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	r.revFile = r.revocations(1, now.Add(-time.Hour), now.Add(24*time.Hour))
	return r
}

func (r *rig) revocations(seq uint64, from, to time.Time, ids ...string) string {
	r.t.Helper()
	env, err := release.SignRevocations(r.rootKey, seq, ids, from, to)
	if err != nil {
		r.t.Fatal(err)
	}
	path := filepath.Join(r.t.TempDir(), "revocations.json")
	if err := release.WriteEnvelope(path, env); err != nil {
		r.t.Fatal(err)
	}
	return path
}

// release signs a full release, then keeps only what the installer
// downloads: the metadata and the amd64 binaries.
func (r *rig) release(version, commit string) string {
	r.t.Helper()
	dir := r.t.TempDir()
	for _, a := range artifacts {
		if err := os.WriteFile(filepath.Join(dir, a), []byte(a+" "+version+" "+commit), 0755); err != nil {
			r.t.Fatal(err)
		}
	}
	if _, err := release.SignDir(release.SignInput{
		Dir: dir, Version: version, Commit: commit, CreatedAt: time.Now(),
		Provenance: release.Provenance{Builder: "test", GoVersion: "go-test"},
		Key:        r.relKey, Cert: r.cert, Root: r.rootPub,
	}); err != nil {
		r.t.Fatal(err)
	}
	for _, a := range []string{"baft-linux-arm64", "baft-pair-linux-arm64"} {
		os.Remove(filepath.Join(dir, a))
	}
	return dir
}

type opts struct {
	rev, root      string
	update, downgr bool
	want           string
}

func (r *rig) verify(dir string, o opts) (string, error) {
	r.t.Helper()
	if o.rev == "" {
		o.rev = r.revFile
	}
	if o.root == "" {
		o.root = release.EncodePublic(r.rootPub)
	}
	if o.want == "" {
		o.want = "baft-linux-amd64 baft-pair-linux-amd64"
	}
	cmd := exec.Command("bash", "../../install.sh", "--verify-release", dir)
	cmd.Env = append(os.Environ(),
		"BAFT_ROOT_PUB="+o.root, "BAFT_REVOCATIONS_FILE="+o.rev, "BAFT_RELEASE_STATE="+r.state,
		"BAFT_VERIFY_ARTIFACTS="+o.want, "BAFT_UPDATE_STATE="+b(o.update), "BAFT_ALLOW_DOWNGRADE="+b(o.downgr))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func b(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func (r *rig) wantOK(dir string, o opts) string {
	r.t.Helper()
	out, err := r.verify(dir, o)
	if err != nil {
		r.t.Fatalf("verify failed: %v\n%s", err, out)
	}
	return out
}

func (r *rig) wantReject(dir string, o opts, reason string) {
	r.t.Helper()
	out, err := r.verify(dir, o)
	if err == nil {
		r.t.Fatalf("verify accepted; want rejection containing %q\n%s", reason, out)
	}
	if !strings.Contains(out, reason) {
		r.t.Fatalf("verify rejected for the wrong reason; want %q\n%s", reason, out)
	}
}

func TestInstallerAcceptsGoSignedRelease(t *testing.T) {
	r := newRig(t)
	dir := r.release("v1.0.0", commitA)
	if out := r.wantOK(dir, opts{}); !strings.Contains(out, "v1.0.0 "+commitA) {
		t.Fatalf("unexpected output %q", out)
	}
	if _, err := os.Stat(r.state); !os.IsNotExist(err) {
		t.Fatal("check-only verification wrote the trust state")
	}
}

func TestInstallerRejectsTamperingAndUntrustedKeys(t *testing.T) {
	r := newRig(t)
	dir := r.release("v1.0.0", commitA)

	os.WriteFile(filepath.Join(dir, "baft-linux-amd64"), []byte("evil"), 0755)
	r.wantReject(dir, opts{}, "hash or size does not match")

	dir = r.release("v1.0.0", commitA)
	os.WriteFile(filepath.Join(dir, "baft-worker-linux-amd64"), []byte("x"), 0755)
	r.wantReject(dir, opts{}, "not part of the signed release")

	dir = r.release("v1.0.0", commitA)
	os.Remove(filepath.Join(dir, "baft-pair-linux-amd64"))
	r.wantReject(dir, opts{}, "baft-pair-linux-amd64: not in the signed release")

	dir = r.release("v1.0.0", commitA)
	sums := filepath.Join(dir, release.SumsFile)
	b, _ := os.ReadFile(sums)
	os.WriteFile(sums, append(b, '\n'), 0644)
	r.wantReject(dir, opts{}, "SHA256SUMS does not match")

	dir = r.release("v1.0.0", commitA)
	otherRoot, _, _ := release.GenerateKey()
	r.wantReject(dir, opts{root: release.EncodePublic(otherRoot)}, "signed by key")

	// A manifest re-signed by a key the root never certified.
	_, rogue, _ := release.GenerateKey()
	dir = r.release("v1.0.0", commitA)
	m, _ := release.ReadEnvelope(filepath.Join(dir, release.ManifestFile))
	payload, _ := decode(m.Payload)
	release.WriteEnvelope(filepath.Join(dir, release.ManifestFile), release.Sign(release.PayloadTypeManifest, payload, rogue))
	r.wantReject(dir, opts{}, "manifest: signed by key")
}

func TestInstallerEnforcesRevocationList(t *testing.T) {
	r := newRig(t)
	dir := r.release("v1.0.0", commitA)
	now := time.Now()

	revoked := r.revocations(2, now.Add(-time.Hour), now.Add(time.Hour), release.KeyID(r.relPub))
	r.wantReject(dir, opts{rev: revoked}, "is revoked")

	expired := r.revocations(2, now.Add(-48*time.Hour), now.Add(-24*time.Hour))
	r.wantReject(dir, opts{rev: expired}, "revocation list: expired")

	_, otherRoot, _ := release.GenerateKey()
	forged, _ := release.SignRevocations(otherRoot, 3, nil, now, now.Add(time.Hour))
	forgedPath := filepath.Join(t.TempDir(), "forged.json")
	release.WriteEnvelope(forgedPath, forged)
	r.wantReject(dir, opts{rev: forgedPath}, "revocation list: signed by key")

	// After list 2 is recorded, list 1 is a replay.
	list2 := r.revocations(2, now.Add(-time.Hour), now.Add(time.Hour))
	r.wantOK(dir, opts{rev: list2, update: true})
	r.wantReject(dir, opts{}, "sequence 1 is older than 2")
}

func TestInstallerRefusesDowngradeAndRetag(t *testing.T) {
	r := newRig(t)
	r.wantOK(r.release("v1.2.0", commitA), opts{update: true})

	older := r.release("v1.1.9", commitA)
	r.wantReject(older, opts{}, "downgrade refused")
	r.wantOK(older, opts{downgr: true})

	retag := r.release("v1.2.0", commitB)
	r.wantReject(retag, opts{}, "already accepted as commit")
	r.wantReject(retag, opts{downgr: true}, "already accepted as commit")

	r.wantOK(r.release("v1.2.0", commitA), opts{})
	r.wantOK(r.release("v1.3.0-rc.1", commitA), opts{update: true})
}

// The installer and baft-release share one trust-state file format.
func TestTrustStateIsSharedWithGo(t *testing.T) {
	r := newRig(t)
	r.wantOK(r.release("v1.2.0", commitA), opts{update: true})
	st, err := release.ReadState(r.state)
	if err != nil {
		t.Fatalf("Go cannot read the installer's state: %v", err)
	}
	if st.Version != "v1.2.0" || st.Commit != commitA || st.RevocationSequence != 1 {
		t.Fatalf("unexpected state %+v", st)
	}
	st.Version, st.RevocationSequence = "v2.0.0", 5
	if err := release.WriteState(r.state, st); err != nil {
		t.Fatal(err)
	}
	r.wantReject(r.release("v1.9.0", commitA), opts{}, "sequence 1 is older than 5")
}

// SemVer precedence must match Go's CompareVersions, or downgrade protection
// would differ between the installer and the agent.
func TestInstallerVersionOrderMatchesGo(t *testing.T) {
	r := newRig(t)
	order := []string{"v1.0.0-alpha", "v1.0.0-alpha.1", "v1.0.0-alpha.beta", "v1.0.0-beta.2", "v1.0.0-beta.11", "v1.0.0-rc.1", "v1.0.0", "v1.0.1", "v1.10.0"}
	rels := map[string]string{}
	for _, v := range order {
		rels[v] = r.release(v, commitA)
	}
	for i, installed := range order {
		for _, candidate := range []string{order[max(i-1, 0)], order[min(i+1, len(order)-1)]} {
			os.Remove(r.state)
			st := release.TrustState{SchemaVersion: 1, Version: installed, Commit: commitA, RevocationSequence: 1}
			if err := release.WriteState(r.state, st); err != nil {
				t.Fatal(err)
			}
			c, _ := release.CompareVersions(candidate, installed)
			_, err := r.verify(rels[candidate], opts{})
			if (err == nil) != (c >= 0) {
				t.Errorf("installed %s, candidate %s: installer accepted=%v, Go order %d", installed, candidate, err == nil, c)
			}
		}
	}
}

func decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
