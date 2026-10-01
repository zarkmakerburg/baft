package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zarkmakerburg/baft/internal/cluster"
	"github.com/zarkmakerburg/baft/internal/clustersync"
	"github.com/zarkmakerburg/baft/internal/config"
)

type fileFlags []string

func (f *fileFlags) String() string { return strings.Join(*f, ",") }
func (f *fileFlags) Set(v string) error {
	if strings.TrimSpace(v) == "" { return fmt.Errorf("empty config path") }
	*f = append(*f, v)
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var files fileFlags
	fs := flag.NewFlagSet("baft-master", flag.ExitOnError)
	fs.Var(&files, "file", "dialer config file; repeat once per foreign node")
	exportToken := fs.Bool("export-token", false, "export one encrypted cluster token instead of starting runtimes")
	workerPublic := fs.String("worker-public-key", "", "X25519 worker public key file")
	signingKey := fs.String("signing-key", "", "Ed25519 master signing private key file")
	clusterID := fs.String("cluster-id", "goldapp-baft", "cluster identity")
	generation := fs.Uint64("generation", 1, "monotonic cluster configuration generation")
	ttl := fs.Duration("token-ttl", 15*time.Minute, "token lifetime, maximum 24h")
	fs.Parse(os.Args[1:])

	if len(files) < cluster.MinForeignNodes || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: baft-master --file <node.yaml> [--file <node2.yaml> ...] [--export-token --worker-public-key <file> --signing-key <file>]")
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

	if *exportToken {
		if *workerPublic == "" || *signingKey == "" {
			fmt.Fprintln(os.Stderr, "--export-token requires --worker-public-key and --signing-key")
			os.Exit(2)
		}
		wp, err := clustersync.ReadWorkerPublic(*workerPublic)
		if err != nil { fmt.Fprintln(os.Stderr, "worker public key:", err); os.Exit(1) }
		sk, err := clustersync.ReadSigningPrivate(*signingKey)
		if err != nil { fmt.Fprintln(os.Stderr, "signing key:", err); os.Exit(1) }
		manifest, err := clustersync.ManifestFromConfigs(*clusterID,*generation,*ttl,time.Now(),cfgs)
		if err != nil { fmt.Fprintln(os.Stderr, "manifest:", err); os.Exit(1) }
		token, err := clustersync.Seal(manifest,wp,sk)
		if err != nil { fmt.Fprintln(os.Stderr, "seal token:", err); os.Exit(1) }
		fmt.Println(token)
		return
	}

	fmt.Printf("starting BAFT master %s with %d foreign nodes\n", cfgs[0].Node.ID, len(cfgs))
	if err := cluster.NewMaster().Run(ctx, cfgs); err != nil {
		fmt.Fprintln(os.Stderr, "master stopped:", err)
		os.Exit(1)
	}
}
