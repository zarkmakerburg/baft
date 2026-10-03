package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/node"
)

var version = "0.2.0-pro-rc1"

type versionInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	args := os.Args[1:]
	// `baft` alone, or `baft menu`, opens the interactive menu on a terminal.
	// Without one, `baft` prints usage exactly as before and every other
	// command stays script-friendly: no splash, no escape sequences.
	if len(args) == 0 || args[0] == "menu" {
		os.Exit(menuEntry(args, os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(runContext(ctx, args, os.Stdout, os.Stderr))
}

func menuEntry(args []string, stdin, stdout *os.File, stderr io.Writer) int {
	if !isTerminal(stdin) || !isTerminal(stdout) {
		if len(args) == 0 {
			usage(stderr)
		} else {
			fmt.Fprintln(stderr, "baft menu needs an interactive terminal; use baft status, baft doctor or baft logs in scripts")
		}
		return 2
	}
	var rest []string
	if len(args) > 0 {
		rest = args[1:]
	}
	caps := detectTerm(os.Getenv, true, terminalWidth(stdout))
	return runMenu(rest, stdin, stdout, stderr, hostOps, caps, time.Now)
}

func run(args []string, stdout, stderr io.Writer) int {
	return runContext(context.Background(), args, stdout, stderr)
}

func runContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "config":
		return runConfig(args[1:], stdout, stderr)
	case "run":
		return runNode(ctx, args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr, hostOps)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr, hostOps)
	case "logs":
		return runLogs(args[1:], stdout, stderr, hostOps)
	case "support-bundle":
		return runSupportBundle(args[1:], stdout, stderr, hostOps, time.Now)
	default:
		usage(stderr)
		return 2
	}
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--json" {
		_ = json.NewEncoder(stdout).Encode(versionInfo{Name: "baft", Version: version})
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stdout, "baft", version)
		return 0
	}
	fmt.Fprintln(stderr, "usage: baft version [--json]")
	return 2
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "stealth-pro" {
		return runStealthConfig(args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "validate" {
		fmt.Fprintln(stderr, "usage: baft config validate --file <config.yaml>")
		return 2
	}
	fs := flag.NewFlagSet("config validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file")
	if err := fs.Parse(args[1:]); err != nil || *file == "" || fs.NArg() != 0 {
		return 2
	}
	cfg, err := config.LoadFile(*file)
	if err != nil {
		fmt.Fprintln(stderr, "invalid config:", err)
		return 1
	}
	// A listener refuses to start without a valid revocation list, so the
	// list is part of what "valid" means.
	if cfg.Revocation != nil {
		if _, err := config.LoadRevocationFile(cfg.Revocation.File); err != nil {
			fmt.Fprintln(stderr, "invalid revocation file:", err)
			return 1
		}
	}
	fmt.Fprintln(stdout, "valid")
	return 0
}

func runNode(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "configuration file")
	if err := fs.Parse(args); err != nil || *file == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: baft run --file <config.yaml>")
		return 2
	}
	cfg, err := config.LoadFile(*file)
	if err != nil {
		fmt.Fprintln(stderr, "load config:", err)
		return 1
	}
	fmt.Fprintf(stdout, "starting BAFT node %s (%s)\n", cfg.Node.ID, cfg.Node.Role)
	rt := node.NewRuntime()
	stopReload := reloadOnHUP(ctx, rt, stderr)
	defer stopReload()
	if err := rt.Run(ctx, cfg); err != nil {
		fmt.Fprintln(stderr, "node stopped:", err)
		return 1
	}
	return 0
}

// reloadOnHUP re-applies revocation.file on SIGHUP (`systemctl reload baft`).
// Handling SIGHUP unconditionally also keeps a reload from terminating a node
// that has no revocation file.
func reloadOnHUP(ctx context.Context, rt *node.Runtime, stderr io.Writer) func() {
	ctx, cancel := context.WithCancel(ctx)
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				n, err := rt.ReloadRevocations()
				switch {
				case errors.Is(err, node.ErrNoRevocationFile):
					fmt.Fprintln(stderr, "reload: no revocation.file configured")
				case err != nil:
					fmt.Fprintln(stderr, "reload: revocation file rejected, keeping current revocations:", err)
				default:
					fmt.Fprintf(stderr, "reload: %d revocation entries applied\n", n)
				}
			}
		}
	}()
	return func() {
		signal.Stop(hup)
		cancel()
		<-done
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "BAFT research software")
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  baft                     # interactive menu (on a terminal)")
	fmt.Fprintln(w, "  baft version [--json]")
	fmt.Fprintln(w, "  baft config validate --file <config.yaml>")
	fmt.Fprintln(w, "  baft config stealth-pro --file <config.yaml> [padding/jitter flags]")
	fmt.Fprintln(w, "  baft run --file <config.yaml>")
	fmt.Fprintln(w, "  baft status [--file /etc/baft/baft.yaml] [--service baft] [--json]")
	fmt.Fprintln(w, "  baft doctor [--file /etc/baft/baft.yaml] [--service baft] [--json] [--preview-fixes]")
	fmt.Fprintln(w, "  baft logs [--service baft] [-n 100] [-f]")
	fmt.Fprintln(w, "  baft support-bundle [--out file.tar.gz] [-n 500]   # secret-safe diagnostics archive")
}
