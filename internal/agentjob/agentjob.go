// Package agentjob defines the jobs BCC hands to node agents and how an
// agent decides to run one (Launch-1 P1-D).
//
// BCC signs every job with its job-signing Ed25519 key; an agent pins that
// public key at enrollment. An agent runs a job only if the signature
// verifies against the pinned key, the job names this node, the action is on
// the fixed allowlist with valid parameters, the job is inside its short
// validity window, and its ID has not been seen before. There is no shell or
// free-form command action: a compromised BCC can only ask for allowlisted
// operations, and nobody else can ask for anything.
package agentjob

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/zarkmakerburg/baft/internal/release"
)

const (
	PayloadType   = "application/vnd.baft.agent-job+json"
	SchemaVersion = 1
	// MaxLifetime bounds how long a signed job stays runnable.
	MaxLifetime = 24 * time.Hour
	// ClockSkew tolerates an agent clock slightly behind BCC's.
	ClockSkew = 5 * time.Minute
	// maxParamLen bounds any one parameter (pairing codes are the largest).
	maxParamLen = 8192
)

// Actions an agent can run. Anything else is refused.
const (
	ActionHealth     = "health"
	ActionRestart    = "restart"
	ActionReload     = "reload"
	ActionUpdateBAFT = "update_baft"

	// Tunnel changes (P1-E). prepare stages, commit installs, health checks,
	// finalize makes permanent, rollback restores the previous state.
	ActionTunnelPrepareEX = "tunnel_prepare_ex"
	ActionTunnelPrepareIR = "tunnel_prepare_ir"
	ActionTunnelCommitEX  = "tunnel_commit_ex"
	ActionTunnelCommitIR  = "tunnel_commit_ir"
	ActionTunnelHealth    = "tunnel_health"
	ActionTunnelObserve   = "tunnel_observe"
	ActionTunnelFinalize  = "tunnel_finalize"
	ActionTunnelRollback  = "tunnel_rollback"
)

var (
	idRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	versionRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

	tunnelIDRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	addressRe       = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.:-]{0,251}[A-Za-z0-9])?$`)
	portRe          = regexp.MustCompile(`^[0-9]{1,5}$`)
	fixedTargetR    = regexp.MustCompile(`^(([0-9]{1,3}\.){3}[0-9]{1,3}|\[[0-9A-Fa-f:]+\]):[0-9]{1,5}$`)
	loopbackListenR = regexp.MustCompile(`^(127\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|\[::1\]):[0-9]{1,5}$`)
	boolRe          = regexp.MustCompile(`^(true|false)$`)
	pairCodeRe      = regexp.MustCompile(`^BAFTPAIR1:[A-Za-z0-9_-]{16,}$`)
	replyCodeRe     = regexp.MustCompile(`^BAFTREPLY1:[A-Za-z0-9_-]{16,}$`)
)

// paramRules lists, per action, the parameters it takes and how each is
// checked. A job with a missing, extra or malformed parameter is refused.
var paramRules = map[string]map[string]*regexp.Regexp{
	ActionHealth:     {},
	ActionRestart:    {},
	ActionReload:     {},
	ActionUpdateBAFT: {"version": versionRe},

	ActionTunnelPrepareEX: {"tunnel_id": tunnelIDRe, "public_address": addressRe, "port": portRe, "target": fixedTargetR, "route_id": tunnelIDRe, "record_shaping": boolRe},
	ActionTunnelPrepareIR: {"tunnel_id": tunnelIDRe, "code": pairCodeRe, "route_listen": loopbackListenR, "route_id": tunnelIDRe},
	ActionTunnelCommitEX:  {"tunnel_id": tunnelIDRe, "reply": replyCodeRe},
	ActionTunnelCommitIR:  {"tunnel_id": tunnelIDRe},
	ActionTunnelHealth:    {"tunnel_id": tunnelIDRe},
	ActionTunnelObserve:   {"tunnel_id": tunnelIDRe},
	ActionTunnelFinalize:  {"tunnel_id": tunnelIDRe},
	ActionTunnelRollback:  {"tunnel_id": tunnelIDRe},
}

// Actions returns the allowlist, sorted.
func Actions() []string {
	out := make([]string, 0, len(paramRules))
	for a := range paramRules {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

type Job struct {
	SchemaVersion int               `json:"schema_version"`
	JobID         string            `json:"job_id"`
	NodeID        string            `json:"node_id"`
	Action        string            `json:"action"`
	Params        map[string]string `json:"params,omitempty"`
	IssuedAt      time.Time         `json:"issued_at"`
	ExpiresAt     time.Time         `json:"expires_at"`
}

// Validate checks a job's shape: IDs, allowlisted action, exact parameters
// and validity window. It does not check time against a clock.
func (j Job) Validate() error {
	if j.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported job schema %d", j.SchemaVersion)
	}
	if !idRe.MatchString(j.JobID) || !idRe.MatchString(j.NodeID) {
		return errors.New("malformed job or node id")
	}
	rules, ok := paramRules[j.Action]
	if !ok {
		return fmt.Errorf("action %q is not allowed", j.Action)
	}
	for k, v := range j.Params {
		re, ok := rules[k]
		if !ok {
			return fmt.Errorf("action %s does not take parameter %q", j.Action, k)
		}
		if len(v) > maxParamLen || !re.MatchString(v) {
			return fmt.Errorf("parameter %s is malformed", k)
		}
	}
	for k := range rules {
		if _, ok := j.Params[k]; !ok {
			return fmt.Errorf("action %s needs parameter %q", j.Action, k)
		}
	}
	if !j.ExpiresAt.After(j.IssuedAt) || j.ExpiresAt.Sub(j.IssuedAt) > MaxLifetime {
		return errors.New("invalid job validity window")
	}
	return nil
}

// Sign has BCC sign a job after checking it.
func Sign(key ed25519.PrivateKey, j Job) (release.Envelope, error) {
	j.SchemaVersion = SchemaVersion
	j.IssuedAt = j.IssuedAt.UTC().Truncate(time.Second)
	j.ExpiresAt = j.ExpiresAt.UTC().Truncate(time.Second)
	if err := j.Validate(); err != nil {
		return release.Envelope{}, err
	}
	payload, err := json.Marshal(j)
	if err != nil {
		return release.Envelope{}, err
	}
	return release.Sign(PayloadType, payload, key), nil
}

// Verifier is the agent side.
type Verifier struct {
	BCCKey ed25519.PublicKey // pinned at enrollment
	NodeID string            // this node
	// Seen reports whether a job ID was already accepted; Verify does not
	// record it, so the caller records only jobs it actually runs.
	Seen func(jobID string) bool
}

// Verify returns the job if this agent may run it now.
func (v Verifier) Verify(env release.Envelope, now time.Time) (Job, error) {
	var j Job
	payload, err := env.Open(PayloadType, v.BCCKey)
	if err != nil {
		return j, fmt.Errorf("job signature: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		return j, fmt.Errorf("job payload: %w", err)
	}
	if dec.More() {
		return j, errors.New("job payload: trailing data")
	}
	if err := j.Validate(); err != nil {
		return j, err
	}
	if j.NodeID != v.NodeID {
		return j, fmt.Errorf("job is for node %s, not this node", j.NodeID)
	}
	if j.IssuedAt.After(now.Add(ClockSkew)) {
		return j, errors.New("job is issued in the future")
	}
	if !now.Before(j.ExpiresAt) {
		return j, errors.New("job has expired")
	}
	if v.Seen != nil && v.Seen(j.JobID) {
		return j, fmt.Errorf("job %s was already run", j.JobID)
	}
	return j, nil
}
