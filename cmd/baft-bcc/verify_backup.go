package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/zarkmakerburg/baft/internal/bcc"
)

// runVerifyBackup: baft-bcc verify-backup --dir DIR [--json]
//
// Read-only. Verifies an emergency backup `baft uninstall` took before it
// deleted BCC state: every file against its manifest, the state database
// opened by BCC's own reader (on a private copy) and the audit hash chain.
func runVerifyBackup(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify-backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "emergency backup directory (bcc-emergency-<time>)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *dir == "" {
		fmt.Fprintln(stderr, "usage: baft-bcc verify-backup --dir <bcc-emergency-dir> [--json]")
		return 2
	}
	r := bcc.VerifyEmergencyBackup(*dir)
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(r)
	} else if r.Verified {
		fmt.Fprintln(stdout, "emergency backup verified:", r.Summary)
	} else {
		fmt.Fprintln(stdout, "EMERGENCY BACKUP NOT VERIFIED:", r.Problem)
	}
	if !r.Verified {
		return 1
	}
	return 0
}
