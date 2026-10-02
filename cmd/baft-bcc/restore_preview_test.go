package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/bcc"
)

func TestRestorePreviewCommand(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "bcc-state.json")
	store, err := bcc.OpenStore(state)
	if err != nil {
		t.Fatal(err)
	}
	app, err := bcc.NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{7}, 32)
	backup := filepath.Join(dir, "b.baftbak")
	if _, err := app.BackupToFile(backup, key, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	run := func(env string, args ...string) (int, string) {
		t.Setenv("BAFT_BCC_BACKUP_KEY", env)
		var out, errOut bytes.Buffer
		code := runRestorePreview(args, &out, &errOut)
		return code, out.String() + errOut.String()
	}
	good := base64.StdEncoding.EncodeToString(key)

	before, _ := os.ReadFile(state)
	code, out := run(good, "--backup", backup, "--state-file", state)
	if code != 0 || !strings.Contains(out, "Backup verified") || !strings.Contains(out, "nothing was changed") {
		t.Fatalf("good preview: %d\n%s", code, out)
	}
	if after, _ := os.ReadFile(state); !bytes.Equal(before, after) {
		t.Fatal("the preview changed the state file")
	}
	if code, out := run(good, "--backup", backup, "--state-file", state, "--json"); code != 0 || !strings.Contains(out, `"verified": true`) {
		t.Fatalf("json: %d\n%s", code, out)
	}
	wrong := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
	if code, out := run(wrong, "--backup", backup, "--state-file", state); code != 1 || !strings.Contains(out, "NOT VERIFIED") {
		t.Fatalf("wrong key: %d\n%s", code, out)
	}
	if code, _ := run("", "--backup", backup); code != 2 {
		t.Fatalf("missing key gave %d", code)
	}
	if code, _ := run(good); code != 2 {
		t.Fatalf("missing --backup gave %d", code)
	}
}
