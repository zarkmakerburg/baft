package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/zarkmakerburg/baft/internal/cluster"
	"github.com/zarkmakerburg/baft/internal/config"
)

type fileFlags []string

func (f *fileFlags) String() string { return strings.Join(*f, ",") }
func (f *fileFlags) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("empty config path")
	}
	*f = append(*f, v)
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var files fileFlags
	fs := flag.NewFlagSet("baft-master", flag.ExitOnError)
	fs.Var(&files, "file", "dialer config file; repeat exactly six times")
	fs.Parse(os.Args[1:])

	if len(files) != cluster.RequiredForeignNodes || fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "usage: baft-master --file <ex1.yaml> ... --file <ex6.yaml>\n")
		os.Exit(2)
	}

	cfgs := make([]config.Config, 0, len(files))
	for _, path := range files {
		cfg, err := config.LoadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load %s: %v\n", path, err)
			os.Exit(1)
		}
		cfgs = append(cfgs, cfg)
	}
	if err := cluster.ValidateMasterConfigs(cfgs); err != nil {
		fmt.Fprintln(os.Stderr, "invalid master configuration:", err)
		os.Exit(1)
	}

	fmt.Printf("starting BAFT master %s with %d foreign nodes\n", cfgs[0].Node.ID, len(cfgs))
	if err := cluster.NewMaster().Run(ctx, cfgs); err != nil {
		fmt.Fprintln(os.Stderr, "master stopped:", err)
		os.Exit(1)
	}
}
