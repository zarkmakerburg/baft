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
	cert                     Envelope
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
	for _, name := range []string{"baft-linux-amd64", "baft-linux-arm64", "baft-pair-linux-amd64", "baft-bcc-linux-arm64"} {
		if err := os.WriteFile(filepath.Join(f.dir, name), []byte("binary:"+name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *fixture) sign(t *testing.T) Manifest {
	t.Helper()
	m, err := SignDir(SignInput{
		Dir: f.dir, Version: "v1.0.0-rc1", Commit: testCommit, CreatedAt: f.now,
		Provenance: Provenance{Builder: "test", GoVersion: "go1.27.1"},
		Key:        f.relKey, Cert: f.cert, Root: f.rootPub,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *fixture) verify(rev *Envelope) error {
	_, err := VerifyDir(VerifyInput{Dir: f.dir, Root: f.rootPub, Revocations: rev})
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
	got, err := VerifyDir(VerifyInput{Dir: f.dir, Root: f.rootPub})
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
	m, err := VerifyDir(VerifyInput{Dir: f.dir, Root: f.rootPub})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(m)
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
	f.now = f.notAfter.Add(time.Hour)
	f.sign(t)
	wantErr(t, f.verify(nil), "outside the release key's validity")
}

func TestRevokedReleaseKeyRejected(t *testing.T) {
	f := newFixture(t)
	f.sign(t)
	rev, err := SignRevocations(f.rootKey, []string{"00", KeyID(f.relPub)}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.verify(&rev), "is revoked")

	clean, _ := SignRevocations(f.rootKey, []string{"00"}, f.now)
	if err := f.verify(&clean); err != nil {
		t.Fatalf("unrelated revocation should not block: %v", err)
	}
	// A revocation list not signed by the root is itself rejected.
	_, otherRoot, _ := GenerateKey()
	forged, _ := SignRevocations(otherRoot, nil, f.now)
	wantErr(t, f.verify(&forged), "revocation list: signed by key")
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
	rev, _ := SignRevocations(root, nil, time.Now())
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
