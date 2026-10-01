package resources

import (
	"errors"
	"sync"
)

// FlowSlots bounds the number of concurrently open Flows across every Session
// of one node (limits.max_flows). A nil *FlowSlots admits every Flow.
type FlowSlots struct {
	mu   sync.Mutex
	max  int
	used int
}

func NewFlowSlots(max int) (*FlowSlots, error) {
	if max <= 0 { return nil, errors.New("flow slot limit must be positive") }
	return &FlowSlots{max:max}, nil
}

func (s *FlowSlots) TryAcquire() bool {
	if s == nil { return true }
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used >= s.max { return false }
	s.used++
	return true
}

func (s *FlowSlots) Release() {
	if s == nil { return }
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used > 0 { s.used-- }
}

func (s *FlowSlots) Used() int {
	if s == nil { return 0 }
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used
}
