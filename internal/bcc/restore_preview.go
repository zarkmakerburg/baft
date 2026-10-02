package bcc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RestorePreview says what restoring a backup would do, without doing it.
// It repeats the checks and the anti-rollback merge of the real restore on
// copies, so the answer is what a restore would actually produce. It lists
// identifiers and counts only: no token hash, pairing code or key.
type RestorePreview struct {
	// Verified: the backup decrypted, authenticated, matched its checksum and
	// carries a valid audit chain. Nothing below is meaningful otherwise.
	Verified  bool      `json:"verified"`
	Problem   string    `json:"problem,omitempty"`
	CreatedAt time.Time `json:"backup_created_at"`
	Age       string    `json:"backup_age"`

	// WouldRefuse is set when a real restore would be refused (the current
	// audit log does not extend the backup's anchor, or it is invalid).
	WouldRefuse string `json:"would_refuse,omitempty"`

	Nodes   RestoreDiff `json:"nodes"`
	Tunnels RestoreDiff `json:"tunnels"`
	Jobs    RestoreDiff `json:"jobs"`

	// KeptFromCurrent are things the anti-rollback merge keeps from the live
	// state instead of the backup, so a restore cannot undo them.
	KeptTelemetry []string `json:"kept_telemetry_nodes,omitempty"`
	KeptSecurity  []string `json:"kept_revocation_or_token_nodes,omitempty"`

	AuditCurrent int `json:"audit_entries_current"`
	AuditBackup  int `json:"audit_entries_backup"`

	Warnings []string `json:"warnings,omitempty"`
}

// RestoreDiff compares the live state with the state a restore would leave.
type RestoreDiff struct {
	Current int      `json:"current"`
	After   int      `json:"after_restore"`
	Added   []string `json:"added,omitempty"`   // in the result, not live now
	Removed []string `json:"removed,omitempty"` // live now, gone after the restore
	Changed []string `json:"changed,omitempty"` // present in both, different
}

func diffIDs[T any](cur, after map[string]T, same func(a, b T) bool) RestoreDiff {
	d := RestoreDiff{Current: len(cur), After: len(after)}
	for id, a := range after {
		c, ok := cur[id]
		switch {
		case !ok:
			d.Added = append(d.Added, id)
		case !same(c, a):
			d.Changed = append(d.Changed, id)
		}
	}
	for id := range cur {
		if _, ok := after[id]; !ok {
			d.Removed = append(d.Removed, id)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	sort.Strings(d.Changed)
	return d
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// buildRestorePreview mirrors restoreTransactional up to the point where it
// would start writing.
func buildRestorePreview(payload backupPayload, header BackupHeader, current state, currentAudit []AuditEntry, now time.Time) (RestorePreview, error) {
	p := RestorePreview{
		Verified: true, CreatedAt: header.CreatedAt, Age: now.Sub(header.CreatedAt).Round(time.Minute).String(),
		AuditCurrent: len(currentAudit), AuditBackup: len(payload.Audit),
	}
	if err := verifyAuditEntries(currentAudit); err != nil {
		p.WouldRefuse = "the current audit log is invalid: " + err.Error()
	} else if len(currentAudit) > 0 && !auditContainsAnchor(currentAudit, header.AuditSequence, header.AuditHash) {
		p.WouldRefuse = "the current audit log does not extend the backup's anchor (restore refused)"
	}
	restored, err := cloneState(payload.State)
	if err != nil {
		return p, err
	}
	mergeAntiRollback(&restored, current, header.CreatedAt)

	p.Nodes = diffIDs(current.Nodes, restored.Nodes, func(a, b Node) bool { return sameJSON(a, b) })
	p.Tunnels = diffIDs(current.Tunnels, restored.Tunnels, func(a, b Tunnel) bool { return a.Phase == b.Phase && a.PlanHash == b.PlanHash && a.Error == b.Error })
	p.Jobs = diffIDs(current.Jobs, restored.Jobs, func(a, b Job) bool { return a.Status == b.Status })

	for id, c := range restored.Telemetry {
		if b, ok := payload.State.Telemetry[id]; !ok || !sameJSON(c, b) {
			p.KeptTelemetry = append(p.KeptTelemetry, id)
		}
	}
	for id, n := range restored.Nodes {
		if b, ok := payload.State.Nodes[id]; ok && (n.Revoked != b.Revoked || n.AgentTokenHash != b.AgentTokenHash || n.PreviousAgentTokenHash != b.PreviousAgentTokenHash) {
			p.KeptSecurity = append(p.KeptSecurity, id)
		}
	}
	sort.Strings(p.KeptTelemetry)
	sort.Strings(p.KeptSecurity)

	warn := func(format string, a ...any) { p.Warnings = append(p.Warnings, fmt.Sprintf(format, a...)) }
	if len(p.Nodes.Removed) > 0 {
		warn("%d node(s) enrolled since the backup would disappear: %s", len(p.Nodes.Removed), strings.Join(p.Nodes.Removed, ", "))
	}
	if len(p.Tunnels.Removed) > 0 {
		warn("%d tunnel record(s) created since the backup would disappear while their servers keep running: %s", len(p.Tunnels.Removed), strings.Join(p.Tunnels.Removed, ", "))
	}
	for id, t := range restored.Tunnels {
		if !terminalTunnel(t.Phase) {
			warn("tunnel %s is in progress (%s) in the backup; it would resume or time out", id, t.Phase)
		}
	}
	if len(p.Nodes.Added) > 0 {
		warn("%d node(s) present only in the backup would reappear: %s; check that they are still yours", len(p.Nodes.Added), strings.Join(p.Nodes.Added, ", "))
	}
	if now.Sub(header.CreatedAt) > 7*24*time.Hour {
		warn("the backup is %s old", p.Age)
	}
	return p, nil
}

// PreviewRestore reports what RestoreFromFile(path, key) would do to the
// running server. It changes nothing.
func (s *Server) PreviewRestore(path string, key []byte) (RestorePreview, error) {
	payload, header, err := readBackupFile(path, key)
	if err != nil {
		return RestorePreview{Problem: err.Error()}, nil
	}
	current, err := s.store.snapshotState()
	if err != nil {
		return RestorePreview{}, err
	}
	audit, err := s.audit.List(0)
	if err != nil {
		return RestorePreview{}, err
	}
	return buildRestorePreview(payload, header, current, audit, s.now().UTC())
}

// PreviewRestoreFiles does the same for a BCC that is not running (or is, but
// must not be touched): it reads COPIES of the state file and the audit log, so
// it never writes, migrates or recovers anything in the live directory.
func PreviewRestoreFiles(stateFile, backupPath string, key []byte, now time.Time) (RestorePreview, error) {
	payload, header, err := readBackupFile(backupPath, key)
	if err != nil {
		return RestorePreview{Problem: err.Error()}, nil
	}
	tmp, err := os.MkdirTemp("", "baft-restore-preview-")
	if err != nil {
		return RestorePreview{}, err
	}
	defer os.RemoveAll(tmp)
	var current state
	normalizeState(&current)
	if _, err := os.Stat(stateFile); err == nil {
		copyPath := filepath.Join(tmp, "state")
		if err := copyFile(stateFile, copyPath); err != nil {
			return RestorePreview{}, err
		}
		b, err := os.ReadFile(copyPath)
		if err != nil {
			return RestorePreview{}, err
		}
		if isSQLiteFile(b) {
			if current, err = readStateDB(copyPath); err != nil {
				return RestorePreview{}, fmt.Errorf("read current state: %w", err)
			}
		} else if len(strings.TrimSpace(string(b))) > 0 {
			return RestorePreview{}, errors.New("current state is a legacy JSON file; start BCC once to migrate it, then preview")
		}
	}
	audit, err := readAuditFile(stateFile + ".audit.jsonl")
	if err != nil {
		return RestorePreview{}, err
	}
	return buildRestorePreview(payload, header, current, audit, now.UTC())
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func readAuditFile(path string) ([]AuditEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []AuditEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e AuditEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("decode audit entry: %w", err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}
