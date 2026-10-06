package uninstall

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zarkmakerburg/baft/internal/config"
)

const (
	maxUnits    = 256
	maxRefUnits = 2048
)

var baftUnitRe = regexp.MustCompile(`^baft[A-Za-z0-9._@-]{0,58}\.service$`)

// Unit is one baft*.service unit found on the host.
type Unit struct {
	Name         string    `json:"unit"`
	Path         string    `json:"path"`
	Kind         string    `json:"kind"` // transport, agent, bcc, other
	Ownership    Ownership `json:"ownership"`
	Evidence     string    `json:"evidence"`
	SHA256       string    `json:"sha256,omitempty"`
	ActiveState  string    `json:"active_state"`
	EnabledState string    `json:"enabled_state"`

	Config      string `json:"config,omitempty"`
	StateDir    string `json:"state_dir,omitempty"`
	Role        string `json:"role,omitempty"`
	Listen      string `json:"listen,omitempty"`
	Peer        string `json:"peer,omitempty"`
	Target      string `json:"target,omitempty"`
	RouteListen string `json:"route_listen,omitempty"`
	TunnelID    string `json:"tunnel_id,omitempty"`
	InProgress  string `json:"in_progress,omitempty"`

	execs     []string // programs the unit runs
	configSHA string
	cfg       *config.Config
	agent     *agentParams
	bcc       *bccPaths
}

func (u *Unit) active() bool {
	switch u.ActiveState {
	case "active", "activating", "reloading":
		return true
	}
	return false
}

func (u *Unit) enabled() bool { return strings.HasPrefix(u.EnabledState, "enabled") }

// Artifact is one file or directory the plan talks about.
type Artifact struct {
	Path      string    `json:"path"`
	Class     Class     `json:"class"`
	Component string    `json:"component"`
	Ownership Ownership `json:"ownership"`
	Evidence  string    `json:"evidence"`
	Dir       bool      `json:"dir,omitempty"`
	SHA256    string    `json:"sha256,omitempty"`
	Size      int64     `json:"size,omitempty"`
	Reason    string    `json:"reason,omitempty"`

	owner *Unit  // the unit it belongs to (nil: the node, agent or BCC defaults)
	area  string // services, agent, bcc, binary:<name>
	role  string // "bcc-state" for the BCC state database
}

// Inventory is everything inspection found. Inspection changes nothing.
type Inventory struct {
	Units     []*Unit
	Artifacts []*Artifact
	Errors    []string
	Pending   *Journal
	nested    []nestedDir
	// refs maps an absolute program path to the units that run it.
	refs map[string][]string
}

func (e *Env) defaults() {
	set := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	set(&e.UnitDir, "/etc/systemd/system")
	set(&e.BinDir, "/usr/local/bin")
	set(&e.Prefix, "/opt/baft")
	set(&e.ConfigDir, "/etc/baft")
	set(&e.StateDir, "/var/lib/baft")
	set(&e.AgentDir, "/etc/baft-agent")
	set(&e.AgentStateDir, "/var/lib/baft-agent")
	set(&e.JournalDir, "/var/lib/baft-uninstall")
	set(&e.BackupDir, "/var/backups/baft")
	set(&e.SkelDir, "/etc/skel")
	if e.BCCBinary == "" {
		e.BCCBinary = filepath.Join(e.BinDir, "baft-bcc")
	}
}

func (e *Env) systemctl(ctx context.Context, args ...string) string {
	if e.System == nil {
		return ""
	}
	out, _ := e.System.Systemctl(ctx, args...)
	return strings.TrimSpace(out)
}

// Inspect inventories BAFT on this host. It writes nothing and runs only
// `systemctl is-active` and `systemctl is-enabled`.
func (e *Env) Inspect(ctx context.Context) *Inventory {
	e.defaults()
	inv := &Inventory{refs: map[string][]string{}}
	if j, err := e.pendingJournal(); err == nil && j != nil {
		inv.Pending = j
	} else if err != nil {
		inv.Errors = append(inv.Errors, "uninstall journal: "+clip(err.Error()))
	}
	e.scanUnits(ctx, inv)
	e.scanRefs(inv)
	e.markConfigConflicts(inv)
	e.scanLocations(inv)
	e.scanBinaries(inv)
	return inv
}

func (e *Env) scanUnits(ctx context.Context, inv *Inventory) {
	ents, err := os.ReadDir(e.UnitDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			inv.Errors = append(inv.Errors, "unit directory: "+clip(err.Error()))
		}
		return
	}
	var names []string
	for _, ent := range ents {
		if baftUnitRe.MatchString(ent.Name()) {
			names = append(names, ent.Name())
		}
	}
	sort.Strings(names)
	if len(names) > maxUnits {
		inv.Errors = append(inv.Errors, "too many baft*.service units; only the first ones were inspected")
		names = names[:maxUnits]
	}
	for _, n := range names {
		inv.Units = append(inv.Units, e.inspectUnit(ctx, n))
	}
}

func (e *Env) inspectUnit(ctx context.Context, name string) *Unit {
	u := &Unit{Name: name, Path: filepath.Join(e.UnitDir, name), Kind: "other"}
	u.ActiveState = e.systemctl(ctx, "is-active", name)
	u.EnabledState = e.systemctl(ctx, "is-enabled", name)
	if u.ActiveState == "" {
		u.ActiveState = "unknown"
	}
	b, problem := readRegular(u.Path, maxUnitBytes)
	if problem != "" {
		u.Ownership, u.Evidence = Unknown, "unit file "+problem
		return u
	}
	raw := string(b)
	u.SHA256 = shaHex(b)
	u.execs = unitPrograms(raw)
	prog := ""
	if len(u.execs) > 0 {
		prog = u.execs[0]
	}
	switch filepath.Base(firstField(firstValue(raw, "ExecStart"))) {
	case "baft":
		u.Kind = "transport"
		e.classifyTransport(u, raw)
	case "baft-agent":
		u.Kind = "agent"
		e.classifyAgent(u, raw)
	case "baft-bcc":
		u.Kind = "bcc"
		e.classifyBCC(u, raw)
	default:
		if labelled(raw, "bcc") {
			u.Kind = "bcc"
			u.Ownership, u.Evidence = Conflict, "labelled as BAFT's BCC unit but it does not run baft-bcc"
			return u
		}
		u.Ownership, u.Evidence = Unmanaged, "not a BAFT unit (runs "+clip(prog)+")"
	}
	return u
}

func firstField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return strings.TrimLeft(f[0], "@-:+!")
}

// unitPrograms lists the absolute programs a unit runs (ExecStart*, ExecStop*,
// ExecReload, ExecCondition).
func unitPrograms(raw string) []string {
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		l = strings.TrimSpace(l)
		k, v, ok := strings.Cut(l, "=")
		if !ok || !strings.HasPrefix(k, "Exec") {
			continue
		}
		if p := firstField(v); filepath.IsAbs(p) {
			out = append(out, filepath.Clean(p))
		}
	}
	return out
}

func headerValue(raw, key string) (string, bool) {
	for _, l := range strings.Split(raw, "\n") {
		if strings.HasPrefix(l, "# "+key+":") {
			return strings.TrimSpace(strings.TrimPrefix(l, "# "+key+":")), true
		}
	}
	return "", false
}

func labelled(raw, component string) bool {
	m, _ := headerValue(raw, "baft-managed")
	c, _ := headerValue(raw, "baft-component")
	return m == "true" && c == component
}

func decodeConfig(path string, raw []byte) (config.Config, error) {
	if strings.HasSuffix(path, ".json") {
		return config.DecodeJSON(bytes.NewReader(raw))
	}
	return config.DecodeYAML(bytes.NewReader(raw))
}

type marker struct {
	ManagedBy    string `json:"managed_by"`
	TunnelID     string `json:"tunnel_id"`
	Generation   int    `json:"generation"`
	Role         string `json:"role"`
	ConfigSHA256 string `json:"config_sha256"`
	UnitSHA256   string `json:"unit_sha256"`
}

func (e *Env) classifyTransport(u *Unit, raw string) {
	f := strings.Fields(firstValue(raw, "ExecStart"))
	if len(f) != 4 || f[1] != "run" || f[2] != "--file" || !filepath.IsAbs(f[3]) || strings.Contains(f[3], "..") {
		u.Ownership, u.Evidence = Unmanaged, "runs baft, but not as `baft run --file <absolute config>`"
		return
	}
	u.Config = filepath.Clean(f[3])
	if rw := strings.Fields(firstValue(raw, "ReadWritePaths")); len(rw) == 1 && filepath.IsAbs(rw[0]) {
		u.StateDir = filepath.Clean(rw[0])
	}
	cfgRaw, cfgProblem := readRegular(u.Config, maxConfigBytes)
	if cfgProblem == "" {
		u.configSHA = shaHex(cfgRaw)
		if cfg, err := decodeConfig(u.Config, cfgRaw); err == nil {
			u.cfg = &cfg
			u.Role = cfg.Node.Role
			if cfg.Server != nil {
				u.Listen = cfg.Server.Listen
			}
			if cfg.Peer != nil {
				u.Peer = cfg.Peer.Address
			}
			if len(cfg.Routes) > 0 {
				u.Target, u.RouteListen = cfg.Routes[0].Target, cfg.Routes[0].Listen
			}
		}
	}
	managedHdr, _ := headerValue(raw, "baft-managed")
	tunnelHdr, _ := headerValue(raw, "baft-tunnel")
	mpath := filepath.Join(filepath.Dir(u.Config), "baft.managed.json")
	mraw, mproblem := readRegular(mpath, maxSmallBytes)
	_, statErr := os.Lstat(mpath)
	markerExists := statErr == nil
	u.TunnelID = tunnelHdr
	if u.StateDir != "" {
		u.InProgress = tunnelInProgress(u.StateDir)
	}
	switch {
	case managedHdr == "true" && !markerExists:
		u.Ownership, u.Evidence = Conflict, "the unit says it is BAFT-managed (tunnel "+clip(tunnelHdr)+") but there is no ownership marker beside its config"
	case markerExists && mproblem != "":
		u.Ownership, u.Evidence = Conflict, "ownership marker "+mproblem
	case markerExists:
		var mk marker
		if err := json.Unmarshal(mraw, &mk); err != nil {
			u.Ownership, u.Evidence = Conflict, "ownership marker is not valid JSON"
			return
		}
		switch {
		case mk.ManagedBy != "baft":
			u.Ownership, u.Evidence = Conflict, "the ownership marker names another manager ("+clip(mk.ManagedBy)+")"
		case managedHdr != "true":
			u.Ownership, u.Evidence = Conflict, "a BAFT ownership marker exists beside the config, but the unit carries no BAFT header"
		case mk.TunnelID != tunnelHdr:
			u.Ownership, u.Evidence = Conflict, "the marker names tunnel "+clip(mk.TunnelID)+", the unit header names "+clip(tunnelHdr)
		case mk.UnitSHA256 != u.SHA256 || cfgProblem != "" || mk.ConfigSHA256 != u.configSHA:
			u.Ownership, u.Evidence = Drifted, "BAFT built tunnel "+clip(mk.TunnelID)+" here, but its unit or config was changed since (held for manual review)"
		default:
			u.Ownership, u.Evidence = Managed, "BAFT tunnel "+clip(mk.TunnelID)+": the unit and config match their ownership marker"
		}
	default:
		p, ok := installerServiceParams(raw)
		if ok && renderServiceUnit(p) == raw {
			u.Ownership, u.Evidence = Managed, "byte-identical to the unit install.sh writes"
		} else {
			u.Ownership, u.Evidence = Unmanaged, "not the unit install.sh writes and no BAFT ownership marker"
		}
	}
}

// tunnelInProgress names a tunnel change that has not finished, if any.
func tunnelInProgress(stateDir string) string {
	dir := filepath.Join(stateDir, "tunnels")
	if _, err := os.Lstat(filepath.Join(dir, "active")); err == nil {
		b, _ := readRegular(filepath.Join(dir, "active"), 4096)
		return "tunnel change " + clip(strings.TrimSpace(string(b))) + " is in progress"
	}
	ents, _ := os.ReadDir(dir)
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		b, problem := readRegular(filepath.Join(dir, ent.Name(), "txn.json"), maxSmallBytes)
		if problem != "" {
			continue
		}
		var t struct {
			Phase string `json:"phase"`
		}
		if json.Unmarshal(b, &t) == nil && (t.Phase == "prepared" || t.Phase == "committed") {
			return "tunnel change " + clip(ent.Name()) + " is " + t.Phase + " and not finished"
		}
	}
	return ""
}

func (e *Env) classifyAgent(u *Unit, raw string) {
	p, ok := installerAgentParams(raw)
	if !ok || renderAgentUnit(p) != raw {
		u.Ownership, u.Evidence = Unmanaged, "not the agent unit install.sh writes"
		return
	}
	u.agent = &p
	u.Ownership, u.Evidence = Managed, "byte-identical to the agent unit install.sh writes"
}

type bccPaths struct {
	StateFile, AccessFile, JobKeyFile, BackupDir string
	BootstrapScript, BootstrapSHA256             string   // installer-managed only when the unit marker and digest agree
	Operator                                     []string // operator-provided files it names
}

func (e *Env) classifyBCC(u *Unit, raw string) {
	b := parseBCC(raw)
	u.bcc = b
	if b == nil {
		u.Ownership, u.Evidence = Unknown, "cannot tell where this BCC keeps its state"
		return
	}
	if labelled(raw, "bcc") {
		u.Ownership, u.Evidence = Managed, "labelled as BAFT's BCC unit (# baft-managed: true, # baft-component: bcc)"
		return
	}
	u.Ownership, u.Evidence = Unmanaged, "a BCC unit written by the operator (no `# baft-component: bcc` label)"
}

// parseBCC reads where a BCC unit keeps its files (flags as baft-bcc parses
// them; relative paths are relative to WorkingDirectory, "/" by default).
func parseBCC(raw string) *bccPaths {
	f := strings.Fields(firstValue(raw, "ExecStart"))
	if len(f) == 0 {
		return nil
	}
	wd := firstValue(raw, "WorkingDirectory")
	if !filepath.IsAbs(wd) {
		wd = "/"
	}
	val := map[string]string{}
	for i := 1; i < len(f); i++ {
		a := f[i]
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name := strings.TrimLeft(a, "-")
		if k, v, ok := strings.Cut(name, "="); ok {
			val[k] = v
			continue
		}
		switch name {
		case "allow-insecure-http":
			continue
		}
		if i+1 < len(f) {
			val[name] = f[i+1]
			i++
		}
	}
	abs := func(p string) string {
		if p == "" {
			return ""
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(wd, p)
		}
		return filepath.Clean(p)
	}
	b := &bccPaths{StateFile: abs(val["state-file"])}
	if b.StateFile == "" {
		b.StateFile = abs("./bcc-state.json")
	}
	b.AccessFile = abs(val["access-file"])
	if b.AccessFile == "" {
		b.AccessFile = b.StateFile + ".access.json"
	}
	b.JobKeyFile = abs(val["job-key-file"])
	if b.JobKeyFile == "" {
		b.JobKeyFile = b.StateFile + ".job-key"
	}
	b.BackupDir = abs(val["backup-dir"])
	if b.BackupDir == "" {
		b.BackupDir = abs("./backups")
	}
	for _, k := range []string{"tls-cert", "tls-key", "admin-token-file"} {
		if p := abs(val[k]); p != "" {
			b.Operator = append(b.Operator, p)
		}
	}
	installScript := abs(val["install-script"])
	marker := func(prefix string) string {
		for _, line := range strings.Split(raw, "\n") {
			if strings.HasPrefix(line, prefix) {
				return strings.TrimSpace(strings.TrimPrefix(line, prefix))
			}
		}
		return ""
	}
	markerScript := abs(marker("# baft-bootstrap-script:"))
	markerSHA := marker("# baft-bootstrap-sha256:")
	managedMarker := labelled(raw, "bcc") && markerScript != "" && regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(markerSHA)
	if managedMarker && (installScript == "" || installScript == markerScript) {
		b.BootstrapScript, b.BootstrapSHA256 = markerScript, markerSHA
	} else if installScript != "" {
		// A command-line script without the installer-owned path+digest marker is
		// operator material. Safe uninstall never claims or removes it.
		b.Operator = append(b.Operator, installScript)
	}
	return b
}

// scanRefs records which units (any name, in every scanned unit directory)
// run which absolute programs, so nothing still in use is removed.
func (e *Env) scanRefs(inv *Inventory) {
	seen := 0
	for _, dir := range append([]string{e.UnitDir}, e.RefUnitDirs...) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, ent := range ents {
			n := ent.Name()
			if !strings.HasSuffix(n, ".service") || seen >= maxRefUnits {
				continue
			}
			seen++
			b, problem := readRegular(filepath.Join(dir, n), maxUnitBytes)
			if problem != "" {
				continue
			}
			for _, p := range unitPrograms(string(b)) {
				inv.refs[p] = appendUnique(inv.refs[p], n)
			}
		}
	}
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// markConfigConflicts: two units that run the same config both conflict.
func (e *Env) markConfigConflicts(inv *Inventory) {
	by := map[string][]*Unit{}
	for _, u := range inv.Units {
		if u.Kind == "transport" && u.Config != "" {
			by[u.Config] = append(by[u.Config], u)
		}
	}
	for cfg, us := range by {
		if len(us) < 2 {
			continue
		}
		var names []string
		for _, u := range us {
			names = append(names, u.Name)
		}
		for _, u := range us {
			u.Ownership, u.Evidence = Conflict, "units "+strings.Join(names, ", ")+" all run "+clip(cfg)
		}
	}
}

// ---- files at BAFT's own locations ----

type locKind int

const (
	locConfig locKind = iota
	locState
	locPrefix
	locAgent
	locAgentState
)

type location struct {
	dir   string
	kind  locKind
	owner *Unit
	area  string
	label string // component name for humans
}

func (e *Env) locations(inv *Inventory) []location {
	var locs []location
	seen := map[string]bool{}
	add := func(l location) {
		if l.dir == "" || seen[l.dir] {
			return
		}
		seen[l.dir] = true
		locs = append(locs, l)
	}
	for _, u := range inv.Units {
		switch {
		case u.Kind == "transport" && u.Config != "":
			add(location{dir: filepath.Dir(u.Config), kind: locConfig, owner: u, area: "services", label: u.Name})
			if u.StateDir != "" {
				add(location{dir: u.StateDir, kind: locState, owner: u, area: "services", label: u.Name})
			}
		case u.Kind == "agent" && u.agent != nil:
			add(location{dir: u.agent.AgentDir, kind: locAgent, owner: u, area: "agent", label: u.Name})
			add(location{dir: u.agent.AgentStateDir, kind: locAgentState, owner: u, area: "agent", label: u.Name})
			add(location{dir: u.agent.Prefix, kind: locPrefix, area: "services", label: "node"})
		}
	}
	add(location{dir: e.ConfigDir, kind: locConfig, area: "services", label: "node"})
	add(location{dir: e.StateDir, kind: locState, area: "services", label: "node"})
	add(location{dir: e.Prefix, kind: locPrefix, area: "services", label: "node"})
	add(location{dir: e.AgentDir, kind: locAgent, area: "agent", label: "agent"})
	add(location{dir: e.AgentStateDir, kind: locAgentState, area: "agent", label: "agent"})
	return locs
}

// systemDirs are never treated as BAFT locations, whatever a unit names: a
// config at /etc/baft.yaml does not make /etc BAFT's.
var systemDirs = map[string]bool{"/": true, "/etc": true, "/var": true, "/var/lib": true, "/opt": true, "/usr": true, "/usr/local": true,
	"/usr/local/bin": true, "/usr/bin": true, "/home": true, "/root": true, "/tmp": true, "/run": true, "/srv": true, "/var/tmp": true}

func (e *Env) scanLocations(inv *Inventory) {
	for _, l := range e.locations(inv) {
		if systemDirs[filepath.Clean(l.dir)] {
			inv.Errors = append(inv.Errors, l.dir+" is a system directory; nothing in it is claimed as BAFT's")
			continue
		}
		ents, err := os.ReadDir(l.dir)
		if err != nil {
			continue
		}
		for _, ent := range ents {
			e.classifyEntry(inv, l, filepath.Join(l.dir, ent.Name()))
		}
	}
	for _, u := range inv.Units {
		if u.Kind == "bcc" && u.bcc != nil {
			e.scanBCC(inv, u.bcc, u)
		}
	}
	if e.BCCStateFile != "" {
		known := false
		for _, a := range inv.Artifacts {
			if a.Path == filepath.Clean(e.BCCStateFile) {
				known = true
			}
		}
		if !known {
			st := filepath.Clean(e.BCCStateFile)
			e.scanBCC(inv, &bccPaths{StateFile: st, AccessFile: st + ".access.json", JobKeyFile: st + ".job-key",
				BackupDir: filepath.Join(filepath.Dir(st), "backups")}, nil)
		}
	}
}

func (inv *Inventory) add(a *Artifact) { inv.Artifacts = append(inv.Artifacts, a) }

func newArtifact(l location, path string, class Class) *Artifact {
	return &Artifact{Path: path, Class: class, Component: l.label, owner: l.owner, area: l.area}
}

// classifyEntry decides what one entry of a BAFT location is. Only names and
// formats BAFT itself writes are claimed; anything else stays UNKNOWN.
func (e *Env) classifyEntry(inv *Inventory, l location, path string) {
	name := filepath.Base(path)
	fi, err := os.Lstat(path)
	if err != nil {
		return
	}
	unknown := func(why string) {
		a := newArtifact(l, path, "")
		a.Ownership, a.Evidence, a.Dir = Unknown, why, fi.IsDir()
		inv.add(a)
	}
	file := func(class Class, check func([]byte) bool, evidence string) {
		if fi.Mode()&os.ModeSymlink != 0 {
			unknown("is a symlink (not followed)")
			return
		}
		if !fi.Mode().IsRegular() {
			unknown("is not a regular file")
			return
		}
		a := newArtifact(l, path, class)
		sum, size, problem := hashRegular(path)
		if problem != "" {
			a.Ownership, a.Evidence = Unknown, problem
			inv.add(a)
			return
		}
		a.SHA256, a.Size = sum, size
		if check != nil {
			b, problem := readRegular(path, maxSmallBytes)
			if problem != "" || !check(b) {
				a.Ownership, a.Evidence = Unknown, "not in the format BAFT writes for "+name
				inv.add(a)
				return
			}
		}
		a.Ownership, a.Evidence = Managed, evidence
		inv.add(a)
	}
	dir := func(class Class, evidence string) {
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			unknown("is not a directory")
			return
		}
		a := newArtifact(l, path, class)
		a.Dir = true
		sum, problem := treeHash(path)
		if problem != "" {
			a.Ownership, a.Evidence = Unknown, problem
		} else {
			a.SHA256, a.Ownership, a.Evidence = sum, Managed, evidence
		}
		inv.add(a)
	}
	switch l.kind {
	case locConfig:
		switch {
		case name == "baft.yaml" || name == "baft.new.yaml":
			file(ClassTunnelConfigs, func(b []byte) bool { _, err := decodeConfig(path, b); return err == nil }, "BAFT config at its installer path")
		case name == "baft.managed.json":
			file(ClassTunnelConfigs, isMarker, "BAFT tunnel ownership marker")
		case name == "noise-key.json":
			file(ClassCertificates, isNoiseKey, "BAFT Noise key")
		case name == "pki":
			e.classifyPKI(inv, l, path, fi)
		case strings.HasPrefix(name, "pki.before-"):
			e.walkNested(inv, l, path, pkiBackupRule)
		case (strings.HasPrefix(name, "pki.next-") || strings.HasPrefix(name, "pki.prev-")) && tunnelIDRe.MatchString(name[len("pki.next-"):]):
			e.walkNested(inv, l, path, pkiRotationRule)
		case strings.HasPrefix(name, "baft.yaml.before-repair-") || strings.HasPrefix(name, "baft.yaml.before-stealth-"):
			file(ClassBackups, func(b []byte) bool { _, err := config.DecodeYAML(bytes.NewReader(b)); return err == nil }, "the installer's copy of an earlier config")
		default:
			unknown("not a file BAFT writes here")
		}
	case locState:
		switch {
		case name == "admin.sock":
			if fi.Mode()&os.ModeSocket != 0 {
				a := newArtifact(l, path, ClassRuntime)
				a.Ownership, a.Evidence = Managed, "the BAFT service's admin socket"
				inv.add(a)
			} else {
				unknown("admin.sock is not a socket")
			}
		case name == "pairing.psk":
			file(ClassTunnelConfigs, func(b []byte) bool { return len(b) > 0 && len(b) < 4096 }, "one-time pairing secret left by the installer")
		case name == "pairing.ex.json" || name == "pairing.pending.json":
			file(ClassTunnelConfigs, isJSONObject, "pairing state left by the installer")
		case name == "peer-ca.pem":
			file(ClassCertificates, isPEM("CERTIFICATE"), "the EX certificate authority pinned at pairing")
		case name == "tunnels":
			e.walkNested(inv, l, path, tunnelsRule)
		case name == "rotations":
			e.walkNested(inv, l, path, rotationsRule)
		default:
			// The service user's home is the state directory, and useradd
			// copied the skeleton into it. Each entry identical to its
			// skeleton original is that copy; anything else stays.
			skel := filepath.Join(e.SkelDir, name)
			sfi, err := os.Lstat(skel)
			switch {
			case err != nil:
				unknown("not a file BAFT writes here")
			case fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 && sfi.IsDir() && sfi.Mode()&os.ModeSymlink == 0:
				e.walkNested(inv, l, path, e.skelRule(skel))
			case fi.Mode().IsRegular():
				if class, ev, ok := e.skelRule(e.SkelDir)(name, path, fi); ok {
					file(class, nil, ev)
				} else {
					unknown("differs from " + skel)
				}
			default:
				unknown("not a file BAFT writes here")
			}
		}
	case locPrefix:
		switch {
		case name == "release-state.json":
			file(ClassCertificates, isReleaseState, "the installer's release trust state (anti-rollback record)")
		case name == "backups":
			if !fi.IsDir() {
				unknown("is not a directory")
				return
			}
			ents, _ := os.ReadDir(path)
			for _, ent := range ents {
				p := filepath.Join(path, ent.Name())
				sub, err := os.Lstat(p)
				if err != nil {
					continue
				}
				if strings.HasPrefix(ent.Name(), "rerun-") && sub.IsDir() && sub.Mode()&os.ModeSymlink == 0 {
					e.walkNested(inv, l, p, rerunRule(p))
				} else {
					a := newArtifact(l, p, "")
					a.Ownership, a.Evidence, a.Dir = Unknown, "not a backup the installer writes", sub.IsDir()
					inv.add(a)
				}
			}
		case name == "src":
			if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
				unknown("not a directory")
				return
			}
			if ok, why := sourceCheckout(path); ok {
				dir(ClassInstall, why)
			} else {
				a := newArtifact(l, path, "")
				a.Ownership, a.Evidence, a.Dir = Unknown, why, true
				inv.add(a)
			}
		default:
			unknown("not a file BAFT writes here")
		}
	case locAgent:
		switch name {
		case "token":
			file(ClassCertificates, func(b []byte) bool { return len(b) > 0 && len(b) < 4096 }, "the agent's BCC token")
		case "bcc-job.pub":
			file(ClassCertificates, func(b []byte) bool { return len(b) > 0 && len(b) < 4096 }, "the BCC job key the agent pins")
		case "release-root.pub":
			file(ClassCertificates, func(b []byte) bool { return len(b) > 0 && len(b) < 4096 }, "the release root key the agent pins")
		default:
			unknown("not a file BAFT writes here")
		}
	case locAgentState:
		switch name {
		case "seen-jobs.json":
			file(ClassAudit, isJSONAny, "the agent's record of executed jobs")
		default:
			unknown("not a file BAFT writes here")
		}
	}
}

func (e *Env) classifyPKI(inv *Inventory, l location, path string, fi os.FileInfo) {
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		a := newArtifact(l, path, "")
		a.Ownership, a.Evidence = Unknown, "pki is not a directory"
		inv.add(a)
		return
	}
	ents, _ := os.ReadDir(path)
	for _, ent := range ents {
		p := filepath.Join(path, ent.Name())
		sub := location{dir: path, kind: l.kind, owner: l.owner, area: l.area, label: l.label}
		sfi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		var check func([]byte) bool
		switch ent.Name() {
		case "ca.pem", "server.pem":
			check = isPEM("CERTIFICATE")
		case "ca.key", "server.key":
			check = isPEMKey
		case "host":
			check = isHostMarker
		}
		a := newArtifact(sub, p, ClassCertificates)
		switch {
		case check == nil:
			a.Class, a.Ownership, a.Evidence, a.Dir = "", Unknown, "not a file BAFT writes here", sfi.IsDir()
		case !sfi.Mode().IsRegular():
			a.Ownership, a.Evidence = Unknown, "is not a regular file (not followed)"
		default:
			b, problem := readRegular(p, maxSmallBytes)
			if problem != "" || !check(b) {
				a.Ownership, a.Evidence = Unknown, "not in the format BAFT writes for "+ent.Name()
			} else {
				a.SHA256, a.Size = shaHex(b), int64(len(b))
				a.Ownership, a.Evidence = Managed, "BAFT certificate file written at pairing"
			}
		}
		inv.add(a)
	}
}

func isJSONObject(b []byte) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(b, &m) == nil
}

func isJSONAny(b []byte) bool { return json.Valid(b) }

func isMarker(b []byte) bool {
	var mk marker
	return json.Unmarshal(b, &mk) == nil && mk.ManagedBy == "baft"
}

func isNoiseKey(b []byte) bool {
	var k struct {
		Version int    `json:"version"`
		Private string `json:"private"`
		Public  string `json:"public"`
	}
	return json.Unmarshal(b, &k) == nil && k.Private != "" && k.Public != ""
}

func isReleaseState(b []byte) bool {
	var s struct {
		Schema  int    `json:"schema_version"`
		Version string `json:"version"`
	}
	return json.Unmarshal(b, &s) == nil && s.Version != ""
}

func isPEM(typ string) func([]byte) bool {
	return func(b []byte) bool {
		blk, _ := pem.Decode(b)
		return blk != nil && blk.Type == typ
	}
}

func isPEMKey(b []byte) bool {
	blk, _ := pem.Decode(b)
	return blk != nil && strings.HasSuffix(blk.Type, "PRIVATE KEY")
}

// ---- BCC files ----

func (e *Env) scanBCC(inv *Inventory, b *bccPaths, u *Unit) {
	l := location{owner: u, area: "bcc", label: "bcc"}
	if u != nil {
		l.label = u.Name
	}
	add := func(path string, class Class, check func([]byte) bool, evidence string) *Artifact {
		fi, err := os.Lstat(path)
		if err != nil {
			return nil
		}
		a := newArtifact(l, path, class)
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			a.Ownership, a.Evidence = Unknown, "is not a regular file (not followed)"
			inv.add(a)
			return a
		}
		sum, size, problem := hashRegular(path)
		if problem != "" {
			a.Ownership, a.Evidence = Unknown, problem
			inv.add(a)
			return a
		}
		a.SHA256, a.Size = sum, size
		if check != nil {
			head, _ := readHead(path, 64)
			if !check(head) {
				a.Ownership, a.Evidence = Unknown, "not in the format BCC writes"
				inv.add(a)
				return a
			}
		}
		a.Ownership, a.Evidence = Managed, evidence
		inv.add(a)
		return a
	}
	isState := func(h []byte) bool {
		return bytes.HasPrefix(h, []byte("SQLite format 3\x00")) || bytes.HasPrefix(bytes.TrimSpace(h), []byte("{"))
	}
	isJSONHead := func(h []byte) bool { return bytes.HasPrefix(bytes.TrimSpace(h), []byte("{")) }
	if a := add(b.StateFile, ClassBCCState, isState, "the BCC state database"); a != nil {
		a.role = "bcc-state"
	}
	add(b.StateFile+".json.bak", ClassBCCState, isJSONHead, "the JSON state kept when BCC moved to SQLite")
	add(b.AccessFile, ClassBCCState, isJSONHead, "the BCC web access file (credential hashes)")
	add(b.JobKeyFile, ClassBCCState, nil, "the BCC job-signing key (agents pin its public key)")
	add(b.StateFile+".lock", ClassRuntime, nil, "the BCC process lock")
	add(b.StateFile+".audit.jsonl", ClassAudit, isJSONHead, "the BCC audit log (hash chain)")
	add(b.StateFile+".audit-anchor-outbox.json", ClassAudit, isJSONHead, "the BCC audit anchor outbox")
	if b.BootstrapScript != "" {
		if a := add(b.BootstrapScript, ClassRuntime, nil, "the installer-managed BCC bootstrap script pinned by the unit digest"); a != nil && a.Ownership == Managed {
			if a.SHA256 != b.BootstrapSHA256 {
				a.Ownership = Drifted
				a.Evidence = "BCC bootstrap script digest differs from the installer-owned unit marker"
			}
		}
	}
	if ents, err := os.ReadDir(b.BackupDir); err == nil {
		for _, ent := range ents {
			n := ent.Name()
			if (strings.HasPrefix(n, "daily-") || strings.HasPrefix(n, "weekly-")) && strings.HasSuffix(n, ".baftbak") {
				add(filepath.Join(b.BackupDir, n), ClassBackups, isJSONHead, "an encrypted BCC backup")
			}
		}
	}
	for _, p := range b.Operator {
		if _, err := os.Lstat(p); err == nil {
			a := newArtifact(l, p, "")
			a.Ownership, a.Evidence = Unmanaged, "provided by the operator (named on the BCC command line)"
			inv.add(a)
		}
	}
}

func readHead(path string, n int) ([]byte, string) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, err.Error()
	}
	defer f.Close()
	buf := make([]byte, n)
	k, _ := f.Read(buf)
	return buf[:k], ""
}

// ---- binaries ----

func (e *Env) scanBinaries(inv *Inventory) {
	seen := map[string]bool{}
	add := func(path, name string) {
		path = filepath.Clean(path)
		if seen[path] {
			return
		}
		if _, err := os.Lstat(path); err != nil {
			return
		}
		seen[path] = true
		a := &Artifact{Path: path, Class: ClassBinary, Component: "binaries", area: "binary:" + name}
		a.Ownership, a.Evidence = e.binaryOwnership(path, name)
		if a.Ownership == Managed {
			a.SHA256, a.Size, _ = hashRegular(path)
		}
		inv.add(a)
	}
	for _, n := range []string{"baft", "baft-pair", "baft-agent", "baft-bcc"} {
		add(filepath.Join(e.BinDir, n), n)
	}
	for _, u := range inv.Units {
		for _, p := range u.execs {
			if n := filepath.Base(p); defaultMainPkg[n] != "" {
				add(p, n)
			}
		}
		if u.agent != nil {
			add(filepath.Join(u.agent.BinDir, "baft"), "baft")
			add(filepath.Join(u.agent.BinDir, "baft-pair"), "baft-pair")
		}
	}
}
