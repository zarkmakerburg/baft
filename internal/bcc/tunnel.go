package bcc

// Native tunnel builder (Launch-1 P1-E): BCC plans an IR/EX tunnel between
// two enrolled servers and drives both agents through
//
//	prepare EX -> prepare IR -> commit EX -> commit IR -> health IR/EX -> finalize
//
// with signed jobs only. A node keeps its previous config until the change is
// finalized, so any failure (or an operator cancel) rolls both sides back.
// Secrets (the pairing code and the IR's reply) pass through job params and
// outputs only while a step needs them and are wiped as soon as it ends.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zarkmakerburg/baft/internal/agentjob"
	"github.com/zarkmakerburg/baft/internal/tunnelnode"
)

// Tunnel job types are the agent actions of the same name.
const (
	JobTunnelPrepareEX = agentjob.ActionTunnelPrepareEX
	JobTunnelPrepareIR = agentjob.ActionTunnelPrepareIR
	JobTunnelCommitEX  = agentjob.ActionTunnelCommitEX
	JobTunnelCommitIR  = agentjob.ActionTunnelCommitIR
	JobTunnelHealth    = agentjob.ActionTunnelHealth
	JobTunnelObserve   = agentjob.ActionTunnelObserve
	JobTunnelInspect   = agentjob.ActionTunnelInspect
	JobTunnelDiscover  = agentjob.ActionTunnelDiscover
	JobTunnelFinalize  = agentjob.ActionTunnelFinalize
	JobTunnelRollback  = agentjob.ActionTunnelRollback
)

// Tunnel phases. active, superseded, rolled_back and rollback_failed are final.
const (
	TunnelPreparingEX    = "preparing_ex"
	TunnelPreparingIR    = "preparing_ir"
	TunnelCommittingEX   = "committing_ex"
	TunnelCommittingIR   = "committing_ir"
	TunnelHealthIR       = "health_ir"
	TunnelHealthEX       = "health_ex"
	TunnelObservingIR    = "observing_ir"
	TunnelObservingEX    = "observing_ex"
	TunnelFinalizingIR   = "finalizing_ir"
	TunnelFinalizingEX   = "finalizing_ex"
	TunnelActive         = "active"
	TunnelSuperseded     = "superseded"
	TunnelRollingBack    = "rolling_back"
	TunnelRolledBack     = "rolled_back"
	TunnelRollbackFailed = "rollback_failed"
)

const (
	tunnelStepTimeout     = 20 * time.Minute
	tunnelRollbackTimeout = 2 * time.Hour
	tunnelHealthAttempts  = 5
)

// Tunnel is one planned or running change.
type Tunnel struct {
	ID            string `json:"id"`
	EXNode        string `json:"ex_node"`
	IRNode        string `json:"ir_node"`
	PublicAddress string `json:"public_address"`
	Port          int    `json:"port"`
	Target        string `json:"target"`
	RouteID       string `json:"route_id"`
	RouteListen   string `json:"route_listen"`
	RecordShaping bool   `json:"record_shaping,omitempty"`

	PlanHash string           `json:"plan_hash,omitempty"`
	Evidence []TunnelEvidence `json:"evidence,omitempty"`
	// ExpectedGen is the generation change the reviewed plan promised each
	// node; observation must match it.
	ExpectedGen map[string]GenExpect `json:"expected_generation,omitempty"`
	// ObservedGen is the generation each node proved it runs, applied to the
	// node records only when the tunnel becomes active.
	ObservedGen map[string]int `json:"observed_generation,omitempty"`
	// Digests is BCC's own reference for drift detection: the SHA-256 of the
	// config, unit and ownership marker each node reported when BCC verified
	// the change. Nothing a node says later is compared with a node-held hash.
	Digests map[string]NodeDigests `json:"digests,omitempty"`

	// Drift is the result of the last drift check (active tunnels only);
	// DriftJobs are the inspect jobs of a check in progress.
	Drift        *DriftReport `json:"drift,omitempty"`
	DriftJobs    []string     `json:"drift_jobs,omitempty"`
	DriftStarted time.Time    `json:"drift_started,omitempty"`

	// CertEpoch is the certificate generation of an active tunnel: the epoch
	// of the last completed certificate rotation (0 = pairing certificate).
	CertEpoch  int    `json:"cert_epoch,omitempty"`
	CertSHA256 string `json:"cert_sha256,omitempty"`

	Phase          string    `json:"phase"`
	Error          string    `json:"error,omitempty"`
	JobID          string    `json:"job_id,omitempty"`
	RollbackJobs   []string  `json:"rollback_jobs,omitempty"`
	Jobs           []string  `json:"jobs,omitempty"`
	Touched        []string  `json:"touched,omitempty"`
	HealthAttempts int       `json:"health_attempts,omitempty"`
	StepStarted    time.Time `json:"step_started"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// TunnelEvidence is one piece of proof kept with the deployment: a health
// result or the observed state of a node and what was wrong with it.
type TunnelEvidence struct {
	Step     string    `json:"step"`
	Node     string    `json:"node"`
	At       time.Time `json:"at"`
	OK       bool      `json:"ok"`
	Detail   string    `json:"detail"`
	Problems []string  `json:"problems,omitempty"`
}

// TunnelEvent is a final outcome for the audit log.
type TunnelEvent struct {
	TunnelID string
	Action   string
	Detail   string
}

// TunnelRequest is what an operator asks for; zero fields take defaults.
type TunnelRequest struct {
	EXNode        string `json:"ex_node"`
	IRNode        string `json:"ir_node"`
	PublicAddress string `json:"public_address"`
	Port          int    `json:"port"`
	Target        string `json:"target"`
	RouteListen   string `json:"route_listen"`
	RouteID       string `json:"route_id"`
	RecordShaping bool   `json:"record_shaping"`
	// PlanHash, when set, must equal the hash of the plan BCC computes now.
	PlanHash string `json:"plan_hash,omitempty"`
}

func terminalTunnel(p string) bool {
	switch p {
	case TunnelActive, TunnelSuperseded, TunnelRolledBack, TunnelRollbackFailed:
		return true
	}
	return false
}

// secretParams are job parameters that must not outlive the job.
var secretParams = []string{"code", "reply"}

func wipeSecretParams(j *Job) {
	for _, k := range secretParams {
		if _, ok := j.Params[k]; ok {
			cp := make(map[string]string, len(j.Params))
			for pk, pv := range j.Params {
				if pk != k {
					cp[pk] = pv
				}
			}
			j.Params = cp
		}
	}
}

// publicJob is a job without secrets, for listings.
func publicJob(j Job) Job {
	wipeSecretParams(&j)
	j.Output = ""
	return j
}

func newTunnelID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "tun-" + hex.EncodeToString(b)
}

func (s *Store) tunnelJobLocked(t *Tunnel, node, typ string, params map[string]string, now time.Time) {
	if params == nil {
		params = map[string]string{}
	}
	params["tunnel_id"] = t.ID
	j := s.newJobLocked(Job{Type: typ, NodeID: node, Params: params})
	t.Jobs = append(t.Jobs, j.ID)
	t.JobID = j.ID
	t.StepStarted = now
	t.UpdatedAt = now
}

func touch(t *Tunnel, node string) {
	for _, n := range t.Touched {
		if n == node {
			return
		}
	}
	t.Touched = append(t.Touched, node)
}

// resolveTunnelLocked validates a request against the current state, applies
// the defaults and returns the tunnel it describes with both end nodes. It
// changes nothing: the plan and the real creation share it, so a plan can
// never accept what creation would refuse.
func (s *Store) resolveTunnelLocked(req TunnelRequest, now time.Time) (Tunnel, Node, Node, error) {
	ex, ok := s.st.Nodes[req.EXNode]
	if !ok {
		return Tunnel{}, Node{}, Node{}, fmt.Errorf("unknown EX node %q", req.EXNode)
	}
	ir, ok := s.st.Nodes[req.IRNode]
	if !ok {
		return Tunnel{}, Node{}, Node{}, fmt.Errorf("unknown IR node %q", req.IRNode)
	}
	if req.EXNode == req.IRNode {
		return Tunnel{}, Node{}, Node{}, errors.New("EX and IR must be different nodes")
	}
	if ex.Role != "foreign" {
		return Tunnel{}, Node{}, Node{}, errors.New("the EX end must be a foreign node")
	}
	if ir.Role != "worker" && ir.Role != "master" {
		return Tunnel{}, Node{}, Node{}, errors.New("the IR end must be a worker or master node")
	}
	if ex.Revoked || ir.Revoked {
		return Tunnel{}, Node{}, Node{}, errors.New("a revoked node cannot be part of a tunnel")
	}
	for _, other := range s.st.Tunnels {
		shares := other.EXNode == req.EXNode || other.IRNode == req.IRNode || other.EXNode == req.IRNode || other.IRNode == req.EXNode
		if shares && !terminalTunnel(other.Phase) {
			return Tunnel{}, Node{}, Node{}, fmt.Errorf("tunnel %s is still being built on one of these nodes", other.ID)
		}
		if shares && other.Phase == TunnelRollbackFailed {
			return Tunnel{}, Node{}, Node{}, fmt.Errorf("tunnel %s could not be rolled back on one of these nodes; fix that first", other.ID)
		}
	}
	if r := s.rotationOnNodesLocked(req.EXNode, req.IRNode); r != nil {
		return Tunnel{}, Node{}, Node{}, fmt.Errorf("certificate rotation %s is %s on one of these nodes", r.ID, r.Phase)
	}
	t := Tunnel{
		ID: newTunnelID(), EXNode: req.EXNode, IRNode: req.IRNode, PublicAddress: req.PublicAddress, Port: req.Port,
		Target: req.Target, RouteID: req.RouteID, RouteListen: req.RouteListen, RecordShaping: req.RecordShaping,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if t.PublicAddress == "" {
		host, _, err := net.SplitHostPort(ex.Address)
		if err != nil {
			host = ex.Address
		}
		t.PublicAddress = host
	}
	if t.Port == 0 {
		t.Port = 8443
	}
	if t.Target == "" {
		t.Target = "127.0.0.1:2443"
	}
	if t.RouteListen == "" {
		t.RouteListen = "127.0.0.1:1443"
	}
	if t.RouteID == "" {
		t.RouteID = "service-main"
	}
	// The same rules the agents apply, checked up front so a bad plan never
	// starts.
	exParams := t.prepareEXParams()
	probe := agentjob.Job{SchemaVersion: agentjob.SchemaVersion, JobID: "plan", NodeID: "plan", Action: JobTunnelPrepareEX,
		Params: exParams, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := probe.Validate(); err != nil {
		return Tunnel{}, Node{}, Node{}, fmt.Errorf("invalid tunnel plan: %w", err)
	}
	probe.Action = JobTunnelPrepareIR
	probe.Params = map[string]string{"tunnel_id": t.ID, "code": "BAFTPAIR1:AAAAAAAAAAAAAAAAAAAAAAAAAA", "route_listen": t.RouteListen, "route_id": t.RouteID}
	if err := probe.Validate(); err != nil {
		return Tunnel{}, Node{}, Node{}, fmt.Errorf("invalid tunnel plan: %w", err)
	}
	return t, ex, ir, nil
}

// ErrStalePlan is returned with the current plan when a reviewed plan hash no
// longer matches, or the plan no longer passes its gates.
type ErrStalePlan struct{ Current Plan }

func (ErrStalePlan) Error() string {
	return "the plan is stale or no longer passes its gates; review the new plan"
}

// CreateTunnel validates a request and queues the first step.
func (s *Store) CreateTunnel(req TunnelRequest, now time.Time) (Tunnel, error) {
	t, _, err := s.CreateTunnelFromPlan(req, "", now)
	return t, err
}

// CreateTunnelFromPlan checks the reviewed plan hash (when given) and creates
// the tunnel under one lock, so the state that was reviewed is the state it is
// created against. On a mismatch it creates nothing and returns ErrStalePlan
// carrying the fresh plan.
func (s *Store) CreateTunnelFromPlan(req TunnelRequest, planHash string, now time.Time) (Tunnel, Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if planHash != "" {
		cur, err := s.buildPlanLocked(req, now)
		if err != nil {
			return Tunnel{}, Plan{}, err
		}
		if !cur.OK || cur.Hash != planHash {
			return Tunnel{}, cur, ErrStalePlan{Current: cur}
		}
	}
	t, ex, ir, err := s.resolveTunnelLocked(req, now)
	if err != nil {
		return Tunnel{}, Plan{}, err
	}
	t.PlanHash = planHash
	t.ExpectedGen = map[string]GenExpect{ex.ID: expectedGeneration(ex), ir.ID: expectedGeneration(ir)}
	exParams := t.prepareEXParams()
	t.Phase = TunnelPreparingEX
	touch(&t, t.EXNode)
	s.tunnelJobLocked(&t, t.EXNode, JobTunnelPrepareEX, exParams, now)
	if s.st.Tunnels == nil {
		s.st.Tunnels = map[string]Tunnel{}
	}
	s.st.Tunnels[t.ID] = t
	return t, Plan{}, s.saveLocked()
}

func (t Tunnel) prepareEXParams() map[string]string {
	return map[string]string{
		"tunnel_id": t.ID, "public_address": t.PublicAddress, "port": strconv.Itoa(t.Port), "target": t.Target,
		"route_id": t.RouteID, "record_shaping": strconv.FormatBool(t.RecordShaping),
	}
}

func (s *Store) GetTunnel(id string) (Tunnel, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.st.Tunnels[id]
	return t, ok
}

func (s *Store) ListTunnels() []Tunnel {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Tunnel, 0, len(s.st.Tunnels))
	for _, t := range s.st.Tunnels {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// CancelTunnel rolls a change back on request. A tunnel that is already
// active is final: build a new change to alter it.
func (s *Store) CancelTunnel(id, reason string, now time.Time) (Tunnel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.st.Tunnels[id]
	if !ok {
		return Tunnel{}, errors.New("tunnel not found")
	}
	switch t.Phase {
	case TunnelRollingBack:
		return t, errors.New("tunnel is already rolling back")
	case TunnelActive, TunnelSuperseded, TunnelRolledBack:
		return t, fmt.Errorf("tunnel is %s; nothing to cancel", t.Phase)
	}
	t.Error = "cancelled by operator"
	if r := strings.TrimSpace(reason); r != "" {
		t.Error += ": " + r
	}
	s.startRollbackLocked(&t, now)
	s.st.Tunnels[id] = t
	return t, s.saveLocked()
}

// startRollbackLocked cancels steps nobody has picked up yet, drops secrets
// and asks every touched node to restore itself.
func (s *Store) startRollbackLocked(t *Tunnel, now time.Time) {
	// A node needs a rollback only if one of its jobs left the queue: a job
	// nobody picked up is simply cancelled and never runs.
	reached := map[string]bool{}
	for _, id := range t.Jobs {
		j := s.st.Jobs[id]
		if j.Status == "queued" {
			j.Status, j.Message, j.UpdatedAt = "failed", "cancelled: tunnel is rolling back", now.UTC()
		} else if j.Type != JobTunnelRollback {
			reached[j.NodeID] = true
		}
		wipeSecretParams(&j)
		j.Output = ""
		s.st.Jobs[id] = j
	}
	t.JobID, t.RollbackJobs = "", nil
	// IR first: stop the dialer before the listener goes away.
	var order []string
	for i := len(t.Touched) - 1; i >= 0; i-- {
		if reached[t.Touched[i]] {
			order = append(order, t.Touched[i])
		}
	}
	if len(order) == 0 {
		t.Phase, t.UpdatedAt = TunnelRolledBack, now.UTC()
		return
	}
	t.Phase = TunnelRollingBack
	for _, node := range order {
		j := s.newJobLocked(Job{Type: JobTunnelRollback, NodeID: node, Params: map[string]string{"tunnel_id": t.ID}})
		t.Jobs = append(t.Jobs, j.ID)
		t.RollbackJobs = append(t.RollbackJobs, j.ID)
	}
	t.StepStarted, t.UpdatedAt = now.UTC(), now.UTC()
}

func (s *Store) failLocked(t *Tunnel, reason string, now time.Time) {
	t.Error = reason
	s.startRollbackLocked(t, now)
}

// AdvanceTunnels moves every tunnel whose awaited job finished to its next
// step and returns the final outcomes reached.
func (s *Store) AdvanceTunnels(now time.Time) ([]TunnelEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var events []TunnelEvent
	changed := false
	ids := make([]string, 0, len(s.st.Tunnels))
	for id, t := range s.st.Tunnels {
		if t.Phase == TunnelActive || len(t.DriftJobs) > 0 {
			before := len(t.DriftJobs)
			var bd time.Time
			if t.Drift != nil {
				bd = t.Drift.CheckedAt
			}
			ev := s.advanceDriftLocked(&t, now.UTC())
			var ad time.Time
			if t.Drift != nil {
				ad = t.Drift.CheckedAt
			}
			if len(t.DriftJobs) != before || !ad.Equal(bd) || ev != nil {
				changed = true
				s.st.Tunnels[id] = t
			}
			if ev != nil {
				events = append(events, *ev)
			}
		}
		if !terminalTunnel(t.Phase) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		t := s.st.Tunnels[id]
		before := t.Phase + "|" + t.JobID + "|" + strconv.Itoa(len(t.Jobs))
		ev := s.advanceLocked(&t, now.UTC())
		if t.Phase+"|"+t.JobID+"|"+strconv.Itoa(len(t.Jobs)) != before || ev != nil {
			changed = true
			s.st.Tunnels[id] = t
		}
		if ev != nil {
			events = append(events, *ev)
		}
	}
	rotEvents, rotChanged := s.advanceRotationsLocked(now.UTC())
	events = append(events, rotEvents...)
	if !changed && !rotChanged {
		return nil, nil
	}
	return events, s.saveLocked()
}

func (s *Store) advanceLocked(t *Tunnel, now time.Time) *TunnelEvent {
	if t.Phase == TunnelRollingBack {
		return s.advanceRollbackLocked(t, now)
	}
	j, ok := s.st.Jobs[t.JobID]
	if !ok {
		s.failLocked(t, "internal error: step job missing", now)
		return nil
	}
	switch j.Status {
	case "queued", "dispatched":
		if now.Sub(t.StepStarted) > tunnelStepTimeout {
			s.failLocked(t, fmt.Sprintf("step %s timed out waiting for node %s", t.Phase, j.NodeID), now)
		}
		return nil
	case "failed":
		if t.Phase == TunnelHealthIR || t.Phase == TunnelHealthEX || t.Phase == TunnelObservingIR || t.Phase == TunnelObservingEX {
			step := "health"
			if t.Phase == TunnelObservingIR || t.Phase == TunnelObservingEX {
				step = "observe"
			}
			t.Evidence = append(t.Evidence, TunnelEvidence{Step: step, Node: j.NodeID, At: now, OK: false, Detail: j.Message})
		}
		if (t.Phase == TunnelHealthIR || t.Phase == TunnelHealthEX) && t.HealthAttempts+1 < tunnelHealthAttempts {
			t.HealthAttempts++
			s.tunnelJobLocked(t, j.NodeID, JobTunnelHealth, nil, now)
			return nil
		}
		if t.Phase == TunnelFinalizingIR || t.Phase == TunnelFinalizingEX {
			// Live and healthy; only the cleanup of backups failed.
			t.Error = "finalize incomplete on " + j.NodeID + ": " + j.Message
			return s.nextAfterFinalizeLocked(t, now)
		}
		s.failLocked(t, fmt.Sprintf("%s failed on %s: %s", t.Phase, j.NodeID, j.Message), now)
		return nil
	case "succeeded":
	default:
		return nil
	}
	secret := j.Output
	j.Output = ""
	s.st.Jobs[j.ID] = j
	switch t.Phase {
	case TunnelPreparingEX:
		if secret == "" {
			s.failLocked(t, "EX returned no pairing code", now)
			return nil
		}
		touch(t, t.IRNode)
		t.Phase = TunnelPreparingIR
		s.tunnelJobLocked(t, t.IRNode, JobTunnelPrepareIR, map[string]string{"code": secret, "route_listen": t.RouteListen, "route_id": t.RouteID}, now)
	case TunnelPreparingIR:
		if secret == "" {
			s.failLocked(t, "IR returned no reply code", now)
			return nil
		}
		t.Phase = TunnelCommittingEX
		s.tunnelJobLocked(t, t.EXNode, JobTunnelCommitEX, map[string]string{"reply": secret}, now)
	case TunnelCommittingEX:
		t.Phase = TunnelCommittingIR
		s.tunnelJobLocked(t, t.IRNode, JobTunnelCommitIR, nil, now)
	case TunnelCommittingIR:
		t.Phase = TunnelHealthIR
		t.HealthAttempts = 0
		s.tunnelJobLocked(t, t.IRNode, JobTunnelHealth, nil, now)
	case TunnelHealthIR:
		t.Evidence = append(t.Evidence, TunnelEvidence{Step: "health", Node: j.NodeID, At: now, OK: true, Detail: j.Message})
		t.Phase = TunnelHealthEX
		t.HealthAttempts = 0
		s.tunnelJobLocked(t, t.EXNode, JobTunnelHealth, nil, now)
	case TunnelHealthEX:
		t.Evidence = append(t.Evidence, TunnelEvidence{Step: "health", Node: j.NodeID, At: now, OK: true, Detail: j.Message})
		t.Phase = TunnelObservingIR
		s.tunnelJobLocked(t, t.IRNode, JobTunnelObserve, nil, now)
	case TunnelObservingIR, TunnelObservingEX:
		// Desired == observed, or the change is rolled back: "the command
		// succeeded" is not evidence, the node's own report is.
		role := tunnelnode.RoleIR
		if t.Phase == TunnelObservingEX {
			role = tunnelnode.RoleEX
		}
		var o tunnelnode.Observed
		var problems []string
		if err := json.Unmarshal([]byte(j.Message), &o); err != nil {
			problems = []string{"the node's observed state is unreadable"}
		} else {
			problems = verifyObserved(*t, j.NodeID, role, o)
		}
		t.Evidence = append(t.Evidence, TunnelEvidence{Step: "observe", Node: j.NodeID, At: now, OK: len(problems) == 0, Detail: j.Message, Problems: problems})
		if len(problems) > 0 {
			s.failLocked(t, fmt.Sprintf("observed state of %s differs from the plan: %s", j.NodeID, strings.Join(problems, "; ")), now)
			return nil
		}
		if t.ObservedGen == nil {
			t.ObservedGen = map[string]int{}
		}
		t.ObservedGen[j.NodeID] = o.Generation
		if t.Digests == nil {
			t.Digests = map[string]NodeDigests{}
		}
		t.Digests[j.NodeID] = NodeDigests{Config: o.ConfigSHA256, Unit: o.UnitSHA256, Marker: o.MarkerSHA256}
		if t.Phase == TunnelObservingIR {
			t.Phase = TunnelObservingEX
			s.tunnelJobLocked(t, t.EXNode, JobTunnelObserve, nil, now)
		} else {
			t.Phase = TunnelFinalizingIR
			s.tunnelJobLocked(t, t.IRNode, JobTunnelFinalize, nil, now)
		}
	case TunnelFinalizingIR, TunnelFinalizingEX:
		return s.nextAfterFinalizeLocked(t, now)
	}
	return nil
}

func (s *Store) nextAfterFinalizeLocked(t *Tunnel, now time.Time) *TunnelEvent {
	if t.Phase == TunnelFinalizingIR {
		t.Phase = TunnelFinalizingEX
		s.tunnelJobLocked(t, t.EXNode, JobTunnelFinalize, nil, now)
		return nil
	}
	t.Phase, t.JobID, t.UpdatedAt = TunnelActive, "", now
	for node, gen := range t.ObservedGen {
		if n, ok := s.st.Nodes[node]; ok {
			n.AppliedGeneration = gen
			s.st.Nodes[node] = n
		}
	}
	for id, other := range s.st.Tunnels {
		if id != t.ID && other.Phase == TunnelActive && (other.EXNode == t.EXNode || other.IRNode == t.IRNode || other.EXNode == t.IRNode || other.IRNode == t.EXNode) {
			other.Phase, other.UpdatedAt = TunnelSuperseded, now
			s.st.Tunnels[id] = other
		}
	}
	detail := fmt.Sprintf("%s <-> %s via %s:%d", t.IRNode, t.EXNode, t.PublicAddress, t.Port)
	if t.Error != "" {
		detail += " (" + t.Error + ")"
	}
	return &TunnelEvent{TunnelID: t.ID, Action: "tunnel.active", Detail: detail}
}

func (s *Store) advanceRollbackLocked(t *Tunnel, now time.Time) *TunnelEvent {
	pending, failed := 0, []string{}
	for _, id := range t.RollbackJobs {
		j := s.st.Jobs[id]
		switch j.Status {
		case "queued", "dispatched":
			pending++
		case "failed":
			failed = append(failed, j.NodeID+": "+j.Message)
		}
	}
	if pending > 0 {
		if now.Sub(t.StepStarted) <= tunnelRollbackTimeout {
			return nil
		}
		failed = append(failed, fmt.Sprintf("%d node(s) did not answer in time", pending))
	}
	t.JobID, t.UpdatedAt = "", now
	if len(failed) > 0 {
		t.Phase = TunnelRollbackFailed
		t.Error += "; rollback incomplete: " + strings.Join(failed, "; ")
		return &TunnelEvent{TunnelID: t.ID, Action: "tunnel.rollback_failed", Detail: t.Error}
	}
	t.Phase = TunnelRolledBack
	return &TunnelEvent{TunnelID: t.ID, Action: "tunnel.rolled_back", Detail: t.Error}
}

// ---- server ----

// AdvanceTunnels steps tunnels and records final outcomes in the audit log.
func (s *Server) AdvanceTunnels() {
	if devs, err := s.store.AdvanceDiscovery(s.now()); err == nil {
		s.auditDiscovery(devs)
	}
	events, err := s.store.AdvanceTunnels(s.now())
	if err != nil {
		return
	}
	for _, e := range events {
		outcome := "success"
		switch e.Action {
		case "tunnel.active", "tunnel.in_sync", "cert.rotation.activated", "cert.rotation.complete":
		default:
			outcome = "failure"
		}
		_, _ = s.audit.Append(AuditEntry{
			Timestamp: s.now().UTC(), Actor: "bcc", Action: e.Action, Target: e.TunnelID, Outcome: outcome,
			Details: map[string]any{"detail": e.Detail},
		})
	}
}

// StartTunnelLoop advances tunnels until ctx ends.
func (s *Server) StartTunnelLoop(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.AdvanceTunnels()
			}
		}
	}()
}

func (s *Server) tunnels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !s.admin(w, r) {
			return
		}
		if id := r.URL.Query().Get("id"); id != "" {
			t, ok := s.store.GetTunnel(id)
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, 200, t)
			return
		}
		writeJSON(w, 200, s.store.ListTunnels())
	case http.MethodPost:
		if !s.admin(w, r) {
			return
		}
		s.mutationMu.Lock()
		defer s.mutationMu.Unlock()
		var in TunnelRequest
		if err := decodeJSON(r, &in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		details := map[string]any{"ex_node": in.EXNode, "ir_node": in.IRNode, "port": in.Port, "target": in.Target, "route_listen": in.RouteListen}
		t, _, err := s.store.CreateTunnelFromPlan(in, in.PlanHash, s.now())
		if in.PlanHash != "" {
			details["plan_hash"] = in.PlanHash
		}
		var stale ErrStalePlan
		if errors.As(err, &stale) {
			details["current_plan_hash"] = stale.Current.Hash
			_ = s.auditAdmin(r, "tunnel.create", in.EXNode+"->"+in.IRNode, "failure", details)
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "plan": stale.Current})
			return
		}
		if err != nil {
			s.auditFailure(w, r, "tunnel.create", in.EXNode+"->"+in.IRNode, details, err, 400)
			return
		}
		details["tunnel_id"], details["job_id"] = t.ID, t.JobID
		if err := s.auditAdmin(r, "tunnel.create", t.ID, "success", details); err != nil {
			http.Error(w, "audit log failure", 500)
			return
		}
		writeJSON(w, 202, t)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) tunnelCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.admin(w, r) {
		return
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	var in struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	details := map[string]any{"reason": strings.TrimSpace(in.Reason)}
	t, err := s.store.CancelTunnel(in.ID, in.Reason, s.now())
	if err != nil {
		s.auditFailure(w, r, "tunnel.cancel", in.ID, details, err, 400)
		return
	}
	if err := s.auditAdmin(r, "tunnel.cancel", in.ID, "success", details); err != nil {
		http.Error(w, "audit log failure", 500)
		return
	}
	writeJSON(w, 200, t)
}

// verifyObserved compares what a node reports with what the tunnel asked for.
// It returns every difference, so the evidence shows all of them at once.
func verifyObserved(t Tunnel, node, role string, o tunnelnode.Observed) []string {
	var p []string
	bad := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }
	if o.TunnelID != t.ID {
		bad("change id is %q, want %q", o.TunnelID, t.ID)
	}
	if o.Role != role {
		bad("node role is %q, want %q", o.Role, role)
	}
	if o.Phase != tunnelnode.PhaseCommitted {
		bad("change phase is %q, want committed", o.Phase)
	}
	if o.Generation != o.PreviousGeneration+1 || o.NodeGeneration != o.Generation {
		bad("generation %d (previous %d, node %d) is not one step ahead", o.Generation, o.PreviousGeneration, o.NodeGeneration)
	}
	// The reviewed plan promised a specific generation change. A node that
	// moved by one step from somewhere else did not run the plan that was
	// reviewed (bootstrap nodes have no verified generation to compare to).
	if exp, ok := t.ExpectedGen[node]; ok && !exp.Bootstrap && (o.PreviousGeneration != exp.From || o.Generation != exp.To) {
		bad("generation %d -> %d, but the reviewed plan promised %d -> %d", o.PreviousGeneration, o.Generation, exp.From, exp.To)
	}
	if !o.ServiceActive {
		bad("service is not active")
	}
	if !o.UnitMatches {
		bad("service unit differs from the expected one")
	}
	if len(o.UnitSHA256) != 64 || len(o.MarkerSHA256) != 64 {
		bad("unit or marker digest missing")
	}
	if len(o.ConfigSHA256) != 64 {
		bad("config digest missing")
	}
	if !o.Managed {
		bad("the node has no BAFT ownership marker for this configuration")
	} else {
		if o.MarkerTunnelID != t.ID || o.MarkerGeneration != o.Generation {
			bad("ownership marker is for %q generation %d, want %q generation %d", o.MarkerTunnelID, o.MarkerGeneration, t.ID, o.Generation)
		}
		if !o.MarkerConfigMatches {
			bad("the config differs from the one BAFT installed")
		}
		if !o.MarkerUnitMatches {
			bad("the unit differs from the one BAFT installed")
		}
	}
	if o.RouteID != t.RouteID {
		bad("route id is %q, want %q", o.RouteID, t.RouteID)
	}
	if role == tunnelnode.RoleIR {
		if o.ConfigRole != "dialer" {
			bad("config role is %q, want dialer", o.ConfigRole)
		}
		if o.RouteListen != t.RouteListen {
			bad("route listener is %q, want %q", o.RouteListen, t.RouteListen)
		}
		if want := net.JoinHostPort(t.PublicAddress, strconv.Itoa(t.Port)); o.PeerAddress != want {
			bad("peer address is %q, want %q", o.PeerAddress, want)
		}
	} else {
		if o.ConfigRole != "listener" {
			bad("config role is %q, want listener", o.ConfigRole)
		}
		if want := "0.0.0.0:" + strconv.Itoa(t.Port); o.Listen != want {
			bad("listener is %q, want %q", o.Listen, want)
		}
		if o.Target != t.Target {
			bad("target is %q, want %q", o.Target, t.Target)
		}
	}
	return p
}

// tunnelDrift starts a drift check of an active tunnel: both nodes report
// what they have and the result is stored with the tunnel (`drift`).
func (s *Server) tunnelDrift(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.admin(w, r) {
		return
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	id := r.URL.Query().Get("id")
	t, err := s.store.StartDrift(id, s.now())
	if err != nil {
		s.auditFailure(w, r, "tunnel.drift_check", id, nil, err, 400)
		return
	}
	if err := s.auditAdmin(r, "tunnel.drift_check", id, "success", map[string]any{"jobs": t.DriftJobs}); err != nil {
		http.Error(w, "audit log failure", 500)
		return
	}
	writeJSON(w, 202, t)
}
