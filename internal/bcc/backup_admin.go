package bcc

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var errBackupAdminDisabled = errors.New("backup administration is disabled")

// BackupAdminEntry is the non-secret inventory exposed to BCC administrators.
// Only regular .baftbak files from the configured backup directory are listed.
type BackupAdminEntry struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
}

// ConfigureBackupAdmin binds the admin preview API to the same directory and
// key used by the scheduled backup loop. The key is copied and never exposed.
func (s *Server) ConfigureBackupAdmin(dir string, key []byte) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errors.New("backup directory is required")
	}
	if _, err := newAEAD(key); err != nil {
		return err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("backup directory: %w", err)
	}
	s.backupMu.Lock()
	s.backupDir = abs
	s.backupKey = append([]byte(nil), key...)
	s.backupMu.Unlock()
	return nil
}

func (s *Server) backupAdminConfig() (string, []byte, error) {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	if s.backupDir == "" || len(s.backupKey) != 32 {
		return "", nil, errBackupAdminDisabled
	}
	return s.backupDir, append([]byte(nil), s.backupKey...), nil
}

func (s *Server) listConfiguredBackups() ([]BackupAdminEntry, error) {
	dir, _, err := s.backupAdminConfig()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []BackupAdminEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]BackupAdminEntry, 0, len(entries))
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(e.Name(), ".baftbak") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, BackupAdminEntry{Name: e.Name(), Size: info.Size(), ModifiedAt: info.ModTime().UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

func (s *Server) configuredBackupTarget(name string) (string, []byte, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, errors.New("backup filename is required")
	}
	if filepath.Base(name) != name || strings.ContainsAny(name, `/\`) || !strings.HasSuffix(name, ".baftbak") {
		return "", nil, errors.New("backup filename must name one .baftbak file in the configured backup directory")
	}
	dir, key, err := s.backupAdminConfig()
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, os.ErrNotExist
	}
	if err != nil {
		return "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", nil, errors.New("backup target must be a regular file, not a symlink")
	}
	return path, key, nil
}

func backupAdminHTTPStatus(err error) int {
	switch {
	case errors.Is(err, errBackupAdminDisabled):
		return http.StatusServiceUnavailable
	case errors.Is(err, os.ErrNotExist):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

func (s *Server) backups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	entries, err := s.listConfiguredBackups()
	if err != nil {
		http.Error(w, err.Error(), backupAdminHTTPStatus(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": entries})
}

func (s *Server) restorePreviewAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	var in struct {
		Filename string `json:"filename"`
	}
	if err := decodeJSON(r, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	path, key, err := s.configuredBackupTarget(in.Filename)
	if err != nil {
		s.auditFailure(w, r, "backup.restore.preview", strings.TrimSpace(in.Filename), nil, err, backupAdminHTTPStatus(err))
		return
	}
	preview, err := s.PreviewRestore(path, key)
	if err != nil {
		s.auditFailure(w, r, "backup.restore.preview", filepath.Base(path), nil, err, http.StatusInternalServerError)
		return
	}
	outcome := "success"
	if !preview.Verified || preview.WouldRefuse != "" {
		outcome = "refused"
	}
	if err := s.auditAdmin(r, "backup.restore.preview", filepath.Base(path), outcome, map[string]any{
		"verified":     preview.Verified,
		"would_refuse": preview.WouldRefuse != "",
		"warnings":     len(preview.Warnings),
	}); err != nil {
		http.Error(w, "audit log failure", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}
