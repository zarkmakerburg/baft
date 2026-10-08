package bcc

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCanaryPlanPersistsAcrossRestartWithoutJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.st.Nodes["a"] = Node{ID: "a"}
	s.st.Nodes["b"] = Node{ID: "b"}
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	plan, err := s.PlanCanary([]string{"b", "a"}, "v1.2.3", "a", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCanaryPlan("r1", plan); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCanaryPlan("r1", plan); err != nil {
		t.Fatal("idempotency:", err)
	}
	conflict := plan
	conflict.Version = "v1.2.4"
	if _, err := s.SaveCanaryPlan("r1", conflict); err == nil {
		t.Fatal("conflicting ID accepted")
	}
	restarted, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := restarted.GetCanaryRollout("r1")
	if !ok || stored.Phase != "PLAN" || stored.Plan.Version != "v1.2.3" || !equalNodeIDs(stored.Plan.RemainingNodes, []string{"b"}) {
		t.Fatalf("invalid restored rollout: %+v", stored)
	}
	restarted.mu.Lock()
	jobs := len(restarted.st.Jobs)
	restarted.mu.Unlock()
	if jobs != 0 {
		t.Fatalf("planning dispatched %d jobs", jobs)
	}
	stored.Plan.RemainingNodes[0] = "mutated"
	again, _ := restarted.GetCanaryRollout("r1")
	if again.Plan.RemainingNodes[0] != "b" {
		t.Fatal("getter exposes mutable state")
	}
}
