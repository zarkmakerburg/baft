package bcc

// Transactional certificate rotation of a built tunnel (A4 Stage F). BCC
// drives both agents, with signed jobs only, through
//
//	PREPARE (EX) -> DISTRIBUTE (IR) -> VERIFY (IR, EX) -> ACTIVATE (EX)
//	  -> CONFIRM (IR) [-> HOLD] -> RETIRE_OLD (IR, then EX) -> complete
//
// New certificate failure is never loss of the working old one: until
// RETIRE starts, any failure, timeout or cancel rolls back EX first (back to
// the old certificate while the IR still trusts both) and only then the IR.
// Once RETIRE started there is no rollback: the new certificate is
// confirmed working, failed retire steps are retried and, past the bounded
// overlap, reported as retire_failed with both certificates still trusted.
// The node side (internal/tunnelnode/rotate.go) re-checks every rule.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zarkmakerburg/baft/internal/agentjob"
	"github.com/zarkmakerburg/baft/internal/tunnelnode"
)

// Rotation phases. complete, rolled_back, rollback_failed and retire_failed
// are final.
const (
	CertRotPreparing       = "preparing"
	CertRotDistributing    = "distributing"
	CertRotVerifyingIR     = "verifying_ir"
	CertRotVerifyingEX     = "verifying_ex"
	CertRotActivating      = "activating"
	CertRotConfirming      = "confirming"
	CertRotHolding         = "holding"
	CertRotRetiringIR      = "retiring_ir"
	CertRotRetiringEX      = "retiring_ex"
	CertRotComplete        = "complete"
	CertRotRollingBackEX   = "rolling_back_ex"
	CertRotRollingBackIR   = "rolling_back_ir"
	CertRotRolledBack      = "rolled_back"
	CertRotRollbackFailed  = "rollback_failed"
	CertRotRetireFailed    = "retire_failed"
	certRotConfirmAttempts = 5
	certRotRetireAttempts  = 5
	// The overlap (IR trusts old + new) is bounded by default to a day and
	// never longer than a week.
	certRotDefaultOverlap = 24 * time.Hour
	certRotMaxOverlap     = 7 * 24 * time.Hour
)

// CertRotation is one rotation of a tunnel's outer TLS certificate.
type CertRotation struct {
	ID         string `json:"id"`
	TunnelID   string `json:"tunnel_id"`
	InstanceID string `json:"instance_id,omitempty"`
	EXNode     string `json:"ex_node"`
	IRNode     string `json:"ir_node"`
	Epoch      int    `json:"epoch"`
	Phase      string `json:"phase"`
	Error      string `json:"error,omitempty"`

	// Public material from PREPARE.
	Host          string `json:"host,omitempty"`
	CASHA256      string `json:"ca_sha256,omitempty"`
	CertSHA256    string `json:"cert_sha256,omitempty"`
	OldCertSHA256 string `json:"old_cert_sha256,omitempty"`
	CADER         string `json:"ca_der,omitempty"`
	CertDER       string `json:"cert_der,omitempty"`

	JobID    string           `json:"job_id,omitempty"`
	Jobs     []string         `json:"jobs,omitempty"`
	Attempts int              `json:"attempts,omitempty"`
	Evidence []TunnelEvidence `json:"evidence,omitempty"`
	// ActivationSent records that the EX's ACTIVATE job left the queue: from
	// then on the IR may drop the new CA only after seeing the EX serve a
	// certificate the old trust accepts.
	ActivationSent bool `json:"activation_sent,omitempty"`
	EXReached      bool `json:"ex_reached,omitempty"`
	IRReached      bool `json:"ir_reached,omitempty"`

	HoldSeconds     int       `json:"hold_seconds,omitempty"`
	ConfirmedAt     time.Time `json:"confirmed_at,omitempty"`
	OverlapDeadline time.Time `json:"overlap_deadline"`
	StepStarted     time.Time `json:"step_started"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// CertRotationRequest is what an operator asks for.
type CertRotationRequest struct {
	TunnelID     string `json:"tunnel_id"`
	HoldSeconds  int    `json:"hold_seconds"`
	OverlapHours int    `json:"overlap_hours"`
}

func terminalCertRotation(p string) bool {
	switch p {
	case CertRotComplete, CertRotRolledBack, CertRotRollbackFailed, CertRotRetireFailed:
		return true
	}
	return false
}

func certRotRetiring(p string) bool { return p == CertRotRetiringIR || p == CertRotRetiringEX }

func newRotationID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "rot-" + hex.EncodeToString(b)
}

// rotationOnNodesLocked returns a rotation that blocks a change on the given
// nodes: one still running, or one that ended needing an operator.
func (s *Store) rotationOnNodesLocked(nodes ...string) *CertRotation {
	for _, r := range s.st.CertRotations {
		if terminalCertRotation(r.Phase) && r.Phase != CertRotRollbackFailed && r.Phase != CertRotRetireFailed {
			continue
		}
		for _, n := range nodes {
			if r.EXNode == n || r.IRNode == n {
				rr := r
				return &rr
			}
		}
	}
	return nil
}

// StartCertRotation validates a request, queues PREPARE on the EX, and
// commits the success audit intent in the same state transaction.
func (s *Store) StartCertRotation(req CertRotationRequest, now time.Time, audit AuditEntry) (CertRotation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.st.Tunnels[req.TunnelID]
	if !ok {
		return CertRotation{}, errors.New("tunnel not found")
	}
	if t.Phase != TunnelActive {
		return CertRotation{}, fmt.Errorf("tunnel is %s; only an active tunnel rotates its certificate", t.Phase)
	}
	if len(t.DriftJobs) > 0 {
		return CertRotation{}, errors.New("a drift check of this tunnel is running")
	}
	for _, id := range []string{t.EXNode, t.IRNode} {
		if n, ok := s.st.Nodes[id]; !ok || n.Revoked {
			return CertRotation{}, fmt.Errorf("node %s is missing or revoked", id)
		}
	}
	if r := s.rotationOnNodesLocked(t.EXNode, t.IRNode); r != nil {
		return CertRotation{}, fmt.Errorf("certificate rotation %s is %s on one of these nodes", r.ID, r.Phase)
	}
	for _, other := range s.st.Tunnels {
		shares := other.EXNode == t.EXNode || other.IRNode == t.IRNode || other.EXNode == t.IRNode || other.IRNode == t.EXNode
		if shares && !terminalTunnel(other.Phase) {
			return CertRotation{}, fmt.Errorf("tunnel %s is being changed on one of these nodes", other.ID)
		}
	}
	if req.HoldSeconds < 0 || req.HoldSeconds > 86400 {
		return CertRotation{}, errors.New("hold_seconds must be between 0 and 86400")
	}
	overlap := certRotDefaultOverlap
	if req.OverlapHours != 0 {
		overlap = time.Duration(req.OverlapHours) * time.Hour
	}
	if overlap < time.Hour || overlap > certRotMaxOverlap {
		return CertRotation{}, errors.New("overlap_hours must be between 1 and 168")
	}
	if time.Duration(req.HoldSeconds)*time.Second >= overlap {
		return CertRotation{}, errors.New("the hold must be shorter than the overlap")
	}
	before, err := cloneState(s.st)
	if err != nil {
		return CertRotation{}, err
	}
	r := CertRotation{
		ID: newRotationID(), TunnelID: t.ID, InstanceID: t.InstanceID, EXNode: t.EXNode, IRNode: t.IRNode, Epoch: t.CertEpoch + 1,
		Phase: CertRotPreparing, HoldSeconds: req.HoldSeconds, OverlapDeadline: now.UTC().Add(overlap),
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	s.rotJobLocked(&r, r.EXNode, agentjob.ActionCertPrepareEX, map[string]string{"epoch": strconv.Itoa(r.Epoch)}, now)
	if s.st.CertRotations == nil {
		s.st.CertRotations = map[string]CertRotation{}
	}
	s.st.CertRotations[r.ID] = r
	audit.Timestamp, audit.Action, audit.Target, audit.Outcome = now.UTC(), "cert.rotation.start", r.ID, "success"
	if audit.Actor == "" {
		audit.Actor = "admin"
	}
	audit.Details = mergeAuditDetails(audit.Details, map[string]any{
		"rotation_id": r.ID, "epoch": r.Epoch, "job_id": r.JobID,
		"overlap_deadline": r.OverlapDeadline.Format(time.RFC3339),
	})
	if _, err := s.enqueueSecurityAuditLocked(audit); err != nil {
		s.st = before
		return CertRotation{}, err
	}
	if err := s.saveLocked(); err != nil {
		s.st = before
		return CertRotation{}, err
	}
	return r, nil
}

func (s *Store) rotJobLocked(r *CertRotation, node, typ string, params map[string]string, now time.Time) {
	if params == nil {
		params = map[string]string{}
	}
	params["tunnel_id"], params["rotation_id"] = r.TunnelID, r.ID
	if r.InstanceID != "" {
		params["instance_id"] = r.InstanceID
	}
	j := s.newJobLocked(Job{Type: typ, NodeID: node, Params: params})
	r.Jobs = append(r.Jobs, j.ID)
	r.JobID = j.ID
	r.StepStarted, r.UpdatedAt = now.UTC(), now.UTC()
}

func (s *Store) GetCertRotation(id string) (CertRotation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.st.CertRotations[id]
	return r, ok
}

func (s *Store) ListCertRotations() []CertRotation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CertRotation, 0, len(s.st.CertRotations))
	for _, r := range s.st.CertRotations {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// CancelCertRotation rolls a rotation back on request. Once RETIRE started
// the new certificate is the working one and there is nothing to cancel.
// The state change and its success audit intent are one durable transaction.
func (s *Store) CancelCertRotation(id, reason string, now time.Time, audit AuditEntry) (CertRotation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.st.CertRotations[id]
	if !ok {
		return CertRotation{}, errors.New("rotation not found")
	}
	switch {
	case terminalCertRotation(r.Phase):
		return r, fmt.Errorf("rotation is %s; nothing to cancel", r.Phase)
	case r.Phase == CertRotRollingBackEX || r.Phase == CertRotRollingBackIR:
		return r, errors.New("rotation is already rolling back")
	case certRotRetiring(r.Phase):
		return r, errors.New("the old certificate is being retired; the rotation can no longer be cancelled")
	}
	before, err := cloneState(s.st)
	if err != nil {
		return CertRotation{}, err
	}
	r.Error = "cancelled by operator"
	if v := strings.TrimSpace(reason); v != "" {
		r.Error += ": " + v
	}
	s.startRotationRollbackLocked(&r, now)
	s.st.CertRotations[id] = r
	audit.Timestamp, audit.Action, audit.Target, audit.Outcome = now.UTC(), "cert.rotation.cancel", id, "success"
	if audit.Actor == "" {
		audit.Actor = "admin"
	}
	audit.Details = mergeAuditDetails(audit.Details, map[string]any{"phase": r.Phase})
	if _, err := s.enqueueSecurityAuditLocked(audit); err != nil {
		s.st = before
		return CertRotation{}, err
	}
	if err := s.saveLocked(); err != nil {
		s.st = before
		return CertRotation{}, err
	}
	return r, nil
}

// startRotationRollbackLocked cancels queued steps and rolls back the EX
// first, then the IR, each only if one of its jobs left the queue.
func (s *Store) startRotationRollbackLocked(r *CertRotation, now time.Time) {
	for _, id := range r.Jobs {
		j := s.st.Jobs[id]
		if j.Status == "queued" {
			j.Status, j.Message, j.UpdatedAt = "failed", "cancelled: rotation is rolling back", now.UTC()
			s.st.Jobs[id] = j
			continue
		}
		if j.Type == agentjob.ActionCertRollback {
			continue
		}
		if j.NodeID == r.EXNode {
			r.EXReached = true
			if j.Type == agentjob.ActionCertActivateEX {
				r.ActivationSent = true
			}
		}
		if j.NodeID == r.IRNode {
			r.IRReached = true
		}
	}
	r.JobID, r.Attempts = "", 0
	s.nextRollbackStepLocked(r, "", now)
}

// nextRollbackStepLocked queues the rollback after `done` ("" = start).
func (s *Store) nextRollbackStepLocked(r *CertRotation, done string, now time.Time) {
	if done == "" && r.EXReached {
		r.Phase = CertRotRollingBackEX
		s.rotJobLocked(r, r.EXNode, agentjob.ActionCertRollback, map[string]string{"ex_never_activated": "false"}, now)
		return
	}
	if (done == "" || done == "ex") && r.IRReached {
		r.Phase = CertRotRollingBackIR
		s.rotJobLocked(r, r.IRNode, agentjob.ActionCertRollback, map[string]string{"ex_never_activated": strconv.FormatBool(!r.ActivationSent)}, now)
		return
	}
	r.Phase, r.JobID, r.UpdatedAt = CertRotRolledBack, "", now.UTC()
}

func (s *Store) failRotationLocked(r *CertRotation, reason string, now time.Time) {
	r.Error = reason
	s.startRotationRollbackLocked(r, now)
}

// advanceRotationsLocked moves every rotation whose awaited job finished.
func (s *Store) advanceRotationsLocked(now time.Time) ([]TunnelEvent, bool) {
	var events []TunnelEvent
	changed := false
	ids := make([]string, 0, len(s.st.CertRotations))
	for id, r := range s.st.CertRotations {
		if !terminalCertRotation(r.Phase) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := s.st.CertRotations[id]
		before, _ := json.Marshal(r)
		ev := s.advanceRotationLocked(&r, now)
		after, _ := json.Marshal(r)
		if string(before) != string(after) || ev != nil {
			changed = true
			s.st.CertRotations[id] = r
		}
		if ev != nil {
			events = append(events, *ev)
		}
	}
	return events, changed
}

func rotEvent(r *CertRotation, action, detail string) *TunnelEvent {
	return &TunnelEvent{TunnelID: r.ID, Action: action, Detail: fmt.Sprintf("tunnel %s epoch %d: %s", r.TunnelID, r.Epoch, detail)}
}

func (s *Store) advanceRotationLocked(r *CertRotation, now time.Time) *TunnelEvent {
	if r.Phase == CertRotHolding {
		if now.Before(r.ConfirmedAt.Add(time.Duration(r.HoldSeconds)*time.Second)) && now.Before(r.OverlapDeadline) {
			return nil
		}
		r.Phase, r.Attempts = CertRotRetiringIR, 0
		s.rotJobLocked(r, r.IRNode, agentjob.ActionCertRetireIR, nil, now)
		return nil
	}
	j, ok := s.st.Jobs[r.JobID]
	if !ok {
		if r.Phase == CertRotRollingBackEX || r.Phase == CertRotRollingBackIR {
			r.Phase = CertRotRollbackFailed
			r.Error += "; rollback job missing"
			return rotEvent(r, "cert.rotation.rollback_failed", r.Error)
		}
		s.failRotationLocked(r, "internal error: step job missing", now)
		return nil
	}
	rollingBack := r.Phase == CertRotRollingBackEX || r.Phase == CertRotRollingBackIR
	switch j.Status {
	case "queued", "dispatched":
		limit := tunnelStepTimeout
		if rollingBack {
			limit = tunnelRollbackTimeout
		}
		if now.Sub(r.StepStarted) <= limit {
			return nil
		}
		reason := fmt.Sprintf("step %s timed out waiting for node %s", r.Phase, j.NodeID)
		if j.Status == "queued" {
			j.Status, j.Message, j.UpdatedAt = "failed", "cancelled: timed out", now.UTC()
			s.st.Jobs[j.ID] = j
		}
		switch {
		case rollingBack:
			r.Phase = CertRotRollbackFailed
			r.Error += "; rollback incomplete: " + reason
			return rotEvent(r, "cert.rotation.rollback_failed", r.Error)
		case certRotRetiring(r.Phase):
			return s.retryRetireLocked(r, j, reason, now)
		}
		s.failRotationLocked(r, reason, now)
		return nil
	case "failed":
		r.Evidence = append(r.Evidence, TunnelEvidence{Step: r.Phase, Node: j.NodeID, At: now.UTC(), OK: false, Detail: j.Message})
		switch {
		case rollingBack:
			// Never roll the IR back after the EX failed to: the IR keeps
			// trusting old + new, which accepts whatever the EX serves.
			r.Phase = CertRotRollbackFailed
			r.Error += "; rollback incomplete on " + j.NodeID + ": " + j.Message
			return rotEvent(r, "cert.rotation.rollback_failed", r.Error)
		case certRotRetiring(r.Phase):
			return s.retryRetireLocked(r, j, j.Message, now)
		case r.Phase == CertRotConfirming && r.Attempts+1 < certRotConfirmAttempts:
			r.Attempts++
			s.rotJobLocked(r, r.IRNode, agentjob.ActionCertConfirmIR, map[string]string{"cert_sha256": r.CertSHA256}, now)
			return nil
		}
		s.failRotationLocked(r, fmt.Sprintf("%s failed on %s: %s", r.Phase, j.NodeID, j.Message), now)
		return nil
	case "succeeded":
	default:
		return nil
	}
	out := j.Output
	j.Output = ""
	s.st.Jobs[j.ID] = j
	if rollingBack {
		r.Evidence = append(r.Evidence, TunnelEvidence{Step: r.Phase, Node: j.NodeID, At: now.UTC(), OK: true, Detail: j.Message})
		if r.Phase == CertRotRollingBackEX {
			s.nextRollbackStepLocked(r, "ex", now)
		} else {
			s.nextRollbackStepLocked(r, "ir", now)
		}
		if r.Phase == CertRotRolledBack {
			return rotEvent(r, "cert.rotation.rolled_back", r.Error)
		}
		return nil
	}
	if r.Phase == CertRotPreparing {
		problems := r.acceptPlan(out)
		r.Evidence = append(r.Evidence, TunnelEvidence{Step: r.Phase, Node: j.NodeID, At: now.UTC(), OK: len(problems) == 0, Detail: j.Message, Problems: problems})
		if len(problems) > 0 {
			s.failRotationLocked(r, "the EX's plan is not acceptable: "+strings.Join(problems, "; "), now)
			return nil
		}
		r.Phase = CertRotDistributing
		s.rotJobLocked(r, r.IRNode, agentjob.ActionCertTrustIR, map[string]string{
			"epoch": strconv.Itoa(r.Epoch), "ca_der": r.CADER, "ca_sha256": r.CASHA256}, now)
		return nil
	}
	// Every other step answers with the node's own evidence, which must
	// prove the step: a command that "succeeded" is not evidence.
	var e tunnelnode.RotationEvidence
	var problems []string
	if err := json.Unmarshal([]byte(j.Message), &e); err != nil {
		problems = []string{"the node's evidence is unreadable"}
	} else {
		problems = r.checkEvidence(r.Phase, e)
	}
	r.Evidence = append(r.Evidence, TunnelEvidence{Step: r.Phase, Node: j.NodeID, At: now.UTC(), OK: len(problems) == 0, Detail: j.Message, Problems: problems})
	if len(problems) > 0 {
		reason := fmt.Sprintf("evidence of %s on %s does not prove the step: %s", r.Phase, j.NodeID, strings.Join(problems, "; "))
		if certRotRetiring(r.Phase) {
			return s.retryRetireLocked(r, j, reason, now)
		}
		s.failRotationLocked(r, reason, now)
		return nil
	}
	switch r.Phase {
	case CertRotDistributing:
		r.Phase = CertRotVerifyingIR
		s.rotJobLocked(r, r.IRNode, agentjob.ActionCertVerifyIR, map[string]string{"cert_der": r.CertDER, "cert_sha256": r.CertSHA256}, now)
	case CertRotVerifyingIR:
		r.Phase = CertRotVerifyingEX
		s.rotJobLocked(r, r.EXNode, agentjob.ActionCertVerifyEX, nil, now)
	case CertRotVerifyingEX:
		if !now.Before(r.OverlapDeadline) {
			s.failRotationLocked(r, "the overlap window ended before ACTIVATE", now)
			return nil
		}
		r.Phase = CertRotActivating
		s.rotJobLocked(r, r.EXNode, agentjob.ActionCertActivateEX, nil, now)
	case CertRotActivating:
		r.ActivationSent = true
		r.Phase, r.Attempts = CertRotConfirming, 0
		s.rotJobLocked(r, r.IRNode, agentjob.ActionCertConfirmIR, map[string]string{"cert_sha256": r.CertSHA256}, now)
		return rotEvent(r, "cert.rotation.activated", "the EX serves the new certificate "+r.CertSHA256[:16])
	case CertRotConfirming:
		r.ConfirmedAt = now.UTC()
		r.Phase, r.JobID = CertRotHolding, ""
		return s.advanceRotationLocked(r, now)
	case CertRotRetiringIR:
		r.Phase, r.Attempts = CertRotRetiringEX, 0
		s.rotJobLocked(r, r.EXNode, agentjob.ActionCertRetireEX, nil, now)
	case CertRotRetiringEX:
		r.Phase, r.JobID, r.UpdatedAt = CertRotComplete, "", now.UTC()
		if t, ok := s.st.Tunnels[r.TunnelID]; ok {
			t.CertEpoch, t.CertSHA256, t.UpdatedAt = r.Epoch, r.CertSHA256, now.UTC()
			s.st.Tunnels[r.TunnelID] = t
		}
		return rotEvent(r, "cert.rotation.complete", "old certificate retired on both nodes")
	}
	return nil
}

// retryRetireLocked repeats a failed retire step (they are idempotent)
// until the attempts or the overlap window run out.
func (s *Store) retryRetireLocked(r *CertRotation, j Job, reason string, now time.Time) *TunnelEvent {
	if r.Attempts+1 < certRotRetireAttempts && now.Before(r.OverlapDeadline) {
		r.Attempts++
		s.rotJobLocked(r, j.NodeID, j.Type, nil, now)
		return nil
	}
	r.Phase, r.JobID, r.UpdatedAt = CertRotRetireFailed, "", now.UTC()
	r.Error = "retire did not complete on " + j.NodeID + ": " + reason + "; the new certificate is in use and the old one is still trusted"
	return rotEvent(r, "cert.rotation.retire_failed", r.Error)
}

// acceptPlan checks the EX's PREPARE output and keeps its public material.
func (r *CertRotation) acceptPlan(out string) []string {
	var p tunnelnode.RotationPlan
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		return []string{"the plan is unreadable"}
	}
	var bad []string
	ca, err1 := base64.RawURLEncoding.DecodeString(p.CADER)
	cert, err2 := base64.RawURLEncoding.DecodeString(p.CertDER)
	switch {
	case p.RotationID != r.ID || p.TunnelID != r.TunnelID || p.Epoch != r.Epoch:
		bad = append(bad, "the plan is for another rotation")
	case err1 != nil || err2 != nil || len(p.CADER) > 4096 || len(p.CertDER) > 4096 || len(p.CADER) < 64 || len(p.CertDER) < 64:
		bad = append(bad, "the certificates are malformed")
	case sha256Hex(ca) != p.CASHA256 || sha256Hex(cert) != p.CertSHA256:
		bad = append(bad, "the certificates do not match their digests")
	case p.CertSHA256 == p.OldCertSHA256:
		bad = append(bad, "the new certificate is the old one")
	}
	if len(bad) == 0 {
		r.Host, r.CADER, r.CertDER, r.CASHA256, r.CertSHA256, r.OldCertSHA256 = p.Host, p.CADER, p.CertDER, p.CASHA256, p.CertSHA256, p.OldCertSHA256
	}
	return bad
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// checkEvidence states, per step, what the node must have proved.
func (r *CertRotation) checkEvidence(phase string, e tunnelnode.RotationEvidence) []string {
	var p []string
	bad := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }
	if e.RotationID != r.ID || e.Epoch != r.Epoch {
		bad("evidence is for rotation %q epoch %d", e.RotationID, e.Epoch)
	}
	if !e.ServiceActive {
		bad("service is not active")
	}
	switch phase {
	case CertRotDistributing, CertRotVerifyingIR:
		if !e.OldTrusted || !e.NewTrusted {
			bad("the IR does not trust old + new (old %v, new %v)", e.OldTrusted, e.NewTrusted)
		}
		if e.ServedCertSHA256 != r.OldCertSHA256 {
			bad("the IR does not see the old certificate served and accepted")
		}
	case CertRotVerifyingEX:
		if e.LiveCertSHA256 != r.OldCertSHA256 {
			bad("the EX's live certificate is not the old one")
		}
	case CertRotActivating:
		if !e.ServesNew || e.LiveCertSHA256 != r.CertSHA256 || e.ServedCertSHA256 != r.CertSHA256 {
			bad("the EX does not serve the new certificate")
		}
	case CertRotConfirming:
		if !e.ServesNew || e.ServedCertSHA256 != r.CertSHA256 || !e.NewTrusted {
			bad("the IR does not see the new certificate served and accepted")
		}
	case CertRotRetiringIR:
		if len(e.TrustedCAs) != 1 || e.TrustedCAs[0] != r.CASHA256 || !e.ServesNew {
			bad("the IR does not trust exactly the new CA while the new certificate is served")
		}
	case CertRotRetiringEX:
		if e.Phase != tunnelnode.RotRetired || e.LiveCertSHA256 != r.CertSHA256 || e.NodeEpoch != r.Epoch {
			bad("the EX did not retire the old set (phase %s, epoch %d)", e.Phase, e.NodeEpoch)
		}
	}
	return p
}

// ---- server ----

func (s *Server) certRotations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.admin(w, r) {
		return
	}
	if id := r.URL.Query().Get("id"); id != "" {
		rot, ok := s.store.GetCertRotation(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, rot)
		return
	}
	writeJSON(w, 200, s.store.ListCertRotations())
}

func (s *Server) certRotationStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.admin(w, r) {
		return
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	var in CertRotationRequest
	if err := decodeJSON(r, &in); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	details := map[string]any{"tunnel_id": in.TunnelID, "hold_seconds": in.HoldSeconds, "overlap_hours": in.OverlapHours}
	now := s.now()
	rot, err := s.store.StartCertRotation(in, now, AuditEntry{
		Timestamp: now.UTC(), Actor: "admin", RemoteIP: s.clientIP(r),
		Details: withRequest(r, details),
	})
	if err != nil {
		s.auditFailure(w, r, "cert.rotation.start", in.TunnelID, details, err, 400)
		return
	}
	if err := s.FlushSecurityAuditIntents(); err != nil {
		w.Header().Set("X-BAFT-Audit-State", "pending")
	}
	writeJSON(w, 202, rot)
}

func (s *Server) certRotationCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
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
	now := s.now()
	rot, err := s.store.CancelCertRotation(in.ID, in.Reason, now, AuditEntry{
		Timestamp: now.UTC(), Actor: "admin", RemoteIP: s.clientIP(r),
		Details: withRequest(r, details),
	})
	if err != nil {
		s.auditFailure(w, r, "cert.rotation.cancel", in.ID, details, err, 400)
		return
	}
	if err := s.FlushSecurityAuditIntents(); err != nil {
		w.Header().Set("X-BAFT-Audit-State", "pending")
	}
	writeJSON(w, 200, rot)
}
