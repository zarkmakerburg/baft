package bcc

// Web hardening for the BCC listener: security headers on every response, a
// per-request ID that reaches the audit log, and login limiters beyond the
// per-IP guard (per submitted username, and a global failure ceiling).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type requestIDKey struct{}

// RequestID returns the ID assigned to r by the hardening middleware.
func RequestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey{}).(string)
	return id
}

// contentSecurityPolicy blocks every origin but our own, framing, form posts
// elsewhere and <base> injection. The dashboard still uses inline scripts and
// handlers, so script-src keeps 'unsafe-inline'; moving them to nonces is
// tracked in docs/en/24-p1c-bcc-access.md.
const contentSecurityPolicy = "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// harden is the outermost handler: even a rate-limited or 404 response gets
// the headers and an ID. The client's own X-Request-ID is never trusted.
func (s *Server) harden(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [8]byte
		_, _ = rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		h := w.Header()
		h.Set("X-Request-ID", id)
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), interest-cohort=()")
		h.Set("Cache-Control", "no-store")
		if r.TLS != nil || (s.access != nil && s.access.secure) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// withRequest returns details plus the request ID, without mutating the input.
func withRequest(r *http.Request, details map[string]any) map[string]any {
	out := make(map[string]any, len(details)+1)
	for k, v := range details {
		out[k] = v
	}
	if id := RequestID(r); id != "" {
		out["request_id"] = id
	}
	return out
}

// failureWindow counts failures per key in a sliding window with a bounded
// number of keys, so an attacker inventing keys cannot grow it without limit.
type failureWindow struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	maxKeys int
	hits    map[string][]time.Time
}

func newFailureWindow(limit int, window time.Duration, maxKeys int) *failureWindow {
	return &failureWindow{limit: limit, window: window, maxKeys: maxKeys, hits: map[string][]time.Time{}}
}

func (f *failureWindow) prune(key string, now time.Time) []time.Time {
	cut := now.Add(-f.window)
	hs := f.hits[key]
	i := 0
	for i < len(hs) && !hs[i].After(cut) {
		i++
	}
	hs = hs[i:]
	if len(hs) == 0 {
		delete(f.hits, key)
		return nil
	}
	f.hits[key] = hs
	return hs
}

// blocked reports whether key reached the limit and how long until it frees.
func (f *failureWindow) blocked(key string, now time.Time) (bool, time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hs := f.prune(key, now)
	if len(hs) < f.limit {
		return false, 0
	}
	return true, hs[len(hs)-f.limit].Add(f.window).Sub(now)
}

func (f *failureWindow) fail(key string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.hits[key]; !ok && len(f.hits) >= f.maxKeys {
		for k := range f.hits {
			if f.prune(k, now) == nil && len(f.hits) < f.maxKeys {
				break
			}
		}
		if len(f.hits) >= f.maxKeys { // still full of live keys: drop the stalest
			var oldest string
			var at time.Time
			for k, hs := range f.hits {
				if oldest == "" || hs[len(hs)-1].Before(at) {
					oldest, at = k, hs[len(hs)-1]
				}
			}
			delete(f.hits, oldest)
		}
	}
	f.hits[key] = append(f.hits[key], now)
}

func (f *failureWindow) reset(key string) {
	f.mu.Lock()
	delete(f.hits, key)
	f.mu.Unlock()
}

func (f *failureWindow) size() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.hits)
}

// loginLimiter combines a per-username and a global failure ceiling. Only
// failures count, so a legitimate sign-in is never slowed by traffic alone.
type loginLimiter struct {
	users  *failureWindow
	global *failureWindow
}

const globalLoginKey = "all"

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		users:  newFailureWindow(10, 15*time.Minute, 4096),
		global: newFailureWindow(30, time.Minute, 1),
	}
}

// userKey stores a digest, never the submitted text.
func userKey(username string) string {
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) > 256 {
		username = username[:256]
	}
	sum := sha256.Sum256([]byte(username))
	return hex.EncodeToString(sum[:8])
}

func (l *loginLimiter) check(username string, now time.Time) (bool, time.Duration) {
	if b, d := l.global.blocked(globalLoginKey, now); b {
		return true, d
	}
	return l.users.blocked(userKey(username), now)
}

func (l *loginLimiter) failed(username string, now time.Time) {
	l.users.fail(userKey(username), now)
	l.global.fail(globalLoginKey, now)
}

func (l *loginLimiter) succeeded(username string) { l.users.reset(userKey(username)) }

func retryAfter(d time.Duration) string {
	secs := int((d + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return strconv.Itoa(secs)
}

// hashSlots bounds concurrent password hashing: each Argon2id run takes tens
// of MiB, so unbounded parallel logins would be a memory attack.
type hashSlots chan struct{}

func newHashSlots(n int) hashSlots { return make(hashSlots, n) }

func (h hashSlots) acquire(timeout time.Duration) bool {
	select {
	case h <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case h <- struct{}{}:
		return true
	case <-t.C:
		return false
	}
}

func (h hashSlots) release() { <-h }
