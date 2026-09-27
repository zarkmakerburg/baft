package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

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
	os.Exit(runContext(ctx, os.Args[1:], os.Stdout, os.Stderr))
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
	if _, err := config.LoadFile(*file); err != nil {
		fmt.Fprintln(stderr, "invalid config:", err)
		return 1
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
	if err := node.NewRuntime().Run(ctx, cfg); err != nil {
		fmt.Fprintln(stderr, "node stopped:", err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "BAFT research software")
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  baft version [--json]")
	fmt.Fprintln(w, "  baft config validate --file <config.yaml>")
	fmt.Fprintln(w, "  baft config stealth-pro --file <config.yaml> [padding/jitter flags]")
	fmt.Fprintln(w, "  baft run --file <config.yaml>")
}
