package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDetectTerm(t *testing.T) {
	utf := "en_US.UTF-8"
	cases := []struct {
		name  string
		env   map[string]string
		tty   bool
		width int
		want  termCaps
	}{
		{"truecolor wide", map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "LANG": utf}, true, 120, termCaps{true, colorFull, true, 120, layoutWide}},
		{"256 colors normal", map[string]string{"TERM": "xterm-256color", "LANG": utf}, true, 80, termCaps{true, color256, true, 80, layoutNormal}},
		{"plain xterm is monochrome", map[string]string{"TERM": "xterm", "LANG": utf}, true, 80, termCaps{true, colorMono, true, 80, layoutNormal}},
		{"NO_COLOR beats truecolor", map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "NO_COLOR": "1", "LANG": utf}, true, 100, termCaps{true, colorMono, true, 100, layoutWide}},
		{"TERM=dumb is plain", map[string]string{"TERM": "dumb", "COLORTERM": "truecolor"}, true, 80, termCaps{true, colorPlain, false, 80, layoutNormal}},
		{"not a tty is plain", map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "LANG": utf}, false, 80, termCaps{false, colorPlain, false, 80, layoutNormal}},
		{"no utf-8 locale falls back to ASCII", map[string]string{"TERM": "xterm-256color", "LANG": "C"}, true, 80, termCaps{true, color256, false, 80, layoutNormal}},
		{"phone width is compact", map[string]string{"TERM": "xterm-256color", "LANG": utf}, true, 45, termCaps{true, color256, true, 45, layoutCompact}},
		{"width from COLUMNS", map[string]string{"TERM": "xterm", "COLUMNS": "69", "LANG": utf}, true, 0, termCaps{true, colorMono, true, 69, layoutCompact}},
		{"unknown width defaults to 80", map[string]string{"TERM": "xterm", "LANG": utf}, true, 0, termCaps{true, colorMono, true, 80, layoutNormal}},
		{"boundary 70", map[string]string{"TERM": "xterm", "LANG": utf}, true, 70, termCaps{true, colorMono, true, 70, layoutNormal}},
		{"boundary 99", map[string]string{"TERM": "xterm", "LANG": utf}, true, 99, termCaps{true, colorMono, true, 99, layoutNormal}},
	}
	for _, c := range cases {
		if got := detectTerm(env(c.env), c.tty, c.width); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

var info = headerInfo{Version: "v0.1.2", Node: "ir-gold-01", Role: "IR (dialer)", Health: "HEALTHY", Release: "signed v0.1.1"}

func header(c termCaps, h headerInfo) string {
	var b bytes.Buffer
	renderHeader(&b, c, h)
	return b.String()
}

func TestHeaderPerModeIsAccessibleBoundedAndHonestAboutColor(t *testing.T) {
	for _, color := range []string{colorFull, color256, colorMono, colorPlain} {
		for _, uni := range []bool{true, false} {
			if color == colorPlain && uni {
				continue
			}
			for _, w := range []int{40, 60, 69, 70, 85, 99, 100, 140} {
				c := termCaps{TTY: true, Color: color, Unicode: uni, Width: w}
				switch {
				case w >= 100:
					c.Layout = layoutWide
				case w >= 70:
					c.Layout = layoutNormal
				default:
					c.Layout = layoutCompact
				}
				out := header(c, info)
				name := fmt.Sprintf("%s unicode=%v width=%d", color, uni, w)
				colored := color == colorFull || color == color256
				if colored != strings.Contains(out, "\x1b[") {
					t.Errorf("%s: escape sequences present=%v, want %v", name, strings.Contains(out, "\x1b["), colored)
				}
				plain := stripANSI(out)
				// The facts are always printed as words: color is never the only indicator.
				for _, want := range []string{"BAFT", "v0.1.2", "ir-gold-01", "IR (dialer)", "HEALTHY"} {
					if !strings.Contains(strings.ReplaceAll(plain, "B  A  F  T", "BAFT"), want) {
						t.Errorf("%s: header lacks %q:\n%s", name, want, plain)
					}
				}
				if !uni {
					for _, r := range plain {
						if r > 126 {
							t.Errorf("%s: non-ASCII %q in the ASCII fallback", name, r)
							break
						}
					}
				}
				for _, line := range strings.Split(strings.TrimRight(plain, "\n"), "\n") {
					if n := displayWidth(line); n > w {
						t.Errorf("%s: a line is %d columns wide:\n%s", name, n, line)
					}
				}
				if color == colorPlain && strings.ContainsAny(plain, "╭│╮╰╯▓▒░●─") {
					t.Errorf("%s: graphics on a dumb terminal", name)
				}
			}
		}
	}
}

func TestHeaderLayoutsDifferByWidthAndCompactIsTwoLines(t *testing.T) {
	wide := header(termCaps{TTY: true, Color: colorMono, Unicode: true, Width: 120, Layout: layoutWide}, info)
	normal := header(termCaps{TTY: true, Color: colorMono, Unicode: true, Width: 80, Layout: layoutNormal}, info)
	compact := header(termCaps{TTY: true, Color: colorMono, Unicode: true, Width: 50, Layout: layoutCompact}, info)
	if !(strings.Count(wide, "\n") > strings.Count(normal, "\n")-5 && strings.Count(compact, "\n") == 2) {
		t.Fatalf("wide %d lines, normal %d, compact %d", strings.Count(wide, "\n"), strings.Count(normal, "\n"), strings.Count(compact, "\n"))
	}
	if !strings.Contains(compact, "BAFT ◈ v0.1.2") || !strings.Contains(compact, "ir-gold-01 · IR (dialer) · HEALTHY") {
		t.Fatalf("compact header:\n%s", compact)
	}
	if !strings.Contains(wide, "Resilient Network Fabric") || !strings.Contains(wide, "Release  signed v0.1.1") {
		t.Fatalf("wide header:\n%s", wide)
	}
}

func TestHostileTextNeverReachesTheTerminal(t *testing.T) {
	if got := clean("a\x1b[31mb\x07c\x00d\u0085e\n"); got != "a[31mbcde" {
		t.Fatalf("clean = %q", got)
	}
	// The menu cleans what it prints and what it reads.
	h := headerInfo{Version: "v1", Node: clean("\x1b]0;pwned\x07evil\x1b[2J"), Role: "IR", Health: "HEALTHY", Release: "x"}
	out := header(termCaps{TTY: true, Color: colorFull, Unicode: true, Width: 120, Layout: layoutWide}, h)
	// Only our own color sequences may contain ESC; none may be a clear-screen or title sequence.
	if strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x1b]") {
		t.Fatal("an injected escape sequence reached the output")
	}
}

// ---- the menu ----

type menuRig struct {
	h        *fakeHost
	out      bytes.Buffer
	errOut   bytes.Buffer
	commands []string
	cfg      string
	dir      string
}

func newMenuRig(t *testing.T) *menuRig {
	t.Helper()
	r := &menuRig{h: newFakeHost(t), dir: t.TempDir()}
	r.cfg, _ = writeDialerConfig(t, 0o600)
	orig := r.h.env.output
	r.h.env.output = func(name string, args ...string) (string, error) {
		r.commands = append(r.commands, name+" "+strings.Join(args, " "))
		return orig(name, args...)
	}
	origStream := r.h.env.stream
	r.h.env.stream = func(name string, args []string, so, se io.Writer) error {
		r.commands = append(r.commands, name+" "+strings.Join(args, " "))
		return origStream(name, args, so, se)
	}
	return r
}

func (r *menuRig) run(t *testing.T, caps termCaps, input string, extra ...string) int {
	t.Helper()
	args := append([]string{"--file", r.cfg, "--release-state", writeReleaseState(t, "v"+version), "--unit-dir", filepath.Join(r.dir, "units"), "--state-dir", filepath.Join(r.dir, "state")}, extra...)
	return runMenu(args, strings.NewReader(input), &r.out, &r.errOut, r.h.env, caps, time.Now)
}

var monoCaps = termCaps{TTY: true, Color: colorMono, Unicode: true, Width: 100, Layout: layoutWide}

func TestMenuNavigationExitAndBadInput(t *testing.T) {
	for _, exit := range []string{"0\n", "q\n", "exit\n", ""} { // EOF exits too
		r := newMenuRig(t)
		if code := r.run(t, monoCaps, "banana\n\n99\n-1\n"+exit); code != 0 {
			t.Fatalf("exit via %q: %d", exit, code)
		}
		o := r.out.String()
		if strings.Count(o, "Unknown choice") != 3 || !strings.Contains(o, "Overview / Status") || !strings.Contains(o, "Exit") {
			t.Fatalf("output for %q:\n%s", exit, o)
		}
	}
}

func TestMenuShowsTheHeaderAndTheListInTheHQOrder(t *testing.T) {
	r := newMenuRig(t)
	r.run(t, monoCaps, "0\n")
	o := r.out.String()
	last := -1
	for _, item := range []string{"Overview / Status", "Doctor", "Servers", "Tunnels", "BCC", "Monitoring", "Certificates", "Backup / Restore", "Logs", "Support bundle", "Update", "Repair", "Uninstall", "Advanced", "Exit"} {
		i := strings.Index(o, item)
		if i < 0 || i < last {
			t.Fatalf("%q missing or out of order in:\n%s", item, o)
		}
		last = i
	}
	for _, planned := range []string{"Update (planned)", "Repair (planned)", "Uninstall (planned)"} {
		if !strings.Contains(o, planned) {
			t.Errorf("%s is not marked as planned", planned)
		}
	}
	if !strings.Contains(o, "B  A  F  T") || !strings.Contains(o, "ir-01") || !strings.Contains(o, "IR (dialer)") || !strings.Contains(o, "HEALTHY") {
		t.Fatalf("header context missing:\n%s", o)
	}
}

func TestMenuRunsTheRealCommands(t *testing.T) {
	r := newMenuRig(t)
	r.run(t, monoCaps, "1\n\n2\n1\n\n0\n0\n")
	o := r.out.String()
	if !strings.Contains(o, "baft: active, restarts 0") || !strings.Contains(o, "3 active") {
		t.Errorf("status was not shown:\n%s", o)
	}
	if !strings.Contains(o, "BAFT doctor:") || !strings.Contains(o, "Layers") {
		t.Errorf("doctor was not shown:\n%s", o)
	}
}

func TestPlannedItemsDoNothing(t *testing.T) {
	r := newMenuRig(t)
	before := len(r.commands)
	r.run(t, monoCaps, "11\n\n12\n\n13\n\n0\n")
	o := r.out.String()
	for _, name := range []string{"Update", "Repair", "Uninstall"} {
		if !strings.Contains(o, name+" is not available in this version yet.") {
			t.Errorf("%s: no honest message", name)
		}
	}
	if !strings.Contains(o, "Nothing was changed.") {
		t.Error("planned items must say nothing was changed")
	}
	// Only the header's own read-only service/metrics queries ran.
	for _, c := range r.commands[before:] {
		if !strings.HasPrefix(c, "systemctl is-active") && !strings.HasPrefix(c, "systemctl show") {
			t.Errorf("a planned item ran %q", c)
		}
	}
}

func TestMenuIsReadOnlyAcrossEveryItem(t *testing.T) {
	r := newMenuRig(t)
	cwd, _ := os.Getwd()
	work := t.TempDir()
	os.Chdir(work)
	t.Cleanup(func() { os.Chdir(cwd) })
	// Every item and submenu entry; the support bundle is answered with "n".
	var in strings.Builder
	for _, k := range []string{"1", "3", "4", "5", "6", "7", "8"} {
		in.WriteString(k + "\n\n")
	}
	in.WriteString("2\n1\n\n2\n\n0\n")                                     // doctor: run, preview fixes, back
	in.WriteString("9\n1\n\n2\n\n0\n")                                     // logs
	in.WriteString("10\nn\n\n")                                            // support bundle: declined
	in.WriteString("14\n1\n\n2\n\n3\n\n4\n\n5\n\n6\n\n7\n\n8\n\n9\n\n0\n") // advanced, each entry
	in.WriteString("0\n")
	if code := r.run(t, monoCaps, in.String()); code != 0 {
		t.Fatalf("menu ended with %d: %s", code, r.errOut.String())
	}
	for _, c := range r.commands {
		ok := strings.HasPrefix(c, "systemctl is-active ") || strings.HasPrefix(c, "systemctl show ") ||
			strings.HasPrefix(c, "systemctl cat ") || strings.HasPrefix(c, "journalctl ")
		if !ok {
			t.Errorf("the menu ran %q", c)
		}
		if f := strings.Fields(c); len(f) > 1 {
			for _, bad := range []string{"restart", "stop", "start", "enable", "disable", "daemon-reload", "reload", "kill", "mask", "link", "edit", "set-property"} {
				if f[1] == bad {
					t.Errorf("the menu ran a mutating command: %q", c)
				}
			}
		}
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Fatalf("the menu created files without being asked: %v", entries)
	}
	if r.h.dialed == nil {
		t.Log("doctor dialed nothing")
	}
}

func TestSupportBundleNeedsAnExplicitYes(t *testing.T) {
	cwd, _ := os.Getwd()
	for answer, wantFile := range map[string]bool{"n\n": false, "\n": false, "no\n": false, "y\n": true, "YES\n": true} {
		r := newMenuRig(t)
		work := t.TempDir()
		os.Chdir(work)
		r.run(t, monoCaps, "10\n"+answer+"\n0\n")
		os.Chdir(cwd)
		entries, _ := os.ReadDir(work)
		got := len(entries) == 1 && strings.HasPrefix(entries[0].Name(), "baft-support-")
		if got != wantFile || (!wantFile && len(entries) != 0) {
			t.Errorf("answer %q: files %v, want a bundle=%v", strings.TrimSpace(answer), entries, wantFile)
		}
	}
}

func TestTunnelsViewIsALocalReadOnlyInventory(t *testing.T) {
	r := newMenuRig(t)
	units := filepath.Join(r.dir, "units")
	os.MkdirAll(units, 0o755)
	cfgDir := filepath.Dir(r.cfg)
	os.WriteFile(filepath.Join(units, "baft.service"), []byte("[Service]\n# baft-managed: true\n# baft-tunnel: tun-1\nExecStart=/usr/local/bin/baft run --file "+r.cfg+"\nEnvironment=SECRET=LEAKME\n"), 0o644)
	os.WriteFile(filepath.Join(cfgDir, "baft.managed.json"), []byte(`{"managed_by":"baft","tunnel_id":"tun-1","generation":3,"role":"ir"}`), 0o644)
	snap := func() map[string]string {
		m := map[string]string{}
		filepath.Walk(r.dir, func(p string, i os.FileInfo, err error) error {
			if err == nil && i.Mode().IsRegular() {
				b, _ := os.ReadFile(p)
				m[p] = string(b) + i.ModTime().String()
			}
			return nil
		})
		return m
	}
	before := snap()
	r.run(t, monoCaps, "4\n\n0\n", "--service", "baft")
	o := r.out.String()
	for _, want := range []string{"baft.service", "(this node's service)", "role IR (dialer)", "managed_by baft, tunnel tun-1, generation 3, role ir", "never adopts"} {
		if !strings.Contains(o, want) {
			t.Errorf("tunnels view lacks %q:\n%s", want, o)
		}
	}
	if strings.Contains(o, "LEAKME") {
		t.Fatal("the tunnels view leaked unit contents")
	}
	after := snap()
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("the tunnels view changed %s", k)
		}
	}
}

func selfSigned(t *testing.T, cn string, notAfter time.Time) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), cn+".pem")
	os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	return p
}

func TestCertificateStatusShowsExpiry(t *testing.T) {
	for name, tc := range map[string]struct {
		notAfter time.Time
		want     []string
	}{
		"healthy":  {time.Now().Add(200 * 24 * time.Hour), []string{"ACTIVE", "days left"}},
		"expiring": {time.Now().Add(10 * 24 * time.Hour), []string{"WARNING", "days left"}},
		"expired":  {time.Now().Add(-5 * 24 * time.Hour), []string{"FAILED", "EXPIRED"}},
	} {
		r := newMenuRig(t)
		ca := selfSigned(t, "ca-"+name, tc.notAfter)
		raw, _ := os.ReadFile(r.cfg)
		os.WriteFile(r.cfg, []byte(strings.ReplaceAll(string(raw), "/tmp/ca.pem", ca)), 0o600)
		r.run(t, monoCaps, "7\n\n0\n")
		o := r.out.String()
		for _, w := range tc.want {
			if !strings.Contains(o, w) {
				t.Errorf("%s: output lacks %q:\n%s", name, w, o)
			}
		}
	}
}

func TestDirectCommandsStayScriptFriendly(t *testing.T) {
	// Not a terminal: `baft` prints usage and exits 2 exactly as before, and
	// `baft menu` refuses; no splash, no escape sequence anywhere.
	pr, pw, _ := os.Pipe()
	defer pr.Close()
	defer pw.Close()
	var errOut bytes.Buffer
	if code := menuEntry(nil, pr, pw, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage:") {
		t.Fatalf("baft without a terminal: %d %q", code, errOut.String())
	}
	errOut.Reset()
	if code := menuEntry([]string{"menu"}, pr, pw, &errOut); code != 2 || !strings.Contains(errOut.String(), "interactive terminal") {
		t.Fatalf("baft menu without a terminal: %d %q", code, errOut.String())
	}
	h := newFakeHost(t)
	cfg, _ := writeDialerConfig(t, 0o600)
	for _, args := range [][]string{{"status", "--json"}, {"doctor", "--json"}, {"status"}, {"doctor"}, {"version"}, {"version", "--json"}} {
		var out, e bytes.Buffer
		switch args[0] {
		case "status":
			runStatus(append(args[1:], "--file", cfg), &out, &e, h.env)
		case "doctor":
			runDoctor(append(args[1:], "--file", cfg), &out, &e, h.env)
		default:
			runVersion(args[1:], &out, &e)
		}
		if strings.ContainsAny(out.String()+e.String(), "\x1b") || strings.Contains(out.String(), "B  A  F  T") || strings.Contains(out.String(), "Resilient Network Fabric") {
			t.Errorf("baft %v printed terminal decoration", args)
		}
	}
}

func TestMenuNeverPrintsInjectedTerminalControlSequences(t *testing.T) {
	old := hostnameFn
	hostnameFn = func() (string, error) { return "evil\x1b[2J\x1b]0;pwned\x07host", nil }
	t.Cleanup(func() { hostnameFn = old })
	r := newMenuRig(t)
	r.cfg = filepath.Join(r.dir, "missing.yaml") // so the hostname is what the header shows
	r.run(t, termCaps{TTY: true, Color: colorMono, Unicode: true, Width: 100, Layout: layoutWide}, "4\n\n0\n")
	o := r.out.String()
	if strings.ContainsAny(o, "\x1b\x07") {
		t.Fatalf("a control character reached the terminal: %q", o[:200])
	}
	if !strings.Contains(o, "evil[2J]0;pwnedhost") {
		t.Fatalf("the cleaned hostname is not shown:\n%s", o)
	}
}

func TestTheMenuSystemAdapterOnlyAllowsIsActive(t *testing.T) {
	r := newMenuRig(t)
	a := sysAdapter{r.h.env}
	for _, args := range [][]string{{"restart", "baft"}, {"stop", "baft"}, {"enable", "baft"}, {"daemon-reload"}, {}} {
		if _, err := a.Systemctl(nil, args...); err == nil {
			t.Errorf("systemctl %v was allowed", args)
		}
	}
	if _, err := a.Run(nil, "rm", "-rf", "/"); err == nil {
		t.Error("Run was allowed")
	}
	if out, err := a.Systemctl(nil, "is-active", "baft"); err != nil || out != "active" {
		t.Errorf("is-active: %q %v", out, err)
	}
	for _, c := range r.commands {
		if !strings.HasPrefix(c, "systemctl is-active") {
			t.Errorf("ran %q", c)
		}
	}
}

func TestIsTerminalIsARealTerminalTestNotJustACharacterDevice(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if st, _ := null.Stat(); st.Mode()&os.ModeCharDevice == 0 {
		t.Skip("/dev/null is not a character device here")
	}
	if isTerminal(null) {
		t.Fatal("/dev/null (a character device that is not a terminal) was taken for a terminal")
	}
	pr, pw, _ := os.Pipe()
	defer pr.Close()
	defer pw.Close()
	reg, _ := os.CreateTemp(t.TempDir(), "x")
	defer reg.Close()
	for name, f := range map[string]*os.File{"pipe": pr, "pipe write end": pw, "regular file": reg} {
		if isTerminal(f) {
			t.Errorf("%s was taken for a terminal", name)
		}
	}
	if pty := openPTYForTest(t); pty != nil && !isTerminal(pty) {
		t.Fatal("a real pseudo-terminal was not recognized")
	}
}

func TestBaftWithCharacterDevicesThatAreNotTerminalsKeepsTheOldContract(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	var errOut bytes.Buffer
	if code := menuEntry(nil, null, null, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage:") {
		t.Fatalf("baft with stdin and stdout on /dev/null entered the menu or changed the contract: %d %q", code, errOut.String())
	}
	errOut.Reset()
	if code := menuEntry([]string{"menu"}, null, null, &errOut); code != 2 || !strings.Contains(errOut.String(), "interactive terminal") {
		t.Fatalf("baft menu on /dev/null: %d %q", code, errOut.String())
	}
}

func TestLongValuesAreBoundedAndNO_COLORStaysFreeOfEscapes(t *testing.T) {
	long := strings.Repeat("n", 64) // a valid 64-byte node id
	h := headerInfo{Version: "v0.1.2", Node: long, Role: "IR (dialer)", Health: "DEGRADED", Release: "signed v0.1.2-rc.1+" + strings.Repeat("r", 40)}
	for _, color := range []string{colorMono, colorPlain, color256, colorFull} {
		for _, uni := range []bool{true, false} {
			for _, w := range []int{30, 40, 50, 60, 69, 70, 72, 80, 99, 100, 110, 140} {
				c := termCaps{TTY: true, Color: color, Unicode: uni && color != colorPlain, Width: w}
				switch {
				case w >= 100:
					c.Layout = layoutWide
				case w >= 70:
					c.Layout = layoutNormal
				default:
					c.Layout = layoutCompact
				}
				out := header(c, h)
				name := fmt.Sprintf("%s unicode=%v width=%d", color, c.Unicode, w)
				if (color == colorMono || color == colorPlain) && strings.Contains(out, "\x1b") {
					t.Errorf("%s: an escape sequence with no color: %q", name, out[:min(len(out), 120)])
				}
				for _, line := range strings.Split(strings.TrimRight(stripANSI(out), "\n"), "\n") {
					if n := displayWidth(line); n > w {
						t.Errorf("%s: a line is %d columns:\n%s", name, n, line)
					}
				}
			}
		}
	}
}
