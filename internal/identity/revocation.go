package identity

import (
	"strings"
	"sync"
)

type RevocationSet struct {
	mu           sync.Mutex
	identities   map[string]struct{}
	serials      map[string]struct{}
	fingerprints map[string]struct{}
	watchers     map[uint64]revocationWatcher
	nextWatcher  uint64
}

type revocationWatcher struct {
	identity    string
	serial      string
	fingerprint string
	ch          chan struct{}
}

func NewRevocationSet() *RevocationSet {
	return &RevocationSet{
		identities:   map[string]struct{}{},
		serials:      map[string]struct{}{},
		fingerprints: map[string]struct{}{},
		watchers:     map[uint64]revocationWatcher{},
	}
}

func (r *RevocationSet) IsRevoked(identity, serial, fingerprint string) bool {
	if r == nil {
		return false
	}
	serial = normalizeHex(serial)
	fingerprint = normalizeHex(fingerprint)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.revokedLocked(identity, serial, fingerprint)
}

func (r *RevocationSet) Watch(identity, serial, fingerprint string) (<-chan struct{}, func()) {
	ch := make(chan struct{})
	if r == nil {
		return ch, func() {}
	}
	serial = normalizeHex(serial)
	fingerprint = normalizeHex(fingerprint)
	r.mu.Lock()
	if r.revokedLocked(identity, serial, fingerprint) {
		close(ch)
		r.mu.Unlock()
		return ch, func() {}
	}
	r.nextWatcher++
	id := r.nextWatcher
	r.watchers[id] = revocationWatcher{identity: identity, serial: serial, fingerprint: fingerprint, ch: ch}
	r.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.watchers, id)
			r.mu.Unlock()
		})
	}
}

func (r *RevocationSet) RevokeIdentity(identity string) {
	if r == nil || identity == "" {
		return
	}
	r.revoke(func(w revocationWatcher) bool { return w.identity == identity }, func() {
		r.identities[identity] = struct{}{}
	})
}

func (r *RevocationSet) RevokeSerial(serial string) {
	if r == nil {
		return
	}
	serial = normalizeHex(serial)
	if serial == "" {
		return
	}
	r.revoke(func(w revocationWatcher) bool { return w.serial == serial }, func() {
		r.serials[serial] = struct{}{}
	})
}

func (r *RevocationSet) RevokeFingerprint(fingerprint string) {
	if r == nil {
		return
	}
	fingerprint = normalizeHex(fingerprint)
	if fingerprint == "" {
		return
	}
	r.revoke(func(w revocationWatcher) bool { return w.fingerprint == fingerprint }, func() {
		r.fingerprints[fingerprint] = struct{}{}
	})
}

func (r *RevocationSet) revokedLocked(identity, serial, fingerprint string) bool {
	if _, ok := r.identities[identity]; ok {
		return true
	}
	if _, ok := r.serials[serial]; ok && serial != "" {
		return true
	}
	if _, ok := r.fingerprints[fingerprint]; ok && fingerprint != "" {
		return true
	}
	return false
}

func (r *RevocationSet) revoke(match func(revocationWatcher) bool, record func()) {
	r.mu.Lock()
	record()
	for id, w := range r.watchers {
		if match(w) {
			close(w.ch)
			delete(r.watchers, id)
		}
	}
	r.mu.Unlock()
}

func normalizeHex(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, ":", "")
	return s
}
