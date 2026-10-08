package bcc

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCanaryTransitionFailClosedAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.st.Nodes["a"] = Node{ID: "a"}
	s.st.Nodes["b"] = Node{ID: "b"}
	s.mu.Unlock()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	plan, err := s.PlanCanary([]string{"a", "b"}, "v1.0.0", "a", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCanaryPlan("rollout", plan); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{CanaryPhaseUpdate, CanaryPhaseValidate, CanaryPhaseReady} {
		if _, err := s.AdvanceCanary("rollout", phase, nil, now, time.Minute); err == nil {
			t.Fatalf("unsafe phase %s accepted", phase)
		}
	}
	if _, err := s.AdvanceCanary("rollout", CanaryPhaseVerify, nil, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvanceCanary("rollout", CanaryPhaseUpdate, nil, now, time.Minute); err == nil {
		t.Fatal("update without attestation accepted")
	}
	if _, err := s.AdvanceCanary("rollout", CanaryPhaseFailed, nil, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := restarted.GetCanaryRollout("rollout")
	if !ok || got.Phase != CanaryPhaseFailed {
		t.Fatalf("phase not durable: %+v", got)
	}
	if _, err := restarted.AdvanceCanary("rollout", CanaryPhaseVerify, nil, now, time.Minute); err == nil {
		t.Fatal("failed rollout resurrected")
	}
	restarted.mu.Lock()
	jobs := len(restarted.st.Jobs)
	restarted.mu.Unlock()
	if jobs != 0 {
		t.Fatal("transition dispatched jobs")
	}
}
