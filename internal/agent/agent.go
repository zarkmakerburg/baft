// Package agent is the node side of BCC operations (Launch-1 P1-D): it pulls
// jobs, runs only those agentjob.Verifier accepts, and reports the outcome.
//
// Updates install only signed releases that verify against the pinned
// release root (never the BCC key), keep the previous binaries, restart the
// service, and roll back if the service does not come up.
package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zarkmakerburg/baft/internal/agentjob"
	"github.com/zarkmakerburg/baft/internal/release"
	"github.com/zarkmakerburg/baft/internal/tunnelnode"
)

// System is how the agent touches the host; tests replace it.
type System interface {
	// Systemctl runs `systemctl <args>` and returns its trimmed output.
	Systemctl(ctx context.Context, args ...string) (string, error)
	// Run runs a binary with arguments and returns combined output.
	Run(ctx context.Context, name string, args ...string) (string, error)
}

type Config struct {
	BCCURL    string // e.g. https://bcc.example.com
	NodeID    string
	Token     string            // agent bearer token
	BCCJobKey ed25519.PublicKey // pinned at enrollment
	StateDir  string            // seen-job log
	Service   string            // systemd unit, e.g. "baft"

	// Updates.
	ReleaseRoot    ed25519.PublicKey // pinned release root
	ReleaseBaseURL string            // assets at <base>/<version>/<file>
	RevocationsURL string
	ReleaseState   string // installer trust state, e.g. /opt/baft/release-state.json
	BinDir         string // where baft and baft-pair live, e.g. /usr/local/bin
	Arch           string // defaults to runtime.GOARCH
	// SettleTime is how long the service must stay active after an update.
	SettleTime time.Duration

	// Tunnel runs tunnel_* jobs; nil refuses them.
	Tunnel *tunnelnode.Manager

	HTTP   *http.Client
	System System
	Now    func() time.Time
}

type Agent struct {
	cfg            Config
	mu             sync.Mutex
	seen           map[string]time.Time
	probeMu        sync.Mutex
	probeListeners map[string]*pathProbeListener
	// unacked holds results BCC did not receive (network error, rate limit,
	// BCC restarting). They are sent again on the next poll so a finished
	// step does not wait for BCC's step timeout. Memory only: an output may
	// be a one-time secret.
	unacked []pendingAck
}

type pendingAck struct {
	jobID, status, message, output string
	tries                          int
}

// maxAckTries bounds how often one result is offered again.
const maxAckTries = 30

// errAckRefused is BCC answering that it will not take the result (the job
// is unknown or already completed): offering it again cannot help.
var errAckRefused = errors.New("BCC refused the result")

const seenRetention = 7 * 24 * time.Hour

func New(cfg Config) (*Agent, error) {
	if cfg.BCCURL == "" || cfg.NodeID == "" || cfg.Token == "" || len(cfg.BCCJobKey) != ed25519.PublicKeySize || cfg.StateDir == "" {
		return nil, errors.New("agent needs BCC URL, node ID, token, pinned BCC job key and state dir")
	}
	if cfg.Service == "" {
		cfg.Service = "baft"
	}
	if cfg.Arch == "" {
		cfg.Arch = runtime.GOARCH
	}
	if cfg.SettleTime == 0 {
		cfg.SettleTime = 5 * time.Second
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 60 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	a := &Agent{cfg: cfg, seen: map[string]time.Time{}, probeListeners: map[string]*pathProbeListener{}}
	if err := a.loadSeen(); err != nil {
		return nil, err
	}
	return a, nil
}

// ---- seen jobs ----

func (a *Agent) seenPath() string { return filepath.Join(a.cfg.StateDir, "seen-jobs.json") }

func (a *Agent) loadSeen() error {
	b, err := os.ReadFile(a.seenPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &a.seen)
}

// markSeen records a job before it runs, so a crash mid-run cannot replay it.
func (a *Agent) markSeen(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.cfg.Now()
	for k, t := range a.seen {
		if now.Sub(t) > seenRetention {
			delete(a.seen, k)
		}
	}
	a.seen[id] = now
	b, err := json.Marshal(a.seen)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.cfg.StateDir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(a.cfg.StateDir, ".seen-jobs-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.seenPath()); err != nil {
		return err
	}
	dir, err := os.Open(a.cfg.StateDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (a *Agent) isSeen(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.seen[id]
	return ok
}

// ---- BCC API ----

type pulledJob struct {
	ID     string            `json:"id"`
	Signed *release.Envelope `json:"signed"`
}

func (a *Agent) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.cfg.BCCURL, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	return a.cfg.HTTP.Do(req)
}

func (a *Agent) pull(ctx context.Context) ([]pulledJob, error) {
	resp, err := a.request(ctx, http.MethodGet, "/api/agent/jobs?node_id="+url.QueryEscape(a.cfg.NodeID), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pull jobs: HTTP %d", resp.StatusCode)
	}
	var jobs []pulledJob
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&jobs)
	return jobs, err
}

func (a *Agent) ack(ctx context.Context, jobID, status, message, output string) error {
	if len(message) > 2000 {
		message = message[:2000]
	}
	if len(output) > maxOutput {
		output = ""
		status, message = "failed", "job output too large"
	}
	body := map[string]string{"NodeID": a.cfg.NodeID, "JobID": jobID, "Status": status, "Message": message}
	if output != "" {
		body["Output"] = output
	}
	resp, err := a.request(ctx, http.MethodPost, "/api/agent/ack", body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("ack %s: HTTP %d: %w", jobID, resp.StatusCode, errAckRefused)
	}
	return fmt.Errorf("ack %s: HTTP %d", jobID, resp.StatusCode)
}

// flushAcks offers the results BCC has not received yet again.
func (a *Agent) flushAcks(ctx context.Context) {
	var keep []pendingAck
	for _, p := range a.unacked {
		err := a.ack(ctx, p.jobID, p.status, p.message, p.output)
		if err == nil || errors.Is(err, errAckRefused) || p.tries+1 >= maxAckTries {
			continue
		}
		p.tries++
		keep = append(keep, p)
	}
	a.unacked = keep
}

// Result is one job's outcome, for logs and tests.
type Result struct {
	JobID  string
	Action string
	Status string // succeeded, failed, refused
	Detail string
}

// RunOnce pulls and handles the pending jobs once.
func (a *Agent) RunOnce(ctx context.Context) ([]Result, error) {
	a.flushAcks(ctx)
	jobs, err := a.pull(ctx)
	if err != nil {
		return nil, err
	}
	v := agentjob.Verifier{BCCKey: a.cfg.BCCJobKey, NodeID: a.cfg.NodeID, Seen: a.isSeen}
	var out []Result
	for _, pj := range jobs {
		r := Result{JobID: pj.ID}
		if pj.Signed == nil {
			r.Status, r.Detail = "refused", "unsigned job"
			out = append(out, r)
			_ = a.ack(ctx, pj.ID, "failed", "refused by agent: unsigned job", "")
			continue
		}
		job, err := v.Verify(*pj.Signed, a.cfg.Now())
		if err != nil {
			r.Status, r.Detail = "refused", err.Error()
			out = append(out, r)
			// Only acknowledge a job ID the signed payload agrees with.
			if job.JobID == pj.ID && job.NodeID == a.cfg.NodeID {
				_ = a.ack(ctx, pj.ID, "failed", "refused by agent: "+err.Error(), "")
			}
			continue
		}
		r.Action = job.Action
		if err := a.markSeen(job.JobID); err != nil {
			r.Status, r.Detail = "refused", "cannot record job: "+err.Error()
			out = append(out, r)
			continue
		}
		detail, output, err := a.executeFull(ctx, job)
		r.Status, r.Detail = "succeeded", detail
		if err != nil {
			r.Status, r.Detail, output = "failed", err.Error(), ""
		}
		if aerr := a.ack(ctx, job.JobID, r.Status, r.Detail, output); aerr != nil {
			if !errors.Is(aerr, errAckRefused) {
				a.unacked = append(a.unacked, pendingAck{jobID: job.JobID, status: r.Status, message: r.Detail, output: output})
			}
			if err == nil {
				r.Detail += " (ack failed: " + aerr.Error() + "; offered again next poll)"
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// Run polls BCC until ctx ends.
func (a *Agent) Run(ctx context.Context, interval time.Duration, logf func(string, ...any)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		results, err := a.RunOnce(ctx)
		if err != nil {
			logf("agent: %v", err)
		}
		for _, r := range results {
			logf("agent: job %s %s: %s %s", r.JobID, r.Action, r.Status, r.Detail)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---- actions ----

func (a *Agent) execute(ctx context.Context, j agentjob.Job) (string, error) {
	switch j.Action {
	case agentjob.ActionHealth:
		out, err := a.cfg.System.Run(ctx, filepath.Join(a.cfg.BinDir, "baft"), "doctor", "--service", a.cfg.Service)
		if err != nil {
			return "", fmt.Errorf("baft doctor: %v\n%s", err, out)
		}
		return out, nil
	case agentjob.ActionRestart, agentjob.ActionReload:
		if _, err := a.cfg.System.Systemctl(ctx, j.Action, a.cfg.Service); err != nil {
			return "", fmt.Errorf("systemctl %s %s: %w", j.Action, a.cfg.Service, err)
		}
		return a.waitActive(ctx)
	case agentjob.ActionUpdateBAFT:
		return a.update(ctx, j.Params["version"])
	}
	return "", fmt.Errorf("action %s has no handler", j.Action)
}

func (a *Agent) waitActive(ctx context.Context) (string, error) {
	timer := time.NewTimer(a.cfg.SettleTime)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
	}
	state, err := a.cfg.System.Systemctl(ctx, "is-active", a.cfg.Service)
	if err != nil || state != "active" {
		return "", fmt.Errorf("%s is %q after %s", a.cfg.Service, state, a.cfg.SettleTime)
	}
	return a.cfg.Service + " is active", nil
}

func (a *Agent) download(ctx context.Context, rawURL, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := a.cfg.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", rawURL, resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 512<<20)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// update installs a signed release: verify against the release root, the
// current revocation list and the trust state; keep the old binaries; swap;
// restart; roll back if the service does not stay up; record the release.
func (a *Agent) update(ctx context.Context, version string) (string, error) {
	if len(a.cfg.ReleaseRoot) != ed25519.PublicKeySize || a.cfg.ReleaseBaseURL == "" || a.cfg.RevocationsURL == "" || a.cfg.ReleaseState == "" || a.cfg.BinDir == "" {
		return "", errors.New("updates are not configured on this agent")
	}
	work, err := os.MkdirTemp("", "baft-update-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	dir := filepath.Join(work, "release")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	bins := map[string]string{"baft": "baft-linux-" + a.cfg.Arch, "baft-pair": "baft-pair-linux-" + a.cfg.Arch}
	artifacts := []string{bins["baft"], bins["baft-pair"]}
	base := strings.TrimRight(a.cfg.ReleaseBaseURL, "/") + "/" + url.PathEscape(version) + "/"
	for _, f := range append([]string{release.ManifestFile, release.CertFile, release.SumsFile}, artifacts...) {
		if err := a.download(ctx, base+f, filepath.Join(dir, f)); err != nil {
			return "", err
		}
	}
	revPath := filepath.Join(work, "revocations.json")
	if err := a.download(ctx, a.cfg.RevocationsURL, revPath); err != nil {
		return "", err
	}
	rev, err := release.ReadEnvelope(revPath)
	if err != nil {
		return "", fmt.Errorf("revocation list: %w", err)
	}
	state, err := release.ReadState(a.cfg.ReleaseState)
	if err != nil {
		return "", err
	}
	verified, err := release.VerifyDir(release.VerifyInput{
		Dir: dir, Root: a.cfg.ReleaseRoot, Revocations: &rev, State: &state, Only: artifacts, Now: a.cfg.Now(),
	})
	if err != nil {
		return "", fmt.Errorf("release %s did not verify; nothing changed: %w", version, err)
	}
	if verified.Version != version {
		return "", fmt.Errorf("asked for %s but the signed release is %s; nothing changed", version, verified.Version)
	}

	// Stage next to the targets, keep the current binaries, then swap.
	names := []string{"baft", "baft-pair"}
	sort.Strings(names)
	for _, n := range names {
		staged := filepath.Join(a.cfg.BinDir, "."+n+".new")
		if err := copyFile(filepath.Join(dir, bins[n]), staged, 0o755); err != nil {
			return "", err
		}
	}
	var swapped []string
	rollback := func() {
		for _, n := range swapped {
			target := filepath.Join(a.cfg.BinDir, n)
			_ = os.Rename(target+".prev", target)
		}
	}
	for _, n := range names {
		target := filepath.Join(a.cfg.BinDir, n)
		if err := copyFile(target, target+".prev", 0o755); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollback()
			return "", err
		}
		if err := os.Rename(filepath.Join(a.cfg.BinDir, "."+n+".new"), target); err != nil {
			rollback()
			return "", err
		}
		swapped = append(swapped, n)
	}
	if _, err := a.cfg.System.Systemctl(ctx, "restart", a.cfg.Service); err == nil {
		if _, err = a.waitActive(ctx); err == nil {
			if err := release.WriteState(a.cfg.ReleaseState, state.Advance(verified, a.cfg.Now())); err != nil {
				return "", fmt.Errorf("installed %s but could not record it: %w", version, err)
			}
			return fmt.Sprintf("updated to %s (commit %.12s); previous binaries kept as .prev", verified.Version, verified.Commit), nil
		}
	}
	rollback()
	_, _ = a.cfg.System.Systemctl(ctx, "restart", a.cfg.Service)
	if _, err := a.waitActive(ctx); err != nil {
		return "", fmt.Errorf("update to %s failed and the rolled-back service is not healthy either: %w", version, err)
	}
	return "", fmt.Errorf("update to %s did not come up; rolled back to the previous binaries", version)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
