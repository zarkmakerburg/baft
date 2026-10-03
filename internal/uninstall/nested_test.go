package uninstall

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func tunnelsTree(r *rig) {
	r.write("var/tunnels/generation", "4\n", 0o600)
	r.write("var/tunnels/t1/txn.json", `{"version":1,"id":"t1","phase":"finalized"}`, 0o600)
	r.write("var/tunnels/t1/backup/baft.yaml", r.exConfig("etc"), 0o600)
	r.write("var/tunnels/t1/backup/marker.json", `{"managed_by":"baft","tunnel_id":"t0"}`, 0o600)
}

var allData = map[Class]bool{ClassTunnelConfigs: true, ClassCertificates: true, ClassBackups: true, ClassAudit: true}

// 1, 6, 7, 10: an unknown file in tunnels/ survives; the records go; the
// parent stays; the preview shows the survivor.
func TestNestedTunnelsUnknownSurvives(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	tunnelsTree(r)
	r.write("var/tunnels/operator-note.txt", "mine", 0o644)
	r.write("var/tunnels/t1/notes.md", "mine too", 0o644)
	p := r.plan(yes(Options{Scope: Scope{Full: true}, Delete: map[Class]bool{ClassTunnelConfigs: true}}))
	for _, rm := range []string{"var/tunnels/generation", "var/tunnels/t1/txn.json", "var/tunnels/t1/backup/baft.yaml", "var/tunnels/t1/backup/marker.json"} {
		if has(p.Remove, r.p(rm)) == nil {
			t.Errorf("%s not removable", rm)
		}
	}
	for _, a := range p.Remove {
		if a.Path == r.p("var/tunnels") || a.Path == r.p("var/tunnels/t1") {
			t.Fatalf("a directory is removed whole: %s", a.Path)
		}
	}
	var b strings.Builder
	p.Render(&b)
	if !strings.Contains(b.String(), "var/tunnels/operator-note.txt") || !strings.Contains(b.String(), "Never touched") {
		t.Fatalf("preview does not show the survivor:\n%s", b.String())
	}
	if _, err := r.env.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"var/tunnels/operator-note.txt", "var/tunnels/t1/notes.md"} {
		if !exists(r.p(s)) {
			t.Fatalf("%s removed", s)
		}
	}
	if exists(r.p("var/tunnels/generation")) || exists(r.p("var/tunnels/t1/backup")) {
		t.Fatal("BAFT records or their empty directory left")
	}
	if !exists(r.p("var")) {
		t.Fatal("a parent holding kept data was removed")
	}
}

// 2: pki.before-* keeps what is not BAFT's.
func TestNestedPKIBackupUnknownSurvives(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	r.write("etc/pki.before-123/ca.pem", pemCert, 0o644)
	r.write("etc/pki.before-123/ca.key", pemKey, 0o600)
	r.write("etc/pki.before-123/my-own-file.txt", "mine", 0o644)
	r.write("etc/pki.before-123/server.key", "not a key", 0o600)
	p := r.plan(yes(Options{Scope: Scope{Full: true}, Delete: map[Class]bool{ClassBackups: true}}))
	if _, err := r.env.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	if exists(r.p("etc/pki.before-123/ca.pem")) || exists(r.p("etc/pki.before-123/ca.key")) {
		t.Fatal("proven backup files kept")
	}
	if !exists(r.p("etc/pki.before-123/my-own-file.txt")) || !exists(r.p("etc/pki.before-123/server.key")) {
		t.Fatal("unknown files removed")
	}
}

func rerunDir(r *rig, name string, manifest bool) {
	r.write("opt/backups/"+name+"/_etc_baft_baft.yaml", "old config", 0o600)
	r.write("opt/backups/"+name+"/_usr_local_bin_baft", "old binary", 0o755)
	if manifest {
		m := shaHex([]byte("old config")) + "  _etc_baft_baft.yaml\n" + shaHex([]byte("old binary")) + "  _usr_local_bin_baft\n"
		r.write("opt/backups/"+name+"/MANIFEST.sha256", m, 0o600)
	}
}

// 3: rerun backups go only as far as the installer's manifest proves.
func TestNestedRerunBackups(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	rerunDir(r, "rerun-1", true)
	r.write("opt/backups/rerun-1/extra.txt", "mine", 0o600)
	rerunDir(r, "rerun-2", false) // older installer: no manifest, nothing proven
	rerunDir(r, "rerun-3", true)
	r.write("opt/backups/rerun-3/_etc_baft_baft.yaml", "changed since", 0o600)
	p := r.plan(yes(Options{Scope: Scope{Full: true}, Delete: map[Class]bool{ClassBackups: true}}))
	if _, err := r.env.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	if exists(r.p("opt/backups/rerun-1/_etc_baft_baft.yaml")) || exists(r.p("opt/backups/rerun-1/MANIFEST.sha256")) {
		t.Fatal("proven rerun copies kept")
	}
	for _, s := range []string{"opt/backups/rerun-1/extra.txt", "opt/backups/rerun-2/_etc_baft_baft.yaml", "opt/backups/rerun-3/_etc_baft_baft.yaml"} {
		if !exists(r.p(s)) {
			t.Errorf("%s removed", s)
		}
	}
	if exists(r.p("opt/backups/rerun-3/_usr_local_bin_baft")) {
		t.Fatal("a manifest-proven file kept")
	}
}

func gitRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.MkdirAll(dir+"/cmd/baft", 0o755)
	os.WriteFile(dir+"/go.mod", []byte("module "+baftModule+"\n\ngo 1.22\n"), 0o644)
	os.WriteFile(dir+"/cmd/baft/main.go", []byte("package main\n"), 0o644)
	run("init", "-q")
	run("add", ".")
	run("commit", "-q", "-m", "x")
}

// 4, 5, 7: the source checkout is fail-closed.
func TestSourceCheckoutFailClosed(t *testing.T) {
	cases := map[string]func(dir string){
		"clean":     nil,
		"modified":  func(d string) { os.WriteFile(d+"/cmd/baft/main.go", []byte("package main // local work\n"), 0o644) },
		"untracked": func(d string) { os.WriteFile(d+"/notes.txt", []byte("operator notes"), 0o644) },
		"ignored": func(d string) {
			os.WriteFile(d+"/.git/info/exclude", []byte("*.local\n"), 0o644)
			os.WriteFile(d+"/secret.local", []byte("x"), 0o644)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.installerEX(false)
			gitRepo(t, r.p("opt/src"))
			if mutate != nil {
				mutate(r.p("opt/src"))
			}
			p := r.plan(yes(Options{Scope: Scope{Full: true}, Delete: allData}))
			a := has(p.Remove, r.p("opt/src"))
			if mutate == nil {
				if a == nil || !strings.Contains(a.Evidence, "proven clean") {
					t.Fatalf("clean checkout not removable: %+v", has(p.Untouched, r.p("opt/src")))
				}
			} else {
				if a != nil {
					t.Fatal("dirty checkout scheduled for removal")
				}
				if u := has(p.Untouched, r.p("opt/src")); u == nil || u.Ownership != Unknown {
					t.Fatalf("dirty checkout: %+v", u)
				}
			}
			if _, err := r.env.Apply(ctx, p); err != nil {
				t.Fatal(err)
			}
			if exists(r.p("opt/src")) == (mutate == nil) {
				t.Fatalf("after apply: src exists=%v", exists(r.p("opt/src")))
			}
		})
	}
}

// 8: a nested entry that changes between plan and apply aborts and rolls back.
func TestNestedChangeAfterPlanRollsBack(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	tunnelsTree(r)
	p := r.plan(yes(Options{Scope: Scope{Full: true}, Delete: map[Class]bool{ClassTunnelConfigs: true}}))
	r.write("var/tunnels/t1/txn.json", `{"version":1,"id":"t1","phase":"finalized","x":1}`, 0o600)
	before := r.snapshot()
	if _, err := r.env.Apply(ctx, p); err == nil || !strings.Contains(err.Error(), "changed since the plan") {
		t.Fatalf("apply: %v", err)
	}
	if d := diffSnap(before, r.snapshot()); len(d) > 0 {
		t.Fatalf("not rolled back: %v", d)
	}
}

// 9: symlinks inside a candidate tree are never followed or removed, and a
// planned file swapped for a symlink aborts the run.
func TestNestedSymlinks(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	tunnelsTree(r)
	r.write("elsewhere/precious.txt", "do not touch", 0o644)
	os.Symlink(r.p("elsewhere"), r.p("var/tunnels/t2"))
	os.Symlink(r.p("elsewhere/precious.txt"), r.p("var/tunnels/t1/psk"))
	p := r.plan(yes(Options{Scope: Scope{Full: true}, Delete: map[Class]bool{ClassTunnelConfigs: true}}))
	for _, l := range []string{"var/tunnels/t2", "var/tunnels/t1/psk"} {
		if a := has(p.Untouched, r.p(l)); a == nil || a.Ownership != Unknown {
			t.Fatalf("%s: %+v", l, a)
		}
	}
	// Swap a planned file for a symlink to something precious.
	os.Remove(r.p("var/tunnels/generation"))
	os.Symlink(r.p("elsewhere/precious.txt"), r.p("var/tunnels/generation"))
	if _, err := r.env.Apply(ctx, p); err == nil {
		t.Fatal("swapped file removed")
	}
	if b, _ := os.ReadFile(r.p("elsewhere/precious.txt")); string(b) != "do not touch" {
		t.Fatal("symlink target touched")
	}
	for _, l := range []string{"var/tunnels/t2", "var/tunnels/t1/psk", "var/tunnels/generation"} {
		if fi, err := os.Lstat(r.p(l)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s gone or replaced", l)
		}
	}
	_ = filepath.Join
}

// Many nested files: each step is one appended line, so a large tree is
// linear, and a crash in the middle is still resumed from the log.
func TestLargeTreeCrashResume(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	for i := 0; i < 3000; i++ {
		rel := filepath.Join(".tool", "lib", strconv.Itoa(i/100), "f"+strconv.Itoa(i))
		r.write("skel/"+rel, strconv.Itoa(i), 0o644)
		r.write("var/"+rel, strconv.Itoa(i), 0o644)
	}
	os.Symlink("lib/0/f1", r.p("var/.tool/link"))
	os.Symlink("lib/0/f1", r.p("skel/.tool/link"))
	p := r.plan(yes(Options{}))
	start := time.Now()
	withCrash(t, "before-commit", func() { r.env.Apply(ctx, p) })
	pj, err := r.env.Pending()
	if err != nil || pj == nil {
		t.Fatalf("pending: %v", err)
	}
	moved := 0
	for _, f := range pj.Files {
		if f.State == "moved" {
			moved++
		}
	}
	if moved < 3000 {
		t.Fatalf("journal replay shows %d moved", moved)
	}
	res, err := r.env.Resume(ctx)
	if err != nil || res.Status != stCommitted {
		t.Fatalf("resume: %v %+v", err, res)
	}
	if d := time.Since(start); d > 60*time.Second {
		t.Fatalf("too slow: %v", d)
	}
	if exists(r.p("var/.tool/lib/0/f2")) {
		t.Fatal("skeleton copies left")
	}
	if fi, err := os.Lstat(r.p("var/.tool/link")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("a symlink inside the tree was removed")
	}
}

func TestProgressLogTornLine(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	p := r.plan(yes(Options{}))
	withCrash(t, "after-first-move", func() { r.env.Apply(ctx, p) })
	pj, _ := r.env.Pending()
	f, _ := os.OpenFile(filepath.Join(pj.Dir(), "progress.log"), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("F 1 mov") // a crash mid-write
	f.Close()
	if _, err := r.env.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(r.p("units/baft.service")) {
		t.Fatal("not restored")
	}
}

// Certificate rotation artifacts (A4): proven files go with their class,
// anything else in those directories stays.
func TestNestedRotationArtifacts(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	r.write("etc/pki/host", "203.0.113.7\n", 0o644)
	r.write("etc/pki.prev-rot-1/ca.pem", pemCert, 0o644)
	r.write("etc/pki.prev-rot-1/ca.key", pemKey, 0o600)
	r.write("etc/pki.prev-rot-1/host", "203.0.113.7\n", 0o644)
	r.write("etc/pki.prev-rot-1/operator.txt", "mine", 0o644)
	r.write("etc/pki.next-rot-1/server.pem", pemCert, 0o644)
	r.write("var/rotations/epoch", "2\n", 0o600)
	r.write("var/rotations/active", "rot-1\n", 0o600)
	r.write("var/rotations/rot-1/rot.json", `{"version":1,"id":"rot-1","phase":"active","epoch":2}`, 0o600)
	r.write("var/rotations/rot-1/new-ca.pem", pemCert, 0o644)
	r.write("var/rotations/rot-1/backup/ca.pem", pemCert, 0o644)
	r.write("var/rotations/rot-1/notes.md", "mine", 0o644)
	p := r.plan(yes(Options{Scope: Scope{Full: true}, Delete: map[Class]bool{ClassCertificates: true}}))
	if _, err := r.env.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"etc/pki/host", "etc/pki.prev-rot-1/ca.pem", "etc/pki.prev-rot-1/ca.key", "etc/pki.prev-rot-1/host",
		"etc/pki.next-rot-1", "var/rotations/epoch", "var/rotations/active", "var/rotations/rot-1/rot.json",
		"var/rotations/rot-1/new-ca.pem", "var/rotations/rot-1/backup"} {
		if exists(r.p(gone)) {
			t.Errorf("proven rotation artifact kept: %s", gone)
		}
	}
	for _, kept := range []string{"etc/pki.prev-rot-1/operator.txt", "var/rotations/rot-1/notes.md"} {
		if !exists(r.p(kept)) {
			t.Errorf("unknown file removed: %s", kept)
		}
	}
}
