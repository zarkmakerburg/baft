package bcc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
	for id, r := range restored.CertRotations {
		if !terminalCertRotation(r.Phase) {
			warn("certificate rotation %s is in progress (%s) in the backup; it would resume or time out and roll back", id, r.Phase)
		}
	}
	for id, r := range current.CertRotations {
		if _, ok := restored.CertRotations[id]; !ok && r.Phase == CertRotComplete {
			warn("certificate rotation %s of tunnel %s completed since the backup; the restored record would show an older certificate epoch than the servers use", id, r.TunnelID)
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
// running server. It changes nothing. It takes the same locks in the same
// order as the real restore (backupMu, then mutationMu) and verifies the
// current audit before reading, so the state and the audit it compares come
// from one instant at which no mutation is in progress, exactly the boundary
// at which RestoreFromFile decides.
func (s *Server) PreviewRestore(path string, key []byte) (RestorePreview, error) {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	payload, header, err := readBackupFile(path, key)
	if err != nil {
		return RestorePreview{Problem: err.Error()}, nil
	}
	var auditProblem string
	if err := s.audit.Verify(); err != nil {
		auditProblem = "current audit verification failed: " + err.Error()
	}
	current, err := s.store.snapshotState()
	if err != nil {
		return RestorePreview{}, err
	}
	audit, err := s.audit.List(0)
	if err != nil {
		return RestorePreview{}, err
	}
	p, err := buildRestorePreview(payload, header, current, audit, s.now().UTC())
	if err == nil && auditProblem != "" {
		p.WouldRefuse = auditProblem
	}
	return p, err
}

// PreviewRestoreFiles does the same for state files on disk, for a BCC that is
// NOT running. State and audit live in two files that a running BCC changes at
// different moments, and another process cannot share its in-process lock, so
// a running BCC cannot be previewed this way: the function takes the state
// file's process lock (which BCC holds for its whole life) and fails if it
// cannot, so it is never run against a live BCC and nothing can start writing
// while it reads. It also refuses when an interrupted write or restore is
// pending (start BCC once to let it recover). It reads into memory and a
// private temp directory and never writes anything in the live directory. To
// preview against a running BCC use Server.PreviewRestore, which shares the
// restore's own locks.
func PreviewRestoreFiles(stateFile, backupPath string, key []byte, now time.Time) (RestorePreview, error) {
	payload, header, err := readBackupFile(backupPath, key)
	if err != nil {
		return RestorePreview{Problem: err.Error()}, nil
	}
	release, err := LockState(stateFile)
	if err != nil {
		return RestorePreview{}, fmt.Errorf("stop BCC before previewing from files: %w", err)
	}
	defer release()
	for _, pending := range []string{stateFile + "-journal", restoreJournalPath(stateFile)} {
		if _, err := os.Stat(pending); err == nil {
			return RestorePreview{}, fmt.Errorf("%s exists: an interrupted write or restore is pending; start BCC once so it can recover, stop it, then preview", filepath.Base(pending))
		}
	}
	stateBytes, err := os.ReadFile(stateFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return RestorePreview{}, err
	}
	auditBytes, err := os.ReadFile(stateFile + ".audit.jsonl")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return RestorePreview{}, err
	}
	var current state
	normalizeState(&current)
	if len(stateBytes) > 0 {
		if !isSQLiteFile(stateBytes) {
			if len(strings.TrimSpace(string(stateBytes))) > 0 {
				return RestorePreview{}, errors.New("current state is a legacy JSON file; start BCC once to migrate it, then preview")
			}
		} else {
			tmp, err := os.MkdirTemp("", "baft-restore-preview-")
			if err != nil {
				return RestorePreview{}, err
			}
			defer os.RemoveAll(tmp)
			copyPath := filepath.Join(tmp, "state")
			if err := os.WriteFile(copyPath, stateBytes, 0o600); err != nil {
				return RestorePreview{}, err
			}
			if current, err = readStateDB(copyPath); err != nil {
				return RestorePreview{}, fmt.Errorf("read current state: %w", err)
			}
		}
	}
	audit, err := parseAuditBytes(auditBytes)
	if err != nil {
		return RestorePreview{}, err
	}
	return buildRestorePreview(payload, header, current, audit, now.UTC())
}

func parseAuditBytes(b []byte) ([]AuditEntry, error) {
	var out []AuditEntry
	sc := bufio.NewScanner(bytes.NewReader(b))
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
