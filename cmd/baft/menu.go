package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/tunnelnode"
	"github.com/zarkmakerburg/baft/internal/uninstall"
)

// The interactive BAFT menu (HQ A3): `baft` on a terminal. It is a front for
// what already exists, over plain numbered lines (no raw mode, so it works over
// SSH and on a phone). It is READ-ONLY apart from the support bundle, which
// writes one archive after an explicit yes, and Uninstall (13), which runs the
// same flow as `baft uninstall`: an exact preview, separate confirmations
// (each data class defaults to NO, active tunnels, then typing "uninstall"),
// and a journaled, verified removal. Anything that is not built yet is marked
// "(planned)" and says so when chosen; nothing here installs, repairs or
// updates anything.

type menuCfg struct {
	file, service, releaseState string
	unitDir, stateDir           string
	agentUnit, bccService       string
}

type menu struct {
	in   *bufio.Reader
	out  io.Writer
	errw io.Writer
	env  opsEnv
	cfg  menuCfg
	caps termCaps
	st   style
	now  func() time.Time
	host func() (string, error)
}

type menuItem struct {
	label   string
	planned bool
	run     func(m *menu)
}

// hostnameFn is os.Hostname; tests replace it.
var hostnameFn = os.Hostname

func runMenu(args []string, in io.Reader, stdout, stderr io.Writer, env opsEnv, caps termCaps, now func() time.Time) int {
	var mc menuCfg
	f, ok := parseOpsFlags("menu", args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&mc.unitDir, "unit-dir", "/etc/systemd/system", "systemd unit directory")
		fs.StringVar(&mc.stateDir, "state-dir", "/var/lib/baft", "BAFT state directory")
		fs.StringVar(&mc.agentUnit, "agent-unit", "baft-agent", "agent service name")
		fs.StringVar(&mc.bccService, "bcc-service", "baft-bcc", "BCC service name")
	})
	if !ok || f.json {
		fmt.Fprintln(stderr, "usage: baft menu [--file baft.yaml] [--service baft] [--release-state path] [--unit-dir dir] [--state-dir dir]")
		return 2
	}
	mc.file, mc.service, mc.releaseState = f.file, f.service, f.releaseState
	m := &menu{in: bufio.NewReader(in), out: stdout, errw: stderr, env: env, cfg: mc, caps: caps, st: style{caps}, now: now, host: hostnameFn}
	return m.loop()
}

func (m *menu) printf(format string, a ...any) { fmt.Fprintf(m.out, format, a...) }

// readLine returns the next input line without its newline; ok is false at EOF.
func (m *menu) readLine() (string, bool) {
	s, err := m.in.ReadString('\n')
	if err != nil && s == "" {
		return "", false
	}
	return clean(strings.TrimSpace(s)), true
}

func (m *menu) pause() {
	m.printf("\n%s", m.st.dim("Press Enter to return... "))
	m.readLine()
}

// ask prints a question and returns the (cleaned) answer.
func (m *menu) ask(q string) (string, bool) {
	m.printf("%s", q)
	return m.readLine()
}

func (m *menu) confirm(q string) bool {
	a, ok := m.ask(q + " [y/N]: ")
	return ok && (strings.EqualFold(a, "y") || strings.EqualFold(a, "yes"))
}

func (m *menu) loop() int {
	renderHeader(m.out, m.caps, m.header())
	items := m.mainItems()
	for {
		m.printf("\n")
		for i, it := range items {
			m.printf("  %s %s\n", m.st.highlight(fmt.Sprintf("%2d", i+1)), m.itemLabel(it))
		}
		m.printf("  %s %s\n", m.st.highlight(" 0"), "Exit")
		a, ok := m.ask(m.st.gold("\nbaft> "))
		if !ok {
			m.printf("\n")
			return 0
		}
		switch strings.ToLower(a) {
		case "0", "q", "quit", "exit":
			return 0
		case "":
			continue
		}
		n, err := strconv.Atoi(a)
		if err != nil || n < 1 || n > len(items) {
			m.printf("%s\n", m.st.dim("Unknown choice "+quote(a)+". Enter a number from the list, or 0 to exit."))
			continue
		}
		m.printf("\n")
		m.runItem(items[n-1])
	}
}

func quote(s string) string { return "'" + clean(s) + "'" }

func (m *menu) itemLabel(it menuItem) string {
	if it.planned {
		return it.label + " " + m.st.dim("(planned)")
	}
	return it.label
}

func (m *menu) runItem(it menuItem) {
	if it.planned {
		m.printf("%s is not available in this version yet.\nNothing was changed.\n", it.label)
	} else {
		it.run(m)
	}
	m.pause()
}

func (m *menu) submenu(title string, items []menuItem) {
	for {
		m.printf("\n%s\n", m.st.goldBold(title))
		for i, it := range items {
			m.printf("  %s %s\n", m.st.highlight(fmt.Sprintf("%2d", i+1)), m.itemLabel(it))
		}
		m.printf("  %s %s\n", m.st.highlight(" 0"), "Back")
		a, ok := m.ask(m.st.gold("\nbaft> "))
		if !ok || a == "0" || strings.EqualFold(a, "b") || strings.EqualFold(a, "back") {
			return
		}
		n, err := strconv.Atoi(a)
		if err != nil || n < 1 || n > len(items) {
			m.printf("%s\n", m.st.dim("Unknown choice "+quote(a)+"."))
			continue
		}
		m.printf("\n")
		m.runItem(items[n-1])
	}
}

// ---- header context ----

func (m *menu) loadConfig() (config.Config, error) { return config.LoadFile(m.cfg.file) }

func roleLabel(r string) string {
	switch r {
	case "dialer":
		return "IR (dialer)"
	case "listener":
		return "EX (listener)"
	case "":
		return "unknown"
	}
	return clean(r)
}

// quickHealth is a fast local verdict for the header: the service state and the
// loopback metrics only, no remote probe, so opening the menu is instant. The
// full picture is `baft doctor`.
func (m *menu) quickHealth(cfg config.Config, cfgErr error) string {
	state, restarts := m.env.serviceState(m.cfg.service)
	switch {
	case strings.HasPrefix(state, "unknown"):
		return "UNKNOWN"
	case state != "active":
		return "DOWN"
	case cfgErr != nil:
		return "DEGRADED"
	}
	if restarts != "" && restarts != "0" {
		return "DEGRADED"
	}
	met, err := m.env.metrics(cfg)
	if err != nil || met["baft_conservation_invariant_violations"] != 0 {
		return "DEGRADED"
	}
	return "HEALTHY"
}

func (m *menu) header() headerInfo {
	cfg, cfgErr := m.loadConfig()
	h := headerInfo{Version: "v" + strings.TrimPrefix(version, "v"), Node: "unknown", Role: "unknown", Release: "unknown"}
	if name, err := m.host(); err == nil && name != "" {
		h.Node = clean(name)
	}
	if cfgErr == nil {
		if cfg.Node.ID != "" {
			h.Node = clean(cfg.Node.ID)
		}
		h.Role = roleLabel(cfg.Node.Role)
	}
	h.Health = m.quickHealth(cfg, cfgErr)
	if rs, err := readReleaseState(m.cfg.releaseState); err == nil {
		h.Release = "signed " + clean(rs.Version)
	} else {
		h.Release = "no release state (source install?)"
	}
	return h
}

// ---- items ----

func (m *menu) mainItems() []menuItem {
	return []menuItem{
		{label: "Overview / Status", run: func(m *menu) { m.runCmd("status") }},
		{label: "Doctor", run: func(m *menu) { m.doctorMenu() }},
		{label: "Servers", run: (*menu).servers},
		{label: "Tunnels (this node)", run: (*menu).tunnels},
		{label: "BCC", run: (*menu).bcc},
		{label: "Monitoring", run: (*menu).monitoring},
		{label: "Certificates", run: (*menu).certificates},
		{label: "Backup / Restore", run: (*menu).backup},
		{label: "Logs", run: func(m *menu) { m.logsMenu() }},
		{label: "Support bundle", run: (*menu).supportBundle},
		{label: "Update", planned: true},
		{label: "Repair", planned: true},
		{label: "Uninstall", run: (*menu).uninstallMenu},
		{label: "Advanced", run: func(m *menu) { m.advancedMenu() }},
	}
}

// runCmd runs one of the direct commands with this menu's paths.
func (m *menu) runCmd(name string, extra ...string) {
	args := append([]string{"--file", m.cfg.file, "--service", m.cfg.service, "--release-state", m.cfg.releaseState}, extra...)
	switch name {
	case "status":
		runStatus(args, m.out, m.errw, m.env)
	case "doctor":
		runDoctor(args, m.out, m.errw, m.env)
	case "support-bundle":
		runSupportBundle(args, m.out, m.errw, m.env, m.now)
	}
}

func (m *menu) doctorMenu() {
	m.submenu("Doctor", []menuItem{
		{label: "Run doctor", run: func(m *menu) { m.runCmd("doctor") }},
		{label: "Preview fixes (shows commands, never runs them)", run: func(m *menu) { m.runCmd("doctor", "--preview-fixes") }},
	})
}

func (m *menu) logsMenu() {
	show := func(n int) func(*menu) {
		return func(m *menu) {
			runLogs([]string{"--service", m.cfg.service, "-n", strconv.Itoa(n)}, m.out, m.errw, m.env)
			m.printf("\nTo follow the log live, leave the menu and run: baft logs -f\n")
		}
	}
	m.submenu("Logs", []menuItem{
		{label: "Last 100 lines", run: show(100)},
		{label: "Last 500 lines", run: show(500)},
	})
}

func (m *menu) servers() {
	m.printf("Servers are registered and managed in BCC (Servers, Bootstrap, Tunnels).\n\nThis server:\n")
	cfg, err := m.loadConfig()
	if err == nil {
		m.printf("  node id     %s\n  role        %s\n", clean(cfg.Node.ID), roleLabel(cfg.Node.Role))
	} else {
		m.printf("  config      %s\n", m.st.dim("cannot be loaded: "+clean(err.Error())))
	}
	state, _ := m.env.serviceState(m.cfg.agentUnit)
	m.printf("  agent       %s (%s)\n", m.st.status(strings.ToUpper(firstWord(state))), clean(m.cfg.agentUnit))
	m.printf("\nEnrolling another server is done from BCC; the installer's --agent-only mode prepares it.\n")
}

func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return "unknown"
}

type sysAdapter struct{ env opsEnv }

func (s sysAdapter) Systemctl(_ context.Context, args ...string) (string, error) {
	// Discovery only ever asks `is-active`; anything else is refused here too.
	if len(args) == 0 || args[0] != "is-active" {
		return "", fmt.Errorf("the menu is read-only: systemctl %v refused", args)
	}
	return s.env.output("systemctl", args...)
}
func (s sysAdapter) Run(context.Context, string, ...string) (string, error) {
	return "", fmt.Errorf("the menu is read-only")
}

func (m *menu) tunnels() {
	mgr := &tunnelnode.Manager{Env: tunnelnode.Env{
		ConfigDir: filepath.Dir(m.cfg.file), StateDir: m.cfg.stateDir, UnitDir: m.cfg.unitDir, Service: m.cfg.service, System: sysAdapter{m.env},
	}}
	rep := mgr.Discover(context.Background())
	m.printf("Local view of this node (read-only). BCC classifies each unit as MANAGED, DRIFTED, MISSING,\nDISCOVERED_UNMANAGED, OWNERSHIP_CONFLICT or UNKNOWN (BCC -> Existing tunnels).\n\n")
	shown := 0
	for _, in := range rep.Instances {
		if !in.Present {
			continue
		}
		shown++
		tag := ""
		if in.Primary {
			tag = " (this node's service)"
		}
		m.printf("%s%s\n", m.st.goldBold(clean(in.Unit)), tag)
		m.printf("  service   %s\n", m.st.status(strings.ToUpper(firstWord(in.ServiceState))))
		switch {
		case !in.Recognized:
			m.printf("  unit      %s\n", m.st.dim(clean(in.Problem)))
			continue
		case !in.ConfigLoads:
			m.printf("  config    %s %s\n", clean(in.ConfigPath), m.st.dim("("+clean(orStr(in.Problem, "not readable"))+")"))
		default:
			m.printf("  config    %s  role %s\n", clean(in.ConfigPath), roleLabel(in.ConfigRole))
			if in.PeerAddress != "" {
				m.printf("  peer      %s\n", clean(in.PeerAddress))
			}
			if in.Listen != "" {
				m.printf("  listen    %s\n", clean(in.Listen))
			}
			if in.RouteID != "" {
				m.printf("  route     %s  listen %s  target %s\n", clean(in.RouteID), clean(orStr(in.RouteListen, "-")), clean(orStr(in.Target, "-")))
			}
		}
		if in.Marker != nil {
			m.printf("  marker    managed_by %s, tunnel %s, generation %d, role %s\n", clean(in.Marker.ManagedBy), clean(in.Marker.TunnelID), in.Marker.Generation, clean(in.Marker.Role))
		} else if in.MarkerProblem != "" {
			m.printf("  marker    %s\n", m.st.dim(clean(in.MarkerProblem)))
		} else {
			m.printf("  marker    none (no BAFT ownership proof)\n")
		}
	}
	if shown == 0 {
		m.printf("No BAFT unit was found in %s.\n", clean(m.cfg.unitDir))
	}
	m.printf("\nThis menu never adopts, changes, restarts or removes a tunnel.\n")
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (m *menu) bcc() {
	state, _ := m.env.serviceState(m.cfg.bccService)
	m.printf("BCC service on this host: %s (%s)\n\n", m.st.status(strings.ToUpper(firstWord(state))), clean(m.cfg.bccService))
	m.printf("Run these on the BCC host (they are separate commands, not started from here):\n")
	m.printf("  baft-bcc access show                      show the secret admin URL\n")
	m.printf("  baft-bcc jobkey show                      the job-signing public key agents pin\n")
	m.printf("  baft-bcc restore-preview --backup FILE    what restoring a backup would do (BCC stopped)\n")
}

func (m *menu) monitoring() {
	cfg, err := m.loadConfig()
	if err != nil {
		m.printf("Configuration cannot be loaded: %s\n", clean(err.Error()))
		return
	}
	met, err := m.env.metrics(cfg)
	if err != nil {
		m.printf("The metrics endpoint does not answer: %s\n", clean(err.Error()))
		return
	}
	keys := make([]string, 0, len(met))
	for k := range met {
		if strings.HasPrefix(k, "baft_active_flows") || strings.HasPrefix(k, "baft_recovery_") || strings.HasPrefix(k, "baft_conservation_") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	m.printf("Local metrics (%s):\n", clean(cfg.Management.MetricsListen))
	for _, k := range keys {
		m.printf("  %-48s %g\n", clean(k), met[k])
	}
	if len(keys) == 0 {
		m.printf("  (no BAFT counters reported yet)\n")
	}
	m.printf("\nFleet monitoring, layered health and alerts are in BCC.\n")
}

func (m *menu) certificates() {
	cfg, err := m.loadConfig()
	if err != nil {
		m.printf("Configuration cannot be loaded: %s\n", clean(err.Error()))
		return
	}
	any := false
	for _, f := range []struct{ label, path string }{{"CA", cfg.TLS.CAFile}, {"certificate", cfg.TLS.CertFile}} {
		if f.path == "" {
			continue
		}
		any = true
		m.printf("%s  %s\n", m.st.goldBold(f.label), clean(f.path))
		certs, err := readCerts(f.path)
		if err != nil {
			m.printf("  %s\n", m.st.dim(clean(err.Error())))
			continue
		}
		for _, c := range certs {
			left := int(c.NotAfter.Sub(m.now()).Hours() / 24)
			status := "ACTIVE"
			note := fmt.Sprintf("%d days left", left)
			switch {
			case left < 0:
				status, note = "FAILED", fmt.Sprintf("EXPIRED %d days ago", -left)
			case left < 30:
				status = "WARNING"
			}
			m.printf("  %s  subject %s\n", m.st.status(status), clean(c.Subject.String()))
			m.printf("          valid until %s (%s)\n", c.NotAfter.UTC().Format("2006-01-02"), note)
		}
	}
	if !any {
		m.printf("This configuration lists no TLS certificate files.\n")
	}
	m.printf("\nTransactional certificate rotation is planned; today certificates are renewed by re-pairing.\n")
}

func readCerts(path string) ([]*x509.Certificate, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return nil, fmt.Errorf("not a regular file of at most 1 MiB")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []*x509.Certificate
	for len(out) < 8 {
		var blk *pem.Block
		blk, raw = pem.Decode(raw)
		if blk == nil {
			break
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		if c, err := x509.ParseCertificate(blk.Bytes); err == nil {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no certificate found in the file")
	}
	return out, nil
}

func (m *menu) backup() {
	m.printf("Backups are made by BCC (encrypted, daily and weekly) and restored locally on the BCC host.\n\n")
	m.printf("Preview a restore (read-only, BCC must be stopped):\n  baft-bcc restore-preview --backup FILE.baftbak --state-file bcc-state.json\n\n")
	m.printf("Commit a verified local restore (BCC must be stopped; explicit confirmation required):\n  baft-bcc restore --backup FILE.baftbak --state-file bcc-state.json --yes\n")
}

// uninstallMenu is HQ's uninstall submenu; every entry shows the exact plan
// first and changes nothing without the explicit confirmations.
func (m *menu) uninstallMenu() {
	run := func(s uninstall.Scope, preview bool) func(*menu) {
		return func(m *menu) {
			env := &uninstall.Env{System: uninstallSystem, MainPkg: uninstallMainPkg, RequireRoot: uninstallRoot, Log: m.errw,
				UnitDir: m.cfg.unitDir, StateDir: m.cfg.stateDir, ConfigDir: filepath.Dir(m.cfg.file),
				RefUnitDirs: []string{"/run/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system"}}
			if uninstallEnvHook != nil {
				uninstallEnvHook(env)
			}
			o := uninstall.Options{Scope: s, Delete: map[uninstall.Class]bool{}}
			runUninstallFlow(context.Background(), env, o, preview, false, uninstallIO{in: m.in, out: m.out, errw: m.errw, interactive: true})
		}
	}
	m.submenu("BAFT Uninstall", []menuItem{
		{label: "Remove BAFT binaries only", run: run(uninstall.Scope{Binaries: true}, false)},
		{label: "Remove Agent only", run: run(uninstall.Scope{Agent: true}, false)},
		{label: "Remove BCC only", run: run(uninstall.Scope{BCC: true}, false)},
		{label: "Remove BAFT services + binaries", run: run(uninstall.Scope{Services: true}, false)},
		{label: "Full uninstall", run: run(uninstall.Scope{Full: true}, false)},
		{label: "Preview uninstall", run: run(uninstall.Scope{}, true)},
	})
}

func (m *menu) supportBundle() {
	m.printf("The support bundle is one archive with status, doctor output, the config, release\nstate and recent logs. User payload is never included. It writes one file here.\n")
	if !m.confirm("Create it now?") {
		m.printf("Nothing was created.\n")
		return
	}
	m.runCmd("support-bundle")
}

// ---- advanced ----

func (m *menu) advancedMenu() {
	m.submenu("Advanced", []menuItem{
		{label: "Configuration", run: (*menu).advConfig},
		{label: "Release information", run: (*menu).advRelease},
		{label: "Agent information", run: (*menu).advAgent},
		{label: "Network diagnostics", run: (*menu).advNetwork},
		{label: "Debug information", run: (*menu).advDebug},
		{label: "Service information", run: (*menu).advService},
		{label: "Ownership / Drift information", run: (*menu).tunnels},
		{label: "Local paths", run: (*menu).advPaths},
		{label: "Version / Build metadata", run: (*menu).advBuild},
	})
}

func (m *menu) advConfig() {
	m.printf("Configuration file: %s\n", clean(m.cfg.file))
	var out, errOut bytes.Buffer
	if code := runConfig([]string{"validate", "--file", m.cfg.file}, &out, &errOut); code == 0 {
		m.printf("Validation: %s\n", m.st.status("ACTIVE")+" "+strings.TrimSpace(out.String()))
	} else {
		m.printf("Validation: %s %s\n", m.st.status("FAILED"), clean(strings.TrimSpace(errOut.String())))
	}
	m.printf("To change the configuration use BCC (tunnel builder) or the installer; this menu does not edit it.\n")
}

func (m *menu) advRelease() {
	rs, err := readReleaseState(m.cfg.releaseState)
	if err != nil {
		m.printf("No installer release state at %s (%s).\nA source install has none; production servers are installed from a signed release.\n", clean(m.cfg.releaseState), clean(err.Error()))
		return
	}
	m.printf("Installed signed release: %s\nCommit: %s\nBinary version: v%s\n", clean(rs.Version), clean(rs.Commit), strings.TrimPrefix(version, "v"))
	if strings.TrimPrefix(rs.Version, "v") != strings.TrimPrefix(version, "v") {
		m.printf("%s the binary and the release state disagree; re-run the installer.\n", m.st.status("WARNING"))
	}
}

func (m *menu) advAgent() {
	state, restarts := m.env.serviceState(m.cfg.agentUnit)
	m.printf("Agent service: %s (%s), restarts %s\n", m.st.status(strings.ToUpper(firstWord(state))), clean(m.cfg.agentUnit), clean(orStr(restarts, "unknown")))
	if s, err := m.env.output("systemctl", "show", m.cfg.agentUnit, "-p", "ActiveState,SubState,MainPID,ActiveEnterTimestamp"); err == nil {
		for _, l := range strings.Split(s, "\n") {
			m.printf("  %s\n", clean(l))
		}
	}
	m.printf("The agent runs only signed, allowlisted jobs from BCC; it has no shell access.\n")
}

func (m *menu) advNetwork() {
	m.runCmd("doctor")
	m.printf("\nThe network tuning findings above are recommendations; nothing is applied.\n")
}

func (m *menu) advDebug() {
	m.printf("baft %s  %s/%s  %s\n", clean(version), runtime.GOOS, runtime.GOARCH, runtime.Version())
	if h, err := m.host(); err == nil {
		m.printf("host %s\n", clean(h))
	}
	m.printf("terminal: %s, %s, width %d, layout %s\n", m.caps.Color, map[bool]string{true: "unicode", false: "ascii"}[m.caps.Unicode], m.caps.Width, m.caps.Layout)
}

func (m *menu) advService() {
	for _, s := range []string{m.cfg.service, m.cfg.agentUnit, m.cfg.bccService} {
		state, restarts := m.env.serviceState(s)
		m.printf("  %-18s %s  restarts %s\n", clean(s), m.st.status(strings.ToUpper(firstWord(state))), clean(orStr(restarts, "-")))
	}
}

func (m *menu) advPaths() {
	m.printf("  config         %s\n  release state  %s\n  state dir      %s\n  unit dir       %s\n",
		clean(m.cfg.file), clean(m.cfg.releaseState), clean(m.cfg.stateDir), clean(m.cfg.unitDir))
}

func (m *menu) advBuild() {
	m.printf("version  %s\n", clean(version))
	if bi, ok := debug.ReadBuildInfo(); ok {
		m.printf("go       %s\n", clean(bi.GoVersion))
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision", "vcs.time", "vcs.modified", "CGO_ENABLED", "-trimpath":
				m.printf("%-8s %s\n", clean(s.Key), clean(s.Value))
			}
		}
	}
}
