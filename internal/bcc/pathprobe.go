package bcc

import (
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
)

const (
	PathProbeQueued           = "queued"
	PathProbeStartingListener = "starting_listener"
	PathProbeRunning          = "running"
	PathProbeStopping         = "stopping_listener"
	PathProbeComplete         = "complete"
	PathProbeFailed           = "failed"
	PathProbeCancelled        = "cancelled"

	PathFullData      = "FULL_DATA"
	PathByteCeiling   = "BYTE_CEILING"
	PathHandshakeOnly = "HANDSHAKE_ONLY"
	PathConnectOnly   = "CONNECT_ONLY"
	PathUnreachable   = "TIMEOUT_UNREACHABLE"
	PathLocalConflict = "LOCAL_CONFLICT"
	PathUnknown       = "UNKNOWN"

	pathProbeTTL        = 45
	pathProbeJobTimeout = 90 * time.Second
)

type PathProbeRequest struct {
	SourceNode      string `json:"source_node"`
	DestinationNode string `json:"destination_node"`
	Family          string `json:"family"`
	CandidatePorts  []int  `json:"candidate_ports"`
}

type PathProbeCandidate struct {
	Port             int     `json:"port"`
	Class            string  `json:"class,omitempty"`
	ByteCeiling      int64   `json:"byte_ceiling,omitempty"`
	ConnectSuccesses int     `json:"connect_successes,omitempty"`
	ACKSuccesses     int     `json:"ack_successes,omitempty"`
	ConnectRTTMS     int64   `json:"connect_rtt_ms,omitempty"`
	TricklePassed    bool    `json:"trickle_passed,omitempty"`
	Received         []int64 `json:"received,omitempty"`
	Detail           string  `json:"detail,omitempty"`
	ListenJobID      string  `json:"listen_job_id,omitempty"`
	RunJobID         string  `json:"run_job_id,omitempty"`
	StopJobID        string  `json:"stop_job_id,omitempty"`
}

type PathProbe struct {
	ID              string               `json:"id"`
	SourceNode      string               `json:"source_node"`
	DestinationNode string               `json:"destination_node"`
	Family          string               `json:"family"`
	DestinationIP   string               `json:"destination_ip"`
	Phase           string               `json:"phase"`
	Candidates      []PathProbeCandidate `json:"candidates"`
	Current         int                  `json:"current,omitempty"`
	JobID           string               `json:"job_id,omitempty"`
	RecommendedPort int                  `json:"recommended_port,omitempty"`
	Error           string               `json:"error,omitempty"`
	CancelRequested bool                 `json:"cancel_requested,omitempty"`
	CreatedAt       time.Time            `json:"created_at"`
	UpdatedAt       time.Time            `json:"updated_at"`
}

type PathProbeEvent struct {
	ProbeID string
	Action  string
	Detail  string
}

type probeFlowEvidence struct {
	Requested    int64  `json:"requested"`
	BytesWritten int64  `json:"bytes_written"`
	ConnectMS    int64  `json:"connect_ms"`
	DurationMS   int64  `json:"duration_ms"`
	Connected    bool   `json:"connected"`
	Handshake    bool   `json:"handshake"`
	ACK          bool   `json:"ack"`
	Error        string `json:"error,omitempty"`
}

type probeRunEvidence struct {
	ProbeID string              `json:"probe_id"`
	Family  string              `json:"family"`
	Target  string              `json:"target"`
	Bulk    []probeFlowEvidence `json:"bulk"`
	Trickle probeFlowEvidence   `json:"trickle"`
}

type probeListenEvidence struct {
	ProbeID  string   `json:"probe_id"`
	Family   string   `json:"family,omitempty"`
	Port     int      `json:"port,omitempty"`
	Received []int64  `json:"received,omitempty"`
	Full     []bool   `json:"full,omitempty"`
	Errors   []string `json:"errors,omitempty"`
	Expired  bool     `json:"expired,omitempty"`
}

func newPathProbeID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "pp-" + hex.EncodeToString(b), nil
}

func nodePathIP(n Node, family string) (string, error) {
	raw := ""
	switch family {
	case "4":
		raw = strings.TrimSpace(n.PathIPv4)
	case "6":
		raw = strings.TrimSpace(n.PathIPv6)
	default:
		return "", errors.New("family must be 4 or 6")
	}
	if raw == "" {
		host, _, err := net.SplitHostPort(strings.TrimSpace(n.Address))
		if err != nil {
			return "", fmt.Errorf("node %s has no usable path address for IPv%s", n.ID, family)
		}
		raw = host
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		return "", fmt.Errorf("node %s path address must be a literal IP", n.ID)
	}
	if family == "4" {
		if ip.To4() == nil {
			return "", fmt.Errorf("node %s has no IPv4 path address", n.ID)
		}
		return ip.To4().String(), nil
	}
	if ip.To4() != nil || ip.To16() == nil {
		return "", fmt.Errorf("node %s has no IPv6 path address", n.ID)
	}
	return ip.String(), nil
}

func validateProbePorts(in []int) ([]int, error) {
	if len(in) == 0 || len(in) > 16 {
		return nil, errors.New("candidate_ports must contain 1 to 16 ports")
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(in))
	for _, p := range in {
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("invalid candidate port %d", p)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("no unique candidate port remains")
	}
	sort.Ints(out)
	return out, nil
}

func (s *Store) StartPathProbe(req PathProbeRequest, now time.Time) (PathProbe, error) {
	req.SourceNode = strings.TrimSpace(req.SourceNode)
	req.DestinationNode = strings.TrimSpace(req.DestinationNode)
	if req.SourceNode == "" || req.DestinationNode == "" || req.SourceNode == req.DestinationNode {
		return PathProbe{}, errors.New("source_node and destination_node must name two different enrolled nodes")
	}
	if req.Family != "4" && req.Family != "6" {
		return PathProbe{}, errors.New("family must be 4 or 6")
	}
	ports, err := validateProbePorts(req.CandidatePorts)
	if err != nil {
		return PathProbe{}, err
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.st.Nodes[req.SourceNode]
	if !ok || src.Revoked {
		return PathProbe{}, errors.New("source node is not an active enrolled node")
	}
	dst, ok := s.st.Nodes[req.DestinationNode]
	if !ok || dst.Revoked {
		return PathProbe{}, errors.New("destination node is not an active enrolled node")
	}
	ip, err := nodePathIP(dst, req.Family)
	if err != nil {
		return PathProbe{}, err
	}
	id, err := newPathProbeID()
	if err != nil {
		return PathProbe{}, err
	}
	p := PathProbe{
		ID: id, SourceNode: src.ID, DestinationNode: dst.ID, Family: req.Family,
		DestinationIP: ip, Phase: PathProbeQueued, CreatedAt: now, UpdatedAt: now,
	}
	for _, port := range ports {
		p.Candidates = append(p.Candidates, PathProbeCandidate{Port: port})
	}
	s.st.PathProbes[p.ID] = p
	s.scheduleQueuedPathProbesLocked(now)
	p = s.st.PathProbes[p.ID]
	if err := s.saveLocked(); err != nil {
		delete(s.st.PathProbes, p.ID)
		return PathProbe{}, err
	}
	return p, nil
}

func (s *Store) activePathProbeCountLocked() int {
	n := 0
	for _, p := range s.st.PathProbes {
		switch p.Phase {
		case PathProbeQueued, PathProbeComplete, PathProbeFailed, PathProbeCancelled:
		default:
			n++
		}
	}
	return n
}

func (s *Store) scheduleQueuedPathProbesLocked(now time.Time) {
	available := 4 - s.activePathProbeCountLocked()
	if available <= 0 {
		return
	}
	ids := make([]string, 0)
	for id, p := range s.st.PathProbes {
		if p.Phase == PathProbeQueued {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := s.st.PathProbes[ids[i]], s.st.PathProbes[ids[j]]
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID < b.ID
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
	for _, id := range ids {
		if available <= 0 {
			break
		}
		p := s.st.PathProbes[id]
		s.queuePathProbeListenLocked(&p, now)
		s.st.PathProbes[id] = p
		available--
	}
}

func (s *Store) createPathProbeLocked(source, destination, family, destinationIP string, ports []int, now time.Time) (PathProbe, error) {
	id, err := newPathProbeID()
	if err != nil {
		return PathProbe{}, err
	}
	p := PathProbe{ID: id, SourceNode: source, DestinationNode: destination, Family: family, DestinationIP: destinationIP, Phase: PathProbeQueued, CreatedAt: now, UpdatedAt: now}
	for _, port := range ports {
		p.Candidates = append(p.Candidates, PathProbeCandidate{Port: port})
	}
	s.st.PathProbes[id] = p
	return p, nil
}

func (s *Store) queuePathProbeListenLocked(p *PathProbe, now time.Time) {
	c := &p.Candidates[p.Current]
	j := s.newJobLocked(Job{
		Type: agentjob.ActionPathProbeListen, NodeID: p.DestinationNode,
		Params: map[string]string{
			"probe_id": p.ID, "family": p.Family, "port": strconv.Itoa(c.Port),
			"ttl_seconds": strconv.Itoa(pathProbeTTL),
		},
	})
	c.ListenJobID, p.JobID, p.Phase, p.UpdatedAt = j.ID, j.ID, PathProbeStartingListener, now
}

func (s *Store) queuePathProbeRunLocked(p *PathProbe, now time.Time) {
	c := &p.Candidates[p.Current]
	target := net.JoinHostPort(p.DestinationIP, strconv.Itoa(c.Port))
	j := s.newJobLocked(Job{
		Type: agentjob.ActionPathProbeRun, NodeID: p.SourceNode,
		Params: map[string]string{
			"probe_id": p.ID, "family": p.Family, "target": target,
			"payload_bytes": "65536", "attempts": "3",
			"trickle_bytes": "16384", "trickle_ms": "1000",
		},
	})
	c.RunJobID, p.JobID, p.Phase, p.UpdatedAt = j.ID, j.ID, PathProbeRunning, now
}

func (s *Store) queuePathProbeStopLocked(p *PathProbe, now time.Time) {
	c := &p.Candidates[p.Current]
	j := s.newJobLocked(Job{
		Type: agentjob.ActionPathProbeStop, NodeID: p.DestinationNode,
		Params: map[string]string{"probe_id": p.ID},
	})
	c.StopJobID, p.JobID, p.Phase, p.UpdatedAt = j.ID, j.ID, PathProbeStopping, now
}

func pathProbeJobDone(j Job, now time.Time) (done bool, timedOut bool) {
	if j.Status == "succeeded" || j.Status == "failed" {
		return true, false
	}
	if !j.CreatedAt.IsZero() && now.Sub(j.CreatedAt) > pathProbeJobTimeout {
		return true, true
	}
	return false, false
}

func (s *Store) advancePathProbeCandidateLocked(p *PathProbe, now time.Time) {
	p.Current++
	p.JobID = ""
	p.UpdatedAt = now
	if p.Current >= len(p.Candidates) {
		p.Phase = PathProbeComplete
		p.RecommendedPort = recommendProbePort(p.Candidates)
		return
	}
	s.queuePathProbeListenLocked(p, now)
}

func recommendProbePort(cs []PathProbeCandidate) int {
	bestPort, bestRTT := 0, int64(1<<62)
	for _, c := range cs {
		if c.Class != PathFullData {
			continue
		}
		rtt := c.ConnectRTTMS
		if rtt <= 0 {
			rtt = 1 << 61
		}
		if bestPort == 0 || rtt < bestRTT || (rtt == bestRTT && c.Port < bestPort) {
			bestPort, bestRTT = c.Port, rtt
		}
	}
	return bestPort
}

func repeatedCeiling(values []int64, limit int64) int64 {
	count := map[int64]int{}
	var best int64
	bestN := 0
	for _, v := range values {
		if v <= 0 || v >= limit {
			continue
		}
		count[v]++
		if count[v] > bestN || (count[v] == bestN && (best == 0 || v < best)) {
			best, bestN = v, count[v]
		}
	}
	if bestN >= 2 {
		return best
	}
	return 0
}

func classifyPathProbeCandidate(c *PathProbeCandidate, run probeRunEvidence, listen probeListenEvidence) {
	connectSum := int64(0)
	handshakes := 0
	for _, f := range run.Bulk {
		if f.Connected {
			c.ConnectSuccesses++
			connectSum += f.ConnectMS
		}
		if f.Handshake {
			handshakes++
		}
		if f.ACK {
			c.ACKSuccesses++
		}
	}
	if c.ConnectSuccesses > 0 {
		c.ConnectRTTMS = connectSum / int64(c.ConnectSuccesses)
	}
	c.TricklePassed = run.Trickle.ACK
	c.Received = append([]int64(nil), listen.Received...)
	allBulk := len(run.Bulk) > 0 && c.ACKSuccesses == len(run.Bulk)
	if allBulk && run.Trickle.ACK {
		c.Class = PathFullData
		c.Detail = "integrity-checked bulk attempts and trickle flow completed"
		return
	}
	if ceiling := repeatedCeiling(listen.Received, 65536); ceiling > 0 {
		c.Class, c.ByteCeiling = PathByteCeiling, ceiling
		c.Detail = fmt.Sprintf("repeated receiver ceiling at %d bytes", ceiling)
		return
	}
	if handshakes > 0 {
		c.Class = PathHandshakeOnly
		c.Detail = "probe application handshake completed but integrity-checked data did not"
		return
	}
	if c.ConnectSuccesses > 0 {
		c.Class = PathConnectOnly
		c.Detail = "TCP connected but the probe handshake did not complete"
		return
	}
	c.Class = PathUnreachable
	c.Detail = "no probe flow delivered application data"
}

func (s *Store) CancelPathProbe(id string, now time.Time) (PathProbe, error) {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.st.PathProbes[id]
	if !ok {
		return PathProbe{}, errors.New("path probe not found")
	}
	if p.Phase == PathProbeComplete || p.Phase == PathProbeFailed || p.Phase == PathProbeCancelled {
		return p, nil
	}
	p.CancelRequested = true
	p.Error = "cancelled by operator"
	if p.Phase != PathProbeStopping {
		s.queuePathProbeStopLocked(&p, now)
	}
	p.UpdatedAt = now
	s.st.PathProbes[id] = p
	return p, s.saveLocked()
}

func (s *Store) AdvancePathProbes(now time.Time) ([]PathProbeEvent, error) {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	before, err := cloneState(s.st)
	if err != nil {
		return nil, err
	}
	var events []PathProbeEvent
	ids := make([]string, 0, len(s.st.PathProbes))
	for id := range s.st.PathProbes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	changed := false
	for _, id := range ids {
		p := s.st.PathProbes[id]
		if p.Phase == PathProbeComplete || p.Phase == PathProbeFailed || p.Phase == PathProbeCancelled || p.Current >= len(p.Candidates) {
			continue
		}
		c := &p.Candidates[p.Current]
		j, ok := s.st.Jobs[p.JobID]
		if !ok {
			p.Phase, p.Error, p.UpdatedAt = PathProbeFailed, "current probe job is missing", now
			s.st.PathProbes[id] = p
			events = append(events, PathProbeEvent{ProbeID: id, Action: "pathprobe.failed", Detail: p.Error})
			changed = true
			continue
		}
		done, timedOut := pathProbeJobDone(j, now)
		if !done {
			continue
		}
		if timedOut {
			j.Status = "failed"
			j.Message = "path probe step timed out"
			j.UpdatedAt = now
			s.st.Jobs[j.ID] = j
		}
		switch p.Phase {
		case PathProbeStartingListener:
			if timedOut || j.Status == "failed" {
				if strings.Contains(strings.ToLower(j.Message), "address already in use") {
					c.Class = PathLocalConflict
					c.Detail = "candidate port is occupied on destination"
				} else {
					c.Class = PathUnknown
					if timedOut {
						c.Detail = "listener start timed out; self-expiry cleans temporary listener"
					} else {
						c.Detail = "listener could not start: " + j.Message
					}
				}
				s.advancePathProbeCandidateLocked(&p, now)
			} else {
				s.queuePathProbeRunLocked(&p, now)
			}
		case PathProbeRunning:
			s.queuePathProbeStopLocked(&p, now)
		case PathProbeStopping:
			if p.CancelRequested {
				c.Class = PathUnknown
				c.Detail = "probe cancelled; temporary listener stop was requested and also self-expires"
				p.Phase, p.JobID, p.UpdatedAt = PathProbeCancelled, "", now
				events = append(events, PathProbeEvent{ProbeID: p.ID, Action: "pathprobe.cancelled", Detail: p.Error})
				break
			}
			var run probeRunEvidence
			var listen probeListenEvidence
			if rj, ok := s.st.Jobs[c.RunJobID]; ok && rj.Status == "succeeded" {
				_ = json.Unmarshal([]byte(rj.Output), &run)
			}
			if j.Status == "succeeded" {
				_ = json.Unmarshal([]byte(j.Output), &listen)
			}
			classifyPathProbeCandidate(c, run, listen)
			s.advancePathProbeCandidateLocked(&p, now)
		}
		if p.Phase == PathProbeComplete {
			events = append(events, PathProbeEvent{
				ProbeID: p.ID, Action: "pathprobe.complete",
				Detail: fmt.Sprintf("family IPv%s; recommended port %d", p.Family, p.RecommendedPort),
			})
		}
		s.st.PathProbes[id] = p
		changed = true
	}
	beforeQueued := s.activePathProbeCountLocked()
	s.scheduleQueuedPathProbesLocked(now)
	if s.activePathProbeCountLocked() != beforeQueued {
		changed = true
	}
	if !changed {
		return nil, nil
	}
	if err := s.saveLocked(); err != nil {
		s.st = before
		return nil, err
	}
	return events, nil
}

func (s *Store) GetPathProbe(id string) (PathProbe, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.st.PathProbes[id]
	return p, ok
}

func (s *Store) ListPathProbes() []PathProbe {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PathProbe, 0, len(s.st.PathProbes))
	for _, p := range s.st.PathProbes {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *Server) AdvancePathProbes() {
	events, err := s.store.AdvancePathProbes(s.now())
	if err != nil {
		return
	}
	for _, e := range events {
		outcome := "success"
		if e.Action == "pathprobe.failed" {
			outcome = "failure"
		}
		_, _ = s.audit.Append(AuditEntry{
			Timestamp: s.now().UTC(), Actor: "bcc", Action: e.Action, Target: e.ProbeID,
			Outcome: outcome, Details: map[string]any{"detail": e.Detail},
		})
	}
}

func (s *Server) pathProbes(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if id := strings.TrimSpace(r.URL.Query().Get("id")); id != "" {
			p, ok := s.store.GetPathProbe(id)
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, http.StatusOK, p)
			return
		}
		writeJSON(w, http.StatusOK, s.store.ListPathProbes())
	case http.MethodPost:
		s.mutationMu.Lock()
		defer s.mutationMu.Unlock()
		var in PathProbeRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		details := map[string]any{
			"source_node": in.SourceNode, "destination_node": in.DestinationNode,
			"family": in.Family, "candidate_ports": append([]int(nil), in.CandidatePorts...),
		}
		p, err := s.store.StartPathProbe(in, s.now())
		if err != nil {
			s.auditFailure(w, r, "pathprobe.start", in.SourceNode+"->"+in.DestinationNode, details, err, http.StatusBadRequest)
			return
		}
		details["probe_id"] = p.ID
		if err := s.auditAdmin(r, "pathprobe.start", p.ID, "success", details); err != nil {
			http.Error(w, "audit log failure", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusAccepted, p)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) pathProbeCancel(w http.ResponseWriter, r *http.Request) {
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
		ID string `json:"id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p, err := s.store.CancelPathProbe(strings.TrimSpace(in.ID), s.now())
	if err != nil {
		s.auditFailure(w, r, "pathprobe.cancel", in.ID, nil, err, http.StatusBadRequest)
		return
	}
	if err := s.auditAdmin(r, "pathprobe.cancel", p.ID, "success", map[string]any{"cleanup_job_id": p.JobID}); err != nil {
		http.Error(w, "audit log failure", 500)
		return
	}
	writeJSON(w, http.StatusAccepted, p)
}
