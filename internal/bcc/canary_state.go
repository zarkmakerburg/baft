package bcc

import (
	"errors"
	"fmt"
)

// CanaryRollout is a durable plan only. Planned rollouts NEVER enqueue jobs.
// Future mutation paths must independently verify signed artifacts and health.
type CanaryRollout struct {
	ID    string     `json:"id"`
	Plan  CanaryPlan `json:"plan"`
	Phase string     `json:"phase"`
}

// SaveCanaryPlan is idempotent for the same ID and exact plan, and rejects
// conflicting reuse. It does not create deployment jobs.
func (s *Store) SaveCanaryPlan(id string, plan CanaryPlan) (CanaryRollout, error) {
	if id == "" || len(id) > 128 {
		return CanaryRollout{}, errors.New("invalid rollout id")
	}
	if !validVersion(plan.Version) || plan.CanaryNode == "" || len(plan.RemainingNodes) == 0 || plan.CreatedAt.IsZero() {
		return CanaryRollout{}, errors.New("incomplete rollout plan")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.st.CanaryRollouts[id]; ok {
		if old.Plan.Version != plan.Version || old.Plan.CanaryNode != plan.CanaryNode || !old.Plan.CreatedAt.Equal(plan.CreatedAt) || !equalNodeIDs(old.Plan.RemainingNodes, plan.RemainingNodes) {
			return CanaryRollout{}, fmt.Errorf("conflicting rollout id %q", id)
		}
		return old, nil
	}
	ids := append([]string{plan.CanaryNode}, plan.RemainingNodes...)
	seen := map[string]bool{}
	for _, nodeID := range ids {
		if seen[nodeID] {
			return CanaryRollout{}, errors.New("duplicate rollout node")
		}
		seen[nodeID] = true
		n, ok := s.st.Nodes[nodeID]
		if !ok || n.Revoked {
			return CanaryRollout{}, fmt.Errorf("unavailable node %q", nodeID)
		}
	}
	if s.st.CanaryRollouts == nil {
		s.st.CanaryRollouts = map[string]CanaryRollout{}
	}
	v := CanaryRollout{ID: id, Plan: plan, Phase: "PLAN"}
	s.st.CanaryRollouts[id] = v
	if err := s.saveLocked(); err != nil {
		delete(s.st.CanaryRollouts, id)
		return CanaryRollout{}, err
	}
	return v, nil
}

func equalNodeIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Store) GetCanaryRollout(id string) (CanaryRollout, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.st.CanaryRollouts[id]
	v.Plan.RemainingNodes = append([]string(nil), v.Plan.RemainingNodes...)
	return v, ok
}
