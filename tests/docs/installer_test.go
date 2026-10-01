package docs_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The runtime refuses a private key that group or other can access
// (node.requirePrivateKeyPermissions), and the service runs as $BAFT_USER.
// install.sh cannot be executed in CI, so this pins the part of it that must
// agree with that contract: every key the service reads is owned by the
// service user and is owner-only.
func TestInstallerKeyOwnershipMatchesRuntimeContract(t *testing.T) {
	b, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	for _, key := range []string{`"$NOISE_KEY"`, `"$pki/server.key"`} {
		q := regexp.QuoteMeta(key)
		if !regexp.MustCompile(`(?m)^\s*chown "\$BAFT_USER:\$BAFT_USER" ` + q + `\s*$`).MatchString(script) {
			t.Errorf("install.sh does not give %s to the service user", key)
		}
		modes := regexp.MustCompile(`(?m)^\s*chmod (\d+) .*` + q).FindAllStringSubmatch(script, -1)
		if len(modes) == 0 {
			t.Errorf("install.sh never sets a mode on %s", key)
		}
		for _, m := range modes {
			if m[1] != "0600" {
				t.Errorf("install.sh sets mode %s on %s; the runtime requires owner-only (0600)", m[1], key)
			}
		}
	}
	// The CA signing key must stay out of the service user's reach.
	if strings.Contains(script, `chown "$BAFT_USER:$BAFT_USER" "$pki/ca.key"`) || regexp.MustCompile(`chown [^\n]*BAFT_USER[^\n]*ca\.key`).MatchString(script) {
		t.Error("install.sh gives the CA private key to the service user")
	}
}

// install.sh pins the release root key it trusts. Once the owner commits
// release/keys/root.pub, the installer must pin exactly that key.
func TestInstallerPinsTheCommittedReleaseRoot(t *testing.T) {
	b, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^BAFT_PINNED_ROOT_PUB="([^"]*)"$`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("install.sh has no BAFT_PINNED_ROOT_PUB line")
	}
	root, err := os.ReadFile("../../release/keys/root.pub")
	if os.IsNotExist(err) {
		if m[1] != "" {
			t.Fatalf("install.sh pins root %q but release/keys/root.pub is not committed", m[1])
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(string(root)); m[1] != want {
		t.Fatalf("install.sh pins root %q, release/keys/root.pub is %q", m[1], want)
	}
}
