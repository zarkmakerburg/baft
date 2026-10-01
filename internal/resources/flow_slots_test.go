package resources

import "testing"

func TestFlowSlotsBoundAndRelease(t *testing.T) {
	s, err := NewFlowSlots(2)
	if err != nil { t.Fatal(err) }
	if !s.TryAcquire() || !s.TryAcquire() { t.Fatal("first two slots must be admitted") }
	if s.TryAcquire() { t.Fatal("third slot exceeded max_flows") }
	s.Release()
	if !s.TryAcquire() { t.Fatal("released slot was not reusable") }
	if got := s.Used(); got != 2 { t.Fatalf("used=%d want 2", got) }
	if _, err := NewFlowSlots(0); err == nil { t.Fatal("zero limit must be rejected") }
}

func TestNilFlowSlotsAreUnlimited(t *testing.T) {
	var s *FlowSlots
	for i := 0; i < 1000; i++ {
		if !s.TryAcquire() { t.Fatal("nil FlowSlots must admit every Flow") }
	}
	s.Release()
	if s.Used() != 0 { t.Fatal("nil FlowSlots must report zero") }
}
