package integration_test

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/node"
)

// Adding a peer to revocation.file and reloading (SIGHUP in production) must
// cut that peer's established carrier and its live Flow immediately.
func TestRevocationReloadCutsEstablishedCarrier(t *testing.T) {
	file := filepath.Join(t.TempDir(), "revoked.yaml")
	if err := os.WriteFile(file, nil, 0600); err != nil { t.Fatal(err) }
	p := startRuntimePair(t, 1, false, func(ex, _ *config.Config) {
		ex.Revocation = &config.Revocation{File: file}
	})
	defer p.closeAllowErrors()
	c := openRecoveryFlow(t, p)
	defer c.Close()

	if err := os.WriteFile(file, []byte("identities:\n  - urn:baft:node:ir-recovery\n"), 0600); err != nil { t.Fatal(err) }
	n, err := p.exRuntime.ReloadRevocations()
	if err != nil || n != 1 { t.Fatalf("reload applied %d entries, err=%v", n, err) }

	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("live Flow of the revoked peer was not closed: %v", err)
	}
	select {
	case err := <-p.irDone:
		p.irDone <- err
		if err == nil { t.Fatal("IR runtime returned nil after its carrier was revoked") }
	case <-time.After(5 * time.Second):
		t.Fatal("revoked IR kept its carrier")
	}
}

func TestListenerRefusesToStartWithInvalidRevocationFile(t *testing.T) {
	ex, err := config.LoadFile("../../configs/example-ex.yaml")
	if err != nil { t.Fatal(err) }
	file := filepath.Join(t.TempDir(), "revoked.yaml")
	if err := os.WriteFile(file, []byte("identities: [not-a-node-identity]\n"), 0600); err != nil { t.Fatal(err) }
	ex.Revocation = &config.Revocation{File: file}
	err = node.NewRuntime().Run(t.Context(), ex)
	if err == nil || !strings.Contains(err.Error(), "revocation") {
		t.Fatalf("listener with an invalid revocation list: err=%v, want a revocation error", err)
	}
}
