package bcc

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	DoctorPass        = "PASS"
	DoctorFail        = "FAIL"
	DoctorNotAssessed = "NOT_ASSESSED"
)

type DoctorStageEvidence struct {
	Stage     string `json:"stage"`
	State     string `json:"state"`
	Detail    string `json:"detail,omitempty"`
	Reference string `json:"reference,omitempty"`
}

type RouteDoctorRun struct {
	ID                string                `json:"id"`
	TunnelID          string                `json:"tunnel_id"`
	CreatedAt         time.Time             `json:"created_at"`
	Verdict           string                `json:"verdict"`
	FirstFailingStage string                `json:"first_failing_stage,omitempty"`
	SummaryEN         string                `json:"summary_en"`
	SummaryFA         string                `json:"summary_fa"`
	EvidenceDigest    string                `json:"evidence_digest"`
	Stages            []DoctorStageEvidence `json:"stages"`
}

var doctorStages = []string{
	"node_service", "local_listener", "tcp_path", "tls13", "carrier_handshake",
	"route_authorization", "target_reachability", "application_flow", "consistency",
}

func DiagnoseRouteDoctor(tunnelID string, at time.Time, evidence []DoctorStageEvidence) RouteDoctorRun {
	by := map[string]DoctorStageEvidence{}
	for _, e := range evidence {
		by[e.Stage] = e
	}
	out := RouteDoctorRun{TunnelID: tunnelID, CreatedAt: at.UTC(), Verdict: DoctorPass}
	blocked := false
	for _, name := range doctorStages {
		e, ok := by[name]
		if blocked || !ok {
			e = DoctorStageEvidence{Stage: name, State: DoctorNotAssessed}
		}
		if e.State == "" {
			e.State = DoctorNotAssessed
		}
		out.Stages = append(out.Stages, e)
		if !blocked && e.State == DoctorFail {
			blocked = true
			out.Verdict = DoctorFail
			out.FirstFailingStage = name
		}
	}
	if out.Verdict == DoctorPass {
		for _, e := range out.Stages {
			if e.State != DoctorPass {
				out.Verdict = DoctorNotAssessed
				break
			}
		}
	}
	switch out.Verdict {
	case DoctorPass:
		out.SummaryEN, out.SummaryFA = "All assessed Route Doctor stages passed.", "همه مراحل ارزیابی‌شده Route Doctor با موفقیت عبور کردند."
	case DoctorFail:
		out.SummaryEN = "First causally blocking stage: " + out.FirstFailingStage + "."
		out.SummaryFA = "اولین مرحله مسدودکننده: " + out.FirstFailingStage + "."
	default:
		out.SummaryEN, out.SummaryFA = "Diagnosis is incomplete because required evidence is unavailable.", "تشخیص کامل نیست چون شواهد لازم موجود نیست."
	}
	canonical, _ := json.Marshal(out.Stages)
	sum := sha256.Sum256(canonical)
	out.EvidenceDigest = hex.EncodeToString(sum[:])
	return out
}

func newDoctorID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "doctor-" + time.Now().UTC().Format("20060102150405")
	}
	return "doctor-" + hex.EncodeToString(b)
}

func (s *Store) RunRouteDoctor(tunnelID string, now time.Time) (RouteDoctorRun, error) {
	t, ok := s.GetTunnel(strings.TrimSpace(tunnelID))
	if !ok {
		return RouteDoctorRun{}, errors.New("tunnel not found")
	}
	if t.Phase != TunnelActive {
		return RouteDoctorRun{}, errors.New("Route Doctor requires an active tunnel")
	}

	healthIR := s.HealthSnapshot(t.IRNode, 1)
	healthEX := s.HealthSnapshot(t.EXNode, 1)
	matrix := s.PathMatrix(now)
	evidence := []DoctorStageEvidence{}
	nodesOK := len(healthIR) == 1 && len(healthEX) == 1 && healthIR[0].Overall != HealthDown && healthEX[0].Overall != HealthDown
	evidence = append(evidence, DoctorStageEvidence{Stage: "node_service", State: boolDoctor(nodesOK), Reference: "health:" + t.IRNode + "," + t.EXNode, Detail: "layered node health snapshot"})

	listenerOK := false
	for _, e := range t.Evidence {
		if e.Step == "listener" || e.Step == "listen" || strings.Contains(strings.ToLower(e.Step), "listen") {
			listenerOK = e.OK
			break
		}
	}
	evidence = append(evidence, DoctorStageEvidence{Stage: "local_listener", State: boolDoctor(listenerOK), Reference: "tunnel:" + t.ID, Detail: "stored tunnel listener evidence"})

	var cell *PathMatrixCell
	for i := range matrix.Cells {
		if matrix.Cells[i].SourceNode == t.IRNode && matrix.Cells[i].DestinationNode == t.EXNode {
			cell = &matrix.Cells[i]
			break
		}
	}
	tcp, tls, carrier, app := DoctorNotAssessed, DoctorNotAssessed, DoctorNotAssessed, DoctorNotAssessed
	ref := "path-matrix:" + t.IRNode + "->" + t.EXNode
	if cell != nil && cell.State != PathMatrixStale && len(cell.Candidates) > 0 {
		c := cell.Candidates[0]
		switch c.Class {
		case PathFullData:
			tcp, tls, carrier, app = DoctorPass, DoctorPass, DoctorPass, DoctorPass
		case PathByteCeiling:
			tcp, tls, carrier, app = DoctorPass, DoctorPass, DoctorPass, DoctorFail
		case PathHandshakeOnly:
			tcp, tls, carrier = DoctorPass, DoctorPass, DoctorFail
		case PathConnectOnly:
			tcp, tls = DoctorPass, DoctorFail
		case PathUnreachable, PathLocalConflict:
			tcp = DoctorFail
		}
		ref = "probe:" + c.ProbeID
	} else if cell == nil || cell.State == PathMatrixStale || cell.State == PathMatrixNotAssessed {
		family := "4"
		if n, ok := s.GetNode(t.EXNode); ok && strings.TrimSpace(n.PathIPv4) == "" && strings.TrimSpace(n.PathIPv6) != "" {
			family = "6"
		}
		if probe, err := s.StartPathProbe(PathProbeRequest{SourceNode: t.IRNode, DestinationNode: t.EXNode, Family: family, CandidatePorts: []int{t.Port}}, now); err == nil {
			ref = "probe:" + probe.ID + ":scheduled"
		}
	}
	evidence = append(evidence,
		DoctorStageEvidence{Stage: "tcp_path", State: tcp, Reference: ref},
		DoctorStageEvidence{Stage: "tls13", State: tls, Reference: ref},
		DoctorStageEvidence{Stage: "carrier_handshake", State: carrier, Reference: ref},
	)
	authOK := t.RouteID != "" && t.PlanHash != ""
	evidence = append(evidence, DoctorStageEvidence{Stage: "route_authorization", State: boolDoctor(authOK), Reference: "tunnel:" + t.ID})
	targetOK := false
	for _, e := range t.Evidence {
		if strings.Contains(strings.ToLower(e.Step), "target") {
			targetOK = e.OK
			break
		}
	}
	evidence = append(evidence, DoctorStageEvidence{Stage: "target_reachability", State: boolDoctor(targetOK), Reference: "tunnel:" + t.ID})
	evidence = append(evidence, DoctorStageEvidence{Stage: "application_flow", State: app, Reference: ref})
	consistent := t.Drift == nil
	if t.Drift != nil {
		consistent = true
		for _, n := range t.Drift.Nodes {
			if n.State != "IN_SYNC" {
				consistent = false
				break
			}
		}
	}
	evidence = append(evidence, DoctorStageEvidence{Stage: "consistency", State: boolDoctor(consistent), Reference: "tunnel:" + t.ID})
	run := DiagnoseRouteDoctor(t.ID, now, evidence)
	run.ID = newDoctorID()

	s.mu.Lock()
	if s.st.RouteDoctorRuns == nil {
		s.st.RouteDoctorRuns = map[string]RouteDoctorRun{}
	}
	s.st.RouteDoctorRuns[run.ID] = run
	err := s.saveLocked()
	s.mu.Unlock()
	return run, err
}

func boolDoctor(ok bool) string {
	if ok {
		return DoctorPass
	}
	return DoctorFail
}

func (s *Store) ListRouteDoctorRuns(tunnelID string) []RouteDoctorRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []RouteDoctorRun{}
	for _, r := range s.st.RouteDoctorRuns {
		if tunnelID == "" || r.TunnelID == tunnelID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *Server) routeDoctorAPI(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.store.ListRouteDoctorRuns(strings.TrimSpace(r.URL.Query().Get("tunnel_id"))))
	case http.MethodPost:
		var in struct {
			TunnelID string `json:"tunnel_id"`
		}
		if err := decodeJSON(r, &in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		run, err := s.store.RunRouteDoctor(in.TunnelID, s.now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = s.audit.Append(AuditEntry{Timestamp: s.now().UTC(), Actor: "admin", RemoteIP: s.clientIP(r), Action: "route.doctor.run", Outcome: run.Verdict, Details: withRequest(r, map[string]any{"run_id": run.ID, "tunnel_id": run.TunnelID, "evidence_digest": run.EvidenceDigest})})
		writeJSON(w, http.StatusCreated, run)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
