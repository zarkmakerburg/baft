package main

import (
    "context"
    "encoding/json"
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

func main() {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    fs := flag.NewFlagSet("baft-worker", flag.ExitOnError)
    token := fs.String("token", "", "BAFT cluster token; defaults to BAFT_CLUSTER_TOKEN")
    workerKey := fs.String("worker-key", "", "X25519 cluster-token private key file")
    masterPublic := fs.String("master-public-key", "", "Ed25519 master signing public key file")
    clusterID := fs.String("cluster-id", "goldapp-baft", "expected cluster identity")
    checkOnly := fs.Bool("check-only", false, "verify token and print mirrored snapshot without starting runtimes")

    noiseKey := fs.String("noise-key", "", "Worker Noise static private key file")
    tlsCA := fs.String("tls-ca", "", "TLS CA file")
    tlsCert := fs.String("tls-cert", "", "TLS certificate file")
    tlsKey := fs.String("tls-key", "", "TLS private key file")
    stateDir := fs.String("state-dir", "/run/baft-worker", "Worker runtime state directory")
    routeBase := fs.Int("route-base-port", 16000, "identity-derived loopback service port base")
    metricsBase := fs.Int("metrics-base-port", 9400, "identity-derived loopback metrics port base")
    nodeID := fs.String("node-id", "ir-worker", "Worker node identity")
    fs.Parse(os.Args[1:])

    if strings.TrimSpace(*token)=="" { *token = os.Getenv("BAFT_CLUSTER_TOKEN") }
    if strings.TrimSpace(*token)=="" || *workerKey=="" || *masterPublic=="" || fs.NArg()!=0 {
        fmt.Fprintln(os.Stderr,"usage: baft-worker --worker-key <file> --master-public-key <file> [--token <token>] [--check-only] [runtime flags]")
        os.Exit(2)
    }

    wk, err := clustersync.ReadWorkerPrivate(*workerKey)
    if err != nil { fmt.Fprintln(os.Stderr,"worker key:",err); os.Exit(1) }
    mp, err := clustersync.ReadSigningPublic(*masterPublic)
    if err != nil { fmt.Fprintln(os.Stderr,"master public key:",err); os.Exit(1) }

    engine, err := clustersync.NewEngine(wk,mp,*clusterID)
    if err != nil { fmt.Fprintln(os.Stderr,"sync engine:",err); os.Exit(1) }

    if *checkOnly {
        snap,changed,err:=engine.Apply(*token,time.Now())
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
        return
    }

    if *noiseKey=="" || *tlsCA=="" || *tlsCert=="" || *tlsKey=="" {
        fmt.Fprintln(os.Stderr,"runtime mode requires --noise-key --tls-ca --tls-cert --tls-key")
        os.Exit(2)
    }
    template:=clustersync.WorkerTemplate{
        NodeID:*nodeID,
        NoiseKeyFile:*noiseKey,
        TLS:config.TLS{MinVersion:"1.3",CAFile:*tlsCA,CertFile:*tlsCert,KeyFile:*tlsKey,SessionTickets:false},
        Limits:config.Limits{MaxFlows:256,DataMemoryMiB:256,ReceiveInitialKiB:64,ReceiveMaxMiB:16,ReplayMaxMiB:16},
        RouteBasePort:*routeBase,
        MetricsBasePort:*metricsBase,
        StateDir:*stateDir,
    }
    controller,err:=cluster.NewWorkerController(ctx,engine,template)
    if err!=nil{fmt.Fprintln(os.Stderr,"worker controller:",err);os.Exit(1)}
    defer controller.Close()

    snap,changed,err:=controller.ApplyToken(*token,time.Now())
    if err!=nil{fmt.Fprintln(os.Stderr,"cluster token/runtime apply:",err);os.Exit(1)}
    fmt.Printf("cluster token committed generation=%d revision=%s changed=%v active_nodes=%d\n",snap.Generation,snap.Revision,changed,len(controller.ActiveNodeIDs()))

    select{
    case <-ctx.Done():
        return
    case failure:=<-controller.RuntimeFailures():
        fmt.Fprintf(os.Stderr,"worker runtime failed node=%s instance=%d err=%v\n",failure.NodeID,failure.Instance,failure.Err)
        _=controller.Close()
        os.Exit(1)
    }
}
