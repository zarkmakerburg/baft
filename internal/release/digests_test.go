package release

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Signing from digests must give byte-identical metadata to signing the
// files, and the result must verify against the real artifacts.
func TestSignDigestsMatchesSignDir(t *testing.T) {
	f := newFixture(t)
	arts, err := ScanDigests(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(t.TempDir(), "candidate-digests.json")
	if err := WriteDigests(list, arts); err != nil {
		t.Fatal(err)
	}
	read, err := ReadDigests(list)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	in := SignInput{Version: "v1.0.0-rc1", Commit: testCommit, CreatedAt: f.now,
		Provenance: Provenance{Builder: "test", GoVersion: "go1.27.1"}, Key: f.relKey, Cert: f.cert, Root: f.rootPub}
	if _, err := SignDigests(in, read, out); err != nil {
		t.Fatal(err)
	}
	f.sign(t)
	for _, name := range []string{SumsFile, ManifestFile, CertFile} {
		a, _ := os.ReadFile(filepath.Join(out, name))
		b, _ := os.ReadFile(filepath.Join(f.dir, name))
		if len(a) == 0 || !bytes.Equal(a, b) {
			t.Fatalf("%s differs between SignDigests and SignDir", name)
		}
	}
	// The digest-signed metadata verifies with the real artifacts...
	for _, name := range []string{SumsFile, ManifestFile, CertFile} {
		b, _ := os.ReadFile(filepath.Join(out, name))
		if err := os.WriteFile(filepath.Join(f.dir, name), b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.verify(nil); err != nil {
		t.Fatal(err)
	}
	// ...and not with an artifact that differs from the signed digest.
	if err := os.WriteFile(filepath.Join(f.dir, "baft-linux-amd64"), []byte("other"), 0755); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.verify(nil), "does not match")
}

func TestDigestListValidation(t *testing.T) {
	good := Artifact{Name: "baft-linux-amd64", OS: "linux", Arch: "amd64", Size: 3, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	second := good
	second.Name, second.Arch = "baft-linux-arm64", "arm64"
	if err := ValidateDigests([]Artifact{good, second}); err != nil {
		t.Fatal(err)
	}
	mutate := func(fn func(*Artifact)) []Artifact { a := good; fn(&a); return []Artifact{a} }
	cases := map[string][]Artifact{
		"empty":         nil,
		"bad name":      mutate(func(a *Artifact) { a.Name = "../baft-linux-amd64" }),
		"meta name":     mutate(func(a *Artifact) { a.Name = "SHA256SUMS" }),
		"arch mismatch": mutate(func(a *Artifact) { a.Arch = "arm64" }),
		"zero size":     mutate(func(a *Artifact) { a.Size = 0 }),
		"upper hash":    mutate(func(a *Artifact) { a.SHA256 = "0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef" }),
		"short hash":    mutate(func(a *Artifact) { a.SHA256 = "00" }),
		"duplicate":     {good, good},
		"unsorted":      {second, good},
	}
	for name, arts := range cases {
		if err := ValidateDigests(arts); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	path := filepath.Join(t.TempDir(), "d.json")
	os.WriteFile(path, []byte(`[{"name":"baft-linux-amd64","os":"linux","arch":"amd64","size":3,"sha256":"`+good.SHA256+`","extra":1}]`), 0644)
	if _, err := ReadDigests(path); err == nil {
		t.Error("unknown field accepted")
	}
}
