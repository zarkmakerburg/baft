package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/zarkmakerburg/baft/internal/uninstall"
)

// baft uninstall (HQ A3, safe uninstall). Local only: there is no remote or
// fleet uninstall, and the agent has no way to run it.

// hostSystem runs the real systemctl and helper programs.
type hostSystem struct{}

func (hostSystem) Systemctl(ctx context.Context, args ...string) (string, error) {
	return runCapture(ctx, "systemctl", args...)
}

func (hostSystem) Run(ctx context.Context, name string, args ...string) (string, error) {
	return runCapture(ctx, name, args...)
}

func runCapture(ctx context.Context, name string, args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil && errb.Len() > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), err
}

// uninstallIO is the terminal (or the menu) the flow talks to.
type uninstallIO struct {
	in          *bufio.Reader
	out, errw   io.Writer
	interactive bool
}

func (u uninstallIO) ask(q string) (string, bool) {
	fmt.Fprint(u.out, q)
	s, err := u.in.ReadString('\n')
	if err != nil && s == "" {
		return "", false
	}
	return clean(strings.TrimSpace(s)), true
}

func (u uninstallIO) yes(q string) bool {
	a, ok := u.ask(q + " [y/N]: ")
	return ok && (strings.EqualFold(a, "y") || strings.EqualFold(a, "yes"))
}

// uninstallSystem and uninstallMainPkg are replaced by tests.
var (
	uninstallSystem  uninstall.System = hostSystem{}
	uninstallMainPkg map[string]string
	uninstallRoot    = true
	// uninstallEnvHook lets tests point every path at a fake tree.
	uninstallEnvHook func(*uninstall.Env)
)

const uninstallUsage = `usage: baft uninstall [--preview [--json]] [scope] [data] [--stop-active-tunnels] [--yes]
       baft uninstall --resume | --restore
scope (default: BAFT services + agent, with their binaries):
  --binaries   BAFT binaries only          --agent     the agent only
  --bcc        the BCC only                --services  BAFT services + binaries
  --full       everything above
data (kept unless chosen, each separately):
  --delete-bcc-state  --delete-certificates  --delete-backups  --delete-tunnel-configs  --delete-audit
  --no-backup  delete BCC state without the emergency backup (owner's explicit choice)
paths: --unit-dir --bin-dir --prefix --config-dir --state-dir --agent-dir --agent-state-dir
       --bcc-state-file --journal-dir --backup-dir`

func runUninstall(ctx context.Context, args []string, stdin *os.File, stdout, stderr io.Writer) int {
	interactive := isTerminal(stdin)
	if f, ok := stdout.(*os.File); !ok || !isTerminal(f) {
		interactive = false
	}
	return uninstallMain(ctx, args, uninstallIO{in: bufio.NewReader(stdin), out: stdout, errw: stderr, interactive: interactive})
}

func uninstallMain(ctx context.Context, args []string, ui uninstallIO) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(ui.errw)
	env := &uninstall.Env{System: uninstallSystem, MainPkg: uninstallMainPkg, RequireRoot: uninstallRoot,
		RefUnitDirs: []string{"/run/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system"}}
	var o uninstall.Options
	o.Delete = map[uninstall.Class]bool{}
	preview := fs.Bool("preview", false, "show the exact plan and change nothing")
	asJSON := fs.Bool("json", false, "with --preview: machine-readable plan")
	resume := fs.Bool("resume", false, "finish an interrupted uninstall")
	restore := fs.Bool("restore", false, "undo an interrupted uninstall")
	fs.BoolVar(&o.Scope.Binaries, "binaries", false, "BAFT binaries only")
	fs.BoolVar(&o.Scope.Agent, "agent", false, "the agent only")
	fs.BoolVar(&o.Scope.BCC, "bcc", false, "the BCC only")
	fs.BoolVar(&o.Scope.Services, "services", false, "BAFT services and their binaries")
	fs.BoolVar(&o.Scope.Full, "full", false, "everything")
	del := map[uninstall.Class]*bool{}
	for _, c := range uninstall.DataClasses {
		del[c] = fs.Bool(strings.TrimPrefix(c.Flag(), "--"), false, c.Question())
	}
	fs.BoolVar(&o.NoBackup, "no-backup", false, "delete BCC state without the emergency backup")
	fs.BoolVar(&o.StopActiveTunnels, "stop-active-tunnels", false, "consent to stopping running tunnels")
	fs.BoolVar(&o.Yes, "yes", false, "apply without the interactive confirmation")
	fs.StringVar(&env.UnitDir, "unit-dir", "/etc/systemd/system", "systemd unit directory")
	fs.StringVar(&env.BinDir, "bin-dir", "/usr/local/bin", "BAFT binary directory")
	fs.StringVar(&env.Prefix, "prefix", "/opt/baft", "installer prefix")
	fs.StringVar(&env.ConfigDir, "config-dir", "/etc/baft", "BAFT config directory")
	fs.StringVar(&env.StateDir, "state-dir", "/var/lib/baft", "BAFT state directory")
	fs.StringVar(&env.AgentDir, "agent-dir", "/etc/baft-agent", "agent directory")
	fs.StringVar(&env.AgentStateDir, "agent-state-dir", "/var/lib/baft-agent", "agent state directory")
	fs.StringVar(&env.BCCStateFile, "bcc-state-file", "", "a BCC state file whose service unit is gone")
	fs.StringVar(&env.JournalDir, "journal-dir", "/var/lib/baft-uninstall", "uninstall journal and quarantine")
	fs.StringVar(&env.BackupDir, "backup-dir", "/var/backups/baft", "where emergency backups go (never removed)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(ui.errw, uninstallUsage)
		return 2
	}
	for c, v := range del {
		if *v {
			o.Delete[c] = true
		}
	}
	env.Log = ui.errw
	if uninstallEnvHook != nil {
		uninstallEnvHook(env)
	}
	if *resume || *restore {
		if *resume && *restore || *preview {
			fmt.Fprintln(ui.errw, uninstallUsage)
			return 2
		}
		return uninstallRecover(ctx, env, *resume, ui)
	}
	if *asJSON && !*preview {
		fmt.Fprintln(ui.errw, "--json goes with --preview")
		return 2
	}
	if err := uninstall.ValidateOptions(o); err != nil {
		fmt.Fprintln(ui.errw, "baft uninstall:", err)
		return 2
	}
	return runUninstallFlow(ctx, env, o, *preview, *asJSON, ui)
}

func scopeArgs(o uninstall.Options) []string {
	var a []string
	s := o.Scope
	for _, x := range []struct {
		on   bool
		flag string
	}{{s.Full, "--full"}, {s.Services, "--services"}, {s.Agent, "--agent"}, {s.BCC, "--bcc"}, {s.Binaries, "--binaries"}} {
		if x.on {
			a = append(a, x.flag)
		}
	}
	for _, c := range uninstall.DataClasses {
		if o.Delete[c] {
			a = append(a, c.Flag())
		}
	}
	if o.NoBackup {
		a = append(a, "--no-backup")
	}
	return a
}

// runUninstallFlow is shared by the command and menu item 13:
// INSPECT -> PLAN -> SHOW -> CONFIRM -> (BACKUP) -> STOP -> REMOVE -> VERIFY.
func runUninstallFlow(ctx context.Context, env *uninstall.Env, o uninstall.Options, preview, asJSON bool, ui uninstallIO) int {
	if !preview && uninstallRoot && os.Geteuid() != 0 {
		fmt.Fprintln(ui.errw, "baft uninstall must run as root (baft uninstall --preview shows the plan without changing anything)")
		return 1
	}
	p := env.BuildPlan(ctx, o)
	if preview {
		if asJSON {
			b, err := p.JSON()
			if err != nil {
				fmt.Fprintln(ui.errw, err)
				return 1
			}
			ui.out.Write(append(b, '\n'))
			return 0
		}
		p.Render(ui.out)
		if uninstallRoot && os.Geteuid() != 0 {
			fmt.Fprintln(ui.out, "\nnote: not running as root; files you cannot read show as UNKNOWN")
		}
		fmt.Fprintln(ui.out, "\nThis was a preview; nothing was changed.")
		return 0
	}
	p.Render(ui.out)
	switch {
	case p.Pending != "":
		fmt.Fprintln(ui.out, "\nNothing was changed. Run `baft uninstall --resume` or `baft uninstall --restore` first.")
		return 5
	case len(p.Blocked) > 0:
		fmt.Fprintln(ui.out, "\nNothing was changed.")
		return 3
	case !p.HasWork():
		fmt.Fprintln(ui.out, "\nNothing to remove in this scope. Nothing was changed.")
		return 0
	}
	if ui.interactive && !o.Yes {
		var ok bool
		if p, ok = confirmUninstall(ctx, env, &o, p, ui); !ok {
			fmt.Fprintln(ui.out, "Not confirmed. Nothing was changed.")
			return 4
		}
	} else if len(p.Needs) > 0 {
		fmt.Fprintf(ui.out, "\nThis plan needs %s to be applied. Nothing was changed.\n  baft uninstall %s\n",
			strings.Join(p.Needs, " and "), strings.Join(append(scopeArgs(o), p.Needs...), " "))
		return 4
	}
	fmt.Fprintln(ui.out)
	res, err := env.Apply(ctx, p)
	switch {
	case errors.Is(err, uninstall.ErrBlocked):
		return 3
	case errors.Is(err, uninstall.ErrConfirmation):
		return 4
	case errors.Is(err, uninstall.ErrPending):
		return 5
	case err != nil:
		fmt.Fprintln(ui.errw, "baft uninstall:", err)
		return 1
	}
	printUninstallResult(ui.out, res)
	return 0
}

// confirmUninstall asks, separately and defaulting to NO, for each data class
// (full uninstall), for stopping active tunnels, and finally for the plan.
func confirmUninstall(ctx context.Context, env *uninstall.Env, o *uninstall.Options, p *uninstall.Plan, ui uninstallIO) (*uninstall.Plan, bool) {
	if o.Scope.Full && len(p.Deletable) > 0 {
		fmt.Fprintln(ui.out, "\nData is kept unless you choose otherwise:")
		for _, c := range p.Deletable {
			if o.Delete[c] {
				continue
			}
			if ui.yes(c.Question()) {
				o.Delete[c] = true
			}
		}
		if o.Delete[uninstall.ClassBCCState] && !o.NoBackup {
			fmt.Fprintf(ui.out, "BCC state is backed up and the backup verified before it is deleted (into %s).\n", env.BackupDir)
			if a, _ := ui.ask("Press Enter to keep that backup, or type NO BACKUP to skip it: "); a == "NO BACKUP" {
				o.NoBackup = true
			}
		}
		np := env.BuildPlan(ctx, *o)
		if len(np.Remove) != len(p.Remove) {
			fmt.Fprintln(ui.out, "\nWith your choices the plan is now:")
			np.Render(ui.out)
		}
		p = np
		if len(p.Blocked) > 0 {
			return p, false
		}
	}
	if len(p.ActiveTunnels) > 0 {
		fmt.Fprintf(ui.out, "\n%d active tunnel(s) will be stopped and traffic through them interrupted.\n", len(p.ActiveTunnels))
		if !ui.yes("Stop them?") {
			return p, false
		}
		o.StopActiveTunnels = true
	}
	a, ok := ui.ask("\nType uninstall to apply this plan: ")
	if !ok || a != "uninstall" {
		return p, false
	}
	o.Yes = true
	p = env.BuildPlan(ctx, *o)
	if len(p.Blocked) > 0 || len(p.Needs) > 0 {
		p.Render(ui.out)
		return p, false
	}
	return p, true
}

func printUninstallResult(w io.Writer, r *uninstall.Result) {
	fmt.Fprintf(w, "Uninstall finished and verified.\n")
	if len(r.Stopped) > 0 {
		fmt.Fprintf(w, "  stopped:  %s\n", strings.Join(r.Stopped, ", "))
	}
	fmt.Fprintf(w, "  removed:  %d item(s)\n", len(r.Removed))
	if len(r.RemovedDirs) > 0 {
		fmt.Fprintf(w, "  removed empty directories: %s\n", strings.Join(r.RemovedDirs, ", "))
	}
	if r.Backup != nil && r.Backup.Dir != "" {
		fmt.Fprintf(w, "  BCC emergency backup: %s (verified: %s%s)\n", r.Backup.Dir, r.Backup.VerifiedBy, summarySuffix(r.Backup.Summary))
	}
	fmt.Fprintf(w, "  journal:  %s\n", r.Journal)
}

func summarySuffix(s string) string {
	if s == "" {
		return ""
	}
	return "; " + s
}

func uninstallRecover(ctx context.Context, env *uninstall.Env, resume bool, ui uninstallIO) int {
	if uninstallRoot && os.Geteuid() != 0 {
		fmt.Fprintln(ui.errw, "baft uninstall must run as root")
		return 1
	}
	if resume {
		res, err := env.Resume(ctx)
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(ui.out, "No unfinished uninstall.")
			return 0
		}
		if err != nil {
			fmt.Fprintln(ui.errw, "baft uninstall --resume:", err)
			return 1
		}
		printUninstallResult(ui.out, res)
		return 0
	}
	j, err := env.Restore(ctx)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(ui.out, "No unfinished uninstall.")
		return 0
	}
	if err != nil {
		fmt.Fprintln(ui.errw, "baft uninstall --restore:", err)
		return 1
	}
	fmt.Fprintf(ui.out, "Restored: everything the interrupted uninstall moved is back and its services run as before.\n  journal: %s\n", j.Dir())
	return 0
}
