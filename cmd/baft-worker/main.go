package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zarkmakerburg/baft/internal/clustersync"
)

func main() {
	fs := flag.NewFlagSet("baft-worker", flag.ExitOnError)
	token := fs.String("token", "", "BAFT cluster token; defaults to BAFT_CLUSTER_TOKEN")
	workerKey := fs.String("worker-key", "", "X25519 worker private key file")
	masterPublic := fs.String("master-public-key", "", "Ed25519 master signing public key file")
	clusterID := fs.String("cluster-id", "goldapp-baft", "expected cluster identity")
	fs.Parse(os.Args[1:])

	if strings.TrimSpace(*token)=="" { *token = os.Getenv("BAFT_CLUSTER_TOKEN") }
	if strings.TrimSpace(*token)=="" || *workerKey=="" || *masterPublic=="" || fs.NArg()!=0 {
		fmt.Fprintln(os.Stderr,"usage: baft-worker --worker-key <file> --master-public-key <file> [--token <token>] [--cluster-id <id>]")
		os.Exit(2)
	}

	wk, err := clustersync.ReadWorkerPrivate(*workerKey)
	if err != nil { fmt.Fprintln(os.Stderr,"worker key:",err); os.Exit(1) }
	mp, err := clustersync.ReadSigningPublic(*masterPublic)
	if err != nil { fmt.Fprintln(os.Stderr,"master public key:",err); os.Exit(1) }

	engine, err := clustersync.NewEngine(wk,mp,*clusterID)
	if err != nil { fmt.Fprintln(os.Stderr,"sync engine:",err); os.Exit(1) }
	snap, changed, err := engine.Apply(*token,time.Now())
	if err != nil { fmt.Fprintln(os.Stderr,"cluster token rejected:",err); os.Exit(1) }

	out := struct {
		Changed bool
		Snapshot clustersync.Snapshot
	}{Changed:changed, Snapshot:snap}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("","  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr,"encode snapshot:",err)
		os.Exit(1)
	}
}
