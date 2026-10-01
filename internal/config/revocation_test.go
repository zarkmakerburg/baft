package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRevocationFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0600); err != nil { t.Fatal(err) }
	return p
}

func TestLoadRevocationFileYAML(t *testing.T) {
	p := writeRevocationFile(t, "revoked.yaml", `
identities:
  - urn:baft:node:ir-02
serials:
  - "0A:1B:2C"
fingerprints:
  - "`+strings.Repeat("ab", 32)+`"
`)
	l, err := LoadRevocationFile(p)
	if err != nil { t.Fatal(err) }
	if len(l.Identities) != 1 || l.Identities[0] != "urn:baft:node:ir-02" || len(l.Serials) != 1 || len(l.Fingerprints) != 1 {
		t.Fatalf("unexpected list: %+v", l)
	}
}

func TestEmptyRevocationFileIsAnEmptyList(t *testing.T) {
	l, err := LoadRevocationFile(writeRevocationFile(t, "revoked.yaml", "\n"))
	if err != nil { t.Fatal(err) }
	if len(l.Identities)+len(l.Serials)+len(l.Fingerprints) != 0 { t.Fatalf("unexpected list: %+v", l) }
}

func TestRevocationFileRejectsInvalidEntries(t *testing.T) {
	for name, body := range map[string]string{
		"unknown key":        "identity: [urn:baft:node:ir-02]\n",
		"identity scheme":    "identities: [ir-02]\n",
		"empty node id":      "identities: [\"urn:baft:node:\"]\n",
		"serial not hex":     "serials: [\"0x1g\"]\n",
		"serial too long":    "serials: [\"" + strings.Repeat("ab", 21) + "\"]\n",
		"short fingerprint":  "fingerprints: [\"abcd\"]\n",
		"yaml alias":         "identities: &a [urn:baft:node:ir-02]\nserials: *a\n",
		"two yaml documents": "identities: []\n---\nserials: []\n",
	} {
		if _, err := LoadRevocationFile(writeRevocationFile(t, "revoked.yaml", body)); err == nil {
			t.Errorf("%s: invalid revocation file was accepted", name)
		}
	}
	if _, err := LoadRevocationFile(writeRevocationFile(t, "revoked.txt", "identities: []\n")); err == nil {
		t.Error("unsupported extension was accepted")
	}
}

func TestRevocationConfigValidation(t *testing.T) {
	ex, err := LoadFile("../../configs/example-ex.yaml")
	if err != nil { t.Fatal(err) }
	ex.Revocation = &Revocation{File: "/etc/baft/revoked.yaml"}
	if err := Validate(ex); err != nil { t.Fatalf("listener revocation rejected: %v", err) }
	ex.Revocation = &Revocation{File: "revoked.yaml"}
	if err := Validate(ex); err == nil { t.Fatal("relative revocation.file accepted") }

	ir, err := LoadFile("../../configs/example-ir.yaml")
	if err != nil { t.Fatal(err) }
	ir.Revocation = &Revocation{File: "/etc/baft/revoked.yaml"}
	if err := Validate(ir); err == nil { t.Fatal("dialer accepted a revocation section it does not enforce") }
}
