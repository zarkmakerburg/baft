package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/zarkmakerburg/baft/internal/bcc"
)

type localRestoreResult struct {
	Restored             bool               `json:"restored"`
	ConfirmationRequired bool               `json:"confirmation_required,omitempty"`
	Preview              bcc.RestorePreview `json:"preview"`
}

// runRestore: baft-bcc restore --backup FILE [--state-file F] [--yes] [--json]
//
// This is intentionally local/offline only. The state-file lock must prove
// that BCC is stopped. A verified restore preview is mandatory before commit,
// and --yes is an explicit second gate. RestoreFiles repeats all safety checks
// at commit time, so the preview is not treated as authority.
func runRestore(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	backup := fs.String("backup", "", "encrypted backup file (.baftbak)")
	stateFile := fs.String("state-file", "./bcc-state.json", "BCC state file to restore")
	yes := fs.Bool("yes", false, "commit the restore after a verified preflight")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *backup == "" {
		fmt.Fprintln(stderr, "usage: baft-bcc restore --backup <file.baftbak> [--state-file bcc-state.json] [--yes] [--json]   (key: BAFT_BCC_BACKUP_KEY)")
		return 2
	}
	key, err := bcc.BackupKeyFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, "backup key:", err)
		return 2
	}

	preview, err := bcc.PreviewRestoreFiles(*stateFile, *backup, key, time.Now())
	if err != nil {
		fmt.Fprintln(stderr, "restore preflight:", err)
		return 1
	}
	if !preview.Verified || preview.WouldRefuse != "" {
		if *asJSON {
			_ = json.NewEncoder(stdout).Encode(localRestoreResult{Preview: preview})
		} else {
			printRestorePreview(stdout, preview)
		}
		return 1
	}

	if !*yes {
		if *asJSON {
			_ = json.NewEncoder(stdout).Encode(localRestoreResult{
				ConfirmationRequired: true,
				Preview:              preview,
			})
		} else {
			printRestorePreview(stdout, preview)
			fmt.Fprintln(stdout, "\nRESTORE NOT RUN. Re-run the same command with --yes to commit this restore after a fresh preflight.")
		}
		return 4
	}

	// RestoreFiles reacquires the process lock and repeats backup/audit/anchor
	// checks before entering the already fault-tested transactional restore.
	if err := bcc.RestoreFiles(*stateFile, *backup, key, time.Now()); err != nil {
		fmt.Fprintln(stderr, "restore:", err)
		return 1
	}

	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(localRestoreResult{Restored: true, Preview: preview})
	} else {
		fmt.Fprintf(stdout, "Restore committed from %s. Backup was verified and the current audit/anchor checks passed again at commit time.\n", *backup)
	}
	return 0
}
