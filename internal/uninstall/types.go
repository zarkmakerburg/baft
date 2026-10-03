// Package uninstall removes BAFT from this host safely (HQ A3, safe
// uninstall): INSPECT -> BUILD REMOVAL PLAN -> SHOW EXACT CHANGES ->
// EXPLICIT CONFIRMATION -> BACKUP IF REQUIRED -> STOP SERVICES -> REMOVE ->
// VERIFY.
//
// Only artifacts whose BAFT ownership is proven are eligible: a unit
// byte-identical to what install.sh writes, a tunnel unit whose ownership
// marker matches it, a BCC unit labelled as BAFT's, a binary whose embedded
// Go build information names BAFT, or a file BAFT writes at its own path in
// its own format. DISCOVERED_UNMANAGED and UNKNOWN artifacts are never
// touched; OWNERSHIP_CONFLICT holds for manual review and blocks the run.
// Data (BCC state, certificates, backups, tunnel configs, audit) is kept
// unless each class is explicitly chosen. Removal moves files into a journaled
// quarantine first, so an interrupted run can be restored or resumed; the
// quarantine is purged only after verification.
package uninstall

import (
	"context"
	"io"
	"time"
)

// Ownership is the proven owner of an artifact (the discovery vocabulary).
type Ownership string

const (
	Managed   Ownership = "BAFT_MANAGED"
	Drifted   Ownership = "DRIFTED" // BAFT wrote it, but it was changed since
	Unmanaged Ownership = "DISCOVERED_UNMANAGED"
	Conflict  Ownership = "OWNERSHIP_CONFLICT"
	Unknown   Ownership = "UNKNOWN"
)

// Class says what an artifact is. The data classes are kept unless each is
// chosen explicitly.
type Class string

const (
	ClassUnit          Class = "unit"
	ClassBinary        Class = "binary"
	ClassRuntime       Class = "runtime" // sockets, lock files, home skeleton
	ClassInstall       Class = "install" // the installer's source tree
	ClassBCCState      Class = "bcc-state"
	ClassCertificates  Class = "certificates"
	ClassBackups       Class = "backups"
	ClassTunnelConfigs Class = "tunnel-configs"
	ClassAudit         Class = "audit"
)

// DataClasses are kept by default; each needs its own confirmation.
var DataClasses = []Class{ClassBCCState, ClassCertificates, ClassBackups, ClassTunnelConfigs, ClassAudit}

func (c Class) IsData() bool {
	for _, d := range DataClasses {
		if c == d {
			return true
		}
	}
	return false
}

// Question is the confirmation asked for a data class.
func (c Class) Question() string {
	switch c {
	case ClassBCCState:
		return "Delete BCC state?"
	case ClassCertificates:
		return "Delete certificates?"
	case ClassBackups:
		return "Delete backups?"
	case ClassTunnelConfigs:
		return "Delete tunnel configs?"
	case ClassAudit:
		return "Delete audit history?"
	}
	return "Delete " + string(c) + "?"
}

// Flag is the command-line flag that chooses a data class for deletion.
func (c Class) Flag() string { return "--delete-" + string(c) }

// Scope selects what to remove. Nothing set means the standard node
// uninstall: BAFT services and the agent with their binaries.
type Scope struct {
	Binaries bool // BAFT binaries only
	Agent    bool
	BCC      bool
	Services bool // BAFT transport services (+ their binaries)
	Full     bool
}

func (s Scope) none() bool { return !s.Binaries && !s.Agent && !s.BCC && !s.Services && !s.Full }

func (s Scope) services() bool { return s.Services || s.Full || s.none() }
func (s Scope) agent() bool    { return s.Agent || s.Full || s.none() }
func (s Scope) bcc() bool      { return s.BCC || s.Full }

// binary reports whether a BAFT binary of this name is in scope.
func (s Scope) binary(name string) bool {
	if s.Binaries || s.Full {
		return true
	}
	switch name {
	case "baft", "baft-pair":
		return s.services()
	case "baft-agent":
		return s.agent()
	case "baft-bcc":
		return s.bcc()
	}
	return false
}

// Names lists the selected scope for humans and JSON.
func (s Scope) Names() []string {
	if s.Full {
		return []string{"full"}
	}
	var n []string
	if s.none() {
		return []string{"services", "agent"}
	}
	if s.Binaries {
		n = append(n, "binaries")
	}
	if s.Services {
		n = append(n, "services")
	}
	if s.Agent {
		n = append(n, "agent")
	}
	if s.BCC {
		n = append(n, "bcc")
	}
	return n
}

// System runs systemctl and helper programs (fake in tests).
type System interface {
	Systemctl(ctx context.Context, args ...string) (string, error)
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// Env locates BAFT on this host. Zero values take the installer's defaults.
type Env struct {
	UnitDir string // /etc/systemd/system
	// RefUnitDirs are scanned only for units that run a BAFT binary, so a
	// binary something else still uses is kept.
	RefUnitDirs   []string
	BinDir        string // /usr/local/bin
	Prefix        string // /opt/baft
	ConfigDir     string // /etc/baft
	StateDir      string // /var/lib/baft
	AgentDir      string // /etc/baft-agent
	AgentStateDir string // /var/lib/baft-agent
	// BCCStateFile names a BCC state file whose service unit is gone.
	BCCStateFile string
	JournalDir   string // /var/lib/baft-uninstall
	BackupDir    string // /var/backups/baft: emergency backups, never removed
	// BCCBinary verifies an emergency backup ("" = <BinDir>/baft-bcc).
	BCCBinary string
	// SkelDir is useradd's skeleton (/etc/skel); copies of it in the
	// service user's home (the state directory) are BAFT's.
	SkelDir string
	System  System
	// MainPkg overrides the expected Go main package per binary (tests).
	MainPkg map[string]string
	Now     func() time.Time
	Log     io.Writer
	// RequireRoot refuses to apply unless running as root.
	RequireRoot bool
}

// Options is what the operator chose.
type Options struct {
	Scope             Scope
	Delete            map[Class]bool
	NoBackup          bool // delete BCC state without an emergency backup
	StopActiveTunnels bool // explicit consent to interrupt running tunnels
	Yes               bool // apply without an interactive confirmation
}

func (o Options) deletes(c Class) bool { return o.Delete != nil && o.Delete[c] }
