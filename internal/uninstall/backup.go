package uninstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// BackupResult is the verified emergency backup of BCC state.
type BackupResult struct {
	Planned    bool         `json:"planned"`
	StateFile  string       `json:"state_file"`
	PlanFiles  []string     `json:"plan_files,omitempty"`
	Dir        string       `json:"dir,omitempty"`
	Files      []BackupFile `json:"files,omitempty"`
	VerifiedBy string       `json:"verified_by,omitempty"` // "sha256" or "sha256+baft-bcc"
	Summary    string       `json:"summary,omitempty"`     // what baft-bcc verified
}

type BackupFile struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// emergencyBackup copies BCC's files while holding BCC's state lock (so BCC
// is provably not running and state and audit are one snapshot), verifies
// every copy byte for byte, writes a manifest, and, when the baft-bcc binary
// is there, has it open the copied database and verify the audit chain.
func (e *Env) emergencyBackup(ctx context.Context, bp *BackupPlan) (*BackupResult, error) {
	lockPath := bp.StateFile + ".lock"
	createdLock := !exists(lockPath)
	release, err := lockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("BCC is still running or another tool holds %s (%v); stop it first", lockPath, err)
	}
	defer func() {
		release()
		if createdLock {
			os.Remove(lockPath)
		}
	}()
	for _, x := range []string{"-journal", ".restore-journal.json"} {
		if exists(bp.StateFile + x) {
			return nil, fmt.Errorf("BCC has an unfinished write or restore (%s); start BCC once so it recovers, stop it, then retry", bp.StateFile+x)
		}
	}
	if err := os.MkdirAll(bp.Dir, 0o700); err != nil {
		return nil, err
	}
	dir := filepath.Join(bp.Dir, "bcc-emergency-"+e.now().UTC().Format("20060102T150405Z"))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	res := &BackupResult{Planned: true, StateFile: bp.StateFile, PlanFiles: bp.Files, Dir: dir, VerifiedBy: "sha256"}
	used := map[string]bool{}
	for _, src := range bp.Files {
		name := filepath.Base(src)
		if used[name] {
			name = fmt.Sprintf("%d-%s", len(used), name)
		}
		used[name] = true
		sum, size, err := copyVerified(src, filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		res.Files = append(res.Files, BackupFile{Name: name, Source: src, Size: size, SHA256: sum})
	}
	m, err := json.MarshalIndent(struct {
		Kind      string       `json:"kind"`
		StateFile string       `json:"state_file"`
		Files     []BackupFile `json:"files"`
		Restore   string       `json:"restore"`
	}{"baft-bcc-emergency-backup", bp.StateFile, res.Files,
		"stop BCC, copy each file back to its source path (mode 0600), start BCC"}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeSynced(filepath.Join(dir, "manifest.json"), append(m, '\n')); err != nil {
		return nil, err
	}
	if err := syncDir(dir); err != nil {
		return nil, err
	}
	// Semantic check by BCC itself, when its binary is here and is BAFT's.
	if own, _ := e.binaryOwnership(e.BCCBinary, "baft-bcc"); own == Managed && e.System != nil {
		out, err := e.System.Run(ctx, e.BCCBinary, "verify-backup", "--dir", dir, "--json")
		if err != nil {
			return nil, fmt.Errorf("baft-bcc verify-backup: %v: %s", err, clip(strings.TrimSpace(out)))
		}
		var v struct {
			Verified bool   `json:"verified"`
			Summary  string `json:"summary"`
		}
		if json.Unmarshal([]byte(out), &v) != nil || !v.Verified {
			return nil, fmt.Errorf("baft-bcc did not verify the emergency backup: %s", clip(strings.TrimSpace(out)))
		}
		res.VerifiedBy, res.Summary = "sha256+baft-bcc", v.Summary
	}
	return res, nil
}

func copyVerified(src, dst string) (string, int64, error) {
	in, err := openNoFollow(src)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return "", 0, fmt.Errorf("%s is not a regular file", src)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", 0, err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return "", 0, err
	}
	if err := out.Close(); err != nil {
		return "", 0, err
	}
	a, size, pa := hashRegular(src)
	b, _, pb := hashRegular(dst)
	if pa != "" || pb != "" || a != b {
		return "", 0, errors.New("the copy of " + src + " does not match the original")
	}
	return a, size, nil
}

func writeSynced(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
