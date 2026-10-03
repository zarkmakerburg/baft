package uninstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var ctx = context.Background()

func yes(o Options) Options {
	o.Yes, o.StopActiveTunnels = true, true
	return o
}

// The standard uninstall (no scope): services + agent go, every data class stays.
func TestDefaultUninstallKeepsData(t *testing.T) {
	r := newRig(t)
	r.installerEX(true)
	r.agent(true)
	r.socket("var/admin.sock")
	p := r.plan(Options{})
	if len(p.Blocked) != 0 {
		t.Fatalf("blocked: %v", p.Blocked)
	}
	for _, want := range []string{"units/baft.service", "units/baft-agent.service", "bin/baft", "bin/baft-pair", "bin/baft-agent", "var/admin.sock"} {
		if has(p.Remove, r.p(want)) == nil {
			t.Errorf("%s not removed; remove=%v", want, paths(p.Remove))
		}
	}
	for _, keep := range []string{"etc/baft.yaml", "etc/noise-key.json", "etc/pki/ca.key", "etc/pki/server.pem", "opt/release-state.json", "var/pairing.psk",
		"agent/token", "agent/bcc-job.pub", "agentstate/seen-jobs.json"} {
		a := has(p.Keep, r.p(keep))
		if a == nil || !strings.Contains(a.Reason, "kept by default") {
			t.Errorf("%s not kept as data: %+v", keep, a)
		}
	}
	if len(p.Stop) != 2 || len(p.ActiveTunnels) != 1 || p.ActiveTunnels[0].Unit != "baft.service" {
		t.Fatalf("stop=%v tunnels=%v", p.Stop, p.ActiveTunnels)
	}
	if strings.Join(p.Needs, ",") != "--stop-active-tunnels,--yes" {
		t.Fatalf("needs %v", p.Needs)
	}
	if !strings.Contains(has(p.Remove, r.p("units/baft.service")).Evidence, "byte-identical") {
		t.Fatal("installer unit not proven by the template")
	}
	// baft-bcc is not in the standard scope.
	r.binaries("baft-bcc")
	p = r.plan(Options{})
	if a := has(p.Keep, r.p("bin/baft-bcc")); a == nil || a.Reason != "not in the selected scope" {
		t.Fatalf("baft-bcc: %+v", a)
	}
}

func TestApplyRemovesVerifiesCommits(t *testing.T) {
	r := newRig(t)
	r.installerEX(true)
	r.agent(true)
	p := r.plan(yes(Options{}))
	if len(p.Needs) != 0 {
		t.Fatalf("needs %v", p.Needs)
	}
	res, err := r.env.Apply(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"units/baft.service", "units/baft-agent.service", "bin/baft", "bin/baft-agent", "bin/baft-pair"} {
		if exists(r.p(gone)) {
			t.Errorf("%s still there", gone)
		}
	}
	for _, kept := range []string{"etc/baft.yaml", "etc/noise-key.json", "opt/release-state.json", "agent/token"} {
		if !exists(r.p(kept)) {
			t.Errorf("%s was removed", kept)
		}
	}
	if a, e := r.sys.state("baft.service"); a != "inactive" || e != "disabled" {
		t.Fatalf("baft.service %s/%s", a, e)
	}
	j, err := readJournal(res.Journal)
	if err != nil || j.Status != stCommitted {
		t.Fatalf("journal %+v %v", j, err)
	}
	if exists(filepath.Join(res.Journal, "quarantine")) {
		t.Fatal("quarantine not purged")
	}
	// The journal records paths and digests, never contents.
	raw, _ := os.ReadFile(filepath.Join(res.Journal, "journal.json"))
	if strings.Contains(string(raw), "tok") && strings.Contains(string(raw), `"tok`) {
		t.Fatal("journal holds a secret")
	}
	// The agent directory is empty only when its data is deleted; it still has the token.
	if !exists(r.p("agent")) {
		t.Fatal("non-empty directory removed")
	}
}

// Full uninstall removes only the data classes that were chosen.
func TestFullRemovesOnlyConfirmedData(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	r.agent(false)
	p := r.plan(yes(Options{Scope: Scope{Full: true}, Delete: map[Class]bool{ClassTunnelConfigs: true}}))
	if _, err := r.env.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	if exists(r.p("etc/baft.yaml")) || exists(r.p("var/pairing.psk")) {
		t.Fatal("chosen tunnel configs kept")
	}
	for _, kept := range []string{"etc/noise-key.json", "etc/pki/ca.key", "opt/release-state.json", "agent/token", "agentstate/seen-jobs.json"} {
		if !exists(r.p(kept)) {
			t.Errorf("%s removed without being chosen", kept)
		}
	}
	// Everything chosen: nothing of BAFT's is left, and empty directories go.
	p = r.plan(yes(Options{Scope: Scope{Full: true}, Delete: map[Class]bool{ClassCertificates: true, ClassAudit: true, ClassBackups: true, ClassTunnelConfigs: true}}))
	if _, err := r.env.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"etc", "var", "opt", "agent", "agentstate"} {
		if exists(r.p(d)) {
			ents, _ := os.ReadDir(r.p(d))
			t.Errorf("%s left: %v", d, ents)
		}
	}
}

// An unmanaged unit, its config and the binary it runs survive, running.
func TestUnmanagedTunnelSurvives(t *testing.T) {
	r := newRig(t)
	r.installerEX(true)
	r.write("hand/x.yaml", r.exConfig("hand"), 0o640)
	hand := strings.Replace(renderServiceUnit(serviceParams{User: "baft", Bin: r.p("bin/baft"), Config: r.p("hand/x.yaml"), StateDir: r.p("handstate")}),
		"Restart=on-failure", "Restart=always", 1)
	r.write("units/baft-hand.service", hand, 0o644)
	r.sys.set("baft-hand.service", "active", "enabled")
	before := r.snapshot()
	p := r.plan(yes(Options{Scope: Scope{Full: true}, Delete: map[Class]bool{ClassTunnelConfigs: true, ClassCertificates: true}}))
	if a := has(p.Untouched, r.p("units/baft-hand.service")); a == nil || a.Ownership != Unmanaged {
		t.Fatalf("hand unit: %+v", a)
	}
	if a := has(p.Keep, r.p("bin/baft")); a == nil || !strings.Contains(a.Reason, "baft-hand.service") {
		t.Fatalf("binary in use: %+v", a)
	}
	if _, err := r.env.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	after := r.snapshot()
	for _, k := range []string{"units/baft-hand.service", "hand/x.yaml", "bin/baft"} {
		if before[k] == "" || before[k] != after[k] {
			t.Errorf("%s changed: %q -> %q", k, before[k], after[k])
		}
	}
	if a, _ := r.sys.state("baft-hand.service"); a != "active" {
		t.Fatal("unmanaged tunnel stopped")
	}
	for _, c := range r.sys.calls {
		if strings.Contains(c, "baft-hand") {
			t.Fatalf("touched the unmanaged unit: %s", c)
		}
	}
}

// An ownership conflict blocks the destructive action; nothing changes.
func TestOwnershipConflictBlocks(t *testing.T) {
	cases := map[string]func(r *rig){
		"header without marker": func(r *rig) {
			u, _ := os.ReadFile(r.p("units/baft.service"))
			r.write("units/baft.service", "# baft-managed: true\n# baft-tunnel: t1\n# baft-generation: 1\n"+string(u), 0o644)
		},
		"marker for another tunnel": func(r *rig) {
			u, _ := os.ReadFile(r.p("units/baft.service"))
			nu := "# baft-managed: true\n# baft-tunnel: t1\n# baft-generation: 1\n" + string(u)
			r.write("units/baft.service", nu, 0o644)
			c, _ := os.ReadFile(r.p("etc/baft.yaml"))
			m, _ := json.Marshal(marker{ManagedBy: "baft", TunnelID: "t2", ConfigSHA256: shaHex(c), UnitSHA256: shaHex([]byte(nu))})
			r.write("etc/baft.managed.json", string(m), 0o644)
		},
		"marker but no header": func(r *rig) {
			m, _ := json.Marshal(marker{ManagedBy: "baft", TunnelID: "t1"})
			r.write("etc/baft.managed.json", string(m), 0o644)
		},
		"marker of another manager": func(r *rig) {
			u, _ := os.ReadFile(r.p("units/baft.service"))
			r.write("units/baft.service", "# baft-managed: true\n# baft-tunnel: t1\n"+string(u), 0o644)
			r.write("etc/baft.managed.json", `{"managed_by":"someone-else","tunnel_id":"t1"}`, 0o644)
		},
		"two units, one config": func(r *rig) {
			u, _ := os.ReadFile(r.p("units/baft.service"))
			r.write("units/baft-copy.service", string(u), 0o644)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.installerEX(true)
			mutate(r)
			before := r.snapshot()
			p := r.plan(yes(Options{Scope: Scope{Full: true}, Delete: map[Class]bool{ClassTunnelConfigs: true}}))
			if len(p.Blocked) == 0 || !strings.Contains(strings.Join(p.Blocked, " "), "OWNERSHIP_CONFLICT") {
				t.Fatalf("not blocked: %v", p.Blocked)
			}
			if _, err := r.env.Apply(ctx, p); !errors.Is(err, ErrBlocked) {
				t.Fatalf("apply: %v", err)
			}
			if d := diffSnap(before, r.snapshot()); len(d) > 0 {
				t.Fatalf("changed: %v", d)
			}
			if len(r.sys.calls) != 0 {
				t.Fatalf("systemctl: %v", r.sys.calls)
			}
		})
	}
}

func managedTunnel(r *rig, editAfter bool) {
	u, _ := os.ReadFile(r.p("units/baft.service"))
	nu := "# baft-managed: true\n# baft-tunnel: t1\n# baft-generation: 3\n" + string(u)
	r.write("units/baft.service", nu, 0o644)
	c, _ := os.ReadFile(r.p("etc/baft.yaml"))
	m, _ := json.Marshal(marker{ManagedBy: "baft", TunnelID: "t1", Generation: 3, Role: "ex", ConfigSHA256: shaHex(c), UnitSHA256: shaHex([]byte(nu))})
	r.write("etc/baft.managed.json", string(m), 0o644)
	if editAfter {
		r.write("etc/baft.yaml", strings.Replace(string(c), "level: info", "level: debug", 1), 0o640)
	}
}

func TestManagedTunnelMarker(t *testing.T) {
	r := newRig(t)
	r.installerEX(true)
	managedTunnel(r, false)
	p := r.plan(yes(Options{Scope: Scope{Services: true}}))
	a := has(p.Remove, r.p("units/baft.service"))
	if a == nil || !strings.Contains(a.Evidence, "ownership marker") {
		t.Fatalf("managed unit: %+v (hold %v)", a, paths(p.Hold))
	}
	if m := has(p.Keep, r.p("etc/baft.managed.json")); m == nil || m.Class != ClassTunnelConfigs {
		t.Fatalf("marker kept as tunnel config: %+v", m)
	}
	if p.ActiveTunnels[0].TunnelID != "t1" {
		t.Fatalf("tunnel %+v", p.ActiveTunnels)
	}
}

// A BAFT tunnel edited by hand is held (not removed, not stopped), not blocking.
func TestDriftedTunnelHeld(t *testing.T) {
	r := newRig(t)
	r.installerEX(true)
	managedTunnel(r, true)
	r.agent(true)
	p := r.plan(yes(Options{}))
	if a := has(p.Hold, r.p("units/baft.service")); a == nil || a.Ownership != Drifted {
		t.Fatalf("drifted unit: %+v", a)
	}
	if len(p.Blocked) != 0 {
		t.Fatalf("drift must not block: %v", p.Blocked)
	}
	for _, s := range p.Stop {
		if s.Unit == "baft.service" {
			t.Fatal("drifted unit stopped")
		}
	}
	if a := has(p.Keep, r.p("bin/baft")); a == nil {
		t.Fatal("binary of a held unit removed")
	}
	if _, err := r.env.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	if !exists(r.p("units/baft.service")) || exists(r.p("units/baft-agent.service")) {
		t.Fatal("wrong units removed")
	}
}

// One byte off the installer's template: not proven, never touched.
func TestTemplateMismatchIsUnmanaged(t *testing.T) {
	r := newRig(t)
	r.installerEX(true)
	u, _ := os.ReadFile(r.p("units/baft.service"))
	r.write("units/baft.service", strings.Replace(string(u), "RestartSec=2s", "RestartSec=3s", 1), 0o644)
	p := r.plan(yes(Options{}))
	a := has(p.Untouched, r.p("units/baft.service"))
	if a == nil || a.Ownership != Unmanaged {
		t.Fatalf("%+v", a)
	}
	if has(p.Keep, r.p("etc/baft.yaml")) == nil && has(p.Untouched, r.p("etc/baft.yaml")) == nil {
		t.Fatal("config of an unmanaged unit not protected")
	}
	if c := has(p.Untouched, r.p("etc/baft.yaml")); c == nil || !strings.Contains(c.Reason, "not BAFT-owned") {
		t.Fatalf("config: %+v", c)
	}
}

func TestBinaryOwnership(t *testing.T) {
	r := newRig(t)
	r.write("bin/baft", "#!/bin/sh\necho hi\n", 0o755)
	r.binaries("baft-pair")
	os.Symlink(r.p("bin/baft-pair"), r.p("bin/baft-agent"))
	p := r.plan(yes(Options{Scope: Scope{Binaries: true}}))
	if a := has(p.Untouched, r.p("bin/baft")); a == nil || a.Ownership != Unmanaged {
		t.Fatalf("script: %+v", a)
	}
	if a := has(p.Untouched, r.p("bin/baft-agent")); a == nil || a.Ownership != Unknown {
		t.Fatalf("symlink: %+v", a)
	}
	if a := has(p.Remove, r.p("bin/baft-pair")); a == nil {
		t.Fatalf("real binary not removable: %v", paths(p.Keep))
	}
	// A Go program that is not this binary's expected package.
	r.env.MainPkg["baft-pair"] = baftModule + "/cmd/baft-pair"
	p = r.plan(yes(Options{Scope: Scope{Binaries: true}}))
	if a := has(p.Untouched, r.p("bin/baft-pair")); a == nil || a.Ownership != Unmanaged {
		t.Fatalf("wrong package: %+v", a)
	}
}

func TestActiveTunnelNeedsExplicitConsent(t *testing.T) {
	r := newRig(t)
	r.installerEX(true)
	p := r.plan(Options{Scope: Scope{Services: true}, Yes: true})
	if strings.Join(p.Needs, ",") != "--stop-active-tunnels" {
		t.Fatalf("needs %v", p.Needs)
	}
	before := r.snapshot()
	if _, err := r.env.Apply(ctx, p); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("apply: %v", err)
	}
	if d := diffSnap(before, r.snapshot()); len(d) > 0 || len(r.sys.calls) > 0 {
		t.Fatalf("changed without consent: %v %v", d, r.sys.calls)
	}
	// An inactive tunnel needs no tunnel consent.
	r.sys.set("baft.service", "inactive", "enabled")
	if p := r.plan(Options{Scope: Scope{Services: true}, Yes: true}); len(p.Needs) != 0 {
		t.Fatalf("needs %v", p.Needs)
	}
}

func TestTunnelChangeInProgressBlocks(t *testing.T) {
	r := newRig(t)
	r.installerEX(true)
	r.write("var/tunnels/t9/txn.json", `{"phase":"prepared"}`, 0o600)
	p := r.plan(yes(Options{}))
	if len(p.Blocked) == 0 || !strings.Contains(p.Blocked[0], "t9") {
		t.Fatalf("blocked %v", p.Blocked)
	}
	r.write("var/tunnels/t9/txn.json", `{"phase":"finalized"}`, 0o600)
	if p := r.plan(yes(Options{})); len(p.Blocked) != 0 {
		t.Fatalf("finished change still blocks: %v", p.Blocked)
	}
}

func TestValidateOptions(t *testing.T) {
	bad := []Options{
		{Scope: Scope{Agent: true}, Delete: map[Class]bool{ClassBCCState: true}},
		{Scope: Scope{BCC: true}, Delete: map[Class]bool{ClassTunnelConfigs: true}},
		{Scope: Scope{Binaries: true}, Delete: map[Class]bool{ClassCertificates: true}},
		{Scope: Scope{Full: true}, NoBackup: true},
	}
	for i, o := range bad {
		if ValidateOptions(o) == nil {
			t.Errorf("%d accepted: %+v", i, o)
		}
	}
	good := []Options{{}, {Delete: map[Class]bool{ClassTunnelConfigs: true, ClassAudit: true}}, {Scope: Scope{BCC: true}, Delete: map[Class]bool{ClassBCCState: true}, NoBackup: true}}
	for i, o := range good {
		if err := ValidateOptions(o); err != nil {
			t.Errorf("%d refused: %v", i, err)
		}
	}
}

// A failure after the services stopped and files moved restores everything:
// files byte-identical, services enabled and running again.
func TestFailureRestoresEverything(t *testing.T) {
	for _, point := range []string{"after-stop", "after-first-move", "before-verify", "before-commit"} {
		t.Run(point, func(t *testing.T) {
			r := newRig(t)
			r.installerEX(true)
			r.agent(true)
			before := r.snapshot()
			t.Setenv("BAFT_UNINSTALL_FAIL_AT", point)
			p := r.plan(yes(Options{Scope: Scope{Full: true}, Delete: map[Class]bool{ClassTunnelConfigs: true, ClassCertificates: true}}))
			if _, err := r.env.Apply(ctx, p); err == nil || !strings.Contains(err.Error(), "restored") {
				t.Fatalf("apply: %v", err)
			}
			if d := diffSnap(before, r.snapshot()); len(d) > 0 {
				t.Fatalf("not restored: %v", d)
			}
			for _, u := range []string{"baft.service", "baft-agent.service"} {
				if a, e := r.sys.state(u); a != "active" || e != "enabled" {
					t.Fatalf("%s %s/%s", u, a, e)
				}
			}
			j, _ := r.env.LastJournal()
			if j.Status != stRestored {
				t.Fatalf("journal %s", j.Status)
			}
			if pj, _ := r.env.Pending(); pj != nil {
				t.Fatal("a restored run is still pending")
			}
		})
	}
}

// A service that was stopped and disabled before stays so after a restore.
func TestRestoreKeepsPriorServiceState(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	r.sys.set("baft.service", "inactive", "disabled")
	t.Setenv("BAFT_UNINSTALL_FAIL_AT", "before-commit")
	p := r.plan(yes(Options{}))
	if _, err := r.env.Apply(ctx, p); err == nil {
		t.Fatal("no failure")
	}
	if a, e := r.sys.state("baft.service"); a != "inactive" || e != "disabled" {
		t.Fatalf("%s/%s", a, e)
	}
}

type crashed struct{}

func withCrash(t *testing.T, point string, fn func()) {
	t.Helper()
	old := crash
	crash = func() { panic(crashed{}) }
	t.Setenv("BAFT_UNINSTALL_CRASH_AT", point)
	defer func() {
		crash = old
		os.Unsetenv("BAFT_UNINSTALL_CRASH_AT")
		if v := recover(); v != nil {
			if _, ok := v.(crashed); !ok {
				panic(v)
			}
			return
		}
		t.Fatal("did not crash")
	}()
	fn()
}

// An uninstall killed midway is recoverable: --restore undoes it exactly.
func TestInterruptedThenRestore(t *testing.T) {
	for _, point := range []string{"after-stop", "after-first-move", "before-commit"} {
		t.Run(point, func(t *testing.T) {
			r := newRig(t)
			r.installerEX(true)
			r.agent(true)
			before := r.snapshot()
			p := r.plan(yes(Options{}))
			withCrash(t, point, func() { r.env.Apply(ctx, p) })
			pj, err := r.env.Pending()
			if err != nil || pj == nil {
				t.Fatalf("no pending journal: %v", err)
			}
			// A new run refuses until the old one is settled.
			if np := r.plan(yes(Options{})); len(np.Blocked) == 0 || np.Pending == "" {
				t.Fatalf("new run not blocked: %v", np.Blocked)
			}
			if _, err := r.env.Restore(ctx); err != nil {
				t.Fatal(err)
			}
			if d := diffSnap(before, r.snapshot()); len(d) > 0 {
				t.Fatalf("not restored: %v", d)
			}
			if a, e := r.sys.state("baft.service"); a != "active" || e != "enabled" {
				t.Fatalf("baft.service %s/%s", a, e)
			}
		})
	}
}

// ... or --resume finishes it.
func TestInterruptedThenResume(t *testing.T) {
	for _, point := range []string{"after-stop", "after-first-move", "before-commit", "mid-commit"} {
		t.Run(point, func(t *testing.T) {
			r := newRig(t)
			r.installerEX(true)
			r.agent(true)
			p := r.plan(yes(Options{}))
			withCrash(t, point, func() { r.env.Apply(ctx, p) })
			if point == "mid-commit" {
				if _, err := r.env.Restore(ctx); err == nil {
					t.Fatal("restore past the commit point")
				}
			}
			res, err := r.env.Resume(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != stCommitted {
				t.Fatalf("status %s", res.Status)
			}
			for _, gone := range []string{"units/baft.service", "bin/baft", "bin/baft-agent"} {
				if exists(r.p(gone)) {
					t.Errorf("%s still there", gone)
				}
			}
			if !exists(r.p("etc/baft.yaml")) {
				t.Fatal("data removed")
			}
			if pj, _ := r.env.Pending(); pj != nil {
				t.Fatal("still pending")
			}
		})
	}
}

// A file that changes between plan and apply is not removed; the run rolls back.
func TestChangedSincePlanRollsBack(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	p := r.plan(yes(Options{Scope: Scope{Services: true}, Delete: map[Class]bool{ClassTunnelConfigs: true}}))
	r.write("etc/baft.yaml", r.exConfig("etc")+"# edited\n", 0o640)
	before := r.snapshot()
	if _, err := r.env.Apply(ctx, p); err == nil || !strings.Contains(err.Error(), "changed since the plan") {
		t.Fatalf("apply: %v", err)
	}
	if d := diffSnap(before, r.snapshot()); len(d) > 0 {
		t.Fatalf("not restored: %v", d)
	}
}

// Files in BAFT's directories that BAFT does not write are never touched,
// and their directory stays.
func TestUnknownFilesStay(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	r.write("etc/notes.txt", "mine", 0o644)
	r.write("etc/pki/extra.pem", pemCert, 0o644)
	r.write("etc/noise-key.json.bak", "x", 0o600)
	p := r.plan(yes(Options{Scope: Scope{Full: true}, Delete: map[Class]bool{ClassTunnelConfigs: true, ClassCertificates: true, ClassBackups: true}}))
	for _, u := range []string{"etc/notes.txt", "etc/pki/extra.pem", "etc/noise-key.json.bak"} {
		if a := has(p.Untouched, r.p(u)); a == nil || a.Ownership != Unknown {
			t.Errorf("%s: %+v", u, a)
		}
	}
	if _, err := r.env.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"etc/notes.txt", "etc/pki/extra.pem", "etc/noise-key.json.bak"} {
		if !exists(r.p(u)) {
			t.Errorf("%s removed", u)
		}
	}
	if exists(r.p("etc/pki/ca.key")) || exists(r.p("etc/baft.yaml")) {
		t.Fatal("BAFT files kept")
	}
}

// Data left by an earlier keep-data uninstall can be deleted later.
func TestOrphanDataPurge(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	os.Remove(r.p("units/baft.service"))
	os.Remove(r.p("bin/baft"))
	os.Remove(r.p("bin/baft-pair"))
	p := r.plan(yes(Options{Scope: Scope{Services: true}, Delete: map[Class]bool{ClassTunnelConfigs: true, ClassCertificates: true}}))
	for _, w := range []string{"etc/baft.yaml", "etc/noise-key.json", "etc/pki/server.key", "opt/release-state.json", "var/pairing.psk"} {
		if has(p.Remove, r.p(w)) == nil {
			t.Errorf("%s not removed", w)
		}
	}
	if _, err := r.env.Apply(ctx, p); err != nil {
		t.Fatal(err)
	}
	if exists(r.p("etc")) || exists(r.p("var")) || exists(r.p("opt")) {
		t.Fatal("empty BAFT directories left")
	}
}

func TestSkeletonFiles(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	r.write("skel/.bashrc", "# skel\n", 0o644)
	r.write("skel/.profile", "# profile\n", 0o644)
	r.write("var/.bashrc", "# skel\n", 0o644)
	r.write("var/.profile", "# changed\n", 0o644)
	p := r.plan(yes(Options{}))
	if a := has(p.Remove, r.p("var/.bashrc")); a == nil || a.Class != ClassRuntime {
		t.Fatalf(".bashrc: %+v", a)
	}
	if a := has(p.Untouched, r.p("var/.profile")); a == nil {
		t.Fatal("edited .profile not protected")
	}
}

func TestPreviewRenderAndJSON(t *testing.T) {
	r := newRig(t)
	r.installerEX(true)
	r.agent(true)
	p := r.plan(Options{})
	var b strings.Builder
	p.Render(&b)
	out := b.String()
	for _, want := range []string{"BAFT Uninstall Preview", "Will stop:", "Will remove:", "Will KEEP:", "Default behavior: KEEP DATA",
		"Delete BCC state?", "Delete certificates?", "Delete backups?", "Delete tunnel configs?", "Delete audit history?", "WARNING: 1 active tunnel"} {
		if !strings.Contains(out, want) {
			t.Errorf("preview lacks %q\n%s", want, out)
		}
	}
	raw, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var j map[string]any
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	if j["default"] != "KEEP DATA" || j["data"].(map[string]any)["certificates"] != "keep" {
		t.Fatalf("json %s", raw)
	}
	if strings.Contains(string(raw), "cHJpdg") || strings.Contains(out, "cHJpdg") {
		t.Fatal("a key leaked into the preview")
	}
}

// Verification catches a side effect on a unit that was to be left alone.
func TestVerifyCatchesCollateralDamage(t *testing.T) {
	r := newRig(t)
	r.installerEX(true)
	r.write("hand/x.yaml", r.exConfig("hand"), 0o640)
	r.write("units/baft-hand.service", "[Service]\nExecStart=/bin/true\n", 0o644)
	r.sys.set("baft-hand.service", "active", "enabled")
	before := r.snapshot()
	r.sys.fail["daemon-reload"] = nil
	p := r.plan(yes(Options{}))
	// Simulate a dependency: stopping baft.service takes baft-hand down too.
	r.sys.mu.Lock()
	r.sys.fail["stop baft.service"] = nil
	r.sys.mu.Unlock()
	orig := r.sys.run
	_ = orig
	hook := &collateral{fakeSys: r.sys}
	r.env.System = hook
	_, err := r.env.Apply(ctx, p)
	if err == nil || !strings.Contains(err.Error(), "baft-hand.service was running") {
		t.Fatalf("apply: %v", err)
	}
	if d := diffSnap(before, r.snapshot()); len(d) > 0 {
		t.Fatalf("not restored: %v", d)
	}
}

type collateral struct{ *fakeSys }

func (c *collateral) Systemctl(cx context.Context, args ...string) (string, error) {
	out, err := c.fakeSys.Systemctl(cx, args...)
	if len(args) == 2 && args[0] == "stop" && args[1] == "baft.service" {
		c.fakeSys.set("baft-hand.service", "inactive", "enabled")
	}
	return out, err
}

// An agent unit edited by hand is not the installer's: never touched.
func TestEditedAgentUnitUntouched(t *testing.T) {
	r := newRig(t)
	r.agent(true)
	u, _ := os.ReadFile(r.p("units/baft-agent.service"))
	r.write("units/baft-agent.service", strings.Replace(string(u), "RestartSec=10s", "RestartSec=10s\nEnvironment=X=1", 1), 0o644)
	p := r.plan(yes(Options{Scope: Scope{Agent: true}}))
	if a := has(p.Untouched, r.p("units/baft-agent.service")); a == nil || a.Ownership != Unmanaged {
		t.Fatalf("agent unit %+v", a)
	}
	if a := has(p.Keep, r.p("bin/baft-agent")); a == nil {
		t.Fatal("binary of an untouched agent removed")
	}
	if len(p.Stop) != 0 {
		t.Fatalf("stopped %v", p.Stop)
	}
}

// A baft unit that cannot be read might run any BAFT binary: keep them.
func TestUnreadableUnitKeepsBinaries(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	r.write("elsewhere.service", "[Service]\nExecStart="+r.p("bin/baft")+" run --file /x.yaml\n", 0o644)
	os.Symlink(r.p("elsewhere.service"), r.p("units/baft-link.service"))
	p := r.plan(yes(Options{}))
	if a := has(p.Untouched, r.p("units/baft-link.service")); a == nil || a.Ownership != Unknown {
		t.Fatalf("link %+v", a)
	}
	for _, b := range []string{"bin/baft", "bin/baft-pair"} {
		if a := has(p.Keep, r.p(b)); a == nil || !strings.Contains(a.Reason, "could not be read") {
			t.Errorf("%s: %+v", b, a)
		}
	}
}

func TestSystemDirectoriesAreNeverClaimed(t *testing.T) {
	r := newRig(t)
	r.binaries("baft")
	r.write("units/baft.service", renderServiceUnit(serviceParams{User: "baft", Bin: r.p("bin/baft"), Config: "/etc/baft.yaml", StateDir: "/var/lib"}), 0o644)
	p := r.plan(yes(Options{Scope: Scope{Full: true}, Delete: map[Class]bool{ClassTunnelConfigs: true, ClassCertificates: true}}))
	for _, a := range p.Remove {
		if strings.HasPrefix(a.Path, "/etc/") || strings.HasPrefix(a.Path, "/var/lib/") {
			t.Fatalf("claimed %s", a.Path)
		}
	}
	for _, d := range p.Dirs {
		if d == "/etc" || d == "/var/lib" {
			t.Fatalf("would rmdir %s", d)
		}
	}
	if !strings.Contains(strings.Join(p.Errors, " "), "system directory") {
		t.Fatalf("errors %v", p.Errors)
	}
}

func TestRecoveryNoteNamesTheQuarantinedBinary(t *testing.T) {
	r := newRig(t)
	r.installerEX(false)
	p := r.plan(yes(Options{}))
	withCrash(t, "before-commit", func() { r.env.Apply(ctx, p) })
	pj, _ := r.env.Pending()
	note, err := os.ReadFile(filepath.Join(pj.Dir(), "README.txt"))
	if err != nil || !strings.Contains(string(note), "uninstall --restore") || !strings.Contains(string(note), "quarantine/") {
		t.Fatalf("note: %s %v", note, err)
	}
	// The quarantined binary is really there to run.
	for _, f := range pj.Files {
		if f.Class == ClassBinary && filepath.Base(f.Path) == "baft" {
			if !exists(filepath.Join(pj.Dir(), f.Quarantine)) {
				t.Fatal("binary not in the quarantine")
			}
		}
	}
	if _, err := r.env.Restore(ctx); err != nil {
		t.Fatal(err)
	}
}
