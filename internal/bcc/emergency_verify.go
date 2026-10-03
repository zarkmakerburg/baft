package bcc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// EmergencyBackupReport is what VerifyEmergencyBackup proved about an
// emergency backup that `baft uninstall` took before deleting BCC state (a
// verified copy of BCC's own files, taken while BCC was stopped).
type EmergencyBackupReport struct {
	Verified     bool   `json:"verified"`
	Problem      string `json:"problem,omitempty"`
	Files        int    `json:"files"`
	Nodes        int    `json:"nodes"`
	Tunnels      int    `json:"tunnels"`
	Jobs         int    `json:"jobs"`
	AuditEntries uint64 `json:"audit_entries"`
	AuditHash    string `json:"audit_hash,omitempty"`
	Summary      string `json:"summary,omitempty"`
}

type emergencyManifest struct {
	Kind      string `json:"kind"`
	StateFile string `json:"state_file"`
	Files     []struct {
		Name   string `json:"name"`
		Source string `json:"source"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// VerifyEmergencyBackup checks every file against the manifest, loads a
// private copy of the state database with BCC's own reader (schema and every
// collection), and verifies the audit log's hash chain. It never writes into
// the backup directory.
func VerifyEmergencyBackup(dir string) EmergencyBackupReport {
	r, err := verifyEmergencyBackup(dir)
	if err != nil {
		return EmergencyBackupReport{Problem: err.Error()}
	}
	return r
}

func verifyEmergencyBackup(dir string) (EmergencyBackupReport, error) {
	var r EmergencyBackupReport
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return r, fmt.Errorf("manifest: %w", err)
	}
	var m emergencyManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return r, fmt.Errorf("manifest: %w", err)
	}
	if m.Kind != "baft-bcc-emergency-backup" || m.StateFile == "" || len(m.Files) == 0 {
		return r, errors.New("manifest: not a BCC emergency backup")
	}
	var statePath, auditPath string
	for _, f := range m.Files {
		if f.Name == "" || strings.ContainsAny(f.Name, "/\\") || f.Name == "." || f.Name == ".." {
			return r, fmt.Errorf("manifest: bad file name %q", f.Name)
		}
		p := filepath.Join(dir, f.Name)
		sum, size, err := fileSHA256(p)
		if err != nil {
			return r, fmt.Errorf("%s: %w", f.Name, err)
		}
		if sum != f.SHA256 || size != f.Size {
			return r, fmt.Errorf("%s does not match the manifest", f.Name)
		}
		switch f.Source {
		case m.StateFile:
			statePath = p
		case m.StateFile + ".audit.jsonl":
			auditPath = p
		}
		r.Files++
	}
	if statePath == "" {
		return r, errors.New("the state database is not in the backup")
	}
	// BCC's reader opens read-write (and may migrate); give it a private copy.
	tmp, err := os.MkdirTemp("", "baft-bcc-verify-")
	if err != nil {
		return r, err
	}
	defer os.RemoveAll(tmp)
	b, err := os.ReadFile(statePath)
	if err != nil {
		return r, err
	}
	cp := filepath.Join(tmp, "state")
	if err := os.WriteFile(cp, b, 0o600); err != nil {
		return r, err
	}
	var st state
	if isSQLiteFile(b) {
		st, err = readStateDB(cp)
		if err != nil {
			return r, fmt.Errorf("state database: %w", err)
		}
	} else if err := json.Unmarshal(b, &st); err != nil {
		return r, fmt.Errorf("state (JSON): %w", err)
	}
	r.Nodes, r.Tunnels, r.Jobs = len(st.Nodes), len(st.Tunnels), len(st.Jobs)
	if auditPath != "" {
		a, err := OpenAuditLog(auditPath) // reads and verifies the chain; writes nothing
		if err != nil {
			return r, fmt.Errorf("audit log: %w", err)
		}
		r.AuditEntries, r.AuditHash = a.nextSeq-1, a.lastHash
	}
	r.Verified = true
	r.Summary = fmt.Sprintf("%d file(s); state: %d node(s), %d tunnel(s), %d job(s); audit chain: %d entr(ies) verified", r.Files, r.Nodes, r.Tunnels, r.Jobs, r.AuditEntries)
	return r, nil
}
