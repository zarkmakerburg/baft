package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
)

// Operator commands: status, doctor and logs. They only read: the config,
// the installer's release state, systemd, the loopback metrics endpoint and
// /proc. Nothing here changes the host.

const (
	defaultConfigFile   = "/etc/baft/baft.yaml"
	defaultService      = "baft"
	defaultReleaseState = "/opt/baft/release-state.json"
)

// opsEnv is the host access the operator commands use, so tests can fake it.
type opsEnv struct {
	output   func(name string, args ...string) (string, error)
	stream   func(name string, args []string, stdout, stderr io.Writer) error
	dial     func(addr string, timeout time.Duration) error
	fetch    func(url string) (string, error)
	procRoot string
}

var hostOps = opsEnv{
	output: func(name string, args ...string) (string, error) {
		out, err := exec.Command(name, args...).Output()
		return strings.TrimSpace(string(out)), err
	},
	stream: func(name string, args []string, stdout, stderr io.Writer) error {
		cmd := exec.Command(name, args...)
		cmd.Stdout, cmd.Stderr = stdout, stderr
		return cmd.Run()
	},
	dial: func(addr string, timeout time.Duration) error {
		c, err := net.DialTimeout("tcp", addr, timeout)
		if err == nil {
			c.Close()
		}
		return err
	},
	fetch: func(url string) (string, error) {
		client := http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(url)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return string(b), err
	},
	procRoot: "/proc",
}

type opsFlags struct {
	file, service, releaseState string
	json                        bool
}

func parseOpsFlags(name string, args []string, stderr io.Writer, extra func(*flag.FlagSet)) (opsFlags, bool) {
	var f opsFlags
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&f.file, "file", defaultConfigFile, "configuration file")
	fs.StringVar(&f.service, "service", defaultService, "systemd service name")
	fs.StringVar(&f.releaseState, "release-state", defaultReleaseState, "installer release state file")
	fs.BoolVar(&f.json, "json", false, "print JSON")
	if extra != nil {
		extra(fs)
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return f, false
	}
	return f, true
}

type releaseState struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

func readReleaseState(path string) (*releaseState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s releaseState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// parseMetrics reads the Prometheus text format; labelled series are summed
// under their bare name.
func parseMetrics(text string) map[string]float64 {
	m := map[string]float64{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		if i < 0 {
			continue
		}
		v, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			continue
		}
		name := line[:i]
		if j := strings.IndexByte(name, '{'); j >= 0 {
			name = name[:j]
		}
		m[name] += v
	}
	return m
}

func (e opsEnv) metrics(cfg config.Config) (map[string]float64, error) {
	if cfg.Management.MetricsListen == "" {
		return nil, errors.New("management.metrics_listen is not set")
	}
	text, err := e.fetch("http://" + cfg.Management.MetricsListen + "/metrics")
	if err != nil {
		return nil, err
	}
	return parseMetrics(text), nil
}

func (e opsEnv) serviceState(service string) (state, restarts string) {
	state, err := e.output("systemctl", "is-active", service)
	if state == "" {
		state = "unknown"
		if err != nil && errors.Is(err, exec.ErrNotFound) {
			state = "unknown (no systemctl)"
		}
	}
	if n, err := e.output("systemctl", "show", "-p", "NRestarts", "--value", service); err == nil && n != "" {
		restarts = n
	}
	return state, restarts
}

// ---- status ----

type statusRoute struct {
	ID   string `json:"id"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type statusReport struct {
	BinaryVersion  string             `json:"binary_version"`
	Release        *releaseState      `json:"release,omitempty"`
	ConfigFile     string             `json:"config_file"`
	ConfigError    string             `json:"config_error,omitempty"`
	NodeID         string             `json:"node_id,omitempty"`
	Role           string             `json:"role,omitempty"`
	Peer           string             `json:"peer,omitempty"`
	Listen         string             `json:"listen,omitempty"`
	Routes         []statusRoute      `json:"routes,omitempty"`
	Service        string             `json:"service"`
	ServiceState   string             `json:"service_state"`
	ServiceRestart string             `json:"service_restarts,omitempty"`
	Metrics        map[string]float64 `json:"metrics,omitempty"`
	MetricsError   string             `json:"metrics_error,omitempty"`
}

func runStatus(args []string, stdout, stderr io.Writer, env opsEnv) int {
	f, ok := parseOpsFlags("status", args, stderr, nil)
	if !ok {
		fmt.Fprintln(stderr, "usage: baft status [--file baft.yaml] [--service baft] [--release-state path] [--json]")
		return 2
	}
	r := statusReport{BinaryVersion: version, ConfigFile: f.file, Service: f.service}
	if rs, err := readReleaseState(f.releaseState); err == nil {
		r.Release = rs
	}
	r.ServiceState, r.ServiceRestart = env.serviceState(f.service)
	cfg, err := config.LoadFile(f.file)
	if err != nil {
		r.ConfigError = err.Error()
	} else {
		r.NodeID, r.Role = cfg.Node.ID, cfg.Node.Role
		if cfg.Peer != nil {
			r.Peer = cfg.Peer.Address
		}
		if cfg.Server != nil {
			r.Listen = cfg.Server.Listen
		}
		for _, rt := range cfg.Routes {
			sr := statusRoute{ID: rt.ID}
			if rt.Direction == "outbound" {
				sr.From, sr.To = rt.Listen, "peer route "+rt.RemoteRoute
			} else {
				sr.To = rt.Target
			}
			r.Routes = append(r.Routes, sr)
		}
		if m, err := env.metrics(cfg); err != nil {
			r.MetricsError = err.Error()
		} else {
			r.Metrics = m
		}
	}
	if f.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
	} else {
		printStatus(stdout, r)
	}
	if r.ConfigError != "" {
		return 1
	}
	return 0
}

func printStatus(w io.Writer, r statusReport) {
	rel := "not installed from a signed release"
	if r.Release != nil {
		rel = fmt.Sprintf("release %s, commit %.12s", r.Release.Version, r.Release.Commit)
	}
	fmt.Fprintf(w, "baft      %s (%s)\n", r.BinaryVersion, rel)
	if r.ConfigError != "" {
		fmt.Fprintf(w, "config    %s: INVALID: %s\n", r.ConfigFile, r.ConfigError)
	} else {
		where := "peer " + r.Peer
		if r.Role == "listener" {
			where = "listening on " + r.Listen
		}
		fmt.Fprintf(w, "node      %s (%s), %s\n", r.NodeID, r.Role, where)
		for _, rt := range r.Routes {
			if rt.From != "" {
				fmt.Fprintf(w, "route     %s: %s -> %s\n", rt.ID, rt.From, rt.To)
			} else {
				fmt.Fprintf(w, "route     %s: -> %s\n", rt.ID, rt.To)
			}
		}
	}
	svc := r.ServiceState
	if r.ServiceRestart != "" {
		svc += ", restarts " + r.ServiceRestart
	}
	fmt.Fprintf(w, "service   %s: %s\n", r.Service, svc)
	switch {
	case r.MetricsError != "":
		fmt.Fprintf(w, "metrics   unavailable: %s\n", r.MetricsError)
	case r.Metrics != nil:
		m := r.Metrics
		fmt.Fprintf(w, "flows     %d active\n", int64(m["baft_active_flows"]))
		fmt.Fprintf(w, "recovery  epoch %d, %d attempts, %d commits, %d failures\n",
			int64(m["baft_recovery_current_epoch"]), int64(m["baft_recovery_attempts_total"]),
			int64(m["baft_recovery_commits_total"]), int64(m["baft_recovery_failures_total"]))
		fmt.Fprintf(w, "integrity %d invariant violations\n", int64(m["baft_conservation_invariant_violations"]))
	}
}

// ---- doctor ----

const (
	checkOK   = "ok"
	checkInfo = "info"
	checkWarn = "warn"
	checkFail = "fail"
)

type check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

type doctor struct {
	env    opsEnv
	checks []check
}

func (d *doctor) add(name, status, detail, hint string) {
	d.checks = append(d.checks, check{Name: name, Status: status, Detail: detail, Hint: hint})
}

func runDoctor(args []string, stdout, stderr io.Writer, env opsEnv) int {
	f, ok := parseOpsFlags("doctor", args, stderr, nil)
	if !ok {
		fmt.Fprintln(stderr, "usage: baft doctor [--file baft.yaml] [--service baft] [--release-state path] [--json]")
		return 2
	}
	d := &doctor{env: env}
	cfg, err := config.LoadFile(f.file)
	if err != nil {
		d.add("config", checkFail, f.file+": "+err.Error(), "fix the file, then run: baft config validate --file "+f.file)
	} else {
		d.add("config", checkOK, f.file+" is valid", "")
		d.checkRevocations(cfg)
		d.checkKeys(cfg)
	}
	d.checkService(f.service)
	d.checkRelease(f.releaseState)
	if err == nil {
		d.checkMetrics(cfg)
		d.checkReachability(cfg)
	}
	d.checkNetwork()

	failed := false
	for _, c := range d.checks {
		failed = failed || c.Status == checkFail
	}
	if f.json {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(struct {
			OK     bool    `json:"ok"`
			Checks []check `json:"checks"`
		}{!failed, d.checks})
	} else {
		for _, c := range d.checks {
			fmt.Fprintf(stdout, "%-5s %-22s %s\n", strings.ToUpper(c.Status), c.Name, c.Detail)
			if c.Hint != "" {
				fmt.Fprintf(stdout, "      %-22s -> %s\n", "", c.Hint)
			}
		}
	}
	if failed {
		return 1
	}
	return 0
}

func (d *doctor) checkRevocations(cfg config.Config) {
	if cfg.Revocation == nil {
		return
	}
	if _, err := config.LoadRevocationFile(cfg.Revocation.File); err != nil {
		d.add("revocation file", checkFail, err.Error(), "the listener refuses to start with an invalid revocation file")
		return
	}
	d.add("revocation file", checkOK, cfg.Revocation.File+" is valid", "")
}

// The runtime refuses a private key that group or other can access.
func (d *doctor) checkKeys(cfg config.Config) {
	var keys []string
	if cfg.Noise != nil && cfg.Noise.KeyFile != "" {
		keys = append(keys, cfg.Noise.KeyFile)
	}
	if cfg.TLS.KeyFile != "" {
		keys = append(keys, cfg.TLS.KeyFile)
	}
	for _, k := range keys {
		st, err := os.Stat(k)
		switch {
		case err != nil:
			d.add("private key", checkFail, err.Error(), "")
		case st.Mode().Perm()&0o077 != 0:
			d.add("private key", checkFail, fmt.Sprintf("%s has mode %04o", k, st.Mode().Perm()), "chmod 0600 "+k+" (owned by the service user)")
		default:
			d.add("private key", checkOK, k+" is owner-only", "")
		}
	}
}

func (d *doctor) checkService(service string) {
	state, restarts := d.env.serviceState(service)
	switch {
	case state == "active":
		detail := service + " is active"
		if restarts != "" && restarts != "0" {
			d.add("service", checkWarn, detail+", restarted "+restarts+" times", "baft logs --service "+service)
			return
		}
		d.add("service", checkOK, detail, "")
	case strings.HasPrefix(state, "unknown"):
		d.add("service", checkWarn, service+": "+state, "")
	default:
		d.add("service", checkFail, service+" is "+state, "baft logs --service "+service)
	}
}

func (d *doctor) checkRelease(path string) {
	rs, err := readReleaseState(path)
	if errors.Is(err, os.ErrNotExist) {
		d.add("release", checkWarn, "no installer release state at "+path+" (source install?)",
			"production servers should be installed from a signed release")
		return
	}
	if err != nil {
		d.add("release", checkFail, err.Error(), "")
		return
	}
	if strings.TrimPrefix(rs.Version, "v") != version {
		d.add("release", checkWarn, fmt.Sprintf("installed release %s but the binary reports %s", rs.Version, version),
			"reinstall the release with install.sh")
		return
	}
	d.add("release", checkOK, fmt.Sprintf("signed release %s (commit %.12s)", rs.Version, rs.Commit), "")
}

func (d *doctor) checkMetrics(cfg config.Config) {
	m, err := d.env.metrics(cfg)
	if err != nil {
		d.add("metrics", checkWarn, "endpoint unreachable: "+err.Error(), "is the service running?")
		return
	}
	d.add("metrics", checkOK, fmt.Sprintf("%d active flows", int64(m["baft_active_flows"])), "")
	if v := int64(m["baft_conservation_invariant_violations"]); v != 0 {
		d.add("integrity", checkFail, fmt.Sprintf("%d conservation invariant violations", v), "collect `baft logs` and report it")
	}
}

// loopback turns a wildcard listen address into one this host can dial.
func loopback(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return net.JoinHostPort(host, port)
}

func (d *doctor) checkReachability(cfg config.Config) {
	const timeout = 3 * time.Second
	if cfg.Peer != nil {
		if err := d.env.dial(cfg.Peer.Address, timeout); err != nil {
			d.add("peer", checkFail, cfg.Peer.Address+" unreachable: "+err.Error(), "check the EX address, its firewall and that its service is up")
		} else {
			d.add("peer", checkOK, cfg.Peer.Address+" accepts TCP", "")
		}
	}
	if cfg.Server != nil {
		if err := d.env.dial(loopback(cfg.Server.Listen), timeout); err != nil {
			d.add("listener", checkFail, cfg.Server.Listen+" is not accepting: "+err.Error(), "is the service running?")
		} else {
			d.add("listener", checkOK, cfg.Server.Listen+" accepts TCP", "")
		}
	}
	for _, rt := range cfg.Routes {
		switch {
		case rt.Direction == "outbound" && rt.Listen != "":
			if err := d.env.dial(rt.Listen, timeout); err != nil {
				d.add("route "+rt.ID, checkWarn, rt.Listen+" is not accepting: "+err.Error(), "the route listens once the carrier is up")
			} else {
				d.add("route "+rt.ID, checkOK, rt.Listen+" accepts local clients", "")
			}
		case rt.Direction == "inbound" && rt.Target != "":
			if err := d.env.dial(rt.Target, timeout); err != nil {
				d.add("route "+rt.ID, checkFail, "target "+rt.Target+" unreachable: "+err.Error(), "start the service BAFT forwards to")
			} else {
				d.add("route "+rt.ID, checkOK, "target "+rt.Target+" accepts TCP", "")
			}
		}
	}
}

func (d *doctor) sysctl(name string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(d.env.procRoot, "sys", strings.ReplaceAll(name, ".", "/")))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// checkNetwork is a read-only diagnosis: it recommends, it never changes.
func (d *doctor) checkNetwork() {
	if cc, ok := d.sysctl("net.ipv4.tcp_congestion_control"); ok {
		if cc == "bbr" {
			d.add("tcp congestion", checkOK, "bbr", "")
		} else {
			d.add("tcp congestion", checkInfo, cc+" (bbr usually does better on long, lossy paths)", "sysctl -w net.ipv4.tcp_congestion_control=bbr")
		}
	}
	if q, ok := d.sysctl("net.core.default_qdisc"); ok {
		if q == "fq" {
			d.add("qdisc", checkOK, "fq", "")
		} else {
			d.add("qdisc", checkInfo, q+" (fq pairs with bbr pacing)", "sysctl -w net.core.default_qdisc=fq")
		}
	}
	const wantBuf = 4 << 20
	for _, name := range []string{"net.core.rmem_max", "net.core.wmem_max"} {
		v, ok := d.sysctl(name)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(v)
		if err == nil && n < wantBuf {
			d.add(name, checkInfo, fmt.Sprintf("%d bytes (high-latency paths need larger socket buffers)", n),
				fmt.Sprintf("sysctl -w %s=%d", name, wantBuf))
		} else if err == nil {
			d.add(name, checkOK, fmt.Sprintf("%d bytes", n), "")
		}
	}
}

// ---- logs ----

func runLogs(args []string, stdout, stderr io.Writer, env opsEnv) int {
	var lines int
	var follow bool
	f, ok := parseOpsFlags("logs", args, stderr, func(fs *flag.FlagSet) {
		fs.IntVar(&lines, "n", 100, "number of lines")
		fs.BoolVar(&follow, "f", false, "follow")
	})
	if !ok || lines < 0 || f.json {
		fmt.Fprintln(stderr, "usage: baft logs [--service baft] [-n 100] [-f]")
		return 2
	}
	jargs := []string{"-u", f.service, "-n", strconv.Itoa(lines), "--no-pager", "-o", "short-iso"}
	if follow {
		jargs = append(jargs, "-f")
	}
	if err := env.stream("journalctl", jargs, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "baft logs:", err)
		return 1
	}
	return 0
}
