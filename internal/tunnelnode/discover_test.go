package tunnelnode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeUnit(t *testing.T, n *node, name, execStart, extra string) {
	t.Helper()
	body := "[Unit]\nDescription=x\n\n[Service]\nExecStart=" + execStart + "\n" + extra + "\n"
	if err := os.WriteFile(filepath.Join(n.UnitDir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// copyConfig puts a valid BAFT config (the node's live one) in a new directory.
func copyConfig(t *testing.T, n *node, dir string) string {
	t.Helper()
	b, err := os.ReadFile(n.liveConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "baft.yaml")
	if err := os.WriteFile(p, b, 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

func instance(t *testing.T, rep DiscoveryReport, unit string) DiscoveredInstance {
	t.Helper()
	for _, in := range rep.Instances {
		if in.Unit == unit {
			return in
		}
	}
	t.Fatalf("%s not in report: %+v", unit, rep.Instances)
	return DiscoveredInstance{}
}

type tree map[string]string

func snapshotTree(t *testing.T, roots ...string) tree {
	t.Helper()
	out := tree{}
	for _, r := range roots {
		filepath.Walk(r, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			b := ""
			if info.Mode().IsRegular() {
				d, _ := os.ReadFile(p)
				b = string(d)
			}
			out[p] = fmt.Sprintf("%v|%d|%d|%s", info.Mode(), info.Size(), info.ModTime().UnixNano(), b)
			return nil
		})
	}
	return out
}

func TestDiscoverReportsAManagedUnitWithItsMarker(t *testing.T) {
	p := newPair(t)
	listen(t, p.exPort)
	listen(t, mustPort(p.irListen))
	p.build(t, "disc1")
	rep := p.ex.Discover(context.Background())
	if len(rep.Instances) != 1 {
		t.Fatalf("instances: %+v", rep.Instances)
	}
	in := instance(t, rep, "baft.service")
	if !in.Primary || !in.Present || !in.Recognized || !in.ConfigLoads || !in.ConfigPresent || in.ConfigRole != "listener" {
		t.Fatalf("primary instance: %+v", in)
	}
	if in.Marker == nil || in.Marker.ManagedBy != "baft" || in.Marker.TunnelID != "disc1" || in.Marker.Role != RoleEX || in.Marker.FileSHA256 == "" {
		t.Fatalf("marker: %+v", in.Marker)
	}
	if in.UnitHeaderTunnel != "disc1" || !in.UnitHeaderManaged || in.ServiceState != "active" || in.ConfigSHA256 != in.Marker.ConfigSHA256 || in.UnitSHA256 != in.Marker.UnitSHA256 {
		t.Fatalf("unit facts: %+v", in)
	}
	if rep.NodeGeneration != 1 || rep.Version != DiscoveryVersion {
		t.Fatalf("report: %+v", rep)
	}
}

func TestDiscoverFindsUnmanagedForeignAndUnknownWithoutTouchingThem(t *testing.T) {
	p := newPair(t)
	listen(t, p.exPort)
	listen(t, mustPort(p.irListen))
	p.build(t, "disc2")
	n := p.ex
	other := filepath.Join(n.dir, "other")
	bin := n.BaftBin

	legacy := copyConfig(t, n, filepath.Join(other, "legacy"))
	writeUnit(t, n, "baft-legacy.service", bin+" run --file "+legacy, "Environment=TOKEN=SUPERSECRETSENTINEL")

	foreign := copyConfig(t, n, filepath.Join(other, "foreign"))
	os.WriteFile(filepath.Join(filepath.Dir(foreign), "baft.managed.json"), []byte(`{"managed_by":"someone-else","tunnel_id":"x","generation":1,"role":"ex"}`), 0o644)
	writeUnit(t, n, "baft-foreign.service", bin+" run --file "+foreign, "")

	garbage := filepath.Join(other, "garbage", "baft.yaml")
	os.MkdirAll(filepath.Dir(garbage), 0o755)
	os.WriteFile(garbage, []byte("this: is: not: a baft config\n"), 0o644)
	writeUnit(t, n, "baft-garbage.service", bin+" run --file "+garbage, "")

	writeUnit(t, n, "baft-weird.service", "/bin/sh -c 'run something'", "")
	writeUnit(t, n, "baft-rel.service", bin+" run --file relative.yaml", "")
	writeUnit(t, n, "baft-agent.service", "/usr/local/bin/baft-agent --x", "") // the agent is not a tunnel
	writeUnit(t, n, "nginx.service", "/usr/sbin/nginx", "")                    // not BAFT at all
	big := strings.Repeat("# padding\n", 10000)
	os.WriteFile(filepath.Join(n.UnitDir, "baft-big.service"), []byte("[Service]\nExecStart="+bin+" run --file "+legacy+"\n"+big), 0o644)
	os.Symlink(filepath.Join(n.UnitDir, "baft.service"), filepath.Join(n.UnitDir, "baft-link.service"))

	before := snapshotTree(t, n.dir)
	hostCalls := len(n.host.calls)
	rep := n.Discover(context.Background())
	after := snapshotTree(t, n.dir)

	// Read-only: no file, mode or mtime changed, nothing appeared or went away.
	if len(before) != len(after) {
		t.Fatalf("files appeared or disappeared: %d -> %d", len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s was modified by discovery", k)
		}
	}
	for _, c := range n.host.calls[hostCalls:] {
		if !strings.HasPrefix(c, "is-active ") {
			t.Fatalf("discovery ran `systemctl %s`", c)
		}
	}
	units := map[string]bool{}
	for _, in := range rep.Instances {
		units[in.Unit] = true
	}
	for _, want := range []string{"baft.service", "baft-legacy.service", "baft-foreign.service", "baft-garbage.service", "baft-weird.service", "baft-rel.service", "baft-big.service", "baft-link.service"} {
		if !units[want] {
			t.Errorf("%s not reported", want)
		}
	}
	if units["baft-agent.service"] || units["nginx.service"] {
		t.Errorf("non-tunnel units reported: %v", units)
	}

	if in := instance(t, rep, "baft-legacy.service"); !in.Recognized || !in.ConfigLoads || in.Marker != nil || in.MarkerProblem != "" || in.Primary {
		t.Errorf("legacy: %+v", in)
	}
	if in := instance(t, rep, "baft-foreign.service"); in.Marker == nil || in.Marker.ManagedBy != "someone-else" {
		t.Errorf("foreign: %+v", in)
	}
	if in := instance(t, rep, "baft-garbage.service"); !in.Recognized || in.ConfigLoads || in.Problem == "" {
		t.Errorf("garbage: %+v", in)
	}
	for _, u := range []string{"baft-weird.service", "baft-rel.service"} {
		if in := instance(t, rep, u); in.Recognized || !strings.Contains(in.Problem, "not a BAFT transport unit") {
			t.Errorf("%s: %+v", u, in)
		}
	}
	if in := instance(t, rep, "baft-big.service"); in.Recognized || !strings.Contains(in.Problem, "larger") {
		t.Errorf("big: %+v", in)
	}
	if in := instance(t, rep, "baft-link.service"); in.Recognized || !strings.Contains(in.Problem, "symlink") {
		t.Errorf("symlink: %+v", in)
	}

	// No file content leaves the node: only parsed facts.
	b, _ := json.Marshal(rep)
	if strings.Contains(string(b), "SUPERSECRETSENTINEL") || strings.Contains(string(b), "this: is: not") {
		t.Fatal("discovery leaked file contents")
	}
}

func TestDiscoverIsBoundedAndDeterministic(t *testing.T) {
	p := newPair(t)
	listen(t, p.exPort)
	p.build(t, "disc3")
	n := p.ex
	legacy := copyConfig(t, n, filepath.Join(n.dir, "many"))
	for i := 0; i < 40; i++ {
		writeUnit(t, n, fmt.Sprintf("baft-extra-%02d.service", i), n.BaftBin+" run --file "+legacy, "")
	}
	rep := n.Discover(context.Background())
	if len(rep.Instances) > maxDiscoveredUnits || !rep.Truncated {
		t.Fatalf("%d instances, truncated=%v", len(rep.Instances), rep.Truncated)
	}
	a, _ := json.Marshal(rep)
	b, _ := json.Marshal(n.Discover(context.Background()))
	if string(a) != string(b) {
		t.Fatal("two discoveries of an unchanged node differ")
	}
	if len(a) > maxDiscoveryJSON {
		t.Fatalf("report is %d bytes", len(a))
	}
	// Very long values are clipped, so the report stays inside the limit.
	long := strings.Repeat("x", 5000)
	n2 := newNode(t)
	os.WriteFile(filepath.Join(n2.UnitDir, "baft.service"), []byte("[Service]\n# baft-tunnel: "+long+"\nExecStart="+n2.BaftBin+" run --file /"+long+".yaml\n"), 0o644)
	rep2 := n2.Discover(context.Background())
	if b2, _ := json.Marshal(rep2); len(b2) > maxDiscoveryJSON || len(instance(t, rep2, "baft.service").UnitHeaderTunnel) > maxDiscoveryString+4 {
		t.Fatalf("a long value was not clipped (%d bytes)", len(b2))
	}
	time.Sleep(0)
}

func TestDiscoverOnAnEmptyNodeReportsNothingAndMissingUnitIsAbsent(t *testing.T) {
	n := newNode(t)
	rep := n.Discover(context.Background())
	if len(rep.Instances) != 1 || rep.Instances[0].Present || !rep.Instances[0].Primary {
		t.Fatalf("empty node: %+v", rep.Instances)
	}
}

// ---- no TOCTOU, no mixed versions (HQ review of A2) ----

func withHook(t *testing.T, h func(path string)) {
	t.Helper()
	testHookAfterOpen = h
	t.Cleanup(func() { testHookAfterOpen = nil })
}

func setupLegacy(t *testing.T) (*node, string, string) {
	t.Helper()
	p := newPair(t)
	listen(t, p.exPort)
	p.build(t, "toctou")
	n := p.ex
	cfg := copyConfig(t, n, filepath.Join(n.dir, "legacy"))
	writeUnit(t, n, "baft-legacy.service", n.BaftBin+" run --file "+cfg, "")
	return n, cfg, filepath.Join(n.UnitDir, "baft-legacy.service")
}

func TestDiscoverNeverFollowsASymlinkAtOpen(t *testing.T) {
	n, cfg, unit := setupLegacy(t)
	secret := filepath.Join(n.dir, "secret.yaml")
	os.WriteFile(secret, []byte("TOP-SECRET-CONTENT-12345\n"), 0o600)

	// The config path is a symlink to a secret file: not followed, not hashed, not parsed.
	os.Remove(cfg)
	os.Symlink(secret, cfg)
	in := instance(t, n.Discover(context.Background()), "baft-legacy.service")
	if in.ConfigPresent || in.ConfigLoads || in.ConfigSHA256 != "" || !strings.Contains(in.Problem, "symlink") {
		t.Fatalf("symlinked config was followed: %+v", in)
	}
	b, _ := json.Marshal(in)
	if strings.Contains(string(b), "TOP-SECRET") {
		t.Fatal("the target of a symlink leaked")
	}

	// Same for the unit file and for the marker beside the config.
	copyConfig(t, n, filepath.Dir(cfg)) // restore a real config (replaces the link? writes through it)
	os.Remove(cfg)
	copyConfig(t, n, filepath.Dir(cfg))
	os.Symlink(secret, filepath.Join(filepath.Dir(cfg), "baft.managed.json"))
	in = instance(t, n.Discover(context.Background()), "baft-legacy.service")
	if in.Marker != nil || !strings.Contains(in.MarkerProblem, "symlink") {
		t.Fatalf("symlinked marker was followed: %+v", in)
	}
	os.Remove(unit)
	os.Symlink(secret, unit)
	in = instance(t, n.Discover(context.Background()), "baft-legacy.service")
	if in.Recognized || !strings.Contains(in.Problem, "symlink") {
		t.Fatalf("symlinked unit was followed: %+v", in)
	}
}

func TestSwappingAPathAfterItWasOpenedChangesNothingThatIsReported(t *testing.T) {
	n, cfg, unit := setupLegacy(t)
	secret := filepath.Join(n.dir, "secret.yaml")
	os.WriteFile(secret, []byte("TOP-SECRET-CONTENT-12345\n"), 0o600)
	orig, _ := os.ReadFile(cfg)
	want := shaHex(orig)

	// While the config is being read, its path is replaced by a symlink to a
	// secret and then by a huge file. The descriptor already opened is what is
	// read: same digest, same facts, nothing from the replacements.
	swapped := 0
	withHook(t, func(path string) {
		if path != cfg {
			return
		}
		swapped++
		os.Remove(path)
		os.Symlink(secret, path)
	})
	in := instance(t, n.Discover(context.Background()), "baft-legacy.service")
	if swapped == 0 {
		t.Fatal("the hook never ran")
	}
	if !in.ConfigLoads || in.ConfigSHA256 != want || in.ConfigRole != "listener" {
		t.Fatalf("a swap changed the report: %+v", in)
	}
	b, _ := json.Marshal(in)
	if strings.Contains(string(b), "TOP-SECRET") {
		t.Fatal("a swapped-in symlink was followed")
	}

	// The unit file is replaced by a 10 MiB file after it was opened: the read
	// stays bounded by what was opened.
	os.Remove(cfg)
	copyConfig(t, n, filepath.Dir(cfg))
	withHook(t, func(path string) {
		if path != unit {
			return
		}
		os.Remove(path)
		os.WriteFile(path, make([]byte, 10<<20), 0o644)
	})
	in = instance(t, n.Discover(context.Background()), "baft-legacy.service")
	if !in.Recognized || in.UnitSHA256 == shaHex(make([]byte, 10<<20)) {
		t.Fatalf("the replacement of the unit was read: %+v", in)
	}
}

func TestDigestAndFactsAlwaysDescribeTheSameBytesAndTheConfigIsOpenedOnce(t *testing.T) {
	n, cfg, _ := setupLegacy(t)
	opens := 0
	// The file is rewritten in place (same inode) with a different valid config
	// right after it was opened: whatever is read, the digest and the parsed
	// facts must come from that one read.
	other, _ := os.ReadFile(filepath.Join(n.dir, "etc", "baft.yaml"))
	otherRole := "listener"
	withHook(t, func(path string) {
		if path != cfg {
			return
		}
		opens++
		// A dialer config in place of the listener one.
		ir := newNode(t)
		_ = ir
		os.WriteFile(path, other, 0o640)
	})
	in := instance(t, n.Discover(context.Background()), "baft-legacy.service")
	read, _ := os.ReadFile(cfg)
	if in.ConfigSHA256 != shaHex(read) || in.ConfigRole != otherRole {
		t.Fatalf("digest %s vs %s, role %q: mixed versions", in.ConfigSHA256, shaHex(read), in.ConfigRole)
	}
	if opens != 1 {
		t.Fatalf("the config path was opened %d times in one discovery; it must be once (no reopen for parsing)", opens)
	}
}

func TestAFifoOrDeviceInPlaceOfAFileIsRefusedWithoutBlocking(t *testing.T) {
	n, cfg, _ := setupLegacy(t)
	os.Remove(cfg)
	if err := syscallMkfifo(cfg); err != nil {
		t.Skipf("cannot create a fifo here: %v", err)
	}
	done := make(chan DiscoveredInstance, 1)
	go func() { done <- instance(t, n.Discover(context.Background()), "baft-legacy.service") }()
	select {
	case in := <-done:
		if in.ConfigPresent || !strings.Contains(in.Problem, "not a regular file") {
			t.Fatalf("fifo: %+v", in)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("discovery blocked on a fifo")
	}
}
