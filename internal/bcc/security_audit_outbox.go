package bcc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

func canonicalAuditIntent(e AuditEntry) ([]byte, error) {
	e.Sequence = 0
	e.PrevHash = ""
	e.Hash = ""
	e.IntentID = ""
	return json.Marshal(e)
}

func stampSecurityAuditIntent(e AuditEntry) (AuditEntry, error) {
	if e.Timestamp.IsZero() {
		return AuditEntry{}, errors.New("security audit intent timestamp is required")
	}
	if e.Action == "" || e.Actor == "" || e.Outcome == "" {
		return AuditEntry{}, errors.New("security audit intent actor/action/outcome is required")
	}
	e.Timestamp = e.Timestamp.UTC()
	raw, err := canonicalAuditIntent(e)
	if err != nil {
		return AuditEntry{}, err
	}
	sum := sha256.Sum256(raw)
	e.IntentID = hex.EncodeToString(sum[:])
	return e, nil
}

func sameSecurityAuditIntent(a, b AuditEntry) bool {
	aa, errA := canonicalAuditIntent(a)
	bb, errB := canonicalAuditIntent(b)
	return errA == nil && errB == nil && string(aa) == string(bb)
}

func mergeAuditDetails(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (s *Store) enqueueSecurityAuditLocked(e AuditEntry) (AuditEntry, error) {
	e, err := stampSecurityAuditIntent(e)
	if err != nil {
		return AuditEntry{}, err
	}
	if s.st.SecurityAuditIntents == nil {
		s.st.SecurityAuditIntents = map[string]AuditEntry{}
	}
	if old, ok := s.st.SecurityAuditIntents[e.IntentID]; ok {
		if !sameSecurityAuditIntent(old, e) {
			return AuditEntry{}, fmt.Errorf("conflicting security audit intent %s", e.IntentID)
		}
		return old, nil
	}
	s.st.SecurityAuditIntents[e.IntentID] = e
	return e, nil
}

func (s *Store) PendingSecurityAuditIntents() []AuditEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AuditEntry, 0, len(s.st.SecurityAuditIntents))
	for _, e := range s.st.SecurityAuditIntents {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].IntentID < out[j].IntentID
		}
		return out[i].Timestamp.Before(out[j].Timestamp)
	})
	return out
}

func (s *Store) AckSecurityAuditIntent(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.st.SecurityAuditIntents[id]
	if !ok {
		return nil
	}
	delete(s.st.SecurityAuditIntents, id)
	if err := s.saveLocked(); err != nil {
		s.st.SecurityAuditIntents[id] = e
		return err
	}
	return nil
}

func (s *Server) FlushSecurityAuditIntents() error {
	s.securityAuditMu.Lock()
	defer s.securityAuditMu.Unlock()
	pending := s.store.PendingSecurityAuditIntents()
	if len(pending) == 0 {
		return nil
	}
	existing, err := s.audit.List(0)
	if err != nil {
		return err
	}
	delivered := make(map[string]AuditEntry, len(existing))
	for _, e := range existing {
		if e.IntentID == "" {
			continue
		}
		stamped, err := stampSecurityAuditIntent(e)
		if err != nil || stamped.IntentID != e.IntentID {
			return fmt.Errorf("audit intent %q failed integrity reconciliation", e.IntentID)
		}
		if _, duplicate := delivered[e.IntentID]; duplicate {
			return fmt.Errorf("duplicate delivered security audit intent %s", e.IntentID)
		}
		delivered[e.IntentID] = e
	}
	for _, intent := range pending {
		if got, ok := delivered[intent.IntentID]; ok {
			if !sameSecurityAuditIntent(got, intent) {
				return fmt.Errorf("audit intent %s conflicts with delivered entry", intent.IntentID)
			}
			if err := s.store.AckSecurityAuditIntent(intent.IntentID); err != nil {
				return err
			}
			continue
		}
		if _, err := s.audit.Append(intent); err != nil {
			return err
		}
		if err := s.store.AckSecurityAuditIntent(intent.IntentID); err != nil {
			return err
		}
	}
	return nil
}

func securityAuditEvent(now time.Time, action, target, outcome, detail string) AuditEntry {
	return AuditEntry{
		Timestamp: now.UTC(),
		Actor:     "bcc",
		Action:    action,
		Target:    target,
		Outcome:   outcome,
		Details:   map[string]any{"detail": detail},
	}
}
