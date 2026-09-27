package recovery

import (
	"errors"
	"math"
	"sync"
)

var (
	ErrStaleEpoch   = errors.New("STALE_EPOCH")
	ErrLeaseConflict = errors.New("epoch lease conflict")
	ErrNotPrepared   = errors.New("epoch lease not prepared")
	ErrEpochExhausted = errors.New("epoch exhausted")
)

// EpochLease gives exactly one prepared replacement carrier the right to
// commit the next epoch. The current carrier remains authoritative until
// Commit succeeds; after Commit, the previous epoch is fenced immediately.
type EpochLease struct {
	mu        sync.Mutex
	current   uint64
	pending   uint64
	candidate string
}

func NewEpochLease(initial uint64) (*EpochLease, error) {
	if initial == 0 {
		return nil, errors.New("initial epoch must be positive")
	}
	return &EpochLease{current: initial}, nil
}

func (l *EpochLease) Current() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.current
}

func (l *EpochLease) Prepare(next uint64, candidateID string) error {
	if candidateID == "" {
		return errors.New("candidate id is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.current == math.MaxUint64 {
		return ErrEpochExhausted
	}
	if next != l.current+1 {
		return ErrStaleEpoch
	}
	if l.pending != 0 {
		if l.pending == next && l.candidate == candidateID {
			return nil
		}
		return ErrLeaseConflict
	}
	l.pending = next
	l.candidate = candidateID
	return nil
}

func (l *EpochLease) Commit(next uint64, candidateID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending != next || l.candidate != candidateID || candidateID == "" {
		return ErrNotPrepared
	}
	l.current = next
	l.pending = 0
	l.candidate = ""
	return nil
}

func (l *EpochLease) Abort(next uint64, candidateID string) {
	l.mu.Lock()
	if l.pending == next && l.candidate == candidateID {
		l.pending = 0
		l.candidate = ""
	}
	l.mu.Unlock()
}

func (l *EpochLease) Authorize(epoch uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return epoch == l.current
}
