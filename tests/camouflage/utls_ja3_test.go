package camouflage

import (
	"strings"
	"testing"

	"github.com/zarkmakerburg/baft/internal/carrier/utlsdial"
)

// TestUTLSClientHelloClosesFingerprintGap proves the uTLS dial closes the two
// cheap passive distinguishers documented in reports/CARRIER-CAMOUFLAGE.md for
// Go's crypto/tls ClientHello: (1) no GREASE, (2) only three cipher suites.
// A parroted Chrome ClientHello must include GREASE and advertise many suites.
func TestUTLSClientHelloClosesFingerprintGap(t *testing.T) {
	raw, err := utlsdial.CaptureClientHello(utlsdial.Config{ServerName: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ja3, parts, grease := ja3FromClientHello(t, raw)
	t.Logf("uTLS JA3: %s", ja3)

	if !grease {
		t.Error("parroted ClientHello has no GREASE; the distinguisher is not closed")
	}
	ciphers := strings.Split(parts[1], "-")
	if len(ciphers) < 10 {
		t.Errorf("parroted ClientHello advertises only %d cipher suites, want a browser-like count (>=10)", len(ciphers))
	}
	// A browser offers both TLS 1.2 and 1.3 suites; the Go carrier offered only
	// the three TLS 1.3 AEAD suites. Confirm we are no longer in that narrow set.
	if parts[1] == "4865-4866-4867" {
		t.Error("ClientHello still shows the tool-like three-suite set")
	}
}
