package release

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testCommit = "4545a784cb6c043ac1f97f88c810c422144d7823"

type fixture struct {
	dir                      string
	rootPub                  ed25519.PublicKey
	rootKey, relKey          ed25519.PrivateKey
	relPub                   ed25519.PublicKey
	cert, rev                Envelope
	notBefore, notAfter, now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{dir: t.TempDir(), now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	var err error
	if f.rootPub, f.rootKey, err = GenerateKey(); err != nil {
		t.Fatal(err)
	}
	if f.relPub, f.relKey, err = GenerateKey(); err != nil {
		t.Fatal(err)
	}
	f.notBefore, f.notAfter = f.now.Add(-time.Hour), f.now.Add(180*24*time.Hour)
	if f.cert, err = Certify(f.rootKey, f.relPub, f.notBefore, f.notAfter); err != nil {
		t.Fatal(err)
	}
	if f.rev, err = SignRevocations(f.rootKey, 1, nil, f.now.Add(-time.Hour), f.now.Add(90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"baft-linux-amd64", "baft-linux-arm64", "baft-pair-linux-amd64", "baft-bcc-linux-arm64"} {
		if err := os.WriteFile(filepath.Join(f.dir, name), []byte("binary:"+name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *fixture) sign(t *testing.T) Manifest {
	return f.signVersion(t, "v1.0.0-rc1", testCommit)
}

func (f *fixture) signVersion(t *testing.T, version, commit string) Manifest {
	t.Helper()
	for _, meta := range []string{ManifestFile, CertFile, SumsFile} {
		os.Remove(filepath.Join(f.dir, meta))
	}
	m, err := SignDir(SignInput{
		Dir: f.dir, Version: version, Commit: commit, CreatedAt: f.now,
		Provenance: Provenance{Builder: "test", GoVersion: "go1.27.1"},
		Key:        f.relKey, Cert: f.cert, Root: f.rootPub,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *fixture) input(rev *Envelope) VerifyInput {
	if rev == nil {
		rev = &f.rev
	}
	return VerifyInput{Dir: f.dir, Root: f.rootPub, Revocations: rev, Now: f.now}
}

// verify checks the release against rev, or the fixture's clean list if nil.
func (f *fixture) verify(rev *Envelope) error {
	_, err := VerifyDir(f.input(rev))
	return err
}

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Fatalf("error = %v, want one containing %q", err, substr)
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	f := newFixture(t)
	signed := f.sign(t)
	got, err := VerifyDir(f.input(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Artifacts) != 4 || got.Version != "v1.0.0-rc1" || got.SigningKeyID != KeyID(f.relPub) {
		t.Fatalf("unexpected manifest %+v", got)
	}
	if got.SHA256SUMS != signed.SHA256SUMS || got.Provenance.GoVersion != "go1.27.1" {
		t.Fatalf("verified manifest differs from signed one")
	}
	sums, _ := os.ReadFile(filepath.Join(f.dir, SumsFile))
	if !strings.Contains(string(sums), "  baft-linux-amd64\n") {
		t.Fatalf("SHA256SUMS not in sha256sum format:\n%s", sums)
	}
}

func TestTamperedArtifactRejected(t *testing.T) {
	f := newFixture(t)
	f.sign(t)
	if err := os.WriteFile(filepath.Join(f.dir, "baft-linux-arm64"), []byte("evil"), 0755); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.verify(nil), "does not match the signed manifest")
}

func TestExtraAndMissingArtifactsRejected(t *testing.T) {
	f := newFixture(t)
	f.sign(t)
	extra := filepath.Join(f.dir, "baft-worker-linux-amd64")
	os.WriteFile(extra, []byte("x"), 0755)
	wantErr(t, f.verify(nil), "not part of the signed release")
	os.Remove(extra)
	os.Remove(filepath.Join(f.dir, "baft-linux-amd64"))
	wantErr(t, f.verify(nil), "missing")
}

func TestTamperedSumsRejected(t *testing.T) {
	f := newFixture(t)
	f.sign(t)
	p := filepath.Join(f.dir, SumsFile)
	b, _ := os.ReadFile(p)
	os.WriteFile(p, append(b, '\n'), 0644)
	wantErr(t, f.verify(nil), "SHA256SUMS does not match")
}

func TestTamperedManifestRejected(t *testing.T) {
	f := newFixture(t)
	f.sign(t)
	p := filepath.Join(f.dir, ManifestFile)
	env, err := ReadEnvelope(p)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := base64.StdEncoding.DecodeString(env.Payload)
	payload = []byte(strings.Replace(string(payload), "v1.0.0-rc1", "v9.9.9", 1))
	env.Payload = base64.StdEncoding.EncodeToString(payload)
	WriteEnvelope(p, env)
	wantErr(t, f.verify(nil), "signature does not verify")
}

func TestManifestSignedByUncertifiedKeyRejected(t *testing.T) {
	f := newFixture(t)
	f.sign(t)
	_, rogue, _ := GenerateKey()
	v, err := VerifyDir(f.input(nil))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(v.Manifest)
	WriteEnvelope(filepath.Join(f.dir, ManifestFile), Sign(PayloadTypeManifest, payload, rogue))
	wantErr(t, f.verify(nil), "manifest: signed by key")
}

func TestCertNotSignedByPinnedRootRejected(t *testing.T) {
	f := newFixture(t)
	f.sign(t)
	_, otherRoot, _ := GenerateKey()
	cert, _ := Certify(otherRoot, f.relPub, f.notBefore, f.notAfter)
	WriteEnvelope(filepath.Join(f.dir, CertFile), cert)
	wantErr(t, f.verify(nil), "release key certificate: signed by key")
}

func TestSignRefusesCertForAnotherKeyOrRoot(t *testing.T) {
	f := newFixture(t)
	_, otherRel, _ := GenerateKey()
	_, err := SignDir(SignInput{Dir: f.dir, Version: "v1.0.0", Commit: testCommit, CreatedAt: f.now, Key: otherRel, Cert: f.cert, Root: f.rootPub})
	wantErr(t, err, "certificate is for key")
	otherRootPub, _, _ := GenerateKey()
	_, err = SignDir(SignInput{Dir: f.dir, Version: "v1.0.0", Commit: testCommit, CreatedAt: f.now, Key: f.relKey, Cert: f.cert, Root: otherRootPub})
	wantErr(t, err, "signed by key")
}

func TestManifestOutsideCertValidityRejected(t *testing.T) {
	f := newFixture(t)
	verifyAt := f.now
	f.now = f.notAfter.Add(time.Hour)
	f.sign(t)
	f.now = verifyAt
	wantErr(t, f.verify(nil), "outside the release key's validity")
}

const otherKeyID = "00000000000000000000000000000000"

func (f *fixture) revocations(t *testing.T, seq uint64, ids ...string) Envelope {
	t.Helper()
	rev, err := SignRevocations(f.rootKey, seq, ids, f.now.Add(-time.Hour), f.now.Add(30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return rev
}

func TestRevokedReleaseKeyRejected(t *testing.T) {
	f := newFixture(t)
	f.sign(t)
	rev := f.revocations(t, 2, otherKeyID, KeyID(f.relPub))
	wantErr(t, f.verify(&rev), "is revoked")

	clean := f.revocations(t, 2, otherKeyID)
	if err := f.verify(&clean); err != nil {
		t.Fatalf("unrelated revocation should not block: %v", err)
	}
	// A revocation list not signed by the root is itself rejected.
	_, otherRoot, _ := GenerateKey()
	forged, _ := SignRevocations(otherRoot, 3, nil, f.now, f.now.Add(time.Hour))
	wantErr(t, f.verify(&forged), "revocation list: signed by key")
}

func TestRevocationListIsRequiredAndFresh(t *testing.T) {
	f := newFixture(t)
	f.sign(t)
	in := f.input(nil)
	in.Revocations = nil
	_, err := VerifyDir(in)
	wantErr(t, err, "revocation list: required")

	// An expired list is refused even though its signature is good.
	in = f.input(nil)
	in.Now = f.now.Add(91 * 24 * time.Hour)
	_, err = VerifyDir(in)
	wantErr(t, err, "revocation list: expired")

	// A replayed older list is refused once a newer one was accepted.
	in = f.input(nil)
	in.State = &TrustState{SchemaVersion: 1, RevocationSequence: 2}
	_, err = VerifyDir(in)
	wantErr(t, err, "sequence 1 is older than 2")
	newer := f.revocations(t, 2)
	in.Revocations = &newer
	if _, err := VerifyDir(in); err != nil {
		t.Fatalf("current list rejected: %v", err)
	}
}

func TestSignRevocationsValidates(t *testing.T) {
	_, root, _ := GenerateKey()
	now := time.Now()
	cases := map[string]struct {
		seq      uint64
		ids      []string
		from, to time.Time
	}{
		"zero sequence":   {0, nil, now, now.Add(time.Hour)},
		"empty window":    {1, nil, now, now},
		"too long":        {1, nil, now, now.Add(MaxRevocationValidity + time.Hour)},
		"malformed keyid": {1, []string{"00"}, now, now.Add(time.Hour)},
	}
	for name, c := range cases {
		if _, err := SignRevocations(root, c.seq, c.ids, c.from, c.to); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDowngradeAndRetagRefused(t *testing.T) {
	f := newFixture(t)
	f.signVersion(t, "v1.2.0", testCommit)
	state := TrustState{SchemaVersion: 1}
	in := f.input(nil)
	in.State = &state
	v, err := VerifyDir(in)
	if err != nil {
		t.Fatal(err)
	}
	state = state.Advance(v, f.now)
	if state.Version != "v1.2.0" || state.RevocationSequence != 1 {
		t.Fatalf("state not advanced: %+v", state)
	}

	f.signVersion(t, "v1.1.9", testCommit)
	_, err = VerifyDir(in)
	wantErr(t, err, "downgrade refused")
	in.AllowDowngrade = true
	if _, err := VerifyDir(in); err != nil {
		t.Fatalf("explicit downgrade refused: %v", err)
	}
	in.AllowDowngrade = false

	// The same version rebuilt from another commit is never accepted.
	f.signVersion(t, "v1.2.0", strings.Repeat("a", 40))
	_, err = VerifyDir(in)
	wantErr(t, err, "already accepted as commit")
	in.AllowDowngrade = true
	_, err = VerifyDir(in)
	wantErr(t, err, "already accepted as commit")
	in.AllowDowngrade = false

	// Reinstalling the accepted release and upgrading both pass.
	f.signVersion(t, "v1.2.0", testCommit)
	if _, err := VerifyDir(in); err != nil {
		t.Fatalf("reinstall refused: %v", err)
	}
	f.signVersion(t, "v1.3.0-rc1", testCommit)
	if _, err := VerifyDir(in); err != nil {
		t.Fatalf("upgrade refused: %v", err)
	}
}

func TestAdvanceNeverLowersRevocationSequence(t *testing.T) {
	s := TrustState{SchemaVersion: 1, RevocationSequence: 5}
	s = s.Advance(Verified{Manifest: Manifest{Version: "v1.0.0", Commit: testCommit}, Revocations: Revocations{Sequence: 3}}, time.Now())
	if s.RevocationSequence != 5 {
		t.Fatalf("sequence lowered to %d", s.RevocationSequence)
	}
}

func TestTrustStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "release-state.json")
	s, err := ReadState(path)
	if err != nil || s.Version != "" || s.RevocationSequence != 0 {
		t.Fatalf("missing state = %+v, %v; want fresh", s, err)
	}
	want := TrustState{SchemaVersion: 1, Version: "v1.0.0", Commit: testCommit, RevocationSequence: 4, UpdatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	if err := WriteState(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadState(path)
	if err != nil || got != want {
		t.Fatalf("round trip = %+v, %v; want %+v", got, err, want)
	}
	os.WriteFile(path, []byte(`{"schema_version":1,"version":"latest"}`), 0644)
	if _, err := ReadState(path); err == nil {
		t.Fatal("malformed state accepted")
	}
}

func TestCompareVersions(t *testing.T) {
	// Each version is lower than the next (SemVer 2.0 section 11 example, plus core ordering).
	order := []string{
		"v0.9.9", "v1.0.0-alpha", "v1.0.0-alpha.1", "v1.0.0-alpha.beta", "v1.0.0-beta",
		"v1.0.0-beta.2", "v1.0.0-beta.11", "v1.0.0-rc.1", "v1.0.0", "v1.0.1", "v1.2.0", "v1.10.0", "v2.0.0",
	}
	for i := range order {
		for j := range order {
			c, err := CompareVersions(order[i], order[j])
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if c != want {
				t.Errorf("CompareVersions(%s, %s) = %d, want %d", order[i], order[j], c, want)
			}
		}
	}
	if _, err := CompareVersions("v1.0", "v1.0.0"); err == nil {
		t.Fatal("malformed version accepted")
	}
}

func TestCertifyBoundsValidity(t *testing.T) {
	_, root, _ := GenerateKey()
	pub, _, _ := GenerateKey()
	now := time.Now()
	if _, err := Certify(root, pub, now, now.Add(MaxCertValidity+time.Hour)); err == nil {
		t.Fatal("over-long certificate accepted")
	}
	if _, err := Certify(root, pub, now, now); err == nil {
		t.Fatal("empty validity window accepted")
	}
}

func TestEnvelopeTypeConfusionRejected(t *testing.T) {
	_, root, _ := GenerateKey()
	rootPub := root.Public().(ed25519.PublicKey)
	rev, _ := SignRevocations(root, 1, nil, time.Now(), time.Now().Add(time.Hour))
	// A root-signed revocation list must not pass as a key certificate.
	rev.PayloadType = PayloadTypeKeyCert
	if _, _, err := OpenCert(rev, rootPub); err == nil {
		t.Fatal("revocation payload accepted as certificate")
	}
}

func TestSignRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	base := SignInput{Dir: f.dir, Version: "v1.0.0", Commit: testCommit, CreatedAt: f.now, Key: f.relKey, Cert: f.cert, Root: f.rootPub}
	in := base
	in.Version = "1.0"
	_, err := SignDir(in)
	wantErr(t, err, "version")
	in = base
	in.Commit = "abc"
	_, err = SignDir(in)
	wantErr(t, err, "commit")
	os.WriteFile(filepath.Join(f.dir, "notes.txt"), []byte("x"), 0644)
	_, err = SignDir(base)
	wantErr(t, err, "artifact names")
}

func TestKeyEncodingRoundTrip(t *testing.T) {
	pub, priv, _ := GenerateKey()
	gotPriv, err := DecodePrivate(EncodePrivate(priv))
	if err != nil || !gotPriv.Equal(priv) {
		t.Fatalf("private key round trip: %v", err)
	}
	gotPub, err := DecodePublic(EncodePublic(pub) + "\n")
	if err != nil || !gotPub.Equal(pub) {
		t.Fatalf("public key round trip: %v", err)
	}
	if _, err := DecodePrivate(EncodePublic(pub) + "AA"); err == nil {
		t.Fatal("malformed private key accepted")
	}
}
