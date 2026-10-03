package uninstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Journal is the durable record of one uninstall run. It is written before
// and after every change, so an interrupted run can be restored or resumed.
// It holds paths, digests and states only, never file contents.
type Journal struct {
	Version  int           `json:"version"`
	ID       string        `json:"id"`
	Status   string        `json:"status"` // applying, committing, committed, restored
	Started  time.Time     `json:"started"`
	Updated  time.Time     `json:"updated"`
	Scope    []string      `json:"scope"`
	Delete   []Class       `json:"delete"`
	Services []JService    `json:"services"`
	Files    []JFile       `json:"files"`
	Dirs     []string      `json:"dirs"`         // to remove at commit if empty
	Removed  []string      `json:"removed_dirs"` // removed at commit
	Kept     []JKept       `json:"kept"`         // must be unchanged at verification
	Running  []string      `json:"running"`      // units not removed that were running and must still be
	Backup   *BackupResult `json:"bcc_backup,omitempty"`
	// BackupSkipped records that the owner chose --no-backup.
	BackupSkipped bool   `json:"bcc_backup_skipped,omitempty"`
	Error         string `json:"error,omitempty"`

	dir      string   // the run directory
	progress *os.File // progress.log, appended as each step happens
}

type JService struct {
	Unit       string `json:"unit"`
	Kind       string `json:"kind"`
	WasActive  bool   `json:"was_active"`
	WasEnabled bool   `json:"was_enabled"`
	Stopped    bool   `json:"stopped"`
	Disabled   bool   `json:"disabled"`
}

type JFile struct {
	Path       string `json:"path"`
	Class      Class  `json:"class"`
	Dir        bool   `json:"dir,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Quarantine string `json:"quarantine"` // relative to the run directory
	State      string `json:"state"`      // pending, moving, moved, restored
}

type JKept struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Dir    bool   `json:"dir,omitempty"`
}

const (
	stApplying   = "applying"
	stCommitting = "committing"
	stCommitted  = "committed"
	stRestored   = "restored"
)

func (j *Journal) path() string { return filepath.Join(j.dir, "journal.json") }

// Dir is the run directory (journal and quarantine).
func (j *Journal) Dir() string { return j.dir }

func (j *Journal) save(now time.Time) error {
	j.Updated = now.UTC()
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp := j.path() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, j.path()); err != nil {
		return err
	}
	return syncDir(j.dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Per-step progress is appended to progress.log (one short fsynced line per
// change: "F <file> <state>" or "S <service> stopped|disabled|reset"), so a
// run touching many files costs O(n), not a rewrite of the whole journal per
// file. journal.json is rewritten only when the run's status changes; on
// reading, the log is replayed over it (replaying is idempotent).
func (j *Journal) appendProgress(line string) error {
	if j.progress == nil {
		f, err := os.OpenFile(filepath.Join(j.dir, "progress.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		j.progress = f
	}
	if _, err := j.progress.WriteString(line + "\n"); err != nil {
		return err
	}
	return j.progress.Sync()
}

func (j *Journal) markFile(i int, state string) error {
	j.Files[i].State = state
	return j.appendProgress(fmt.Sprintf("F %d %s", i, state))
}

func (j *Journal) markService(i int, what string) error {
	s := &j.Services[i]
	switch what {
	case "stopped":
		s.Stopped = true
	case "disabled":
		s.Disabled = true
	case "reset":
		s.Stopped, s.Disabled = false, false
	}
	return j.appendProgress(fmt.Sprintf("S %d %s", i, what))
}

const maxJournalBytes = 1 << 30

var validFileState = map[string]bool{"pending": true, "moving": true, "moved": true, "restored": true}

func readJournal(dir string) (*Journal, error) {
	b, problem := readRegular(filepath.Join(dir, "journal.json"), maxJournalBytes)
	if problem != "" {
		return nil, errors.New("journal " + problem)
	}
	var j Journal
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}
	j.dir = dir
	if pb, problem := readRegular(filepath.Join(dir, "progress.log"), maxJournalBytes); problem == "" {
		lines := strings.Split(string(pb), "\n")
		for n, line := range lines {
			if line == "" {
				continue
			}
			f := strings.Fields(line)
			idx := -1
			if len(f) == 3 {
				fmt.Sscanf(f[1], "%d", &idx)
			}
			switch {
			case len(f) == 3 && f[0] == "F" && idx >= 0 && idx < len(j.Files) && validFileState[f[2]]:
				j.Files[idx].State = f[2]
			case len(f) == 3 && f[0] == "S" && idx >= 0 && idx < len(j.Services) && (f[2] == "stopped" || f[2] == "disabled" || f[2] == "reset"):
				switch f[2] {
				case "stopped":
					j.Services[idx].Stopped = true
				case "disabled":
					j.Services[idx].Disabled = true
				case "reset":
					j.Services[idx].Stopped, j.Services[idx].Disabled = false, false
				}
			case n == len(lines)-1:
				// a torn last line from a crash mid-write: ignore it
			default:
				return nil, fmt.Errorf("progress.log line %d is not valid", n+1)
			}
		}
	} else if exists(filepath.Join(dir, "progress.log")) {
		return nil, errors.New("progress.log " + problem)
	}
	return &j, nil
}

// pendingJournal returns the run that did not finish (applying or
// committing), if any.
func (e *Env) pendingJournal() (*Journal, error) {
	ents, err := os.ReadDir(e.JournalDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, ent := range ents {
		if ent.IsDir() && strings.HasPrefix(ent.Name(), "run-") {
			names = append(names, ent.Name())
		}
	}
	sort.Strings(names)
	for i := len(names) - 1; i >= 0; i-- {
		j, err := readJournal(filepath.Join(e.JournalDir, names[i]))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", names[i], err)
		}
		if j.Status == stApplying || j.Status == stCommitting {
			return j, nil
		}
	}
	return nil, nil
}

// LastJournal returns the most recent run (for reports).
func (e *Env) LastJournal() (*Journal, error) {
	e.defaults()
	ents, err := os.ReadDir(e.JournalDir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, ent := range ents {
		if ent.IsDir() && strings.HasPrefix(ent.Name(), "run-") {
			names = append(names, ent.Name())
		}
	}
	if len(names) == 0 {
		return nil, os.ErrNotExist
	}
	sort.Strings(names)
	return readJournal(filepath.Join(e.JournalDir, names[len(names)-1]))
}

// Pending returns an unfinished run, if any.
func (e *Env) Pending() (*Journal, error) {
	e.defaults()
	return e.pendingJournal()
}
