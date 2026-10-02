package bcc

// Deployment plan: a deterministic, side-effect-free description of what a
// tunnel change will do, produced by the same validation that creation uses.
// The operator reviews it and deploys it by its hash; if anything it depends
// on changed in between, the hash no longer matches and nothing is deployed.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

const agentFreshness = 5 * time.Minute

type PlanGate struct {
	Name   string `json:"name"`
	Status string `json:"status"` // PASS, WARN, FAIL
	Detail string `json:"detail"`
}

type PlanNode struct {
	NodeID         string   `json:"node_id"`
	Role           string   `json:"role"` // ex or ir
	GenerationFrom int      `json:"generation_from"`
	GenerationTo   int      `json:"generation_to"`
	GenerationNote string   `json:"generation_note,omitempty"`
	Changes        []string `json:"changes"`
}

type Plan struct {
	Hash     string        `json:"hash"`
	OK       bool          `json:"ok"`
	Request  TunnelRequest `json:"request"`
	Nodes    []PlanNode    `json:"nodes"`
	Gates    []PlanGate    `json:"gates"`
	Replaces string        `json:"replaces,omitempty"`
	Steps    []string      `json:"steps"`
	Verify   []string      `json:"verify"`
	Rollback []string      `json:"rollback"`
}

// GenExpect is the generation change a reviewed plan promises for one node.
// Bootstrap is true when BCC has no verified generation for the node yet
// (AppliedGeneration == 0): then only the node's own consistency can be
// checked (its generation is one ahead of its previous one) and the values it
// reports are recorded; once BCC has verified a generation, the next change
// must start exactly from it.
type GenExpect struct {
	From      int  `json:"from"`
	To        int  `json:"to"`
	Bootstrap bool `json:"bootstrap,omitempty"`
}

func expectedGeneration(n Node) GenExpect {
	if n.AppliedGeneration > 0 {
		return GenExpect{From: n.AppliedGeneration, To: n.AppliedGeneration + 1}
	}
	return GenExpect{From: 0, To: 1, Bootstrap: true}
}

// BuildPlan describes the change; it never writes.
func (s *Store) BuildPlan(req TunnelRequest, now time.Time) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buildPlanLocked(req, now)
}

func (s *Store) buildPlanLocked(req TunnelRequest, now time.Time) (Plan, error) {
	t, ex, ir, err := s.resolveTunnelLocked(req, now)
	if err != nil {
		return Plan{}, err
	}
	gen := func(n Node) (from, to int, note string) {
		g := expectedGeneration(n)
		if g.Bootstrap {
			return g.From, g.To, "bootstrap: BCC has not verified a generation for this node yet; the node's own counter must be one step ahead and is recorded"
		}
		return g.From, g.To, ""
	}
	exFrom, exTo, exNote := gen(ex)
	irFrom, irTo, irNote := gen(ir)
	peer := net.JoinHostPort(t.PublicAddress, strconv.Itoa(t.Port))
	p := Plan{
		Request: TunnelRequest{
			EXNode: t.EXNode, IRNode: t.IRNode, PublicAddress: t.PublicAddress, Port: t.Port, Target: t.Target,
			RouteListen: t.RouteListen, RouteID: t.RouteID, RecordShaping: t.RecordShaping,
		},
		Nodes: []PlanNode{
			{NodeID: ex.ID, Role: "ex", GenerationFrom: exFrom, GenerationTo: exTo, GenerationNote: exNote, Changes: []string{
				fmt.Sprintf("listener config: 0.0.0.0:%d, route %s exits to the fixed target %s", t.Port, t.RouteID, t.Target),
				"outer-TLS key material and Noise key ensured for " + t.PublicAddress,
				"service unit written and (re)started; previous config and unit backed up",
			}},
			{NodeID: ir.ID, Role: "ir", GenerationFrom: irFrom, GenerationTo: irTo, GenerationNote: irNote, Changes: []string{
				fmt.Sprintf("dialer config: local route %s on %s, peer %s", t.RouteID, t.RouteListen, peer),
				"Noise key ensured; pairing reply produced from the EX's one-time code",
				"service unit written and (re)started; previous config and unit backed up",
			}},
		},
		Steps: []string{
			"prepare EX (nothing live changes)", "prepare IR (nothing live changes)", "commit EX", "commit IR",
			"health check IR then EX", "observe both nodes and compare with this plan", "finalize IR then EX (backups and one-time secrets deleted)",
		},
		Verify: []string{
			"service active and not restarting for a settle window",
			"EX listener and IR local route accept connections",
			"observed role, route, listener/peer/target and unit equal this plan",
			"each node's generation equals the reviewed plan (previous = from, current = to; bootstrap nodes: one step ahead of their own previous) and its own counter",
			"change id on each node equals this deployment",
		},
		Rollback: []string{
			"any failure, timeout or cancel before finalize restores, on every node that reached a step: previous config, unit, service state and generation",
			"IR is restored first, then EX",
		},
	}
	gate := func(name, status, detail string) {
		p.Gates = append(p.Gates, PlanGate{Name: name, Status: status, Detail: detail})
	}
	for _, n := range []Node{ex, ir} {
		switch {
		case n.AgentSeen.IsZero():
			gate("agent contact: "+n.ID, "FAIL", "the agent has never contacted BCC")
		case now.Sub(n.AgentSeen) > agentFreshness:
			gate("agent contact: "+n.ID, "FAIL", fmt.Sprintf("last contact %s ago (limit %s)", now.Sub(n.AgentSeen).Round(time.Second), agentFreshness))
		default:
			gate("agent contact: "+n.ID, "PASS", fmt.Sprintf("seen %s ago", now.Sub(n.AgentSeen).Round(time.Second)))
		}
	}
	gate("node roles", "PASS", "EX is foreign, IR is worker or master, neither is revoked")
	gate("no change in progress on these nodes", "PASS", "no unfinished tunnel change and no failed rollback")
	gate("parameters", "PASS", "validated with the rules the agents apply: fixed-IP target, loopback route listener, strict patterns")
	for id, other := range s.st.Tunnels {
		if other.Phase == TunnelActive && (other.EXNode == t.EXNode || other.IRNode == t.IRNode || other.EXNode == t.IRNode || other.IRNode == t.EXNode) {
			p.Replaces = id
			gate("replaces active tunnel", "WARN", id+" becomes superseded when this one is active; it is restored if this change fails")
		}
	}
	p.OK = true
	for _, g := range p.Gates {
		if g.Status == "FAIL" {
			p.OK = false
		}
	}
	p.Hash = planHash(p)
	return p, nil
}

// planHash covers everything the plan depends on and nothing time-varying, so
// two plans with equal hashes describe the same change.
func planHash(p Plan) string {
	type gate struct{ Name, Status string }
	var gates []gate
	for _, g := range p.Gates {
		gates = append(gates, gate{g.Name, g.Status})
	}
	req := p.Request
	req.PlanHash = ""
	b, _ := json.Marshal(struct {
		Request  TunnelRequest
		Nodes    []PlanNode
		Gates    []gate
		Replaces string
	}{req, p.Nodes, gates, p.Replaces})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *Server) tunnelPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.admin(w, r) {
		return
	}
	var in TunnelRequest
	if err := decodeJSON(r, &in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	plan, err := s.store.BuildPlan(in, s.now())
	details := map[string]any{"ex_node": in.EXNode, "ir_node": in.IRNode}
	if err != nil {
		s.auditFailure(w, r, "tunnel.plan", in.EXNode+"->"+in.IRNode, details, err, 400)
		return
	}
	details["plan_hash"], details["ok"] = plan.Hash, plan.OK
	if err := s.auditAdmin(r, "tunnel.plan", in.EXNode+"->"+in.IRNode, "success", details); err != nil {
		http.Error(w, "audit log failure", 500)
		return
	}
	writeJSON(w, 200, plan)
}
