package bcc

// Web access to the BCC dashboard (Launch-1 P1-C).
//
// Three independent credentials guard the dashboard: a random secret URL
// path, a random username and a strong random password. The password is
// stored only as a PBKDF2 hash. All three are created and rotated together,
// only from the local console (`baft-bcc access init|regenerate`); there is
// no web password recovery. Rotation bumps the generation, which ends every
// session at once. The secret path is an extra layer, not authentication:
// every request under it still needs a session.

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	accessSchemaVersion = 1
	pbkdf2Iterations    = 600_000
	sessionCookie       = "baft_bcc_session"
	sessionIdleTimeout  = 30 * time.Minute
	sessionMaxLifetime  = 12 * time.Hour
	maxSessions         = 256
	csrfHeader          = "X-BAFT-CSRF"
)

// AccessFile is the on-disk access configuration. It never holds the
// password itself.
type AccessFile struct {
	SchemaVersion int       `json:"schema_version"`
	Generation    uint64    `json:"generation"`
	SecretPath    string    `json:"secret_path"`
	Username      string    `json:"username"`
	PasswordHash  string    `json:"password_hash"`
	RotatedAt     time.Time `json:"rotated_at"`
}

// AccessCredentials is what a rotation hands to the operator, once.
type AccessCredentials struct {
	Generation uint64
	SecretPath string
	Username   string
	Password   string
}

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func randomString(alphabet string, n int) (string, error) {
	max := big.NewInt(int64(len(alphabet)))
	b := make([]byte, n)
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b), nil
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, 32)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 100_000 || iter > 10_000_000 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(want) != 32 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, 32)
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

func (a AccessFile) validate() error {
	if a.SchemaVersion != accessSchemaVersion {
		return fmt.Errorf("unsupported access schema %d", a.SchemaVersion)
	}
	if a.Generation == 0 || len(a.SecretPath) < 24 || strings.ContainsAny(a.SecretPath, "/?#%") || a.Username == "" {
		return errors.New("access file is incomplete")
	}
	if !strings.HasPrefix(a.PasswordHash, "pbkdf2-sha256$") {
		return errors.New("access file has no password hash")
	}
	return nil
}

// ReadAccessFile loads and checks an access file. It must not be readable by
// group or other.
func ReadAccessFile(path string) (AccessFile, error) {
	var a AccessFile
	st, err := os.Stat(path)
	if err != nil {
		return a, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return a, fmt.Errorf("%s must not be readable or writable by group/other", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return a, err
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return a, fmt.Errorf("%s: %w", path, err)
	}
	return a, a.validate()
}

// RotateAccess creates new secret path, username and password together and
// writes them (password as a hash only) to path atomically. With no existing
// file it starts at generation 1; requireNew refuses to replace one.
func RotateAccess(path string, requireNew bool, now time.Time) (AccessCredentials, error) {
	var creds AccessCredentials
	prev, err := ReadAccessFile(path)
	switch {
	case err == nil && requireNew:
		return creds, fmt.Errorf("%s already exists; use regenerate", path)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return creds, err
	}
	secret, err := randomString(base62, 32)
	if err != nil {
		return creds, err
	}
	user, err := randomString("abcdefghijkmnpqrstuvwxyz23456789", 10)
	if err != nil {
		return creds, err
	}
	password, err := randomString(base62, 24)
	if err != nil {
		return creds, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return creds, err
	}
	a := AccessFile{
		SchemaVersion: accessSchemaVersion, Generation: prev.Generation + 1,
		SecretPath: secret, Username: "op-" + user, PasswordHash: hash, RotatedAt: now.UTC().Truncate(time.Second),
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return creds, err
	}
	if err := writeFileAtomic(path, append(b, '\n'), 0o600); err != nil {
		return creds, err
	}
	return AccessCredentials{Generation: a.Generation, SecretPath: a.SecretPath, Username: a.Username, Password: password}, nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".access-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ---- server side ----

type session struct {
	generation uint64
	csrf       string
	created    time.Time
	lastSeen   time.Time
}

type accessGate struct {
	path   string
	secure bool

	mu       sync.Mutex
	file     AccessFile
	modTime  time.Time
	checked  time.Time
	sessions map[[32]byte]*session
}

type viaSecretKey struct{}

// ConfigureAccess turns on secret-path web access from an access file. The
// dashboard then lives only under /<secret>/ behind a login, the root path
// answers 404, and the file is re-read when it changes, so a console
// regeneration takes effect (and ends all sessions) without a restart.
// secureCookies marks the session cookie Secure (set it when serving HTTPS).
func (s *Server) ConfigureAccess(path string, secureCookies bool) error {
	a, err := ReadAccessFile(path)
	if err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	s.access = &accessGate{path: path, secure: secureCookies, file: a, modTime: st.ModTime(), sessions: map[[32]byte]*session{}}
	return nil
}

// current re-reads the access file at most once a second when it changed. A
// new generation drops every session. A broken file keeps the last good one.
func (g *accessGate) current(now time.Time) AccessFile {
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Sub(g.checked) < time.Second {
		return g.file
	}
	g.checked = now
	st, err := os.Stat(g.path)
	if err != nil || st.ModTime().Equal(g.modTime) {
		return g.file
	}
	a, err := ReadAccessFile(g.path)
	if err != nil {
		return g.file
	}
	if a.Generation != g.file.Generation {
		g.sessions = map[[32]byte]*session{}
	}
	g.file, g.modTime = a, st.ModTime()
	return g.file
}

func (g *accessGate) newSession(now time.Time, generation uint64) (token string, csrf string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	if csrf, err = randomString(base62, 32); err != nil {
		return "", "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.sessions) >= maxSessions {
		var oldestKey [32]byte
		var oldest time.Time
		for k, v := range g.sessions {
			if oldest.IsZero() || v.lastSeen.Before(oldest) {
				oldestKey, oldest = k, v.lastSeen
			}
		}
		delete(g.sessions, oldestKey)
	}
	g.sessions[sha256.Sum256([]byte(token))] = &session{generation: generation, csrf: csrf, created: now, lastSeen: now}
	return token, csrf, nil
}

// lookup returns the live session for the request's cookie, if any.
func (g *accessGate) lookup(r *http.Request, now time.Time, generation uint64) *session {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	key := sha256.Sum256([]byte(c.Value))
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.sessions[key]
	if s == nil {
		return nil
	}
	if s.generation != generation || now.Sub(s.lastSeen) > sessionIdleTimeout || now.Sub(s.created) > sessionMaxLifetime {
		delete(g.sessions, key)
		return nil
	}
	s.lastSeen = now
	return s
}

func (g *accessGate) drop(r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		g.mu.Lock()
		delete(g.sessions, sha256.Sum256([]byte(c.Value)))
		g.mu.Unlock()
	}
}

func (g *accessGate) cookie(secret, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/" + secret + "/", MaxAge: maxAge,
		HttpOnly: true, Secure: g.secure, SameSite: http.SameSiteStrictMode,
	}
}

// sessionAdmin reports whether r carries a valid dashboard session. It only
// counts on requests routed through the secret path; state-changing requests
// must also echo the session's CSRF token.
func (s *Server) sessionAdmin(r *http.Request) bool {
	if s.access == nil || r.Context().Value(viaSecretKey{}) == nil {
		return false
	}
	now := s.now()
	a := s.access.current(now)
	sess := s.access.lookup(r, now, a.Generation)
	if sess == nil {
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return true
	}
	got := r.Header.Get(csrfHeader)
	return len(got) == len(sess.csrf) && subtle.ConstantTimeCompare([]byte(got), []byte(sess.csrf)) == 1
}

// accessHandler wraps the API mux: /<secret>/ serves the login and the
// dashboard, /<secret>/api/... reaches the admin API with the session, the
// agent and bearer-token API stay at /api/..., and everything else is 404.
func (s *Server) accessHandler(api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			api.ServeHTTP(w, r)
			return
		}
		now := s.now()
		a := s.access.current(now)
		rest, ok := strings.CutPrefix(r.URL.Path, "/")
		first, tail, _ := strings.Cut(rest, "/")
		if !ok || len(first) != len(a.SecretPath) || subtle.ConstantTimeCompare([]byte(first), []byte(a.SecretPath)) != 1 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		base := "/" + a.SecretPath + "/"
		switch {
		case r.URL.Path == "/"+a.SecretPath:
			http.Redirect(w, r, base, http.StatusSeeOther)
		case tail == "":
			s.serveDashboardOrLogin(w, r, a, base, "")
		case tail == "login":
			s.login(w, r, a, base)
		case tail == "logout":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			s.access.drop(r)
			http.SetCookie(w, s.access.cookie(a.SecretPath, "", -1))
			http.Redirect(w, r, base, http.StatusSeeOther)
		case strings.HasPrefix(tail, "api/"):
			r2 := r.Clone(context.WithValue(r.Context(), viaSecretKey{}, true))
			r2.URL.Path = "/" + tail
			r2.URL.RawPath = ""
			api.ServeHTTP(w, r2)
		default:
			http.NotFound(w, r)
		}
	})
}

func (s *Server) serveDashboardOrLogin(w http.ResponseWriter, r *http.Request, a AccessFile, base, loginError string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	sess := s.access.lookup(r, s.now(), a.Generation)
	if sess == nil {
		if loginError != "" {
			w.WriteHeader(http.StatusUnauthorized)
		}
		fmt.Fprintf(w, loginHTML, html.EscapeString(base+"login"), loginError)
		return
	}
	// The dashboard script calls /api/...; route those calls through the
	// secret path with the session cookie and the CSRF token instead of a
	// bearer token.
	shim := fmt.Sprintf(`<script>(()=>{const B=%q,C=%q;localStorage.setItem('bccToken','session');const f=window.fetch.bind(window);window.fetch=(u,o)=>{o=Object.assign({},o||{});if(typeof u==='string'&&u.startsWith('/api/')){u=B+u.slice(1);const h=Object.assign({},o.headers||{});delete h.Authorization;h[%q]=C;o.headers=h;o.credentials='same-origin'}return f(u,o)};window.addEventListener('DOMContentLoaded',()=>{const fm=document.createElement('form');fm.method='post';fm.action=B+'logout';fm.style='position:fixed;top:8px;right:8px';fm.innerHTML='<button>Log out</button>';document.body.appendChild(fm)})})();</script>`,
		base, sess.csrf, csrfHeader)
	page := strings.Replace(dashboardHTML, "<head>", "<head>"+shim, 1)
	_, _ = w.Write([]byte(page))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, a AccessFile, base string) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, base, http.StatusSeeOther)
		return
	}
	ip := s.clientIP(r)
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	user, pass := r.PostForm.Get("username"), r.PostForm.Get("password")
	userOK := len(user) == len(a.Username) && subtle.ConstantTimeCompare([]byte(user), []byte(a.Username)) == 1
	passOK := verifyPassword(a.PasswordHash, pass) // always run: same cost for a wrong username
	if !userOK || !passOK {
		s.guard.AuthFailure(ip, s.now())
		_ = s.auditLogin(r, "failure")
		s.serveDashboardOrLogin(w, r, a, base, "Wrong username or password.")
		return
	}
	s.guard.AuthSuccess(ip)
	token, _, err := s.access.newSession(s.now(), a.Generation)
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	if err := s.auditLogin(r, "success"); err != nil {
		http.Error(w, "audit log failure", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, s.access.cookie(a.SecretPath, token, int(sessionMaxLifetime/time.Second)))
	http.Redirect(w, r, base, http.StatusSeeOther)
}

func (s *Server) auditLogin(r *http.Request, outcome string) error {
	_, err := s.audit.Append(AuditEntry{
		Timestamp: s.now().UTC(), Actor: "web", RemoteIP: s.clientIP(r),
		Action: "access.login", Outcome: outcome,
	})
	return err
}

const loginHTML = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>BAFT Command Center</title><meta name="robots" content="noindex">
<style>body{font-family:system-ui,sans-serif;background:#0f172a;color:#e2e8f0;display:grid;place-items:center;min-height:100vh;margin:0}
form{background:#1e293b;padding:24px;border-radius:12px;display:grid;gap:12px;width:min(320px,90vw)}
input,button{padding:10px;border-radius:8px;border:1px solid #334155;background:#0f172a;color:#e2e8f0}button{background:#2563eb;border:0}
.err{color:#fca5a5}</style></head><body>
<form method="post" action="%s"><b>BAFT Command Center</b>
<input name="username" autocomplete="username" placeholder="username" required>
<input name="password" type="password" autocomplete="current-password" placeholder="password" required>
<button>Sign in</button><div class="err">%s</div></form></body></html>`
