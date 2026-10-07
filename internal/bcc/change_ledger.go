package bcc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type ConfigSnapshot struct {
	Generation   int      `json:"generation,omitempty"`
	ConfigSHA256 string   `json:"config_sha256,omitempty"`
	UnitSHA256   string   `json:"unit_sha256,omitempty"`
	MarkerSHA256 string   `json:"marker_sha256,omitempty"`
	Listeners    []string `json:"listeners,omitempty"`
	RouteID      string   `json:"route_id,omitempty"`
	TunnelID     string   `json:"tunnel_id,omitempty"`
	InstanceID   string   `json:"instance_id,omitempty"`
	Service      string   `json:"service,omitempty"`
	Version      string   `json:"version,omitempty"`
}
type SnapshotDiff struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}
type ChangeRecord struct {
	ID          string         `json:"id"`
	Sequence    uint64         `json:"sequence"`
	At          time.Time      `json:"at"`
	Actor       string         `json:"actor"`
	RequestID   string         `json:"request_id,omitempty"`
	PlanHash    string         `json:"plan_hash,omitempty"`
	NodeID      string         `json:"node_id"`
	InstanceID  string         `json:"instance_id,omitempty"`
	TunnelID    string         `json:"tunnel_id,omitempty"`
	RouteID     string         `json:"route_id,omitempty"`
	JobIDs      []string       `json:"job_ids,omitempty"`
	EvidenceIDs []string       `json:"evidence_ids,omitempty"`
	Outcome     string         `json:"outcome"`
	RollbackOf  string         `json:"rollback_of,omitempty"`
	Before      ConfigSnapshot `json:"before"`
	After       ConfigSnapshot `json:"after"`
	Digest      string         `json:"digest"`
	Diff        []SnapshotDiff `json:"diff,omitempty"`
}

func normalizeSnapshot(x ConfigSnapshot) ConfigSnapshot {
	x.Listeners = append([]string(nil), x.Listeners...)
	sort.Strings(x.Listeners)
	return x
}
func snapshotDiff(a, b ConfigSnapshot) []SnapshotDiff {
	a, b = normalizeSnapshot(a), normalizeSnapshot(b)
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	var am, bm map[string]any
	_ = json.Unmarshal(aa, &am)
	_ = json.Unmarshal(bb, &bm)
	keys := map[string]bool{}
	for k := range am {
		keys[k] = true
	}
	for k := range bm {
		keys[k] = true
	}
	names := []string{}
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	out := []SnapshotDiff{}
	for _, k := range names {
		av, _ := json.Marshal(am[k])
		bv, _ := json.Marshal(bm[k])
		if string(av) != string(bv) {
			out = append(out, SnapshotDiff{Field: k, Before: string(av), After: string(bv)})
		}
	}
	return out
}
func changeDigest(r ChangeRecord) string {
	r.Digest = ""
	r.Diff = nil
	r.Before = normalizeSnapshot(r.Before)
	r.After = normalizeSnapshot(r.After)
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *Store) appendChangeLocked(r ChangeRecord) ChangeRecord {
	s.st.NextChangeSequence++
	r.Sequence = s.st.NextChangeSequence
	if r.ID == "" {
		r.ID = fmt.Sprintf("chg-%012d", r.Sequence)
	}
	r.Before = normalizeSnapshot(r.Before)
	r.After = normalizeSnapshot(r.After)
	r.Diff = snapshotDiff(r.Before, r.After)
	r.Digest = changeDigest(r)
	s.st.ChangeLedger = append(s.st.ChangeLedger, r)
	return r
}
func (s *Store) recordActiveTunnelChangesLocked(t *Tunnel, now time.Time) {
	for _, node := range []string{t.IRNode, t.EXNode} {
		d := t.Digests[node]
		gen := t.ObservedGen[node]
		before := ConfigSnapshot{Generation: gen - 1, InstanceID: t.InstanceID}
		after := ConfigSnapshot{Generation: gen, ConfigSHA256: d.Config, UnitSHA256: d.Unit, MarkerSHA256: d.Marker, RouteID: t.RouteID, TunnelID: t.ID, InstanceID: t.InstanceID, Listeners: []string{fmt.Sprintf("%s:%d", t.PublicAddress, t.Port)}}
		s.appendChangeLocked(ChangeRecord{At: now.UTC(), Actor: "bcc", PlanHash: t.PlanHash, NodeID: node, InstanceID: t.InstanceID, TunnelID: t.ID, RouteID: t.RouteID, JobIDs: append([]string(nil), t.Jobs...), Outcome: "APPLIED", Before: before, After: after})
	}
}
func (s *Store) ListChangeRecords(node, tunnel string) []ChangeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ChangeRecord{}
	for _, r := range s.st.ChangeLedger {
		if node != "" && r.NodeID != node {
			continue
		}
		if tunnel != "" && r.TunnelID != tunnel {
			continue
		}
		out = append(out, r)
	}
	return out
}
func (s *Server) changeLedgerAPI(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	writeJSON(w, 200, s.store.ListChangeRecords(strings.TrimSpace(r.URL.Query().Get("node_id")), strings.TrimSpace(r.URL.Query().Get("tunnel_id"))))
}
