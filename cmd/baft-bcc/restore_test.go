package main

import (
	"bytes"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/bcc"
)

func makeLocalRestoreFixture(t *testing.T) (stateFile, backupFile string, key []byte) {
	t.Helper()
	root := t.TempDir()
	stateFile = filepath.Join(root, "state.db")
	backupFile = filepath.Join(root, "snapshot.baftbak")
	key = bytes.Repeat([]byte{0x42}, 32)

	store, err := bcc.OpenStore(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertNode(bcc.Node{ID: "n-before", Alias: "Before", Address: "127.0.0.1:23001", Role: "foreign"}, "token-before"); err != nil {
		t.Fatal(err)
	}
	app, err := bcc.NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.BackupToFile(backupFile, key, time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertNode(bcc.Node{ID: "n-after", Alias: "After", Address: "127.0.0.1:23002", Role: "foreign"}, "token-after"); err != nil {
		t.Fatal(err)
	}
	return stateFile, backupFile, key
}

func TestLocalRestoreCLIRequiresExplicitYesAndThenRestores(t *testing.T) {
	stateFile, backupFile, key := makeLocalRestoreFixture(t)
	t.Setenv("BAFT_BCC_BACKUP_KEY", base64.StdEncoding.EncodeToString(key))

	var out, errOut bytes.Buffer
	code := runRestore([]string{"--backup", backupFile, "--state-file", stateFile}, &out, &errOut)
	if code != 4 {
		t.Fatalf("preview-only code=%d stderr=%s stdout=%s", code, errOut.String(), out.String())
	}
	if !strings.Contains(out.String(), "RESTORE NOT RUN") {
		t.Fatalf("confirmation message missing: %s", out.String())
	}
	reopened, err := bcc.OpenStore(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.ListNodes()); got != 2 {
		t.Fatalf("preview mutated state, nodes=%d", got)
	}

	out.Reset()
	errOut.Reset()
	code = runRestore([]string{"--backup", backupFile, "--state-file", stateFile, "--yes"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("restore code=%d stderr=%s stdout=%s", code, errOut.String(), out.String())
	}
	if !strings.Contains(out.String(), "Restore committed") {
		t.Fatalf("success message missing: %s", out.String())
	}
	reopened, err = bcc.OpenStore(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	nodes := reopened.ListNodes()
	if len(nodes) != 1 || nodes[0].ID != "n-before" {
		t.Fatalf("restored nodes=%+v", nodes)
	}
}

func TestLocalRestoreCLIJSONReportsConfirmationWithoutMutation(t *testing.T) {
	stateFile, backupFile, key := makeLocalRestoreFixture(t)
	t.Setenv("BAFT_BCC_BACKUP_KEY", base64.StdEncoding.EncodeToString(key))

	var out, errOut bytes.Buffer
	code := runRestore([]string{"--backup", backupFile, "--state-file", stateFile, "--json"}, &out, &errOut)
	if code != 4 {
		t.Fatalf("code=%d stderr=%s stdout=%s", code, errOut.String(), out.String())
	}
	if !strings.Contains(out.String(), `"confirmation_required":true`) || !strings.Contains(out.String(), `"restored":false`) {
		t.Fatalf("json result=%s", out.String())
	}
	reopened, err := bcc.OpenStore(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reopened.ListNodes()); got != 2 {
		t.Fatalf("json preview mutated state, nodes=%d", got)
	}
}
