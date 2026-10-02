// Package tunnelnode is the node side of building a tunnel from BCC
// (Launch-1 P1-E): the local, transactional steps an agent runs to turn a
// bare server into one end of an IR/EX pair, and to undo them.
//
// A change is a transaction with an ID and two phases:
//
//	prepare  generates keys / the pairing code and stages the new config. The
//	         live config, unit and service are not touched.
//	commit   installs the staged config (keeping a backup), starts the service
//	         and fails closed: if it does not come up, the backup is restored.
//
// After both ends committed and BCC saw them healthy, finalize drops the
// backups and the one-time secrets; rollback restores the previous state on
// this node from any earlier point. Every step is idempotent-safe to retry
// and all state lives under <StateDir>/tunnels/<id>.
//
// The pairing itself is done by the same baft-pair binary the installer
// uses; this package never reimplements the handshake or the config writer.
package tunnelnode

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
)

// System is how the package touches the host; tests replace it.
type System interface {
	Systemctl(ctx context.Context, args ...string) (string, error)
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// Env locates everything on the host.
type Env struct {
	ConfigDir string // /etc/baft
	StateDir  string // /var/lib/baft
	UnitDir   string // /etc/systemd/system
	Service   string // baft
	// User owns the service's keys and state; "" skips ownership changes
	// (tests).
	User    string
	BaftBin string
	PairBin string
	// Settle is how long the service must stay up before a commit or health
	// check counts.
	Settle time.Duration
	// MetricsListen is the loopback metrics address written into configs
	// (default 127.0.0.1:9191).
	MetricsListen string
	System        System
	Now           func() time.Time
}

const (
	PhasePrepared   = "prepared"
	PhaseCommitted  = "committed"
	PhaseFinalized  = "finalized"
	PhaseRolledBack = "rolled_back"

	RoleEX = "ex"
	RoleIR = "ir"
)

var (
	idRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	hostRe     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.:-]{0,251}[A-Za-z0-9])?$`)
	fixedIPv4  = regexp.MustCompile(`^([0-9]{1,3}\.){3}[0-9]{1,3}:[0-9]{1,5}$`)
	fixedIPv6  = regexp.MustCompile(`^\[[0-9A-Fa-f:]+\]:[0-9]{1,5}$`)
	loopbackRe = regexp.MustCompile(`^(127\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|\[::1\]):[0-9]{1,5}$`)
)

// Txn is the persisted state of one change on this node.
type Txn struct {
	Version int       `json:"version"`
	ID      string    `json:"id"`
	Role    string    `json:"role"`
	Phase   string    `json:"phase"`
	Created time.Time `json:"created"`

	Port        int    `json:"port,omitempty"`
	RouteListen string `json:"route_listen,omitempty"`

	// What was there before the commit, so rollback can restore it.
	HadConfig     bool `json:"had_config"`
	HadUnit       bool `json:"had_unit"`
	WasActive     bool `json:"was_active"`
	WasEnabled    bool `json:"was_enabled"`
	UnitChanged   bool `json:"unit_changed"`
	PKIReplaced   bool `json:"pki_replaced"`
	ConfigWritten bool `json:"config_written"`

	// Ownership: what this change installed, so rollback only ever restores or
	// deletes files that are still exactly the ones BAFT wrote.
	HadMarker          bool   `json:"had_marker"`
	InstalledConfigSHA string `json:"installed_config_sha,omitempty"`
	InstalledUnitSHA   string `json:"installed_unit_sha,omitempty"`
	InstalledMarkerSHA string `json:"installed_marker_sha,omitempty"`

	// Generation counts committed configuration changes on this node.
	Generation         int `json:"generation,omitempty"`
	PreviousGeneration int `json:"previous_generation,omitempty"`
}

type Manager struct {
	Env
	mu sync.Mutex
}

func New(env Env) (*Manager, error) {
	if env.ConfigDir == "" || env.StateDir == "" || env.UnitDir == "" || env.BaftBin == "" || env.PairBin == "" || env.System == nil {
		return nil, errors.New("tunnelnode: config dir, state dir, unit dir, binaries and system are required")
	}
	if env.Service == "" {
		env.Service = "baft"
	}
	if env.MetricsListen == "" {
		env.MetricsListen = "127.0.0.1:9191"
	}
	if env.Settle == 0 {
		env.Settle = 5 * time.Second
	}
	if env.Now == nil {
		env.Now = time.Now
	}
	return &Manager{Env: env}, nil
}

// ---- parameters ----

type ExParams struct {
	PublicAddress string // host or IP the IR dials; also the certificate name
	Port          int
	Target        string // fixed IP:port the route exits to
	RouteID       string
	RecordShaping bool
	MetricsListen string // default 127.0.0.1:9191
}

type IRParams struct {
	RouteListen   string // loopback address local clients connect to
	RouteID       string
	MetricsListen string
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }

func (p ExParams) validate() error {
	switch {
	case !hostRe.MatchString(p.PublicAddress):
		return errors.New("invalid public address")
	case !validPort(p.Port):
		return errors.New("invalid port")
	case !(fixedIPv4.MatchString(p.Target) || fixedIPv6.MatchString(p.Target)):
		return errors.New("target must be a fixed IP:port")
	case !idRe.MatchString(p.RouteID):
		return errors.New("invalid route id")
	}
	return validTargetPort(p.Target)
}

func validTargetPort(hp string) error {
	host, port, err := net.SplitHostPort(hp)
	if err != nil || net.ParseIP(host) == nil {
		return errors.New("target must be a fixed IP:port")
	}
	if n, err := strconv.Atoi(port); err != nil || !validPort(n) {
		return errors.New("invalid target port")
	}
	return nil
}

func (p IRParams) validate() error {
	if !loopbackRe.MatchString(p.RouteListen) {
		return errors.New("route listen must be a loopback address")
	}
	if !idRe.MatchString(p.RouteID) {
		return errors.New("invalid route id")
	}
	return nil
}

// ---- paths ----

func (m *Manager) stage(id string) string        { return filepath.Join(m.StateDir, "tunnels", id) }
func (m *Manager) txnPath(id string) string      { return filepath.Join(m.stage(id), "txn.json") }
func (m *Manager) activePath() string            { return filepath.Join(m.StateDir, "tunnels", "active") }
func (m *Manager) markerPath() string            { return filepath.Join(m.ConfigDir, "baft.managed.json") }
func (m *Manager) liveConfig() string            { return filepath.Join(m.ConfigDir, "baft.yaml") }
func (m *Manager) noiseKey() string              { return filepath.Join(m.ConfigDir, "noise-key.json") }
func (m *Manager) pkiDir() string                { return filepath.Join(m.ConfigDir, "pki") }
func (m *Manager) unitPath() string              { return filepath.Join(m.UnitDir, m.Service+".service") }
func (m *Manager) backup(id, name string) string { return filepath.Join(m.stage(id), "backup", name) }

func (m *Manager) readTxn(id string) (Txn, error) {
	var t Txn
	b, err := os.ReadFile(m.txnPath(id))
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return t, fmt.Errorf("corrupt state for %s: %w", id, err)
	}
	return t, nil
}

func (m *Manager) generationPath() string { return filepath.Join(m.StateDir, "tunnels", "generation") }

// readGeneration returns the node's current configuration generation (0 on a
// node that never committed a change).
func (m *Manager) readGeneration() int {
	b, err := os.ReadFile(m.generationPath())
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (m *Manager) writeGeneration(n int) error {
	return writeFile(m.generationPath(), []byte(strconv.Itoa(n)+"\n"), 0o600)
}

func (m *Manager) writeTxn(t Txn) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(m.txnPath(t.ID), append(b, '\n'), 0o600)
}

func (m *Manager) activeID() string {
	b, err := os.ReadFile(m.activePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (m *Manager) setActive(id string) error {
	return writeFile(m.activePath(), []byte(id+"\n"), 0o600)
}

func (m *Manager) clearActive() { _ = os.Remove(m.activePath()) }

// begin claims the node for a new change.
func (m *Manager) begin(id, role string) (Txn, error) {
	if !idRe.MatchString(id) {
		return Txn{}, errors.New("invalid tunnel id")
	}
	if cur := m.activeID(); cur != "" && cur != id {
		if t, err := m.readTxn(cur); err == nil && (t.Phase == PhasePrepared || t.Phase == PhaseCommitted) {
			return Txn{}, fmt.Errorf("tunnel change %s is still in progress on this node", cur)
		}
	}
	if _, err := m.readTxn(id); err == nil {
		return Txn{}, fmt.Errorf("tunnel change %s already exists on this node", id)
	}
	t := Txn{Version: 1, ID: id, Role: role, Phase: PhasePrepared, Created: m.Now().UTC()}
	if err := os.MkdirAll(m.stage(id), 0o700); err != nil {
		return t, err
	}
	// The service reads the IR's staged CA from here, so it must be able to
	// traverse the directory the stage lives in.
	if err := m.chownTo(filepath.Dir(m.stage(id)), m.User, m.User); err != nil {
		return t, err
	}
	if err := m.setActive(id); err != nil {
		return t, err
	}
	return t, m.writeTxn(t)
}

// ---- prepare ----

// PrepareEX readies the listener side and returns the one-time pairing code
// (a secret: it carries the pairing PSK and expires after 15 minutes).
func (m *Manager) PrepareEX(ctx context.Context, id string, p ExParams) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := p.validate(); err != nil {
		return "", err
	}
	if p.MetricsListen == "" {
		p.MetricsListen = m.MetricsListen
	}
	t, err := m.begin(id, RoleEX)
	if err != nil {
		return "", err
	}
	t.Port = p.Port
	code, err := func() (string, error) {
		if err := m.ensureNoiseKey(ctx); err != nil {
			return "", err
		}
		replaced, err := m.ensurePKI(ctx, id, p.PublicAddress)
		t.PKIReplaced = replaced
		if err != nil {
			return "", err
		}
		identity := "urn:baft:node:ex-" + randomHex(6)
		args := []string{"ex-code", "--key", m.noiseKey(), "--address", net.JoinHostPort(p.PublicAddress, strconv.Itoa(p.Port)),
			"--server-name", p.PublicAddress, "--identity", identity, "--ca-file", filepath.Join(m.pkiDir(), "ca.pem"),
			"--psk-out", filepath.Join(m.stage(id), "psk"), "--pending-out", filepath.Join(m.stage(id), "pending.json"),
			"--ttl", "15m"}
		if p.RecordShaping {
			args = append(args, "--record-shaping")
		}
		out, err := m.System.Run(ctx, m.PairBin, args...)
		if err != nil {
			return "", fmt.Errorf("baft-pair ex-code: %v: %s", err, tailOf(out))
		}
		return strings.TrimSpace(out), nil
	}()
	if err != nil {
		m.abort(ctx, t)
		return "", err
	}
	// The staged commit arguments are kept so commit needs only the reply.
	if err := writeJSON(filepath.Join(m.stage(id), "ex-params.json"), p, 0o600); err != nil {
		m.abort(ctx, t)
		return "", err
	}
	return code, m.writeTxn(t)
}

// PrepareIR consumes the EX's pairing code, stages the dialer config and
// returns the reply code for the EX (a secret).
func (m *Manager) PrepareIR(ctx context.Context, id, code string, p IRParams) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := p.validate(); err != nil {
		return "", err
	}
	if p.MetricsListen == "" {
		p.MetricsListen = m.MetricsListen
	}
	t, err := m.begin(id, RoleIR)
	if err != nil {
		return "", err
	}
	t.RouteListen = p.RouteListen
	reply, err := func() (string, error) {
		if err := m.ensureNoiseKey(ctx); err != nil {
			return "", err
		}
		stage := m.stage(id)
		out, err := m.System.Run(ctx, m.PairBin, "ir-apply", "--code", code, "--key", m.noiseKey(), "--state-dir", stage,
			"--config-out", filepath.Join(stage, "baft.new.yaml"), "--route-id", p.RouteID, "--route-listen", p.RouteListen,
			"--metrics-listen", p.MetricsListen, "--unix-socket", filepath.Join(m.StateDir, "admin.sock"))
		if err != nil {
			return "", fmt.Errorf("baft-pair ir-apply: %v: %s", err, redact(tailOf(out), code))
		}
		if err := m.chownTree(stage); err != nil {
			return "", err
		}
		if out, err := m.System.Run(ctx, m.BaftBin, "config", "validate", "--file", filepath.Join(stage, "baft.new.yaml")); err != nil {
			return "", fmt.Errorf("staged config is invalid: %v: %s", err, tailOf(out))
		}
		return strings.TrimSpace(out), nil
	}()
	if err != nil {
		m.abort(ctx, t)
		return "", err
	}
	return reply, m.writeTxn(t)
}

// ---- commit ----

// CommitEX verifies the IR's reply, installs the listener config and starts
// the service.
func (m *Manager) CommitEX(ctx context.Context, id, reply string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.requirePhase(id, RoleEX, PhasePrepared)
	if err != nil {
		return "", err
	}
	var p ExParams
	if err := readJSON(filepath.Join(m.stage(id), "ex-params.json"), &p); err != nil {
		return "", fmt.Errorf("prepare state missing: %w", err)
	}
	stage := m.stage(id)
	out, err := m.System.Run(ctx, m.PairBin, "ex-accept", "--reply", reply, "--pending", filepath.Join(stage, "pending.json"),
		"--psk-file", filepath.Join(stage, "psk"), "--key", m.noiseKey(), "--listen", "0.0.0.0:"+strconv.Itoa(p.Port),
		"--ca-file", filepath.Join(m.pkiDir(), "ca.pem"), "--cert-file", filepath.Join(m.pkiDir(), "server.pem"),
		"--cert-key-file", filepath.Join(m.pkiDir(), "server.key"), "--target", p.Target, "--route-id", p.RouteID,
		"--metrics-listen", p.MetricsListen, "--unix-socket", filepath.Join(m.StateDir, "admin.sock"),
		"--config-out", filepath.Join(stage, "baft.new.yaml"))
	if err != nil {
		// Nothing live was touched; the transaction stays prepared so BCC can roll it back.
		return "", fmt.Errorf("baft-pair ex-accept: %v: %s", err, redact(tailOf(out), reply))
	}
	if out, err := m.System.Run(ctx, m.BaftBin, "config", "validate", "--file", filepath.Join(stage, "baft.new.yaml")); err != nil {
		return "", fmt.Errorf("staged config is invalid: %v: %s", err, tailOf(out))
	}
	return m.install(ctx, t, p.Port)
}

// CommitIR installs the staged dialer config and starts the service.
func (m *Manager) CommitIR(ctx context.Context, id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.requirePhase(id, RoleIR, PhasePrepared)
	if err != nil {
		return "", err
	}
	return m.install(ctx, t, 0)
}

// install swaps the staged config in, starts the service, and restores the
// previous state if it does not come up.
func (m *Manager) install(ctx context.Context, t Txn, port int) (string, error) {
	stage := m.stage(t.ID)
	staged := filepath.Join(stage, "baft.new.yaml")
	if _, err := os.Stat(staged); err != nil {
		return "", errors.New("no staged config")
	}
	if b, err := os.ReadFile(m.liveConfig()); err == nil {
		t.HadConfig = true
		if err := writeFile(m.backup(t.ID, "baft.yaml"), b, 0o600); err != nil {
			return "", err
		}
	}
	if b, err := os.ReadFile(m.unitPath()); err == nil {
		t.HadUnit = true
		if err := writeFile(m.backup(t.ID, "unit"), b, 0o600); err != nil {
			return "", err
		}
	}
	state, _ := m.System.Systemctl(ctx, "is-active", m.Service)
	t.WasActive = state == "active"
	if en, _ := m.System.Systemctl(ctx, "is-enabled", m.Service); en == "enabled" {
		t.WasEnabled = true
	}
	t.PreviousGeneration = m.readGeneration()
	t.Generation = t.PreviousGeneration + 1
	if b, err := os.ReadFile(m.markerPath()); err == nil {
		t.HadMarker = true
		if err := writeFile(m.backup(t.ID, "marker.json"), b, 0o600); err != nil {
			return "", err
		}
	}
	staged2, err := os.ReadFile(staged)
	if err != nil {
		return "", err
	}
	want := m.ManagedUnit(port, t.ID, t.Generation)
	t.InstalledUnitSHA = shaHex([]byte(want))
	t.InstalledConfigSHA = shaHex(staged2)
	t.Phase = PhaseCommitted
	t.ConfigWritten = true
	t.UnitChanged = true
	if err := m.writeTxn(t); err != nil {
		return "", err
	}
	if err := m.writeGeneration(t.Generation); err != nil {
		return "", err
	}
	if err := writeFile(m.unitPath(), []byte(want), 0o644); err != nil {
		_, _ = m.rollbackTxn(ctx, t)
		return "", err
	}
	if err := m.writeConfig(staged2); err != nil {
		_, _ = m.rollbackTxn(ctx, t)
		return "", err
	}
	markerBytes, err := json.MarshalIndent(Marker{
		ManagedBy: "baft", Version: 1, TunnelID: t.ID, Generation: t.Generation, Role: t.Role,
		ConfigSHA256: t.InstalledConfigSHA, UnitSHA256: t.InstalledUnitSHA, Updated: m.Now().UTC(),
	}, "", "  ")
	if err == nil {
		markerBytes = append(markerBytes, '\n')
		t.InstalledMarkerSHA = shaHex(markerBytes)
		if err = m.writeTxn(t); err == nil {
			err = writeFile(m.markerPath(), markerBytes, 0o644)
		}
	}
	if err != nil {
		_, _ = m.rollbackTxn(ctx, t)
		return "", err
	}
	_ = os.Remove(staged)
	for _, args := range [][]string{{"daemon-reload"}, {"enable", m.Service}, {"restart", m.Service}} {
		if _, err := m.System.Systemctl(ctx, args...); err != nil {
			msg, _ := m.rollbackTxn(ctx, t)
			return "", fmt.Errorf("systemctl %s: %w (%s)", strings.Join(args, " "), err, msg)
		}
	}
	if err := m.waitActive(ctx); err != nil {
		msg, _ := m.rollbackTxn(ctx, t)
		return "", fmt.Errorf("%v; %s", err, msg)
	}
	return m.Service + " is active with the new config", nil
}

// ---- health ----

// Health checks that the service is up and stays up, and that its listener
// (EX) or local route (IR) accepts connections.
func (m *Manager) Health(ctx context.Context, id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.readTxn(id)
	if err != nil {
		return "", fmt.Errorf("unknown tunnel change %s", id)
	}
	if t.Phase != PhaseCommitted && t.Phase != PhaseFinalized {
		return "", fmt.Errorf("tunnel change %s is %s, not committed", id, t.Phase)
	}
	r0, err := m.stableSample(ctx)
	if err != nil {
		return "", err
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(m.Settle):
	}
	r1, err := m.stableSample(ctx)
	if err != nil {
		return "", err
	}
	if r0 != r1 {
		return "", fmt.Errorf("%s restarted during the check (%s -> %s restarts)", m.Service, r0, r1)
	}
	addr := t.RouteListen
	if t.Role == RoleEX {
		addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(t.Port))
	}
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return "", fmt.Errorf("%s does not accept connections: %v", addr, err)
	}
	c.Close()
	return fmt.Sprintf("%s healthy: active, %s restarts, %s accepting", m.Service, r1, addr), nil
}

func (m *Manager) stableSample(ctx context.Context) (restarts string, err error) {
	state, _ := m.System.Systemctl(ctx, "is-active", m.Service)
	if state != "active" {
		return "", fmt.Errorf("%s is %q", m.Service, state)
	}
	r, _ := m.System.Systemctl(ctx, "show", "-p", "NRestarts", "--value", m.Service)
	return strings.TrimSpace(r), nil
}

// ---- finalize / rollback ----

// Finalize makes the change permanent: backups and one-time secrets go away.
func (m *Manager) Finalize(ctx context.Context, id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.readTxn(id)
	if err != nil {
		return "", fmt.Errorf("unknown tunnel change %s", id)
	}
	if t.Phase == PhaseFinalized {
		return "already finalized", nil
	}
	if t.Phase != PhaseCommitted {
		return "", fmt.Errorf("tunnel change %s is %s, not committed", id, t.Phase)
	}
	t.Phase = PhaseFinalized
	if err := m.writeTxn(t); err != nil {
		return "", err
	}
	for _, f := range []string{"psk", "pending.json", "pairing.pending.json", "ex-params.json"} {
		_ = os.Remove(filepath.Join(m.stage(id), f))
	}
	_ = os.RemoveAll(filepath.Join(m.stage(id), "backup"))
	if t.PKIReplaced {
		_ = os.RemoveAll(m.pkiDir() + ".before-" + id)
	}
	m.clearActive()
	// Older changes are superseded: nothing references their stage any more.
	if ents, err := os.ReadDir(filepath.Join(m.StateDir, "tunnels")); err == nil {
		for _, e := range ents {
			if e.IsDir() && e.Name() != id {
				if old, err := m.readTxn(e.Name()); err == nil && (old.Phase == PhaseFinalized || old.Phase == PhaseRolledBack) {
					_ = os.RemoveAll(m.stage(e.Name()))
				}
			}
		}
	}
	return "finalized", nil
}

// Rollback restores this node to what it was before the change. It is safe
// to call for a change that never started or already rolled back.
func (m *Manager) Rollback(ctx context.Context, id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.readTxn(id)
	if err != nil {
		return "nothing to roll back", nil
	}
	switch t.Phase {
	case PhaseRolledBack:
		return "already rolled back", nil
	case PhaseFinalized:
		return "", fmt.Errorf("tunnel change %s is finalized; make a new change instead", id)
	}
	return m.rollbackTxn(ctx, t)
}

func (m *Manager) abort(ctx context.Context, t Txn) { _, _ = m.rollbackTxn(ctx, t) }

func (m *Manager) rollbackTxn(ctx context.Context, t Txn) (string, error) {
	var errs []string
	note := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}
	if t.ConfigWritten {
		// Only touch what is still exactly what BAFT wrote. If an operator or
		// another tool changed the config, unit or marker since the commit,
		// leave everything as it is and say so: restoring would destroy
		// their change.
		if v := m.ownershipViolations(t); len(v) > 0 {
			msg := "rollback refused: " + strings.Join(v, "; ") + "; nothing was changed"
			return msg, errors.New(msg)
		}
		note(m.writeGeneration(t.PreviousGeneration))
		if t.HadConfig {
			b, err := os.ReadFile(m.backup(t.ID, "baft.yaml"))
			if err != nil {
				note(fmt.Errorf("previous config backup missing: %w", err))
			} else {
				note(m.writeConfig(b))
			}
		} else {
			note(removeIfExists(m.liveConfig()))
		}
		if t.UnitChanged {
			if t.HadUnit {
				if b, err := os.ReadFile(m.backup(t.ID, "unit")); err == nil {
					note(writeFile(m.unitPath(), b, 0o644))
				} else {
					note(err)
				}
			} else {
				note(removeIfExists(m.unitPath()))
			}
		}
		if t.HadMarker {
			if b, err := os.ReadFile(m.backup(t.ID, "marker.json")); err == nil {
				note(writeFile(m.markerPath(), b, 0o644))
			} else {
				note(err)
			}
		} else {
			note(removeIfExists(m.markerPath()))
		}
		_, _ = m.System.Systemctl(ctx, "daemon-reload")
		if t.HadConfig && t.WasActive {
			if _, err := m.System.Systemctl(ctx, "restart", m.Service); err != nil {
				note(err)
			}
		} else {
			_, _ = m.System.Systemctl(ctx, "stop", m.Service)
			if !t.WasEnabled {
				_, _ = m.System.Systemctl(ctx, "disable", m.Service)
			}
		}
	}
	if t.PKIReplaced {
		before := m.pkiDir() + ".before-" + t.ID
		if _, err := os.Stat(before); err == nil {
			note(os.RemoveAll(m.pkiDir()))
			note(os.Rename(before, m.pkiDir()))
		}
	}
	if len(errs) > 0 {
		return "rollback incomplete: " + strings.Join(errs, "; "), errors.New("rollback incomplete: " + strings.Join(errs, "; "))
	}
	for _, f := range []string{"psk", "pending.json", "pairing.pending.json", "ex-params.json", "baft.new.yaml"} {
		_ = os.Remove(filepath.Join(m.stage(t.ID), f))
	}
	t.Phase = PhaseRolledBack
	_ = m.writeTxn(t)
	if m.activeID() == t.ID {
		m.clearActive()
	}
	return "rolled back", nil
}

// ---- helpers ----

func (m *Manager) requirePhase(id, role, phase string) (Txn, error) {
	t, err := m.readTxn(id)
	if err != nil {
		return t, fmt.Errorf("unknown tunnel change %s (was it prepared on this node?)", id)
	}
	if t.Role != role {
		return t, fmt.Errorf("tunnel change %s is the %s side on this node", id, t.Role)
	}
	if t.Phase != phase {
		return t, fmt.Errorf("tunnel change %s is %s, expected %s", id, t.Phase, phase)
	}
	return t, nil
}

func (m *Manager) ensureNoiseKey(ctx context.Context) error {
	if _, err := os.Stat(m.noiseKey()); err == nil {
		return nil
	}
	if err := os.MkdirAll(m.ConfigDir, 0o750); err != nil {
		return err
	}
	if out, err := m.System.Run(ctx, m.PairBin, "keygen", "--file", m.noiseKey()); err != nil {
		return fmt.Errorf("baft-pair keygen: %v: %s", err, tailOf(out))
	}
	// The runtime refuses a private key that group/other can read, and the
	// service reads it as its own user.
	return m.chownTo(m.noiseKey(), m.User, m.User)
}

// ensurePKI creates the EX's outer TLS material for host, replacing (and
// keeping aside for rollback) material made for another name.
func (m *Manager) ensurePKI(ctx context.Context, id, host string) (replaced bool, err error) {
	marker := filepath.Join(m.pkiDir(), "host")
	if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == host {
		return false, nil
	}
	if _, err := os.Stat(m.pkiDir()); err == nil {
		if err := os.Rename(m.pkiDir(), m.pkiDir()+".before-"+id); err != nil {
			return false, err
		}
		replaced = true
	}
	if err := os.MkdirAll(m.ConfigDir, 0o750); err != nil {
		return replaced, err
	}
	if out, err := m.System.Run(ctx, m.PairBin, "pki", "--dir", m.pkiDir(), "--host", host); err != nil {
		return replaced, fmt.Errorf("baft-pair pki: %v: %s", err, tailOf(out))
	}
	if err := writeFile(marker, []byte(host+"\n"), 0o644); err != nil {
		return replaced, err
	}
	// Same modes as install.sh: only the service user reads server.key, the CA
	// signing key stays root-only, certificates are public.
	_ = os.Chmod(m.pkiDir(), 0o750)
	_ = os.Chmod(filepath.Join(m.pkiDir(), "ca.key"), 0o600)
	_ = os.Chmod(filepath.Join(m.pkiDir(), "server.key"), 0o600)
	_ = os.Chmod(filepath.Join(m.pkiDir(), "ca.pem"), 0o644)
	_ = os.Chmod(filepath.Join(m.pkiDir(), "server.pem"), 0o644)
	if err := m.chownTo(filepath.Join(m.pkiDir(), "server.key"), m.User, m.User); err != nil {
		return replaced, err
	}
	return replaced, m.chownTo(m.pkiDir(), "root", m.User)
}

// writeConfig installs b as the live config, root-owned and group-readable
// by the service user, validated by the same loader `baft run` uses.
func (m *Manager) writeConfig(b []byte) error {
	if err := os.MkdirAll(m.ConfigDir, 0o750); err != nil {
		return err
	}
	tmp := filepath.Join(m.ConfigDir, "baft.new.yaml")
	if err := writeFile(tmp, b, 0o640); err != nil {
		return err
	}
	if err := m.chownTo(tmp, "root", m.User); err != nil {
		return err
	}
	if out, err := m.System.Run(context.Background(), m.BaftBin, "config", "validate", "--file", tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("config does not validate: %v: %s", err, tailOf(out))
	}
	return os.Rename(tmp, m.liveConfig())
}

// waitActive requires the service to be active, and to stay active without a
// restart, for the whole settle window. A service that crash-loops is
// reported as failed even if a sample happens to catch it running.
func (m *Manager) waitActive(ctx context.Context) error {
	start := m.Now()
	step := m.Settle / 10
	if step < 5*time.Millisecond {
		step = 5 * time.Millisecond
	}
	var since time.Time
	for {
		state, _ := m.System.Systemctl(ctx, "is-active", m.Service)
		now := m.Now()
		switch {
		case state == "active":
			if since.IsZero() {
				since = now
			}
			if now.Sub(since) >= m.Settle {
				return nil
			}
		case since.IsZero() && (state == "activating" || state == "reloading"):
			// still starting
		case since.IsZero():
			return fmt.Errorf("%s is %q after the change", m.Service, state)
		default:
			return fmt.Errorf("%s went %q after starting: it is restarting", m.Service, state)
		}
		if since.IsZero() && now.Sub(start) > 4*m.Settle {
			return fmt.Errorf("%s is still %q", m.Service, state)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(step):
		}
	}
}

func (m *Manager) chownTree(dir string) error {
	if m.User == "" {
		return nil
	}
	u, err := user.Lookup(m.User)
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	})
}

func (m *Manager) chownTo(path, owner, group string) error {
	if m.User == "" {
		return nil
	}
	uid, gid := -1, -1
	if owner != "" {
		u, err := user.Lookup(owner)
		if err != nil {
			return err
		}
		uid, _ = strconv.Atoi(u.Uid)
	}
	if group != "" {
		g, err := user.LookupGroup(group)
		if err != nil {
			return err
		}
		gid, _ = strconv.Atoi(g.Gid)
	}
	return os.Chown(path, uid, gid)
}

// Unit renders baft.service exactly as install.sh does (port 0 means no
// privileged bind is needed).
func (m *Manager) Unit(port int) string {
	caps := ""
	if port > 0 && port < 1024 {
		caps = "AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE\n"
	}
	user := m.User
	if user == "" {
		user = "baft"
	}
	return fmt.Sprintf(`[Unit]
Description=BAFT transport service
After=network-online.target
Wants=network-online.target
# The IR dialer exits while its EX is unreachable; keep retrying forever.
StartLimitIntervalSec=0

[Service]
Type=simple
User=%[1]s
Group=%[1]s
ExecStart=%[2]s run --file %[3]s
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=2s
%[5]sNoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
ReadWritePaths=%[4]s
UMask=0027

[Install]
WantedBy=multi-user.target
`, user, m.BaftBin, m.liveConfig(), m.StateDir, caps)
}

func writeFile(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func writeJSON(path string, v any, mode os.FileMode) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFile(path, b, mode)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("tunnelnode: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func tailOf(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 800 {
		return "..." + s[len(s)-800:]
	}
	return s
}

func redact(s string, secrets ...string) string {
	for _, sec := range secrets {
		if len(sec) >= 8 {
			s = strings.ReplaceAll(s, sec, "[redacted]")
		}
	}
	return s
}

// Observed is what this node can prove about itself for a change. BCC compares
// it with what it asked for; a command's exit status is never the evidence.
type Observed struct {
	TunnelID           string `json:"tunnel_id"`
	Role               string `json:"role"`
	Phase              string `json:"phase"`
	Generation         int    `json:"generation"`
	PreviousGeneration int    `json:"previous_generation"`
	NodeGeneration     int    `json:"node_generation"`
	ServiceActive      bool   `json:"service_active"`
	Restarts           string `json:"restarts,omitempty"`
	ConfigSHA256       string `json:"config_sha256"`
	ConfigRole         string `json:"config_role"`
	Listen             string `json:"listen,omitempty"`
	RouteID            string `json:"route_id,omitempty"`
	RouteListen        string `json:"route_listen,omitempty"`
	Target             string `json:"target,omitempty"`
	PeerAddress        string `json:"peer_address,omitempty"`
	UnitMatches        bool   `json:"unit_matches"`

	// Ownership marker as found on the node.
	Managed             bool   `json:"managed"`
	MarkerTunnelID      string `json:"marker_tunnel_id,omitempty"`
	MarkerGeneration    int    `json:"marker_generation,omitempty"`
	MarkerConfigMatches bool   `json:"marker_config_matches"`
	MarkerUnitMatches   bool   `json:"marker_unit_matches"`
}

// Observe reads the live state of this node for change id. It only reads.
func (m *Manager) Observe(ctx context.Context, id string) (Observed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.readTxn(id)
	if err != nil {
		return Observed{}, fmt.Errorf("unknown tunnel change %s", id)
	}
	o := Observed{
		TunnelID: t.ID, Role: t.Role, Phase: t.Phase, Generation: t.Generation,
		PreviousGeneration: t.PreviousGeneration, NodeGeneration: m.readGeneration(),
	}
	raw, err := os.ReadFile(m.liveConfig())
	if err != nil {
		return o, fmt.Errorf("live config: %w", err)
	}
	sum := sha256.Sum256(raw)
	o.ConfigSHA256 = hex.EncodeToString(sum[:])
	cfg, err := config.LoadFile(m.liveConfig())
	if err != nil {
		return o, fmt.Errorf("live config does not load: %w", err)
	}
	o.ConfigRole = cfg.Node.Role
	if cfg.Server != nil {
		o.Listen = cfg.Server.Listen
	}
	if cfg.Peer != nil {
		o.PeerAddress = cfg.Peer.Address
	}
	if len(cfg.Routes) > 0 {
		o.RouteID, o.RouteListen, o.Target = cfg.Routes[0].ID, cfg.Routes[0].Listen, cfg.Routes[0].Target
	}
	state, _ := m.System.Systemctl(ctx, "is-active", m.Service)
	o.ServiceActive = state == "active"
	o.Restarts, _ = m.System.Systemctl(ctx, "show", "-p", "NRestarts", "--value", m.Service)
	var unitSHA string
	if unit, err := os.ReadFile(m.unitPath()); err == nil {
		port := t.Port
		if t.Role == RoleIR {
			port = 0
		}
		o.UnitMatches = string(unit) == m.ManagedUnit(port, t.ID, t.Generation)
		unitSHA = shaHex(unit)
	}
	if mb, err := os.ReadFile(m.markerPath()); err == nil {
		var mk Marker
		if json.Unmarshal(mb, &mk) == nil && mk.ManagedBy == "baft" {
			o.Managed = true
			o.MarkerTunnelID, o.MarkerGeneration = mk.TunnelID, mk.Generation
			o.MarkerConfigMatches = mk.ConfigSHA256 == o.ConfigSHA256
			o.MarkerUnitMatches = unitSHA != "" && mk.UnitSHA256 == unitSHA
		}
	}
	return o, nil
}

// Marker is the ownership record next to the configuration (the YAML/JSON
// config itself is strictly decoded and cannot carry comments).
type Marker struct {
	ManagedBy    string    `json:"managed_by"`
	Version      int       `json:"version"`
	TunnelID     string    `json:"tunnel_id"`
	Generation   int       `json:"generation"`
	Role         string    `json:"role"`
	ConfigSHA256 string    `json:"config_sha256"`
	UnitSHA256   string    `json:"unit_sha256"`
	Updated      time.Time `json:"updated"`
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ManagedUnit is the unit with ownership markers in its header.
func (m *Manager) ManagedUnit(port int, tunnelID string, generation int) string {
	return fmt.Sprintf("# baft-managed: true\n# baft-tunnel: %s\n# baft-generation: %d\n", tunnelID, generation) + m.Unit(port)
}

// ownershipViolations lists files this change installed that are no longer
// byte-for-byte what it wrote (and are not simply the original again).
func (m *Manager) ownershipViolations(t Txn) []string {
	var v []string
	check := func(label, live, installed, backup string, had bool) {
		if installed == "" {
			return // change recorded before ownership tracking: nothing to compare
		}
		b, err := os.ReadFile(live)
		if err != nil {
			return // absent: nothing of ours to protect
		}
		sum := shaHex(b)
		if sum == installed {
			return
		}
		if had {
			if bb, err := os.ReadFile(backup); err == nil && shaHex(bb) == sum {
				return // already back to the original
			}
		}
		v = append(v, label+" "+live+" was changed outside BAFT after the commit")
	}
	check("config", m.liveConfig(), t.InstalledConfigSHA, m.backup(t.ID, "baft.yaml"), t.HadConfig)
	check("unit", m.unitPath(), t.InstalledUnitSHA, m.backup(t.ID, "unit"), t.HadUnit)
	check("marker", m.markerPath(), t.InstalledMarkerSHA, m.backup(t.ID, "marker.json"), t.HadMarker)
	return v
}

// Live is what a node actually has right now, read without a change id. It
// is the input of drift detection: BCC compares it with the tunnel it
// believes is active on this node.
type Live struct {
	ConfigPresent bool   `json:"config_present"`
	UnitPresent   bool   `json:"unit_present"`
	ConfigSHA256  string `json:"config_sha256,omitempty"`
	ConfigLoads   bool   `json:"config_loads"`

	MarkerPresent       bool   `json:"marker_present"`
	MarkerManagedBy     string `json:"marker_managed_by,omitempty"`
	MarkerTunnelID      string `json:"marker_tunnel_id,omitempty"`
	MarkerGeneration    int    `json:"marker_generation,omitempty"`
	MarkerConfigMatches bool   `json:"marker_config_matches"`
	MarkerUnitMatches   bool   `json:"marker_unit_matches"`
	NodeGeneration      int    `json:"node_generation"`

	ServiceActive bool   `json:"service_active"`
	ConfigRole    string `json:"config_role,omitempty"`
	Listen        string `json:"listen,omitempty"`
	PeerAddress   string `json:"peer_address,omitempty"`
	RouteID       string `json:"route_id,omitempty"`
	RouteListen   string `json:"route_listen,omitempty"`
	Target        string `json:"target,omitempty"`
}

// Inspect reports the node's live state. It changes nothing and never fails
// because something is missing: absence is the answer.
func (m *Manager) Inspect(ctx context.Context) Live {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := Live{NodeGeneration: m.readGeneration()}
	raw, err := os.ReadFile(m.liveConfig())
	if err == nil {
		l.ConfigPresent = true
		l.ConfigSHA256 = shaHex(raw)
		if cfg, err := config.LoadFile(m.liveConfig()); err == nil {
			l.ConfigLoads = true
			l.ConfigRole = cfg.Node.Role
			if cfg.Server != nil {
				l.Listen = cfg.Server.Listen
			}
			if cfg.Peer != nil {
				l.PeerAddress = cfg.Peer.Address
			}
			if len(cfg.Routes) > 0 {
				l.RouteID, l.RouteListen, l.Target = cfg.Routes[0].ID, cfg.Routes[0].Listen, cfg.Routes[0].Target
			}
		}
	}
	unit, uerr := os.ReadFile(m.unitPath())
	l.UnitPresent = uerr == nil
	var mk Marker
	if err := readJSON(m.markerPath(), &mk); err == nil {
		l.MarkerPresent = true
		l.MarkerManagedBy, l.MarkerTunnelID, l.MarkerGeneration = mk.ManagedBy, mk.TunnelID, mk.Generation
		l.MarkerConfigMatches = l.ConfigPresent && mk.ConfigSHA256 == l.ConfigSHA256
		l.MarkerUnitMatches = l.UnitPresent && mk.UnitSHA256 == shaHex(unit)
	}
	state, _ := m.System.Systemctl(ctx, "is-active", m.Service)
	l.ServiceActive = state == "active"
	return l
}
