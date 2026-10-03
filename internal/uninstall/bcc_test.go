package uninstall

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (r *rig) bcc(labelled, active bool) {
	r.t.Helper()
	r.binaries("baft-bcc")
	r.write("bcc/state.db", "SQLite format 3\x00rest-of-db", 0o600)
	r.write("bcc/state.db.audit.jsonl", `{"sequence":1}`+"\n", 0o600)
	r.write("bcc/state.db.audit-anchor-outbox.json", `{"version":1}`, 0o600)
	r.write("bcc/state.db.access.json", `{"username":"op-x"}`, 0o600)
	r.write("bcc/state.db.job-key", "jobkey-private", 0o600)
	r.write("bcc/state.db.lock", "", 0o600)
	r.write("bcc/backups/daily-20261001T000000Z.baftbak", `{"header":{}}`, 0o600)
	r.write("bcc/admin.token", "admintoken", 0o600)
	label := ""
	if labelled {
		label = "# baft-managed: true\n# baft-component: bcc\n"
	}
	r.write("units/baft-bcc.service", label+`[Unit]
Description=BAFT Command Center

[Service]
WorkingDirectory=`+r.p("bcc")+`
ExecStart=`+r.p("bin/baft-bcc")+` --admin-token-file `+r.p("bcc/admin.token")+` --state-file state.db --listen 127.0.0.1:8080
Restart=on-failure

[Install]
WantedBy=multi-user.target
`, 0o644)
	if active {
		r.sys.set("baft-bcc.service", "active", "enabled")
	}
}

func TestBCCKeepsStateByDefault(t *testing.T) {
	r := newRig(t)
	r.bcc(true, true)
	p := r.plan(yes(Options{Scope: Scope{BCC: true}}))
	if has(p.Remove, r.p("units/baft-bcc.service")) == nil || has(p.Remove, r.p("bin/baft-bcc")) == nil {
		t.Fatalf("remove %v", paths(p.Remove))
	}
	for _, k := range []string{"bcc/state.db", "bcc/state.db.access.json", "bcc/state.db.job-key", "bcc/state.db.audit.jsonl", "bcc/backups/daily-20261001T000000Z.baftbak"} {
		if a := has(p.Keep, r.p(k)); a == nil || !strings.Contains(a.Reason, "kept by default") {
			t.Errorf("%s: %+v", k, a)
		}
	}
	if a := has(p.Untouched, r.p("bcc/admin.token")); a == nil {
		t.Fatal("operator file not protected")
	}
	if p.Backup != nil {
		t.Fatal("backup planned without deleting state")
	}
}

// Deleting BCC state: an emergency backup is created and verified first.
func TestBCCEmergencyBackupBeforeStateDeletion(t *testing.T) {
	r := newRig(t)
	r.bcc(true, true)
	var verifyArgs []string
	r.sys.run = func(name string, args ...string) (string, error) {
		verifyArgs = args
		// BCC must be stopped when the backup is taken.
		if a, _ := r.sys.state("baft-bcc.service"); a != "inactive" {
			return "", errors.New("BCC still running during backup")
		}
		return `{"verified":true,"summary":"3 nodes, 1 tunnel, audit 1"}`, nil
	}
	orig, _ := os.ReadFile(r.p("bcc/state.db"))
	p := r.plan(yes(Options{Scope: Scope{BCC: true}, Delete: map[Class]bool{ClassBCCState: true}}))
	if p.Backup == nil || len(p.Backup.Files) < 5 {
		t.Fatalf("backup plan %+v", p.Backup)
	}
	res, err := r.env.Apply(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Backup == nil || res.Backup.VerifiedBy != "sha256+baft-bcc" || !strings.HasPrefix(res.Backup.Dir, r.p("backups")) {
		t.Fatalf("backup %+v", res.Backup)
	}
	if verifyArgs[0] != "verify-backup" {
		t.Fatalf("verify %v", verifyArgs)
	}
	got, err := os.ReadFile(filepath.Join(res.Backup.Dir, "state.db"))
	if err != nil || string(got) != string(orig) {
		t.Fatal("backup copy differs")
	}
	if _, err := os.Stat(filepath.Join(res.Backup.Dir, "manifest.json")); err != nil {
		t.Fatal("no manifest")
	}
	if exists(r.p("bcc/state.db")) || exists(r.p("bcc/state.db.access.json")) || exists(r.p("bcc/state.db.job-key")) {
		t.Fatal("BCC state kept")
	}
	if !exists(r.p("bcc/state.db.audit.jsonl")) {
		t.Fatal("audit removed without being chosen")
	}
	if !exists(r.p("bcc/admin.token")) {
		t.Fatal("operator file removed")
	}
	// The backup itself survives any later uninstall.
	p = r.plan(yes(Options{Scope: Scope{Full: true}, Delete: map[Class]bool{ClassBackups: true, ClassAudit: true}}))
	for _, a := range p.Remove {
		if strings.HasPrefix(a.Path, r.p("backups")) {
			t.Fatalf("emergency backup scheduled for removal: %s", a.Path)
		}
	}
}

func TestBCCBackupRefusedWhileRunning(t *testing.T) {
	r := newRig(t)
	r.bcc(true, false)
	release, err := lockFile(r.p("bcc/state.db.lock"))
	if err != nil {
		t.Skip("no flock")
	}
	defer release()
	before := r.snapshot()
	p := r.plan(yes(Options{Scope: Scope{BCC: true}, Delete: map[Class]bool{ClassBCCState: true}}))
	_, err = r.env.Apply(ctx, p)
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("apply: %v", err)
	}
	if d := diffSnap(before, r.snapshot()); len(d) > 0 {
		t.Fatalf("changed: %v", d)
	}
}

func TestBCCBackupNotVerifiedRestores(t *testing.T) {
	r := newRig(t)
	r.bcc(true, true)
	r.sys.run = func(string, ...string) (string, error) { return `{"verified":false}`, nil }
	before := r.snapshot()
	p := r.plan(yes(Options{Scope: Scope{BCC: true}, Delete: map[Class]bool{ClassBCCState: true}}))
	if _, err := r.env.Apply(ctx, p); err == nil || !strings.Contains(err.Error(), "did not verify") {
		t.Fatalf("apply: %v", err)
	}
	if d := diffSnap(before, r.snapshot()); len(d) > 0 {
		t.Fatalf("changed: %v", d)
	}
	if a, e := r.sys.state("baft-bcc.service"); a != "active" || e != "enabled" {
		t.Fatalf("BCC not restarted: %s/%s", a, e)
	}
}

func TestBCCNoBackupIsExplicit(t *testing.T) {
	r := newRig(t)
	r.bcc(true, false)
	p := r.plan(yes(Options{Scope: Scope{BCC: true}, Delete: map[Class]bool{ClassBCCState: true}, NoBackup: true}))
	if p.Backup != nil {
		t.Fatal("backup planned")
	}
	var b strings.Builder
	p.Render(&b)
	if !strings.Contains(b.String(), "SKIPPED by the owner") {
		t.Fatal("skipping is not shown")
	}
	res, err := r.env.Apply(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Backup != nil {
		t.Fatal("backup made")
	}
	j, _ := r.env.LastJournal()
	if !j.BackupSkipped {
		t.Fatal("skip not journaled")
	}
}

func TestBCCPendingJournalBlocks(t *testing.T) {
	r := newRig(t)
	r.bcc(true, false)
	r.write("bcc/state.db-journal", "x", 0o600)
	p := r.plan(yes(Options{Scope: Scope{BCC: true}, Delete: map[Class]bool{ClassBCCState: true}}))
	if len(p.Blocked) == 0 || !strings.Contains(p.Blocked[0], "unfinished write") {
		t.Fatalf("blocked %v", p.Blocked)
	}
}

// A BCC unit the operator wrote (no label) is never stopped or removed, and
// nothing it uses goes.
func TestBCCUnlabelledUntouched(t *testing.T) {
	r := newRig(t)
	r.bcc(false, true)
	p := r.plan(yes(Options{Scope: Scope{BCC: true}, Delete: map[Class]bool{ClassBCCState: true, ClassAudit: true}}))
	if a := has(p.Untouched, r.p("units/baft-bcc.service")); a == nil || a.Ownership != Unmanaged {
		t.Fatalf("unit %+v", a)
	}
	if len(p.Stop) != 0 || p.HasWork() {
		t.Fatalf("work planned: stop=%v remove=%v", p.Stop, paths(p.Remove))
	}
	if a := has(p.Untouched, r.p("bcc/state.db")); a == nil {
		t.Fatal("state of an operator unit not protected")
	}
}

// --bcc-state-file reaches BCC data whose unit is already gone.
func TestBCCStateWithoutUnit(t *testing.T) {
	r := newRig(t)
	r.bcc(true, false)
	os.Remove(r.p("units/baft-bcc.service"))
	r.env.BCCStateFile = r.p("bcc/state.db")
	p := r.plan(yes(Options{Scope: Scope{BCC: true}, Delete: map[Class]bool{ClassBCCState: true}}))
	if has(p.Remove, r.p("bcc/state.db")) == nil || p.Backup == nil {
		t.Fatalf("remove %v backup %+v", paths(p.Remove), p.Backup)
	}
}
