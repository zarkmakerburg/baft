package bcc

import (
	"errors"
	"fmt"
	"time"
)

const (
	CanaryPhasePlan     = "PLAN"
	CanaryPhaseVerify   = "VERIFY_ARTIFACT"
	CanaryPhaseUpdate   = "CANARY_UPDATE"
	CanaryPhaseValidate = "CANARY_VALIDATE"
	CanaryPhaseReady    = "CANARY_VERIFIED"
	CanaryPhaseFailed   = "FAILED"
)

// AdvanceCanary records the next durable phase but never dispatches jobs.
// A successful job ACK alone cannot authorize fleet rollout: independent
// fresh health evidence is mandatory for CANARY_VERIFIED.
func (s *Store) AdvanceCanary(id, next string, evidence *CanaryEvidence, now time.Time, maxAge time.Duration) (CanaryRollout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.st.CanaryRollouts[id]
	if !ok {
		return CanaryRollout{}, errors.New("unknown rollout")
	}
	allowed := map[string]string{CanaryPhasePlan: CanaryPhaseVerify, CanaryPhaseVerify: CanaryPhaseUpdate, CanaryPhaseUpdate: CanaryPhaseValidate, CanaryPhaseValidate: CanaryPhaseReady}
	if next == r.Phase {
		return r, nil
	}
	if next != CanaryPhaseFailed && allowed[r.Phase] != next {
		return CanaryRollout{}, fmt.Errorf("invalid canary transition %s to %s", r.Phase, next)
	}
	if r.Phase == CanaryPhaseFailed || r.Phase == CanaryPhaseReady {
		return CanaryRollout{}, errors.New("terminal canary phase")
	}
	// Verification, update and validation are not yet wired to trusted remote
	// attestations; do not let callers manufacture success via phase changes.
	if next == CanaryPhaseUpdate || next == CanaryPhaseValidate {
		return CanaryRollout{}, errors.New("remote artifact and job attestations not integrated")
	}
	if next == CanaryPhaseReady {
		if evidence == nil {
			return CanaryRollout{}, errors.New("missing canary evidence")
		}
		if err := ValidateCanaryEvidence(*evidence, now, maxAge); err != nil {
			return CanaryRollout{}, err
		}
		return CanaryRollout{}, errors.New("trusted job and artifact linkage not integrated")
	}
	prior := r
	r.Phase = next
	s.st.CanaryRollouts[id] = r
	if err := s.saveLocked(); err != nil {
		s.st.CanaryRollouts[id] = prior
		return CanaryRollout{}, err
	}
	return r, nil
}
