package bcc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// RestoreFiles performs the existing transactional BCC restore against local
// state files while BCC is stopped. It deliberately has no HTTP surface.
//
// Safety properties:
//   - the backup is authenticated before any live file is opened for mutation;
//   - the BCC process lock must be available;
//   - unfinished state/restore journals are refused rather than guessed through;
//   - the current audit chain and backup anchor are checked before OpenStore;
//   - RestoreFromFile then repeats the restore checks and transactional commit.
func RestoreFiles(statePath, backupPath string, key []byte, now time.Time) error {
	statePath = strings.TrimSpace(statePath)
	backupPath = strings.TrimSpace(backupPath)
	if statePath == "" {
		return errors.New("state path is required")
	}
	if backupPath == "" {
		return errors.New("backup path is required")
	}

	// Verify the backup first so an invalid/wrong-key backup cannot cause even
	// a legacy-state migration or audit-file creation.
	_, header, err := readBackupFile(backupPath, key)
	if err != nil {
		return fmt.Errorf("verify backup: %w", err)
	}

	release, err := LockState(statePath)
	if err != nil {
		return fmt.Errorf("stop BCC before restoring from files: %w", err)
	}
	defer release()

	for _, pending := range []string{statePath + "-journal", restoreJournalPath(statePath)} {
		if _, err := os.Stat(pending); err == nil {
			return fmt.Errorf("%s exists: an unfinished write or restore is pending; start BCC once so it recovers, stop it, then retry", pending)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	stateBytes, err := os.ReadFile(statePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(bytes.TrimSpace(stateBytes)) > 0 && !isSQLiteFile(stateBytes) {
		return errors.New("current state is a legacy JSON file; start BCC once to migrate it, stop BCC, then retry")
	}

	auditBytes, _, err := readMaybe(statePath + ".audit.jsonl")
	if err != nil {
		return err
	}
	currentAudit, err := parseAuditBytes(auditBytes)
	if err != nil {
		return fmt.Errorf("current audit verification failed: %w", err)
	}
	if err := verifyAuditEntries(currentAudit); err != nil {
		return fmt.Errorf("current audit verification failed: %w", err)
	}
	if !auditContainsAnchor(currentAudit, header.AuditSequence, header.AuditHash) {
		return errors.New("restore refused: current audit does not extend backup anchor")
	}

	store, err := OpenStore(statePath)
	if err != nil {
		return err
	}
	audit, err := OpenAuditLog(statePath + ".audit.jsonl")
	if err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	app := &Server{
		store:        store,
		audit:        audit,
		activeAlerts: store.ActiveAlertsSnapshot(),
		alertConfig: AlertConfig{
			TelemetryStaleAfter:              3 * time.Minute,
			HandshakeErrorRateMilliPerMin: 5000,
			Interval:                         15 * time.Second,
		},
		now: func() time.Time { return now },
	}
	return app.RestoreFromFile(backupPath, key)
}
