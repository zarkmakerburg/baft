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
	"strings"
	"time"

	"github.com/zarkmakerburg/baft/internal/agentjob"
)

const (
	PathDiscoveryInventory = "inventory"
	PathDiscoveryProbing   = "probing"
	PathDiscoveryComplete  = "complete"
	PathDiscoveryFailed    = "failed"

	maxAutoDiscoveryEdges   = 4096
	pathDiscoveryFreshFor   = 30 * time.Minute
	pathDiscoveryRetryAfter = 5 * time.Minute
)

var defaultDiscoveryPorts = []int{22, 80, 443, 8443, 9443, 49152}

type PathInventory struct {
	NodeID string   `json:"node_id"`
	JobID  string   `json:"job_id"`
	IPv4   []string `json:"ipv4,omitempty"`
	IPv6   []string `json:"ipv6,omitempty"`
	Error  string   `json:"error,omitempty"`
}

type PathDiscovery struct {
	ID             string                   `json:"id"`
	Phase          string                   `json:"phase"`
	CandidatePorts []int                    `json:"candidate_ports"`
	Inventory      map[string]PathInventory `json:"inventory"`
	ProbeIDs       []string                 `json:"probe_ids,omitempty"`
	Error          string                   `json:"error,omitempty"`
	CreatedAt      time.Time                `json:"created_at"`
	UpdatedAt      time.Time                `json:"updated_at"`
}

type PathHop struct {
	SourceNode      string `json:"source_node"`
	DestinationNode string `json:"destination_node"`
	Family          string `json:"family"`
	DestinationIP   string `json:"destination_ip"`
	Port            int    `json:"port"`
	ConnectRTTMS    int64  `json:"connect_rtt_ms,omitempty"`
	ProbeID         string `json:"probe_id"`
}

type PathRecommendation struct {
	SourceNode      string    `json:"source_node"`
	DestinationNode string    `json:"destination_node"`
	Hops            []PathHop `json:"hops"`
	Direct          bool      `json:"direct"`
	TotalRTTMS      int64     `json:"total_rtt_ms,omitempty"`
}

type PathGraph struct {
	GeneratedAt     time.Time            `json:"generated_at"`
	Edges           []PathHop            `json:"edges"`
	Recommendations []PathRecommendation `json:"recommendations"`
}

func newPathDiscoveryID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "pd-" + hex.EncodeToString(b), nil
}

func normalizeIPList(values []string, family string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range values {
		ip := net.ParseIP(strings.TrimSpace(raw))
		if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
			continue
		}
		if family == "4" {
			if v4 := ip.To4(); v4 != nil {
				s := v4.String()
				if !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
		} else if family == "6" && ip.To4() == nil && ip.To16() != nil {
			s := ip.String()
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

func inventoryAddresses(n Node, inv PathInventory, family string) []string {
	var raw []string
	if family == "4" {
		if n.PathIPv4 != "" {
			raw = append(raw, n.PathIPv4)
		}
		raw = append(raw, inv.IPv4...)
	} else {
		if n.PathIPv6 != "" {
			raw = append(raw, n.PathIPv6)
		}
		raw = append(raw, inv.IPv6...)
	}
	if host, _, err := net.SplitHostPort(strings.TrimSpace(n.Address)); err == nil {
		raw = append(raw, host)
	}
	return normalizeIPList(raw, family)
}

func (s *Store) StartPathDiscovery(ports []int, now time.Time) (PathDiscovery, error) {
	if len(ports) == 0 {
		ports = append([]int(nil), defaultDiscoveryPorts...)
	}
	var err error
	ports, err = validateProbePorts(ports)
	if err != nil {
		return PathDiscovery{}, err
	}
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.st.PathDiscoveries {
		if d.Phase == PathDiscoveryInventory || d.Phase == PathDiscoveryProbing {
			return PathDiscovery{}, errors.New("a path discovery is already active")
		}
	}
	nodes := make([]Node, 0)
	for _, n := range s.st.Nodes {
		if !n.Revoked {
			nodes = append(nodes, n)
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	if len(nodes) < 2 {
		return PathDiscovery{}, errors.New("at least two active enrolled nodes are required")
	}
	id, err := newPathDiscoveryID()
	if err != nil {
		return PathDiscovery{}, err
	}
	d := PathDiscovery{
		ID: id, Phase: PathDiscoveryInventory, CandidatePorts: ports,
		Inventory: map[string]PathInventory{}, CreatedAt: now, UpdatedAt: now,
	}
	for _, n := range nodes {
		j := s.newJobLocked(Job{
			Type: agentjob.ActionPathProbeInventory, NodeID: n.ID,
			Params: map[string]string{"discovery_id": id},
		})
		d.Inventory[n.ID] = PathInventory{NodeID: n.ID, JobID: j.ID}
	}
	s.st.PathDiscoveries[id] = d
	if err := s.saveLocked(); err != nil {
		delete(s.st.PathDiscoveries, id)
		return PathDiscovery{}, err
	}
	return d, nil
}

func (s *Store) advancePathDiscoveryInventoryLocked(d *PathDiscovery, now time.Time) (bool, error) {
	allDone := true
	successNodes := 0
	for nodeID, inv := range d.Inventory {
		j, ok := s.st.Jobs[inv.JobID]
		if !ok {
			inv.Error = "inventory job missing"
			d.Inventory[nodeID] = inv
			continue
		}
		done, timedOut := pathProbeJobDone(j, now)
		if !done {
			allDone = false
			continue
		}
		if timedOut {
			j.Status = "failed"
			j.Message = "path inventory step timed out"
			j.UpdatedAt = now
			s.st.Jobs[j.ID] = j
		}
		if j.Status == "failed" {
			inv.Error = j.Message
			d.Inventory[nodeID] = inv
			continue
		}
		var ev struct {
			IPv4 []string `json:"ipv4,omitempty"`
			IPv6 []string `json:"ipv6,omitempty"`
		}
		if err := json.Unmarshal([]byte(j.Output), &ev); err != nil {
			inv.Error = "invalid path inventory evidence"
			d.Inventory[nodeID] = inv
			continue
		}
		inv.IPv4 = normalizeIPList(ev.IPv4, "4")
		inv.IPv6 = normalizeIPList(ev.IPv6, "6")
		inv.Error = ""
		d.Inventory[nodeID] = inv
		successNodes++
	}
	if !allDone {
		return false, nil
	}
	if successNodes < 2 {
		d.Phase = PathDiscoveryFailed
		d.Error = "fewer than two nodes returned usable inventory evidence"
		d.UpdatedAt = now
		return true, nil
	}

	nodeIDs := make([]string, 0, len(d.Inventory))
	for id := range d.Inventory {
		if n, ok := s.st.Nodes[id]; ok && !n.Revoked {
			nodeIDs = append(nodeIDs, id)
		}
	}
	sort.Strings(nodeIDs)

	planned := 0
	for _, srcID := range nodeIDs {
		src := s.st.Nodes[srcID]
		srcInv := d.Inventory[srcID]
		for _, dstID := range nodeIDs {
			if srcID == dstID {
				continue
			}
			dst := s.st.Nodes[dstID]
			inv := d.Inventory[dstID]
			for _, family := range []string{"4", "6"} {
				// Do not schedule a family the source node cannot originate.
				// This keeps discovery generic while avoiding impossible IPv6 probes
				// from IPv4-only nodes (and vice versa).
				if len(inventoryAddresses(src, srcInv, family)) == 0 {
					continue
				}
				for _, ip := range inventoryAddresses(dst, inv, family) {
					if planned >= maxAutoDiscoveryEdges {
						d.Phase = PathDiscoveryFailed
						d.Error = fmt.Sprintf("path discovery exceeded bounded edge plan (%d)", maxAutoDiscoveryEdges)
						d.UpdatedAt = now
						return true, nil
					}
					p, err := s.createPathProbeLocked(srcID, dstID, family, ip, d.CandidatePorts, now)
					if err != nil {
						return false, err
					}
					d.ProbeIDs = append(d.ProbeIDs, p.ID)
					planned++
				}
			}
		}
	}
	if len(d.ProbeIDs) == 0 {
		d.Phase = PathDiscoveryFailed
		d.Error = "inventory produced no routable peer address candidates"
		d.UpdatedAt = now
		return true, nil
	}
	s.scheduleQueuedPathProbesLocked(now)
	d.Phase = PathDiscoveryProbing
	d.UpdatedAt = now
	return true, nil
}

func (s *Store) advancePathDiscoveryProbingLocked(d *PathDiscovery, now time.Time) bool {
	for _, id := range d.ProbeIDs {
		p, ok := s.st.PathProbes[id]
		if !ok {
			continue
		}
		switch p.Phase {
		case PathProbeComplete, PathProbeFailed, PathProbeCancelled:
		default:
			return false
		}
	}
	d.Phase = PathDiscoveryComplete
	d.UpdatedAt = now
	return true
}

func (s *Store) AdvancePathDiscoveries(now time.Time) error {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	before, err := cloneState(s.st)
	if err != nil {
		return err
	}
	changed := false
	ids := make([]string, 0, len(s.st.PathDiscoveries))
	for id := range s.st.PathDiscoveries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		d := s.st.PathDiscoveries[id]
		switch d.Phase {
		case PathDiscoveryInventory:
			c, err := s.advancePathDiscoveryInventoryLocked(&d, now)
			if err != nil {
				s.st = before
				return err
			}
			if c {
				changed = true
			}
		case PathDiscoveryProbing:
			if s.advancePathDiscoveryProbingLocked(&d, now) {
				changed = true
			}
		}
		s.st.PathDiscoveries[id] = d
	}
	if !changed {
		return nil
	}
	if err := s.saveLocked(); err != nil {
		s.st = before
		return err
	}
	return nil
}

func (s *Store) ListPathDiscoveries() []PathDiscovery {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PathDiscovery, 0, len(s.st.PathDiscoveries))
	for _, d := range s.st.PathDiscoveries {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func bestFullDataEdge(p PathProbe) (PathHop, bool) {
	best := PathHop{}
	ok := false
	for _, c := range p.Candidates {
		if c.Class != PathFullData {
			continue
		}
		h := PathHop{
			SourceNode: p.SourceNode, DestinationNode: p.DestinationNode,
			Family: p.Family, DestinationIP: p.DestinationIP, Port: c.Port,
			ConnectRTTMS: c.ConnectRTTMS, ProbeID: p.ID,
		}
		if !ok || h.ConnectRTTMS < best.ConnectRTTMS || (h.ConnectRTTMS == best.ConnectRTTMS && h.Port < best.Port) {
			best, ok = h, true
		}
	}
	return best, ok
}

func (s *Store) PathGraph(now time.Time) PathGraph {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := PathGraph{GeneratedAt: now.UTC()}
	edgeByPair := map[string]PathHop{}
	for _, p := range s.st.PathProbes {
		if p.Phase != PathProbeComplete || p.UpdatedAt.IsZero() || now.Sub(p.UpdatedAt) > pathDiscoveryFreshFor {
			continue
		}
		src, srcOK := s.st.Nodes[p.SourceNode]
		dst, dstOK := s.st.Nodes[p.DestinationNode]
		if !srcOK || !dstOK || src.Revoked || dst.Revoked {
			continue
		}
		h, ok := bestFullDataEdge(p)
		if !ok {
			continue
		}
		k := h.SourceNode + "\x00" + h.DestinationNode
		old, exists := edgeByPair[k]
		if !exists || h.ConnectRTTMS < old.ConnectRTTMS ||
			(h.ConnectRTTMS == old.ConnectRTTMS && h.Port < old.Port) {
			edgeByPair[k] = h
		}
	}
	for _, h := range edgeByPair {
		g.Edges = append(g.Edges, h)
	}
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].SourceNode != g.Edges[j].SourceNode {
			return g.Edges[i].SourceNode < g.Edges[j].SourceNode
		}
		return g.Edges[i].DestinationNode < g.Edges[j].DestinationNode
	})

	nodes := make([]string, 0)
	for id, n := range s.st.Nodes {
		if !n.Revoked {
			nodes = append(nodes, id)
		}
	}
	sort.Strings(nodes)
	adj := map[string][]PathHop{}
	for _, e := range g.Edges {
		adj[e.SourceNode] = append(adj[e.SourceNode], e)
	}
	for _, src := range nodes {
		for _, dst := range nodes {
			if src == dst {
				continue
			}
			if rec, ok := bestPathRecommendation(src, dst, adj); ok {
				g.Recommendations = append(g.Recommendations, rec)
			}
		}
	}
	return g
}

type pathSearchState struct {
	node string
	hops []PathHop
	rtt  int64
}

func betterPath(a, b pathSearchState) bool {
	if len(a.hops) != len(b.hops) {
		return len(a.hops) < len(b.hops)
	}
	return a.rtt < b.rtt
}

func bestPathRecommendation(src, dst string, adj map[string][]PathHop) (PathRecommendation, bool) {
	q := []pathSearchState{{node: src}}
	best := map[string]pathSearchState{src: {node: src}}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		if cur.node == dst {
			return PathRecommendation{
				SourceNode: src, DestinationNode: dst, Hops: cur.hops,
				Direct: len(cur.hops) == 1, TotalRTTMS: cur.rtt,
			}, true
		}
		if len(cur.hops) >= 4 {
			continue
		}
		edges := append([]PathHop(nil), adj[cur.node]...)
		sort.Slice(edges, func(i, j int) bool {
			if edges[i].ConnectRTTMS != edges[j].ConnectRTTMS {
				return edges[i].ConnectRTTMS < edges[j].ConnectRTTMS
			}
			return edges[i].DestinationNode < edges[j].DestinationNode
		})
		for _, e := range edges {
			next := pathSearchState{
				node: e.DestinationNode,
				hops: append(append([]PathHop(nil), cur.hops...), e),
				rtt:  cur.rtt + e.ConnectRTTMS,
			}
			old, exists := best[next.node]
			if exists && !betterPath(next, old) {
				continue
			}
			best[next.node] = next
			q = append(q, next)
		}
		sort.SliceStable(q, func(i, j int) bool { return betterPath(q[i], q[j]) })
	}
	return PathRecommendation{}, false
}

func (s *Store) NeedsPathDiscovery(now time.Time) bool {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	activeNodes := make([]string, 0)
	for id, n := range s.st.Nodes {
		if !n.Revoked {
			activeNodes = append(activeNodes, id)
		}
	}
	if len(activeNodes) < 2 {
		return false
	}
	sort.Strings(activeNodes)
	var latest *PathDiscovery
	for _, d := range s.st.PathDiscoveries {
		if d.Phase == PathDiscoveryInventory || d.Phase == PathDiscoveryProbing {
			return false
		}
		copy := d
		if latest == nil || copy.CreatedAt.After(latest.CreatedAt) {
			latest = &copy
		}
	}
	if latest == nil {
		return true
	}
	invNodes := make([]string, 0, len(latest.Inventory))
	for id := range latest.Inventory {
		invNodes = append(invNodes, id)
	}
	for _, n := range s.st.Nodes {
		if !n.Revoked && n.UpdatedAt.After(latest.UpdatedAt) {
			return true
		}
	}
	sort.Strings(invNodes)
	if len(invNodes) != len(activeNodes) {
		return true
	}
	for i := range activeNodes {
		if activeNodes[i] != invNodes[i] {
			return true
		}
	}
	age := now.Sub(latest.UpdatedAt)
	if latest.Phase == PathDiscoveryFailed {
		return age >= pathDiscoveryRetryAfter
	}
	return age >= pathDiscoveryFreshFor
}

func (s *Server) AdvancePathDiscoveries() {
	now := s.now()
	_ = s.store.AdvancePathDiscoveries(now)
	if !s.store.NeedsPathDiscovery(now) {
		return
	}
	d, err := s.store.StartPathDiscovery(nil, now)
	if err != nil {
		return
	}
	_, _ = s.audit.Append(AuditEntry{Timestamp: now.UTC(), Actor: "bcc", Action: "pathdiscovery.auto_start", Target: d.ID, Outcome: "success", Details: map[string]any{"node_count": len(d.Inventory), "candidate_ports": d.CandidatePorts}})
}

func (s *Server) pathDiscoveryAPI(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.store.ListPathDiscoveries())
	case http.MethodPost:
		s.mutationMu.Lock()
		defer s.mutationMu.Unlock()
		var in struct {
			CandidatePorts []int `json:"candidate_ports"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		d, err := s.store.StartPathDiscovery(in.CandidatePorts, s.now())
		if err != nil {
			s.auditFailure(w, r, "pathdiscovery.start", "cluster", nil, err, http.StatusBadRequest)
			return
		}
		if err := s.auditAdmin(r, "pathdiscovery.start", d.ID, "success", map[string]any{
			"candidate_ports": d.CandidatePorts,
			"node_count":      len(d.Inventory),
		}); err != nil {
			http.Error(w, "audit log failure", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusAccepted, d)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) pathGraphAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.store.PathGraph(s.now()))
}
