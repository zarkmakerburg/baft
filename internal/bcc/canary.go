package bcc

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// CanaryPlan is a non-mutating, deterministic rollout proposal. Applying it
// requires separate artifact, compatibility and health gates.
type CanaryPlan struct {
	Version        string    `json:"version"`
	CanaryNode     string    `json:"canary_node"`
	RemainingNodes []string  `json:"remaining_nodes"`
	CreatedAt      time.Time `json:"created_at"`
}

// PlanCanary rejects unknown, revoked, duplicate and empty node selections.
// No jobs are dispatched during planning.
func (s *Store) PlanCanary(ids []string, version, canary string, now time.Time) (CanaryPlan, error) {
	if !validVersion(version) {
		return CanaryPlan{}, errors.New("invalid signed release version")
	}
	if len(ids) < 2 {
		return CanaryPlan{}, errors.New("canary requires at least two enrolled nodes")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]bool, len(ids))
	remaining := make([]string, 0, len(ids)-1)
	for _, id := range ids {
		if id == "" || seen[id] {
			return CanaryPlan{}, fmt.Errorf("duplicate or empty node %q", id)
		}
		seen[id] = true
		n, ok := s.st.Nodes[id]
		if !ok || n.Revoked {
			return CanaryPlan{}, fmt.Errorf("node %q is unavailable", id)
		}
		if id != canary {
			remaining = append(remaining, id)
		}
	}
	if !seen[canary] {
		return CanaryPlan{}, errors.New("canary must be in selected nodes")
	}
	sort.Strings(remaining)
	return CanaryPlan{Version: version, CanaryNode: canary, RemainingNodes: remaining, CreatedAt: now.UTC()}, nil
}
