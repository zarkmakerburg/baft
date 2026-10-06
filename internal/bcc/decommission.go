package bcc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zarkmakerburg/baft/internal/agentjob"
	"github.com/zarkmakerburg/baft/internal/tunnelnode"
)

const JobTunnelRetire = agentjob.ActionTunnelRetire

const (
	TunnelDecommissioningIR  = "decommissioning_ir"
	TunnelDecommissioningEX  = "decommissioning_ex"
	TunnelDecommissioned     = "decommissioned"
	TunnelDecommissionFailed = "decommission_failed"
)

type DecommissionPlanNode struct {
	NodeID     string      `json:"node_id"`
	Role       string      `json:"role"`
	Generation int         `json:"generation"`
	Digests    NodeDigests `json:"digests"`
	Already    bool        `json:"already_decommissioned,omitempty"`
}

type DecommissionPlan struct {
	Hash       string                 `json:"hash"`
	OK         bool                   `json:"ok"`
	TunnelID   string                 `json:"tunnel_id"`
	InstanceID string                 `json:"instance_id,omitempty"`
	Phase      string                 `json:"phase"`
	Disruptive bool                   `json:"disruptive"`
	Warning    string                 `json:"warning"`
	Nodes      []DecommissionPlanNode `json:"nodes"`
	Gates      []PlanGate             `json:"gates"`
	Steps      []string               `json:"steps"`
}

type ErrStaleDecommissionPlan struct{ Current DecommissionPlan }

func (ErrStaleDecommissionPlan) Error() string {
	return "the decommission plan is stale or no longer passes its gates; review the new plan"
}

func goodDigest(v string) bool {
	if len(v) != 64 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}

func (s *Store) BuildDecommissionPlan(id string, now time.Time) (DecommissionPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buildDecommissionPlanLocked(id, now.UTC())
}

func (s *Store) buildDecommissionPlanLocked(id string, now time.Time) (DecommissionPlan, error) {
	t, ok := s.st.Tunnels[id]
	if !ok {
		return DecommissionPlan{}, errors.New("tunnel not found")
	}
	if t.Phase != TunnelActive && t.Phase != TunnelDecommissionFailed {
		return DecommissionPlan{}, fmt.Errorf("tunnel is %s; only active or decommission_failed tunnels can be decommissioned", t.Phase)
	}
	p := DecommissionPlan{
		TunnelID: t.ID, InstanceID: t.InstanceID, Phase: t.Phase, Disruptive: true,
		Warning: "Decommission stops the selected IR route first and then the EX listener. Existing flows are not promised to survive.",
		Steps: []string{
			"verify the reviewed generation and BCC-held config/unit/marker digests",
			"retire the IR dialer/route and prove its managed artifacts are absent",
			"retire the EX listener and prove its managed artifacts are absent",
			"retain tunnel history and audit evidence as decommissioned",
		},
	}
	gate := func(name, status, detail string) {
		p.Gates = append(p.Gates, PlanGate{Name: name, Status: status, Detail: detail})
	}
	if len(t.DriftJobs) > 0 {
		gate("drift check", "FAIL", "a drift check is still running")
	} else if t.Drift != nil && t.Drift.State != DriftInSync && t.Phase == TunnelActive {
		gate("known drift", "FAIL", "the latest drift check is "+t.Drift.State+"; repair or re-check before decommission")
	} else {
		gate("drift check", "PASS", "no conflicting drift check is running; the node re-verifies exact digests immediately before mutation")
	}
	if r := s.rotationOnNodesLocked(t.EXNode, t.IRNode); r != nil {
		gate("certificate rotation", "FAIL", fmt.Sprintf("certificate rotation %s is %s on one of these nodes", r.ID, r.Phase))
	} else {
		gate("certificate rotation", "PASS", "no blocking certificate rotation is running")
	}
	conflict := ""
	for otherID, other := range s.st.Tunnels {
		if otherID == t.ID || !sameManagedSlot(t, other) {
			continue
		}
		if other.Phase == TunnelActive || !terminalTunnel(other.Phase) ||
			other.Phase == TunnelRollbackFailed || other.Phase == TunnelDecommissionFailed {
			conflict = fmt.Sprintf("tunnel %s is %s in the same managed slot", other.ID, other.Phase)
			break
		}
	}
	if conflict != "" {
		gate("conflicting tunnel change", "FAIL", conflict)
	} else {
		gate("conflicting tunnel change", "PASS", "no other live, changing, or operator-repair tunnel owns this managed slot")
	}
	for _, spec := range []struct {
		id, role string
	}{{t.IRNode, tunnelnode.RoleIR}, {t.EXNode, tunnelnode.RoleEX}} {
		already := t.DecommissionedNodes != nil && t.DecommissionedNodes[spec.id]
		gen, gok := t.ObservedGen[spec.id]
		d, dok := t.Digests[spec.id]
		p.Nodes = append(p.Nodes, DecommissionPlanNode{
			NodeID: spec.id, Role: spec.role, Generation: gen, Digests: d, Already: already,
		})
		if already {
			gate("managed proof: "+spec.id, "PASS", "this node already completed decommission in the recorded partial operation")
			continue
		}
		n, nok := s.st.Nodes[spec.id]
		switch {
		case !nok:
			gate("node: "+spec.id, "FAIL", "node record is missing")
		case n.Revoked:
			gate("node: "+spec.id, "FAIL", "node is revoked and cannot execute the signed retire job")
		case n.AgentSeen.IsZero():
			gate("node: "+spec.id, "FAIL", "the agent has never contacted BCC")
		case now.Sub(n.AgentSeen) > agentFreshness:
			gate("node: "+spec.id, "FAIL", fmt.Sprintf("agent evidence is stale: last contact %s ago", now.Sub(n.AgentSeen).Round(time.Second)))
		default:
			gate("node: "+spec.id, "PASS", "node is enrolled, not revoked, and its agent contact is fresh")
		}
		if !gok || gen <= 0 || !dok || !goodDigest(d.Config) || !goodDigest(d.Unit) || !goodDigest(d.Marker) {
			gate("managed proof: "+spec.id, "FAIL", "BCC does not hold a complete verified generation and digest baseline; rebuild or reconcile before destructive retirement")
		} else {
			gate("managed proof: "+spec.id, "PASS", fmt.Sprintf("generation %d and all three BCC-held SHA-256 digests are present", gen))
		}
	}
	p.OK = true
	for _, g := range p.Gates {
		if g.Status == "FAIL" {
			p.OK = false
		}
	}
	p.Hash = decommissionPlanHash(p)
	return p, nil
}

func decommissionPlanHash(p DecommissionPlan) string {
	type gate struct{ Name, Status string }
	gates := make([]gate, 0, len(p.Gates))
	for _, g := range p.Gates {
		gates = append(gates, gate{g.Name, g.Status})
	}
	b, _ := json.Marshal(struct {
		TunnelID   string
		InstanceID string
		Phase      string
		Nodes      []DecommissionPlanNode
		Gates      []gate
	}{p.TunnelID, p.InstanceID, p.Phase, p.Nodes, gates})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *Store) StartDecommissionFromPlan(id, planHash string, now time.Time) (Tunnel, DecommissionPlan, error) {
	return s.startDecommissionFromPlan(id, planHash, now, nil)
}

// StartDecommissionFromPlanAudited commits the destructive operation and its
// request audit intent atomically. If either cannot be persisted, no retire
// job becomes runnable.
func (s *Store) StartDecommissionFromPlanAudited(id, planHash string, now time.Time, audit AuditEntry) (Tunnel, DecommissionPlan, error) {
	return s.startDecommissionFromPlan(id, planHash, now, &audit)
}

func (s *Store) startDecommissionFromPlan(id, planHash string, now time.Time, audit *AuditEntry) (Tunnel, DecommissionPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.buildDecommissionPlanLocked(id, now.UTC())
	if err != nil {
		return Tunnel{}, DecommissionPlan{}, err
	}
	if planHash == "" || !cur.OK || cur.Hash != planHash {
		return Tunnel{}, cur, ErrStaleDecommissionPlan{Current: cur}
	}
	before, err := cloneState(s.st)
	if err != nil {
		return Tunnel{}, DecommissionPlan{}, err
	}
	t := s.st.Tunnels[id]
	t.DecommissionPlanHash = planHash
	t.Error = ""
	if t.DecommissionedNodes == nil {
		t.DecommissionedNodes = map[string]bool{}
	}
	s.queueNextDecommissionLocked(&t, now.UTC())
	s.st.Tunnels[id] = t
	if audit != nil {
		a := *audit
		a.Timestamp, a.Action, a.Target, a.Outcome = now.UTC(), "tunnel.decommission.request", id, "success"
		if a.Actor == "" {
			a.Actor = "admin"
		}
		a.Details = mergeAuditDetails(a.Details, map[string]any{
			"plan_hash": planHash, "job_id": t.JobID, "phase": t.Phase, "disruptive": true,
		})
		if _, err := s.enqueueSecurityAuditLocked(a); err != nil {
			s.st = before
			return Tunnel{}, DecommissionPlan{}, err
		}
	}
	if err := s.saveLocked(); err != nil {
		s.st = before
		return Tunnel{}, DecommissionPlan{}, err
	}
	return t, DecommissionPlan{}, nil
}

func (s *Store) queueNextDecommissionLocked(t *Tunnel, now time.Time) {
	if !t.DecommissionedNodes[t.IRNode] {
		t.Phase = TunnelDecommissioningIR
		s.tunnelJobLocked(t, t.IRNode, JobTunnelRetire, decommissionParams(*t, t.IRNode), now)
		return
	}
	if !t.DecommissionedNodes[t.EXNode] {
		t.Phase = TunnelDecommissioningEX
		s.tunnelJobLocked(t, t.EXNode, JobTunnelRetire, decommissionParams(*t, t.EXNode), now)
		return
	}
	t.Phase, t.JobID, t.UpdatedAt = TunnelDecommissioned, "", now
}

func decommissionParams(t Tunnel, node string) map[string]string {
	d := t.Digests[node]
	return map[string]string{
		"generation":    strconv.Itoa(t.ObservedGen[node]),
		"config_sha256": d.Config,
		"unit_sha256":   d.Unit,
		"marker_sha256": d.Marker,
	}
}

func verifyRetireEvidence(t Tunnel, node string, ev tunnelnode.RetireEvidence) []string {
	var p []string
	bad := func(f string, a ...any) { p = append(p, fmt.Sprintf(f, a...)) }
	if ev.TunnelID != t.ID {
		bad("retire evidence tunnel is %q, want %q", ev.TunnelID, t.ID)
	}
	if ev.Instance != t.InstanceID {
		bad("retire evidence instance is %q, want %q", ev.Instance, t.InstanceID)
	}
	wantGen, ok := t.ObservedGen[node]
	if !ok || ev.After.NodeGeneration != wantGen {
		bad("post-retire node generation is %d, want %d", ev.After.NodeGeneration, wantGen)
	}
	if ev.After.ConfigPresent || ev.After.UnitPresent || ev.After.MarkerPresent {
		bad("managed config/unit/marker still exists after retire")
	}
	if ev.After.ServiceActive {
		bad("service is still active after retire")
	}
	d := t.Digests[node]
	if ev.Before.ConfigPresent && ev.Before.ConfigSHA256 != d.Config {
		bad("pre-retire config digest differs from BCC evidence")
	}
	if ev.Before.UnitPresent && ev.Before.UnitSHA256 != d.Unit {
		bad("pre-retire unit digest differs from BCC evidence")
	}
	if ev.Before.MarkerPresent && ev.Before.MarkerSHA256 != d.Marker {
		bad("pre-retire marker digest differs from BCC evidence")
	}
	return p
}

func (s *Store) advanceDecommissionLocked(t *Tunnel, now time.Time) *TunnelEvent {
	j, ok := s.st.Jobs[t.JobID]
	if !ok {
		t.Phase, t.Error, t.JobID, t.UpdatedAt = TunnelDecommissionFailed, "decommission step job is missing", "", now
		return &TunnelEvent{TunnelID: t.ID, Action: "tunnel.decommission_failed", Detail: t.Error}
	}
	switch j.Status {
	case "queued", "dispatched":
		if now.Sub(t.StepStarted) <= tunnelStepTimeout {
			return nil
		}
		t.Phase = TunnelDecommissionFailed
		t.Error = fmt.Sprintf("decommission timed out waiting for node %s", j.NodeID)
		t.JobID, t.UpdatedAt = "", now
		return &TunnelEvent{TunnelID: t.ID, Action: "tunnel.decommission_failed", Detail: t.Error}
	case "failed":
		t.Evidence = append(t.Evidence, TunnelEvidence{Step: "decommission", Node: j.NodeID, At: now, OK: false, Detail: j.Message})
		t.Phase = TunnelDecommissionFailed
		t.Error = fmt.Sprintf("decommission failed on %s: %s", j.NodeID, j.Message)
		t.JobID, t.UpdatedAt = "", now
		return &TunnelEvent{TunnelID: t.ID, Action: "tunnel.decommission_failed", Detail: t.Error}
	case "succeeded":
	default:
		return nil
	}
	var ev tunnelnode.RetireEvidence
	var problems []string
	if err := json.Unmarshal([]byte(j.Message), &ev); err != nil {
		problems = []string{"node retire evidence is unreadable"}
	} else {
		problems = verifyRetireEvidence(*t, j.NodeID, ev)
	}
	t.Evidence = append(t.Evidence, TunnelEvidence{
		Step: "decommission", Node: j.NodeID, At: now, OK: len(problems) == 0,
		Detail: j.Message, Problems: problems,
	})
	if len(problems) > 0 {
		t.Phase = TunnelDecommissionFailed
		t.Error = "decommission evidence from " + j.NodeID + " is invalid: " + strings.Join(problems, "; ")
		t.JobID, t.UpdatedAt = "", now
		return &TunnelEvent{TunnelID: t.ID, Action: "tunnel.decommission_failed", Detail: t.Error}
	}
	if t.DecommissionedNodes == nil {
		t.DecommissionedNodes = map[string]bool{}
	}
	t.DecommissionedNodes[j.NodeID] = true
	t.JobID = ""
	s.queueNextDecommissionLocked(t, now)
	if t.Phase == TunnelDecommissioned {
		t.Error = ""
		return &TunnelEvent{
			TunnelID: t.ID, Action: "tunnel.decommissioned",
			Detail: fmt.Sprintf("%s <-> %s retired in IR-to-EX order", t.IRNode, t.EXNode),
		}
	}
	return nil
}

func (s *Server) tunnelDecommissionPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(r, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	plan, err := s.store.BuildDecommissionPlan(in.ID, s.now())
	if err != nil {
		s.auditFailure(w, r, "tunnel.decommission.plan", in.ID, nil, err, http.StatusBadRequest)
		return
	}
	details := map[string]any{"plan_hash": plan.Hash, "ok": plan.OK, "disruptive": true}
	if err := s.auditAdmin(r, "tunnel.decommission.plan", in.ID, "success", details); err != nil {
		http.Error(w, "audit log failure", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) tunnelDecommission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	var in struct {
		ID       string `json:"id"`
		PlanHash string `json:"plan_hash"`
	}
	if err := decodeJSON(r, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	audit := AuditEntry{Actor: "admin", Details: map[string]any{"plan_hash": in.PlanHash}}
	t, _, err := s.store.StartDecommissionFromPlanAudited(in.ID, in.PlanHash, s.now(), audit)
	var stale ErrStaleDecommissionPlan
	if errors.As(err, &stale) {
		_ = s.auditAdmin(r, "tunnel.decommission.request", in.ID, "failure", map[string]any{"plan_hash": in.PlanHash, "current_plan_hash": stale.Current.Hash})
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "plan": stale.Current})
		return
	}
	if err != nil {
		s.auditFailure(w, r, "tunnel.decommission.request", in.ID, map[string]any{"plan_hash": in.PlanHash}, err, http.StatusBadRequest)
		return
	}
	if err := s.FlushSecurityAuditIntents(); err != nil {
		// The durable intent remains in BCC state and is retried by the regular
		// flush path; the destructive job was never allowed to exist without it.
	}
	writeJSON(w, http.StatusAccepted, t)
}
