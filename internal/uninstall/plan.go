package uninstall

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Service is a unit the plan stops and disables.
type Service struct {
	Unit         string `json:"unit"`
	Kind         string `json:"kind"`
	ActiveState  string `json:"active_state"`
	EnabledState string `json:"enabled_state"`
}

// Tunnel is a running tunnel the plan would interrupt.
type Tunnel struct {
	Unit        string `json:"unit"`
	Role        string `json:"role,omitempty"`
	TunnelID    string `json:"tunnel_id,omitempty"`
	Listen      string `json:"listen,omitempty"`
	Peer        string `json:"peer,omitempty"`
	Target      string `json:"target,omitempty"`
	RouteListen string `json:"route_listen,omitempty"`
}

// Impact says in one line what stopping the tunnel does.
func (t Tunnel) Impact() string {
	switch t.Role {
	case "listener":
		return fmt.Sprintf("EX: stops accepting on %s; traffic to %s through this tunnel stops", t.Listen, t.Target)
	case "dialer":
		return fmt.Sprintf("IR: clients on %s lose the tunnel to %s", t.RouteListen, t.Peer)
	}
	return "traffic through this tunnel stops"
}

// BackupPlan is the emergency backup taken before BCC state is deleted.
type BackupPlan struct {
	StateFile string   `json:"state_file"`
	Files     []string `json:"files"`
	Dir       string   `json:"dir"` // parent; the run creates bcc-emergency-<time> in it
}

// Plan is the exact removal plan. Building it changes nothing.
type Plan struct {
	Version       int         `json:"version"`
	Scope         []string    `json:"scope"`
	Delete        []Class     `json:"delete"`
	NoBackup      bool        `json:"no_backup,omitempty"`
	Stop          []Service   `json:"stop"`
	Remove        []*Artifact `json:"remove"`
	Keep          []*Artifact `json:"keep"`
	Untouched     []*Artifact `json:"untouched"`
	Hold          []*Artifact `json:"hold"`
	Dirs          []string    `json:"remove_dirs_if_empty"`
	ActiveTunnels []Tunnel    `json:"active_tunnels"`
	Backup        *BackupPlan `json:"bcc_backup,omitempty"`
	Deletable     []Class     `json:"deletable_data"` // data classes that have something to delete in this scope
	Blocked       []string    `json:"blocked"`
	Needs         []string    `json:"needs"`
	Errors        []string    `json:"errors,omitempty"`
	Pending       string      `json:"pending_run,omitempty"`

	units     []*Unit // every unit inspected (for verification)
	removedU  map[string]bool
	inventory *Inventory
}

// HasWork reports whether applying the plan would change anything.
func (p *Plan) HasWork() bool { return len(p.Stop) > 0 || len(p.Remove) > 0 }

func unitArea(u *Unit) string {
	switch u.Kind {
	case "transport":
		return "services"
	case "agent":
		return "agent"
	case "bcc":
		return "bcc"
	}
	return ""
}

func (o Options) areaInScope(area string) bool {
	switch {
	case area == "services":
		return o.Scope.services()
	case area == "agent":
		return o.Scope.agent()
	case area == "bcc":
		return o.Scope.bcc()
	case strings.HasPrefix(area, "binary:"):
		return o.Scope.binary(strings.TrimPrefix(area, "binary:"))
	}
	return false
}

// ValidateOptions refuses data deletions the scope does not cover.
func ValidateOptions(o Options) error {
	need := map[Class][]string{
		ClassBCCState:      {"bcc"},
		ClassTunnelConfigs: {"services"},
		ClassCertificates:  {"services", "agent"},
		ClassAudit:         {"agent", "bcc"},
		ClassBackups:       {"services", "bcc"},
	}
	for c, on := range o.Delete {
		if !on {
			continue
		}
		ok := false
		for _, a := range need[c] {
			ok = ok || o.areaInScope(a)
		}
		if !ok {
			return fmt.Errorf("%s needs a scope that removes %s (%s)", c.Flag(), strings.Join(need[c], " or "), scopeFlags(need[c]))
		}
	}
	if o.NoBackup && !o.deletes(ClassBCCState) {
		return fmt.Errorf("--no-backup only applies together with %s", ClassBCCState.Flag())
	}
	return nil
}

func scopeFlags(areas []string) string {
	var f []string
	for _, a := range areas {
		f = append(f, "--"+a)
	}
	return strings.Join(append(f, "--full"), ", ")
}

// BuildPlan inspects the host and decides, artifact by artifact, what an
// uninstall with these options would do. It changes nothing.
func (e *Env) BuildPlan(ctx context.Context, o Options) *Plan {
	inv := e.Inspect(ctx)
	return e.plan(inv, o)
}

func (e *Env) plan(inv *Inventory, o Options) *Plan {
	p := &Plan{Version: 1, Scope: o.Scope.Names(), NoBackup: o.NoBackup, units: inv.Units, removedU: map[string]bool{}, inventory: inv,
		Stop: []Service{}, Remove: []*Artifact{}, Keep: []*Artifact{}, Untouched: []*Artifact{}, Hold: []*Artifact{},
		Dirs: []string{}, ActiveTunnels: []Tunnel{}, Deletable: []Class{}, Blocked: []string{}, Needs: []string{}, Errors: inv.Errors}
	for _, c := range DataClasses {
		if o.deletes(c) {
			p.Delete = append(p.Delete, c)
		}
	}
	if p.Delete == nil {
		p.Delete = []Class{}
	}
	if inv.Pending != nil {
		p.Pending = inv.Pending.ID
		p.Blocked = append(p.Blocked, fmt.Sprintf("an earlier uninstall (%s) did not finish: run `baft uninstall --resume` to complete it or `baft uninstall --restore` to undo it", inv.Pending.ID))
	}

	// Units: which are removed, held or never touched.
	for _, u := range inv.Units {
		ua := &Artifact{Path: u.Path, Class: ClassUnit, Component: u.Name, Ownership: u.Ownership, Evidence: u.Evidence, SHA256: u.SHA256, owner: u, area: unitArea(u)}
		inScope := ua.area != "" && o.areaInScope(ua.area)
		switch u.Ownership {
		case Managed:
			switch {
			case !inScope:
				ua.Reason = "not in the selected scope"
				p.Keep = append(p.Keep, ua)
			case u.InProgress != "":
				ua.Reason = u.InProgress
				p.Hold = append(p.Hold, ua)
				p.Blocked = append(p.Blocked, u.Name+": "+u.InProgress+"; let it finish or roll back first")
			default:
				p.removedU[u.Name] = true
				p.Remove = append(p.Remove, ua)
				if u.active() || u.enabled() {
					p.Stop = append(p.Stop, Service{Unit: u.Name, Kind: u.Kind, ActiveState: u.ActiveState, EnabledState: u.EnabledState})
				}
				if u.Kind == "transport" && u.active() {
					p.ActiveTunnels = append(p.ActiveTunnels, Tunnel{Unit: u.Name, Role: u.Role, TunnelID: u.TunnelID, Listen: u.Listen, Peer: u.Peer, Target: u.Target, RouteListen: u.RouteListen})
				}
			}
		case Conflict:
			ua.Reason = "OWNERSHIP_CONFLICT: manual review"
			p.Hold = append(p.Hold, ua)
			if inScope || ua.area == "" {
				p.Blocked = append(p.Blocked, u.Name+": OWNERSHIP_CONFLICT ("+u.Evidence+"); resolve it by hand, nothing is removed until then")
			}
		case Drifted:
			ua.Reason = "edited since BAFT wrote it: kept for manual review"
			p.Hold = append(p.Hold, ua)
		default:
			ua.Reason = "not proven BAFT-owned: never touched"
			p.Untouched = append(p.Untouched, ua)
		}
	}

	kept := func(u *Unit) bool { return !p.removedU[u.Name] }

	// What the units that stay still use.
	inUse := map[string]string{}
	use := func(path, by string) {
		if path == "" {
			return
		}
		path = filepath.Clean(path)
		if _, ok := inUse[path]; !ok {
			inUse[path] = by
		}
	}
	unreadable := ""
	for _, u := range inv.Units {
		if !kept(u) {
			continue
		}
		if u.Ownership == Unknown && u.Kind == "other" {
			unreadable = u.Name
		}
		for _, x := range u.execs {
			use(x, u.Name)
		}
		if u.Config != "" {
			use(u.Config, u.Name)
			if u.cfg != nil {
				c := u.cfg
				use(c.TLS.CAFile, u.Name)
				use(c.TLS.CertFile, u.Name)
				use(c.TLS.KeyFile, u.Name)
				if c.Noise != nil {
					use(c.Noise.KeyFile, u.Name)
					use(c.Noise.CoverHTMLFile, u.Name)
				}
				if c.Revocation != nil {
					use(c.Revocation.File, u.Name)
				}
			}
		}
		if u.agent != nil {
			use(filepath.Join(u.agent.BinDir, "baft"), u.Name)
			use(filepath.Join(u.agent.BinDir, "baft-pair"), u.Name)
			use(u.agent.ReleaseState, u.Name)
		}
	}
	for path, units := range inv.refs {
		for _, n := range units {
			if !p.removedU[n] {
				use(path, n)
			}
		}
	}

	// Artifacts.
	deletable := map[Class]bool{}
	var backupFiles []string
	backupState := ""
	for _, a := range inv.Artifacts {
		switch a.Ownership {
		case Unmanaged, Unknown:
			if a.Reason == "" {
				a.Reason = "not proven BAFT-owned: never touched"
			}
			p.Untouched = append(p.Untouched, a)
			continue
		case Conflict, Drifted:
			a.Reason = "manual review"
			p.Hold = append(p.Hold, a)
			continue
		}
		if a.owner != nil && kept(a.owner) {
			switch a.owner.Ownership {
			case Managed:
				a.Reason = "belongs to " + a.owner.Name + ", which stays"
				p.Keep = append(p.Keep, a)
			case Conflict, Drifted:
				a.Reason = "belongs to " + a.owner.Name + ", held for manual review"
				p.Hold = append(p.Hold, a)
			default:
				a.Reason = "belongs to " + a.owner.Name + ", which is not BAFT-owned: never touched"
				p.Untouched = append(p.Untouched, a)
			}
			continue
		}
		if !o.areaInScope(a.area) {
			a.Reason = "not in the selected scope"
			p.Keep = append(p.Keep, a)
			continue
		}
		if by, ok := inUse[a.Path]; ok {
			a.Reason = "still used by " + by + ", which stays"
			p.Keep = append(p.Keep, a)
			continue
		}
		if a.Class == ClassBinary && unreadable != "" {
			a.Reason = "kept: " + unreadable + " could not be read and may run it"
			p.Keep = append(p.Keep, a)
			continue
		}
		if a.Class.IsData() {
			deletable[a.Class] = true
			if !o.deletes(a.Class) {
				a.Reason = "data: kept by default (" + a.Class.Flag() + " to delete)"
				p.Keep = append(p.Keep, a)
				continue
			}
		}
		p.Remove = append(p.Remove, a)
	}
	for _, c := range DataClasses {
		if deletable[c] {
			p.Deletable = append(p.Deletable, c)
		}
	}

	// BCC: an emergency backup comes first whenever BCC state is deleted.
	for _, a := range p.Remove {
		if a.role == "bcc-state" {
			backupState = a.Path
		}
	}
	if backupState != "" {
		for _, x := range []string{"-journal", ".restore-journal.json"} {
			if exists(backupState + x) {
				p.Blocked = append(p.Blocked, "BCC has an unfinished write or restore ("+backupState+x+"): start BCC once so it recovers, stop it, then retry")
			}
		}
		for _, a := range inv.Artifacts {
			if a.area == "bcc" && (a.Class == ClassBCCState || a.Class == ClassAudit) && a.Ownership == Managed {
				backupFiles = append(backupFiles, a.Path)
			}
		}
		sort.Strings(backupFiles)
		if !o.NoBackup {
			p.Backup = &BackupPlan{StateFile: backupState, Files: backupFiles, Dir: e.BackupDir}
		}
	}

	// Directories that become empty go too (never anything still in them).
	seen := map[string]bool{}
	for _, l := range e.locations(inv) {
		if !o.areaInScope(l.area) || (l.owner != nil && kept(l.owner)) || systemDirs[filepath.Clean(l.dir)] {
			continue
		}
		for _, d := range []string{filepath.Join(l.dir, "pki"), filepath.Join(l.dir, "backups"), l.dir} {
			if !seen[d] && isDir(d) {
				seen[d] = true
				p.Dirs = append(p.Dirs, d)
			}
		}
	}
	sort.Slice(p.Dirs, func(i, j int) bool {
		return strings.Count(p.Dirs[i], "/") > strings.Count(p.Dirs[j], "/") || (strings.Count(p.Dirs[i], "/") == strings.Count(p.Dirs[j], "/") && p.Dirs[i] < p.Dirs[j])
	})

	sort.SliceStable(p.Remove, func(i, j int) bool { return removeOrder(p.Remove[i]) < removeOrder(p.Remove[j]) })
	for _, l := range [][]*Artifact{p.Keep, p.Untouched, p.Hold} {
		sort.SliceStable(l, func(i, j int) bool { return l[i].Path < l[j].Path })
	}
	if p.HasWork() && len(p.Blocked) == 0 {
		if len(p.ActiveTunnels) > 0 && !o.StopActiveTunnels {
			p.Needs = append(p.Needs, "--stop-active-tunnels")
		}
		if !o.Yes {
			p.Needs = append(p.Needs, "--yes")
		}
	}
	return p
}

// removeOrder: units first, then data, then directories, binaries last.
func removeOrder(a *Artifact) int {
	switch {
	case a.Class == ClassUnit:
		return 0
	case a.Class == ClassBinary:
		return 9
	case a.Dir:
		return 5
	}
	return 3
}
