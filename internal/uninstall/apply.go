package uninstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Errors the caller maps to exit codes.
var (
	ErrBlocked      = errors.New("blocked")
	ErrConfirmation = errors.New("confirmation required")
	ErrPending      = errors.New("an earlier uninstall did not finish")
)

// Result is what a finished run did.
type Result struct {
	Journal     string        `json:"journal"`
	Stopped     []string      `json:"stopped"`
	Removed     []string      `json:"removed"`
	RemovedDirs []string      `json:"removed_dirs"`
	Kept        int           `json:"kept"`
	Backup      *BackupResult `json:"bcc_backup,omitempty"`
	Status      string        `json:"status"`
}

// Test hooks: BAFT_UNINSTALL_FAIL_AT makes the run fail at a point (and roll
// back); BAFT_UNINSTALL_CRASH_AT exits on the spot like a kill -9 or a power
// loss, leaving the journal for --restore or --resume.
var crash = func() { os.Exit(86) }

func failPoint(p string) error {
	if os.Getenv("BAFT_UNINSTALL_FAIL_AT") == p {
		return fmt.Errorf("injected failure at %s (test hook)", p)
	}
	if os.Getenv("BAFT_UNINSTALL_CRASH_AT") == p {
		crash()
	}
	return nil
}

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Env) logf(format string, a ...any) {
	if e.Log != nil {
		fmt.Fprintf(e.Log, format+"\n", a...)
	}
}

// Apply carries out a plan: BACKUP IF REQUIRED -> STOP SERVICES -> REMOVE
// (into a journaled quarantine) -> VERIFY -> COMMIT (purge). Any failure
// before the commit restores everything it changed.
func (e *Env) Apply(ctx context.Context, p *Plan) (*Result, error) {
	e.defaults()
	if e.RequireRoot && os.Geteuid() != 0 {
		return nil, errors.New("uninstall must run as root")
	}
	if p.Pending != "" {
		return nil, ErrPending
	}
	if len(p.Blocked) > 0 {
		return nil, ErrBlocked
	}
	if len(p.Needs) > 0 {
		return nil, ErrConfirmation
	}
	if !p.HasWork() {
		return &Result{Status: "nothing to do", Stopped: []string{}, Removed: []string{}, RemovedDirs: []string{}}, nil
	}
	release, err := e.lockRuns()
	if err != nil {
		return nil, err
	}
	defer release()
	if pj, err := e.pendingJournal(); err != nil || pj != nil {
		return nil, ErrPending
	}
	j, err := e.newRun(p)
	if err != nil {
		return nil, err
	}
	res, err := e.forward(ctx, j, p)
	if err != nil {
		j.Error = err.Error()
		if rerr := e.restore(ctx, j); rerr != nil {
			return nil, fmt.Errorf("%v; restoring also failed: %v (journal: %s)", err, rerr, j.Dir())
		}
		return nil, fmt.Errorf("%v; everything was restored (journal: %s)", err, j.Dir())
	}
	return res, nil
}

// lockRuns makes uninstall runs on one host strictly one at a time.
func (e *Env) lockRuns() (func(), error) {
	if err := os.MkdirAll(e.JournalDir, 0o700); err != nil {
		return nil, err
	}
	release, err := lockFile(filepath.Join(e.JournalDir, ".lock"))
	if err != nil {
		return nil, fmt.Errorf("another baft uninstall is running (%v)", err)
	}
	return release, nil
}

func (e *Env) newRun(p *Plan) (*Journal, error) {
	if err := os.MkdirAll(e.JournalDir, 0o700); err != nil {
		return nil, err
	}
	now := e.now().UTC()
	var id, dir string
	for n := 0; ; n++ {
		id = fmt.Sprintf("run-%s-%d-%d", now.Format("20060102T150405.000000000Z"), os.Getpid(), n)
		dir = filepath.Join(e.JournalDir, id)
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			break
		}
		if !os.IsExist(err) || n > 100 {
			return nil, err
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "quarantine"), 0o700); err != nil {
		return nil, err
	}
	j := &Journal{Version: 1, ID: id, Status: stApplying, Started: now, Scope: p.Scope, Delete: p.Delete, dir: dir,
		Services: []JService{}, Files: []JFile{}, Dirs: p.Dirs, Removed: []string{}, Kept: []JKept{}, Running: []string{}, BackupSkipped: p.NoBackup}
	// BCC first when its state is backed up: the backup needs it stopped.
	var svcs []Service
	for _, s := range p.Stop {
		if s.Kind == "bcc" {
			svcs = append(svcs, s)
		}
	}
	for _, s := range p.Stop {
		if s.Kind != "bcc" {
			svcs = append(svcs, s)
		}
	}
	for _, s := range svcs {
		j.Services = append(j.Services, JService{Unit: s.Unit, Kind: s.Kind, WasActive: isRunning(s.ActiveState), WasEnabled: strings.HasPrefix(s.EnabledState, "enabled")})
	}
	for i, a := range p.Remove {
		j.Files = append(j.Files, JFile{Path: a.Path, Class: a.Class, Dir: a.Dir, SHA256: a.SHA256,
			Quarantine: filepath.Join("quarantine", fmt.Sprintf("%03d-%s", i, filepath.Base(a.Path))), State: "pending"})
	}
	// What must not change: every unit that stays, and static data that stays.
	for _, l := range [][]*Artifact{p.Keep, p.Untouched, p.Hold} {
		for _, a := range l {
			if a.SHA256 == "" {
				continue
			}
			switch a.Class {
			case ClassUnit, ClassBinary, ClassCertificates, ClassTunnelConfigs, ClassBackups, ClassInstall:
				if a.owner != nil && a.owner.active() && !p.removedU[a.owner.Name] && a.Class != ClassUnit && a.Class != ClassBinary {
					continue // a service that keeps running may change its own files
				}
				j.Kept = append(j.Kept, JKept{Path: a.Path, SHA256: a.SHA256, Dir: a.Dir})
			}
		}
	}
	for _, u := range p.units {
		if !p.removedU[u.Name] && u.ActiveState == "active" {
			j.Running = append(j.Running, u.Name)
		}
	}
	if p.Backup != nil {
		j.Backup = &BackupResult{Planned: true, StateFile: p.Backup.StateFile, PlanFiles: p.Backup.Files}
	}
	if err := j.save(e.now()); err != nil {
		return nil, err
	}
	if err := writeSynced(filepath.Join(dir, "README.txt"), []byte(recoveryNote(j))); err != nil {
		return nil, err
	}
	e.logf("journal: %s", dir)
	return j, nil
}

// recoveryNote tells an operator what to do if this run is interrupted, even
// when the baft binary itself was already moved into the quarantine.
func recoveryNote(j *Journal) string {
	var b strings.Builder
	b.WriteString("BAFT uninstall run " + j.ID + "\n\n")
	b.WriteString("If this run was interrupted, undo it with:   baft uninstall --restore\n")
	b.WriteString("or finish it with:                          baft uninstall --resume\n")
	for _, f := range j.Files {
		if f.Class == ClassBinary && filepath.Base(f.Path) == "baft" {
			b.WriteString("\nIf " + f.Path + " is already gone, run the copy kept here:\n  " +
				filepath.Join(j.dir, f.Quarantine) + " uninstall --restore\n")
		}
	}
	b.WriteString("\njournal.json lists every file (path, digest, state) and service touched.\n")
	return b.String()
}

func isRunning(state string) bool {
	return state == "active" || state == "activating" || state == "reloading"
}

func (e *Env) forward(ctx context.Context, j *Journal, p *Plan) (*Result, error) {
	// BACKUP IF REQUIRED (BCC stopped first: one coherent snapshot).
	if p.Backup != nil {
		for i := range j.Services {
			if j.Services[i].Kind == "bcc" {
				if err := e.stopService(ctx, j, i); err != nil {
					return nil, err
				}
			}
		}
		br, err := e.emergencyBackup(ctx, p.Backup)
		if err != nil {
			return nil, fmt.Errorf("emergency backup of BCC state: %w", err)
		}
		j.Backup = br
		if err := j.save(e.now()); err != nil {
			return nil, err
		}
		e.logf("emergency backup verified: %s", br.Dir)
		if err := failPoint("after-backup"); err != nil {
			return nil, err
		}
	}
	return e.resumeForward(ctx, j)
}

// resumeForward does (or finishes) STOP -> REMOVE -> VERIFY -> COMMIT.
func (e *Env) resumeForward(ctx context.Context, j *Journal) (*Result, error) {
	if j.Status == stApplying && j.Backup != nil && j.Backup.Dir == "" {
		// Interrupted before the emergency backup was verified: take it now,
		// before anything of BCC's is removed.
		for i := range j.Services {
			if j.Services[i].Kind == "bcc" {
				if err := e.stopService(ctx, j, i); err != nil {
					return nil, err
				}
			}
		}
		br, err := e.emergencyBackup(ctx, &BackupPlan{StateFile: j.Backup.StateFile, Files: j.Backup.PlanFiles, Dir: e.BackupDir})
		if err != nil {
			return nil, fmt.Errorf("emergency backup of BCC state: %w", err)
		}
		j.Backup = br
		if err := j.save(e.now()); err != nil {
			return nil, err
		}
	}
	if j.Status == stApplying {
		for i := range j.Services {
			if err := e.stopService(ctx, j, i); err != nil {
				return nil, err
			}
		}
		if err := failPoint("after-stop"); err != nil {
			return nil, err
		}
		for i := range j.Files {
			if err := e.moveOut(j, i); err != nil {
				return nil, err
			}
			if i == 0 {
				if err := failPoint("after-first-move"); err != nil {
					return nil, err
				}
			}
		}
		if e.System != nil {
			if _, err := e.System.Systemctl(ctx, "daemon-reload"); err != nil {
				return nil, fmt.Errorf("systemctl daemon-reload: %w", err)
			}
		}
		if err := failPoint("before-verify"); err != nil {
			return nil, err
		}
		if err := e.verify(ctx, j); err != nil {
			return nil, err
		}
		if err := failPoint("before-commit"); err != nil {
			return nil, err
		}
		j.Status = stCommitting
		if err := j.save(e.now()); err != nil {
			return nil, err
		}
	}
	// COMMIT: past this point there is no way back; it only purges what was
	// verified gone, and removes BAFT directories that became empty.
	if err := os.RemoveAll(filepath.Join(j.dir, "quarantine")); err != nil {
		return nil, err
	}
	_ = failPoint("mid-commit")
	for _, d := range j.Dirs {
		if err := os.Remove(d); err == nil {
			j.Removed = append(j.Removed, d)
		}
	}
	j.Status = stCommitted
	if err := j.save(e.now()); err != nil {
		return nil, err
	}
	res := &Result{Journal: j.dir, Status: stCommitted, Backup: j.Backup, Stopped: []string{}, Removed: []string{}, RemovedDirs: j.Removed}
	for _, s := range j.Services {
		if s.Stopped {
			res.Stopped = append(res.Stopped, s.Unit)
		}
	}
	for _, f := range j.Files {
		res.Removed = append(res.Removed, f.Path)
	}
	res.Kept = len(j.Kept)
	return res, nil
}

func (e *Env) stopService(ctx context.Context, j *Journal, i int) error {
	s := &j.Services[i]
	if s.Stopped && s.Disabled {
		return nil
	}
	if e.System == nil {
		return errors.New("no systemd access")
	}
	if !s.Stopped {
		if _, err := e.System.Systemctl(ctx, "stop", s.Unit); err != nil {
			return fmt.Errorf("systemctl stop %s: %w", s.Unit, err)
		}
		s.Stopped = true
		if err := j.save(e.now()); err != nil {
			return err
		}
		e.logf("stopped %s", s.Unit)
	}
	if !s.Disabled {
		if s.WasEnabled {
			if _, err := e.System.Systemctl(ctx, "disable", s.Unit); err != nil {
				return fmt.Errorf("systemctl disable %s: %w", s.Unit, err)
			}
		}
		s.Disabled = true
		if err := j.save(e.now()); err != nil {
			return err
		}
	}
	return nil
}

// moveOut moves one file into the quarantine, after checking it is still the
// file the plan saw.
func (e *Env) moveOut(j *Journal, i int) error {
	f := &j.Files[i]
	dst := filepath.Join(j.dir, f.Quarantine)
	switch f.State {
	case "moved":
		return nil
	case "moving":
		srcThere, dstThere := exists(f.Path), exists(dst)
		switch {
		case !srcThere && dstThere:
			f.State = "moved"
			return j.save(e.now())
		case srcThere && dstThere:
			// An interrupted cross-device copy: the original is intact.
			if err := os.RemoveAll(dst); err != nil {
				return err
			}
		case !srcThere && !dstThere:
			if f.Class == ClassRuntime {
				f.State = "moved" // a socket removed across filesystems
				return j.save(e.now())
			}
			return fmt.Errorf("%s is gone and not in the quarantine", f.Path)
		}
	}
	if !exists(f.Path) {
		return fmt.Errorf("%s disappeared since the plan was made", f.Path)
	}
	if f.SHA256 != "" {
		var sum, problem string
		if f.Dir {
			sum, problem = treeHash(f.Path)
		} else {
			sum, _, problem = hashRegular(f.Path)
		}
		if problem != "" || sum != f.SHA256 {
			return fmt.Errorf("%s changed since the plan was made; nothing of it was removed", f.Path)
		}
	}
	f.State = "moving"
	if err := j.save(e.now()); err != nil {
		return err
	}
	if err := moveAside(f.Path, dst, f.Dir); err != nil {
		return fmt.Errorf("move %s aside: %w", f.Path, err)
	}
	f.State = "moved"
	if err := j.save(e.now()); err != nil {
		return err
	}
	e.logf("removed %s", f.Path)
	return nil
}

// verify proves the run did what it said and nothing else: every removed path
// is gone, every artifact that stays is byte-identical, and every unit that
// stays and was running still runs.
func (e *Env) verify(ctx context.Context, j *Journal) error {
	var bad []string
	for _, f := range j.Files {
		if exists(f.Path) {
			bad = append(bad, f.Path+" is still there")
		}
	}
	for _, k := range j.Kept {
		var sum, problem string
		if k.Dir {
			sum, problem = treeHash(k.Path)
		} else {
			sum, _, problem = hashRegular(k.Path)
		}
		if problem != "" || sum != k.SHA256 {
			bad = append(bad, k.Path+" changed or disappeared, but it was to be kept")
		}
	}
	for _, u := range j.Running {
		if st := e.systemctl(ctx, "is-active", u); st != "active" {
			bad = append(bad, u+" was running and is not ("+st+"), but it was to be left alone")
		}
	}
	if len(bad) > 0 {
		return errors.New("verification failed: " + strings.Join(bad, "; "))
	}
	return nil
}

// Restore undoes an unfinished run (the journal of an interrupted uninstall):
// files come back from the quarantine and services are enabled and started
// as they were.
func (e *Env) Restore(ctx context.Context) (*Journal, error) {
	e.defaults()
	if e.RequireRoot && os.Geteuid() != 0 {
		return nil, errors.New("uninstall must run as root")
	}
	release, err := e.lockRuns()
	if err != nil {
		return nil, err
	}
	defer release()
	j, err := e.pendingJournal()
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, os.ErrNotExist
	}
	if err := e.restore(ctx, j); err != nil {
		return j, err
	}
	return j, nil
}

func (e *Env) restore(ctx context.Context, j *Journal) error {
	if j.Status == stCommitting || j.Status == stCommitted {
		return fmt.Errorf("run %s is past its commit point (verified and being purged); finish it with --resume", j.ID)
	}
	var errs []string
	for i := len(j.Files) - 1; i >= 0; i-- {
		f := &j.Files[i]
		if f.State != "moved" && f.State != "moving" {
			continue
		}
		dst := filepath.Join(j.dir, f.Quarantine)
		srcThere, dstThere := exists(f.Path), exists(dst)
		switch {
		case !srcThere && dstThere:
			if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
				errs = append(errs, err.Error())
				continue
			}
			if err := moveAside(dst, f.Path, f.Dir); err != nil {
				errs = append(errs, "put back "+f.Path+": "+err.Error())
				continue
			}
		case srcThere && dstThere:
			_ = os.RemoveAll(dst) // an unfinished copy; the original stayed
		}
		f.State = "restored"
		_ = j.save(e.now())
	}
	if e.System != nil {
		_, _ = e.System.Systemctl(ctx, "daemon-reload")
		for i := len(j.Services) - 1; i >= 0; i-- {
			s := &j.Services[i]
			if s.Disabled && s.WasEnabled {
				if _, err := e.System.Systemctl(ctx, "enable", s.Unit); err != nil {
					errs = append(errs, "enable "+s.Unit+": "+err.Error())
				}
			}
			if s.Stopped && s.WasActive {
				if _, err := e.System.Systemctl(ctx, "start", s.Unit); err != nil {
					errs = append(errs, "start "+s.Unit+": "+err.Error())
				}
			}
			s.Stopped, s.Disabled = false, false
		}
	}
	if len(errs) > 0 {
		_ = j.save(e.now())
		return errors.New(strings.Join(errs, "; "))
	}
	j.Status = stRestored
	_ = os.Remove(filepath.Join(j.dir, "quarantine"))
	return j.save(e.now())
}

// Resume finishes an unfinished run.
func (e *Env) Resume(ctx context.Context) (*Result, error) {
	e.defaults()
	if e.RequireRoot && os.Geteuid() != 0 {
		return nil, errors.New("uninstall must run as root")
	}
	release, err := e.lockRuns()
	if err != nil {
		return nil, err
	}
	defer release()
	j, err := e.pendingJournal()
	if err != nil {
		return nil, err
	}
	if j == nil {
		return nil, os.ErrNotExist
	}
	res, err := e.resumeForward(ctx, j)
	if err != nil && j.Status == stApplying {
		j.Error = err.Error()
		_ = j.save(e.now())
		return nil, fmt.Errorf("%v (nothing more was removed; run --restore to undo, or fix the cause and --resume)", err)
	}
	return res, err
}
