// Command baft-agent runs on each BAFT server. It pulls jobs from BCC and
// runs only jobs signed by the pinned BCC key for allowlisted actions; it
// installs only releases signed under the pinned release root.
//
//	baft-agent --bcc-url https://bcc.example.com --node-id ex-1 \
//	  --token-file /etc/baft-agent/token --bcc-job-key /etc/baft-agent/bcc-job.pub \
//	  --release-root /etc/baft-agent/release-root.pub
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zarkmakerburg/baft/internal/agent"
	"github.com/zarkmakerburg/baft/internal/release"
	"github.com/zarkmakerburg/baft/internal/tunnelnode"
)

type hostSystem struct{}

func (hostSystem) Systemctl(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "systemctl", args...).Output()
	return strings.TrimSpace(string(out)), err
}

func (hostSystem) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stderr))
}

func readSecret(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s must not be readable or writable by group/other", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return v, nil
}

func run(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("baft-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bccURL := fs.String("bcc-url", "", "BCC base URL (https)")
	nodeID := fs.String("node-id", "", "this node's ID in BCC")
	tokenFile := fs.String("token-file", "/etc/baft-agent/token", "agent token file (owner-only)")
	jobKeyFile := fs.String("bcc-job-key", "/etc/baft-agent/bcc-job.pub", "pinned BCC job public key (baft-bcc jobkey show)")
	rootFile := fs.String("release-root", "/etc/baft-agent/release-root.pub", "pinned release root public key")
	releaseBase := fs.String("release-base-url", "https://github.com/zarkmakerburg/baft/releases/download", "release assets at <base>/<version>/<file>")
	revocations := fs.String("revocations-url", "https://raw.githubusercontent.com/zarkmakerburg/baft/main/release/keys/revocations.json", "current release revocation list")
	stateDir := fs.String("state-dir", "/var/lib/baft-agent", "agent state (seen jobs)")
	releaseState := fs.String("release-state", "/opt/baft/release-state.json", "installer release trust state")
	binDir := fs.String("bin-dir", "/usr/local/bin", "where baft and baft-pair are installed")
	service := fs.String("service", "baft", "BAFT systemd unit")
	configDir := fs.String("config-dir", "/etc/baft", "BAFT config directory (tunnel jobs write baft.yaml, keys and PKI here)")
	baftState := fs.String("baft-state-dir", "/var/lib/baft", "BAFT service state directory")
	unitDir := fs.String("unit-dir", "/etc/systemd/system", "where the BAFT unit file lives")
	metricsListen := fs.String("metrics-listen", "127.0.0.1:9191", "loopback metrics address given to tunnels this agent builds")
	serviceUser := fs.String("service-user", "baft", "user the BAFT service runs as")
	interval := fs.Duration("interval", 30*time.Second, "poll interval")
	once := fs.Bool("once", false, "handle pending jobs once and exit")
	allowHTTP := fs.Bool("allow-insecure-http", false, "allow a plain-HTTP BCC URL (testing only)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return 2
	}
	if *bccURL == "" || *nodeID == "" {
		fmt.Fprintln(stderr, "baft-agent: --bcc-url and --node-id are required")
		return 2
	}
	if !strings.HasPrefix(*bccURL, "https://") && !*allowHTTP {
		fmt.Fprintln(stderr, "baft-agent: --bcc-url must be https:// (the agent token travels on it)")
		return 2
	}
	token, err := readSecret(*tokenFile)
	if err != nil {
		fmt.Fprintln(stderr, "baft-agent: token:", err)
		return 1
	}
	jobKey, err := release.ReadPublic(*jobKeyFile)
	if err != nil {
		fmt.Fprintln(stderr, "baft-agent: BCC job key:", err)
		return 1
	}
	cfg := agent.Config{
		BCCURL: *bccURL, NodeID: *nodeID, Token: token, BCCJobKey: jobKey, StateDir: *stateDir,
		Service: *service, ReleaseBaseURL: *releaseBase, RevocationsURL: *revocations,
		ReleaseState: *releaseState, BinDir: *binDir, System: hostSystem{},
	}
	tn, err := tunnelnode.New(tunnelnode.Env{
		ConfigDir: *configDir, StateDir: *baftState, UnitDir: *unitDir, Service: *service, User: *serviceUser,
		MetricsListen: *metricsListen, BaftBin: filepath.Join(*binDir, "baft"), PairBin: filepath.Join(*binDir, "baft-pair"), System: hostSystem{},
	})
	if err != nil {
		fmt.Fprintln(stderr, "baft-agent:", err)
		return 1
	}
	cfg.Tunnel = tn
	if root, err := release.ReadPublic(*rootFile); err == nil {
		cfg.ReleaseRoot = root
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stderr, "baft-agent: release root:", err)
		return 1
	} else {
		fmt.Fprintln(stderr, "baft-agent: no release root pinned; update_baft jobs will fail")
	}
	a, err := agent.New(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "baft-agent:", err)
		return 1
	}
	logger := log.New(stderr, "", log.LstdFlags)
	if *once {
		results, err := a.RunOnce(ctx)
		for _, r := range results {
			logger.Printf("job %s %s: %s %s", r.JobID, r.Action, r.Status, r.Detail)
		}
		if err != nil {
			logger.Printf("baft-agent: %v", err)
			return 1
		}
		return 0
	}
	logger.Printf("baft-agent %s polling %s every %s", *nodeID, *bccURL, *interval)
	a.Run(ctx, *interval, logger.Printf)
	return 0
}
