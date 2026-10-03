package uninstall

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// JSON is the machine-readable plan (baft uninstall --preview --json).
func (p *Plan) JSON() ([]byte, error) {
	data := map[Class]string{}
	for _, c := range DataClasses {
		data[c] = "keep"
	}
	for _, c := range p.Delete {
		data[c] = "delete"
	}
	return json.MarshalIndent(struct {
		*Plan
		Data    map[Class]string `json:"data"`
		Default string           `json:"default"`
	}{p, data, "KEEP DATA"}, "", "  ")
}

func describeState(active, enabled string) string {
	var s []string
	if active != "" {
		s = append(s, active)
	}
	if enabled != "" {
		s = append(s, enabled)
	}
	return strings.Join(s, ", ")
}

func classLabel(c Class) string {
	switch c {
	case ClassTunnelConfigs:
		return "tunnel config"
	case ClassCertificates:
		return "certificates"
	case ClassBCCState:
		return "BCC state"
	case ClassAudit:
		return "audit history"
	case ClassBackups:
		return "backup"
	case ClassInstall:
		return "installer files"
	case "":
		return "file"
	}
	return string(c)
}

// Render prints the plan the way HQ specified it: what will stop, what will
// be removed, what is kept and why, and what is never touched.
func (p *Plan) Render(w io.Writer) {
	fmt.Fprintf(w, "BAFT Uninstall Preview\n")
	fmt.Fprintf(w, "Scope: %s\n", strings.Join(p.Scope, " + "))
	if p.Pending != "" {
		fmt.Fprintf(w, "\nUNFINISHED RUN: %s (run --resume or --restore first)\n", p.Pending)
	}
	width := 20
	for _, l := range [][]*Artifact{p.Remove, p.Keep, p.Untouched, p.Hold} {
		for _, a := range l {
			if n := len(a.Path); n > width && n <= 60 {
				width = n
			}
		}
	}
	line := func(path, desc string) { fmt.Fprintf(w, "  %-*s  %s\n", width, path, desc) }

	fmt.Fprintf(w, "\nWill stop:\n")
	if len(p.Stop) == 0 {
		fmt.Fprintf(w, "  nothing\n")
	}
	for _, s := range p.Stop {
		line(s.Unit, describeState(s.ActiveState, s.EnabledState))
	}

	fmt.Fprintf(w, "\nWill remove:\n")
	if len(p.Remove) == 0 {
		fmt.Fprintf(w, "  nothing\n")
	}
	for _, a := range p.Remove {
		line(a.Path, classLabel(a.Class)+" — "+a.Evidence)
	}

	fmt.Fprintf(w, "\nWill KEEP:\n")
	if len(p.Keep) == 0 {
		fmt.Fprintf(w, "  nothing\n")
	}
	for _, a := range p.Keep {
		line(a.Path, classLabel(a.Class)+" — "+a.Reason)
	}

	if len(p.Hold) > 0 {
		fmt.Fprintf(w, "\nHeld for manual review (never removed):\n")
		for _, a := range p.Hold {
			line(a.Path, string(a.Ownership)+" — "+a.Evidence)
		}
	}
	if len(p.Untouched) > 0 {
		fmt.Fprintf(w, "\nNever touched (not proven BAFT-owned):\n")
		for _, a := range p.Untouched {
			line(a.Path, string(a.Ownership)+" — "+a.Evidence)
		}
	}

	if len(p.ActiveTunnels) > 0 {
		fmt.Fprintf(w, "\nWARNING: %d active tunnel(s) would be stopped:\n", len(p.ActiveTunnels))
		for _, t := range p.ActiveTunnels {
			id := ""
			if t.TunnelID != "" {
				id = " (tunnel " + t.TunnelID + ")"
			}
			line(t.Unit+id, t.Impact())
		}
		fmt.Fprintf(w, "  Their configuration is preserved unless --delete-tunnel-configs is chosen.\n")
	}

	if p.Backup != nil {
		fmt.Fprintf(w, "\nBCC emergency backup (before BCC state is deleted):\n  %d file(s) copied and verified into %s/bcc-emergency-<time>/\n", len(p.Backup.Files), p.Backup.Dir)
	} else if p.NoBackup {
		fmt.Fprintf(w, "\nBCC emergency backup: SKIPPED by the owner (--no-backup)\n")
	}

	fmt.Fprintf(w, "\nData:\n")
	deletable := map[Class]bool{}
	for _, c := range p.Deletable {
		deletable[c] = true
	}
	chosen := map[Class]bool{}
	for _, c := range p.Delete {
		chosen[c] = true
	}
	for _, c := range DataClasses {
		ans := "NO"
		if chosen[c] {
			ans = "YES"
		}
		note := c.Flag()
		if !deletable[c] {
			note = "nothing of it in this scope"
		}
		fmt.Fprintf(w, "  %-26s %-4s (%s)\n", c.Question(), ans, note)
	}
	fmt.Fprintf(w, "\nDefault behavior: KEEP DATA\n")

	for _, e := range p.Errors {
		fmt.Fprintf(w, "\nnote: %s\n", e)
	}
	if len(p.Blocked) > 0 {
		fmt.Fprintf(w, "\nBLOCKED (nothing will be changed):\n")
		for _, b := range p.Blocked {
			fmt.Fprintf(w, "  - %s\n", b)
		}
	}
}
