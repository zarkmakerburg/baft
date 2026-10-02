package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/zarkmakerburg/baft/internal/bcc"
)

// runRestorePreview: baft-bcc restore-preview --backup FILE [--state-file F] [--json]
//
// Read-only. It decrypts and verifies the backup (key from BAFT_BCC_BACKUP_KEY),
// then shows what restoring it would do to the BCC state in --state-file,
// working on copies, so it is safe next to a running BCC.
func runRestorePreview(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("restore-preview", flag.ContinueOnError)
	fs.SetOutput(stderr)
	backup := fs.String("backup", "", "encrypted backup file (.baftbak)")
	stateFile := fs.String("state-file", "./bcc-state.json", "the BCC state file the restore would replace")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *backup == "" {
		fmt.Fprintln(stderr, "usage: baft-bcc restore-preview --backup <file.baftbak> [--state-file bcc-state.json] [--json]   (key: BAFT_BCC_BACKUP_KEY)")
		return 2
	}
	key, err := bcc.BackupKeyFromEnv()
	if err != nil {
		fmt.Fprintln(stderr, "backup key:", err)
		return 2
	}
	p, err := bcc.PreviewRestoreFiles(*stateFile, *backup, key, time.Now())
	if err != nil {
		fmt.Fprintln(stderr, "restore-preview:", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(p)
	} else {
		printRestorePreview(stdout, p)
	}
	if !p.Verified || p.WouldRefuse != "" {
		return 1
	}
	return 0
}

func printRestorePreview(w io.Writer, p bcc.RestorePreview) {
	if !p.Verified {
		fmt.Fprintf(w, "BACKUP NOT VERIFIED: %s\nNothing can be said about restoring it.\n", p.Problem)
		return
	}
	fmt.Fprintf(w, "Backup verified (decrypted, authenticated, checksum and audit chain ok), created %s (%s ago).\n", p.CreatedAt.UTC().Format(time.RFC3339), p.Age)
	if p.WouldRefuse != "" {
		fmt.Fprintf(w, "\nA restore would be REFUSED: %s\n", p.WouldRefuse)
	}
	line := func(name string, d bcc.RestoreDiff) {
		fmt.Fprintf(w, "  %-8s %d now -> %d after restore", name, d.Current, d.After)
		if len(d.Added)+len(d.Removed)+len(d.Changed) > 0 {
			fmt.Fprintf(w, "  (+%d, -%d, changed %d)", len(d.Added), len(d.Removed), len(d.Changed))
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "\nWhat a restore would leave:")
	line("nodes", p.Nodes)
	line("tunnels", p.Tunnels)
	line("jobs", p.Jobs)
	fmt.Fprintf(w, "  audit    %d entries now, %d in the backup; the restore appends one entry\n", p.AuditCurrent, p.AuditBackup)
	list := func(label string, ids []string) {
		if len(ids) > 0 {
			fmt.Fprintf(w, "  %s: %s\n", label, strings.Join(ids, ", "))
		}
	}
	fmt.Fprintln(w, "\nDetail:")
	list("would disappear (nodes)", p.Nodes.Removed)
	list("would reappear (nodes)", p.Nodes.Added)
	list("would disappear (tunnels)", p.Tunnels.Removed)
	list("would reappear (tunnels)", p.Tunnels.Added)
	list("kept from now, not rolled back (token/revocation)", p.KeptSecurity)
	list("kept from now, not rolled back (telemetry)", p.KeptTelemetry)
	if len(p.Warnings) > 0 {
		fmt.Fprintln(w, "\nWarnings:")
		for _, x := range p.Warnings {
			fmt.Fprintln(w, "  -", x)
		}
	}
	fmt.Fprintln(w, "\nThis was a preview: nothing was changed.")
}
