package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignedUpdateBackupFailureDoesNotSwapAnyBinary(t *testing.T) {
	r := newRig(t)
	r.publish("v1.1.0")
	// Force the second backup to fail after the first backup succeeds.
	if err := os.Mkdir(filepath.Join(r.binDir, "baft-pair.prev.tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	r.deploy("v1.1.0")
	result := r.runOne()
	if result.Status != "failed" || !strings.Contains(result.Detail, "rollback backup") {
		t.Fatalf("unexpected result: %+v", result)
	}
	if r.bin("baft") != "old baft" || r.bin("baft-pair") != "old baft-pair" {
		t.Fatal("binary swapped despite incomplete rollback set")
	}
	if r.jobStatus() != "failed" {
		t.Fatal("failure not reported to BCC")
	}
}
