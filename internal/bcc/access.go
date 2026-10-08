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
	webui "github.com/zarkmakerburg/baft/web"
	"html"
	"io"
	"io/fs"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	accessSchemaVersion = 1
	pbkdf2Iterations    = 600_000 // legacy hashes only; new ones are Argon2id
	argonTime           = 3
	argonMemoryKiB      = 64 * 1024
	argonThreads        = 4
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

// hashPassword returns an Argon2id hash: 64 MiB, 3 passes, 4 lanes, random
// 16-byte salt, 32-byte output.
func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, 32)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// verifyPassword checks an Argon2id hash, or a PBKDF2 hash written by an
// earlier version until the access file is regenerated. Parameters read from
// the file are bounded so a damaged file cannot demand unbounded work.
func verifyPassword(encoded, password string) bool {
	switch {
	case strings.HasPrefix(encoded, "argon2id$"):
		return verifyArgon2id(encoded, password)
	case strings.HasPrefix(encoded, "pbkdf2-sha256$"):
		return verifyLegacyPBKDF2(encoded, password)
	}
	return false
}

func verifyArgon2id(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[1] != "v="+strconv.Itoa(argon2.Version) {
		return false
	}
	var m, t, p int
	for _, kv := range strings.Split(parts[2], ",") {
		k, v, ok := strings.Cut(kv, "=")
		n, err := strconv.Atoi(v)
		if !ok || err != nil {
			return false
		}
		switch k {
		case "m":
			m = n
		case "t":
			t = n
		case "p":
			p = n
		default:
			return false
		}
	}
	if m < 8*1024 || m > 256*1024 || t < 1 || t > 10 || p < 1 || p > 16 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[3])
	want, err2 := enc.DecodeString(parts[4])
	if err1 != nil || err2 != nil || len(salt) < 8 || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(t), uint32(m), uint8(p), 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func verifyLegacyPBKDF2(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 {
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
	if !strings.HasPrefix(a.PasswordHash, "argon2id$") && !strings.HasPrefix(a.PasswordHash, "pbkdf2-sha256$") {
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
		case tail == "v3":
			http.Redirect(w, r, base+"v3/", http.StatusSeeOther)
		case tail == "v3/":
			s.serveCommandCenterV3(w, r, a, base, "")
		case strings.HasPrefix(tail, "v3/"):
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			name := strings.TrimPrefix(tail, "v3/")
			if !fs.ValidPath(name) || strings.Contains(name, "..") || name == "assets.go" || name == "bcc-command-center-v3-preview.html" {
				http.NotFound(w, r)
				return
			}
			http.StripPrefix(base+"v3/", http.FileServer(http.FS(webui.FS))).ServeHTTP(w, r)
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
	if r.URL.Query().Get("ui") == "v3" {
		s.serveCommandCenterV3(w, r, a, base, loginError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	sess := s.access.lookup(r, s.now(), a.Generation)
	if sess == nil {
		if loginError != "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, renderLogin(base+"login", loginError))
			return
		}
		_, _ = io.WriteString(w, renderWelcome(base+"login"))
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method != http.MethodPost {
		if r.URL.Query().Get("ui") == "v3" {
			s.serveCommandCenterV3(w, r, a, base, "")
			return
		}
		_, _ = io.WriteString(w, renderLogin(base+"login", ""))
		return
	}
	ip := s.clientIP(r)
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	user, pass := r.PostForm.Get("username"), r.PostForm.Get("password")
	now := s.now()
	// Limits first, before any hashing: a blocked username or a global burst of
	// failures costs nothing to answer.
	if blocked, wait := s.loginLim.check(user, now); blocked {
		_ = s.auditLogin(r, "blocked")
		w.Header().Set("Retry-After", retryAfter(wait))
		http.Error(w, "too many failed sign-ins, try again later", http.StatusTooManyRequests)
		return
	}
	if !s.hashing.acquire(2 * time.Second) {
		w.Header().Set("Retry-After", "2")
		http.Error(w, "busy, try again", http.StatusServiceUnavailable)
		return
	}
	userOK := len(user) == len(a.Username) && subtle.ConstantTimeCompare([]byte(user), []byte(a.Username)) == 1
	passOK := verifyPassword(a.PasswordHash, pass) // always run: same cost for a wrong username
	s.hashing.release()
	if !userOK || !passOK {
		s.guard.AuthFailure(ip, now)
		s.loginLim.failed(user, now)
		_ = s.auditLogin(r, "failure")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Query().Get("ui") == "v3" {
			s.serveCommandCenterV3(w, r, a, base, "Wrong username or password.")
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, renderLogin(base+"login", "Wrong username or password."))
		return
	}
	s.loginLim.succeeded(user)
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
	target := base
	if r.URL.Query().Get("ui") == "v3" {
		target = base + "v3/"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) auditLogin(r *http.Request, outcome string) error {
	_, err := s.audit.Append(AuditEntry{
		Timestamp: s.now().UTC(), Actor: "web", RemoteIP: s.clientIP(r),
		Action: "access.login", Outcome: outcome, Details: withRequest(r, nil),
	})
	return err
}

const welcomeHTML = `<!doctype html>
<html lang="fa" dir="rtl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>BAFT Command Center</title><meta name="robots" content="noindex">
<style>
:root{color-scheme:dark;--bg:#030303;--gold:#f3bd45;--gold2:#ffe18a;--muted:#c7bdab;--line:#5b4319;--text:#fff}
*{box-sizing:border-box}body{margin:0;min-height:100vh;background:#030303;color:var(--text);font-family:Vazirmatn,Tahoma,Inter,system-ui,sans-serif;overflow-x:hidden}
.hero{position:relative;min-height:100vh;display:grid;grid-template-columns:minmax(420px,.9fr) minmax(520px,1.1fr);align-items:center;gap:36px;padding:88px 72px 56px;background:radial-gradient(circle at 74% 38%,rgba(243,189,69,.22),rgba(3,3,3,.28) 34%,#030303 72%)}
.hero:before{content:"";position:absolute;inset:0;background:linear-gradient(90deg,#030303 0%,rgba(3,3,3,.94) 30%,rgba(3,3,3,.5) 58%,rgba(3,3,3,.12) 100%);pointer-events:none}
.hero:after{content:"";position:absolute;inset:0;background-image:linear-gradient(rgba(255,255,255,.035) 1px,transparent 1px),linear-gradient(90deg,rgba(255,255,255,.03) 1px,transparent 1px);background-size:92px 92px;mask-image:linear-gradient(to bottom,transparent,#000 12%,#000 88%,transparent);pointer-events:none}
.copy,.stage{position:relative;z-index:1}.brand{display:flex;align-items:center;gap:14px;margin-bottom:36px;direction:ltr}.mark{letter-spacing:12px;font-weight:900;font-size:28px;color:#fff}.live{border:1px solid var(--line);border-radius:999px;padding:8px 13px;color:var(--gold2);font-size:12px;font-weight:800;background:rgba(11,11,12,.78)}
.logo-badge{width:min(260px,42vw);height:190px;margin-bottom:22px;display:grid;place-items:center;border-radius:26px;background:radial-gradient(circle at 50% 32%,rgba(255,225,138,.18),rgba(5,5,5,.62) 54%,rgba(5,5,5,.05));border:1px solid rgba(243,189,69,.22);box-shadow:0 32px 80px rgba(0,0,0,.48)}
.logo-badge .b{font-size:118px;font-weight:950;line-height:1;background:linear-gradient(135deg,#8f5d11,#ffe18a 35%,#f3bd45 62%,#101010 63%,#050505 82%);-webkit-background-clip:text;background-clip:text;color:transparent;text-shadow:0 0 42px rgba(243,189,69,.35)}
h1{font-size:clamp(42px,5.2vw,76px);line-height:1.12;margin:0 0 20px;font-weight:950;letter-spacing:0}.gold{color:var(--gold)}
p{font-size:20px;line-height:1.9;color:#eee6d5;margin:0 0 28px;max-width:720px}.actions{display:flex;gap:16px;align-items:center;flex-wrap:wrap}.btn{display:inline-flex;align-items:center;gap:12px;text-decoration:none;border-radius:16px;padding:17px 28px;font-weight:950;font-size:19px;transition:.2s transform,.2s box-shadow}.primary{background:linear-gradient(135deg,#f3bd45,#ffe18a);color:#090909;box-shadow:0 20px 48px rgba(243,189,69,.26)}.primary:hover{transform:translateY(-2px);box-shadow:0 26px 60px rgba(243,189,69,.34)}.ghost{border:1px solid rgba(255,255,255,.2);color:#fff;background:rgba(11,11,12,.5)}
.strip{display:flex;gap:22px;flex-wrap:wrap;margin-top:48px;color:#eee6d5;direction:ltr}.chip{display:flex;align-items:center;gap:9px;font-size:14px;font-weight:800}.dot{width:10px;height:10px;border-radius:50%;background:var(--gold);box-shadow:0 0 18px var(--gold)}
.stage{min-height:560px;display:grid;place-items:center;perspective:1000px}.panel{position:absolute;right:6%;bottom:9%;width:330px;padding:18px;border-radius:24px;background:rgba(7,7,7,.72);border:1px solid rgba(243,189,69,.24);box-shadow:0 28px 80px rgba(0,0,0,.5);backdrop-filter:blur(10px)}
.panel b{display:block;font-size:18px;margin-bottom:12px}.metrics{display:grid;grid-template-columns:repeat(3,1fr);gap:9px;direction:ltr}.metric{background:#050505;border:1px solid #2f2819;border-radius:14px;padding:10px}.metric span{display:block;color:#a99b83;font-size:10px;font-weight:800}.metric strong{display:block;color:#fff;margin-top:5px}.scene{width:min(720px,52vw);aspect-ratio:1;position:relative;transform-style:preserve-3d;animation:float 7s ease-in-out infinite}
canvas{width:100%;height:100%;display:block;filter:drop-shadow(0 0 35px rgba(243,189,69,.3))}.orbit{position:absolute;inset:8%;border:1px solid rgba(243,189,69,.28);border-radius:50%;transform:rotateX(68deg) rotateZ(-22deg);box-shadow:0 0 50px rgba(243,189,69,.14)}.orbit:nth-child(2){inset:16%;transform:rotateX(72deg) rotateZ(34deg)}.orbit:nth-child(3){inset:24%;transform:rotateX(58deg) rotateZ(-58deg)}
@keyframes float{0%,100%{transform:translateY(0) rotateX(0deg)}50%{transform:translateY(-16px) rotateX(3deg)}}@media(max-width:980px){.hero{grid-template-columns:1fr;padding:72px 24px}.stage{min-height:420px;order:-1}.scene{width:min(620px,92vw)}.panel{position:relative;right:auto;bottom:auto;width:100%;margin-top:-30px}.brand{margin-bottom:18px}.logo-badge{display:none}}@media(max-width:560px){.hero{padding:44px 18px}.mark{font-size:20px;letter-spacing:8px}p{font-size:16px}.actions{align-items:stretch}.btn{justify-content:center;width:100%}.strip{gap:12px}.chip{width:100%}}
</style></head>
<body><main class="hero"><section class="copy">
<div class="brand"><div class="mark">BAFT</div><div class="live">BCC READY</div></div>
<div class="logo-badge" aria-hidden="true"><div class="b">B</div></div>
<h1><span class="gold">بافت؛</span> زیرساخت تاب‌آور برای مسیرهای چندگانه</h1>
<p>مرکز فرمان BAFT آماده است. از این صفحه وارد BCC شوید، وضعیت نودها را ببینید، مسیرهای فعال را کنترل کنید و failover را از یک نقطه مدیریت کنید.</p>
<div class="actions"><a class="btn primary" href="{{LOGIN}}">شروع سریع ←</a><a class="btn ghost" href="#status">وضعیت نصب</a></div>
<div class="strip" id="status"><div class="chip"><i class="dot"></i>ECRL Runtime</div><div class="chip"><i class="dot"></i>N-Node Failover</div><div class="chip"><i class="dot"></i>RTL Safe</div><div class="chip"><i class="dot"></i>Logo Preserved</div></div>
</section><section class="stage" aria-label="Rotating BAFT command globe">
<div class="scene"><canvas id="globe" width="900" height="900"></canvas><i class="orbit"></i><i class="orbit"></i><i class="orbit"></i></div>
<div class="panel"><b>Live Command Surface</b><div class="metrics"><div class="metric"><span>ROUTES</span><strong>N</strong></div><div class="metric"><span>FAILOVER</span><strong style="color:#92f0bf">READY</strong></div><div class="metric"><span>ACCESS</span><strong>SECURE</strong></div></div></div>
</section></main>
<script>
(function(){
const c=document.getElementById("globe"),ctx=c.getContext("2d"),W=c.width,C=W/2,R=310;
const pts=[];for(let lat=-70;lat<=70;lat+=10){for(let lon=-180;lon<180;lon+=10){pts.push({lat:lat*Math.PI/180,lon:lon*Math.PI/180});}}
const hubs=[[-8,52],[35,51],[25,55],[41,29],[52,5],[28,77],[1,103]].map(function(x){return{lat:x[0]*Math.PI/180,lon:x[1]*Math.PI/180};});
function project(lat,lon,t){lon+=t;const x=Math.cos(lat)*Math.sin(lon),y=Math.sin(lat),z=Math.cos(lat)*Math.cos(lon);const s=1.08/(1.55-z*.42);return{x:C+x*R*s,y:C-y*R*s,z:z,s:s};}
function draw(t){ctx.clearRect(0,0,W,W);let g=ctx.createRadialGradient(C-120,C-140,40,C,C,R+90);g.addColorStop(0,"rgba(255,225,138,.58)");g.addColorStop(.28,"rgba(243,189,69,.22)");g.addColorStop(.62,"rgba(20,20,18,.92)");g.addColorStop(1,"rgba(0,0,0,.02)");ctx.fillStyle=g;ctx.beginPath();ctx.arc(C,C,R,0,Math.PI*2);ctx.fill();ctx.strokeStyle="rgba(255,225,138,.35)";ctx.lineWidth=2;ctx.stroke();ctx.save();ctx.beginPath();ctx.arc(C,C,R,0,Math.PI*2);ctx.clip();
for(let lat=-60;lat<=60;lat+=20){ctx.beginPath();let moved=false;for(let lon=-180;lon<=180;lon+=6){let p=project(lat*Math.PI/180,lon*Math.PI/180,t);if(p.z<-.8)continue;if(!moved){ctx.moveTo(p.x,p.y);moved=true}else ctx.lineTo(p.x,p.y)}ctx.strokeStyle="rgba(243,189,69,.18)";ctx.lineWidth=1;ctx.stroke();}
for(let lon=-180;lon<180;lon+=20){ctx.beginPath();let moved=false;for(let lat=-80;lat<=80;lat+=4){let p=project(lat*Math.PI/180,lon*Math.PI/180,t);if(p.z<-.8)continue;if(!moved){ctx.moveTo(p.x,p.y);moved=true}else ctx.lineTo(p.x,p.y)}ctx.strokeStyle="rgba(255,255,255,.07)";ctx.stroke();}
pts.forEach(function(pt){let p=project(pt.lat,pt.lon,t);if(p.z<-.15)return;ctx.fillStyle="rgba(255,225,138,"+(.05+p.z*.18)+")";ctx.fillRect(p.x,p.y,1.2*p.s,1.2*p.s);});
for(let i=0;i<hubs.length;i++){for(let j=i+1;j<hubs.length;j+=2){let a=project(hubs[i].lat,hubs[i].lon,t),b=project(hubs[j].lat,hubs[j].lon,t);if(a.z<-.05||b.z<-.05)continue;ctx.beginPath();ctx.moveTo(a.x,a.y);let mx=(a.x+b.x)/2,my=(a.y+b.y)/2-90*Math.max(a.s,b.s);ctx.quadraticCurveTo(mx,my,b.x,b.y);ctx.strokeStyle="rgba(243,189,69,.32)";ctx.lineWidth=2;ctx.stroke();}}
hubs.forEach(function(h,i){let p=project(h.lat,h.lon,t);if(p.z<-.08)return;ctx.beginPath();ctx.arc(p.x,p.y,7*p.s,0,Math.PI*2);ctx.fillStyle=i%2?"#ffe18a":"#f3bd45";ctx.shadowBlur=18;ctx.shadowColor="#f3bd45";ctx.fill();ctx.shadowBlur=0;});ctx.restore();requestAnimationFrame(function(){draw(t+.0038);});}
draw(0);
})();
</script></body></html>`

const loginHTML = `<!doctype html>
<html lang="fa" dir="rtl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>BAFT Command Center Login</title><meta name="robots" content="noindex">
<style>
:root{color-scheme:dark;--gold:#f3bd45;--gold2:#ffe18a;--muted:#c7bdab;--line:#5b4319}
*{box-sizing:border-box}body{font-family:Vazirmatn,Tahoma,Inter,system-ui,sans-serif;background:radial-gradient(circle at 70% 20%,rgba(243,189,69,.22),#030303 46%);color:#fff;display:grid;place-items:center;min-height:100vh;margin:0;padding:24px}
body:before{content:"";position:fixed;inset:0;background-image:linear-gradient(rgba(255,255,255,.035) 1px,transparent 1px),linear-gradient(90deg,rgba(255,255,255,.03) 1px,transparent 1px);background-size:84px 84px;pointer-events:none}
form{position:relative;background:linear-gradient(180deg,rgba(17,16,13,.9),rgba(5,5,5,.92));border:1px solid rgba(243,189,69,.28);box-shadow:0 34px 90px rgba(0,0,0,.56);padding:30px;border-radius:22px;display:grid;gap:14px;width:min(390px,92vw)}
.brand{direction:ltr;letter-spacing:9px;font-size:24px;font-weight:950;margin-bottom:2px}.sub{color:var(--muted);font-size:14px;line-height:1.7;margin-bottom:10px}
input,button{font:inherit;padding:13px 14px;border-radius:14px;border:1px solid #2f2819;background:#050505;color:#fff}input:focus{outline:2px solid rgba(243,189,69,.32);border-color:var(--gold)}
button{background:linear-gradient(135deg,var(--gold),var(--gold2));color:#090909;border:0;font-weight:950;cursor:pointer}.err{min-height:18px;color:#ffabb6;font-size:13px}.safe{color:#92f0bf;font-size:12px;direction:ltr;text-align:left}
</style></head><body>
<form method="post" action="{{ACTION}}"><div class="brand">BAFT</div><b>ورود به Command Center</b><div class="sub">برای ادامه، اطلاعات دسترسی ساخته‌شده بعد از نصب را وارد کنید.</div>
<input name="username" autocomplete="username" placeholder="username" required>
<input name="password" type="password" autocomplete="current-password" placeholder="password" required>
<button>ورود به BCC</button><div class="err">{{ERROR}}</div><div class="safe">Secret-path protected · Session guarded · No index</div></form></body></html>`

// The page templates contain literal % (CSS), so they are filled with named
// placeholders, never with fmt verbs.
func renderLogin(action, errText string) string {
	return strings.NewReplacer("{{ACTION}}", html.EscapeString(action), "{{ERROR}}", html.EscapeString(errText)).Replace(loginHTML)
}

func renderWelcome(loginURL string) string {
	return strings.NewReplacer("{{LOGIN}}", html.EscapeString(loginURL)).Replace(welcomeHTML)
}

// serveCommandCenterV3 reuses the existing access gate and native POST login.
// The UI is an explicit /<secret>/v3/ staging opt-in; legacy operations stay available.
func (s *Server) serveCommandCenterV3(w http.ResponseWriter, r *http.Request, a AccessFile, base, loginError string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mode := "welcome"
	if s.access.lookup(r, s.now(), a.Generation) != nil {
		mode = "dashboard"
	} else if strings.HasSuffix(r.URL.Path, "login") || loginError != "" {
		mode = "signin"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if loginError != "" {
		w.WriteHeader(http.StatusUnauthorized)
	}
	config := webui.Config{Mode: mode, Base: base, LoginAction: base + "login?ui=v3", Error: loginError}
	if sess := s.access.lookup(r, s.now(), a.Generation); sess != nil {
		config.CSRF = sess.csrf
		config.CSRFHeader = csrfHeader
		begin := strings.Index(dashboardHTML, "<body>")
		end := strings.Index(dashboardHTML, "<script>")
		close := strings.Index(dashboardHTML[end:], "</script>")
		if begin >= 0 && end > begin && close >= 0 {
			config.OperationsHTML = dashboardHTML[begin:end]
			config.OperationsJS = dashboardHTML[end+8 : end+close]
		}
	}
	page := webui.Page(config)
	// A native POST failure is served at /<secret>/login: point relative assets back to /v3/.
	if strings.HasSuffix(r.URL.Path, "login") {
		page = strings.ReplaceAll(page, "./bcc-", base+"v3/bcc-")
		page = strings.ReplaceAll(page, "./assets/", base+"v3/assets/")
	}
	_, _ = io.WriteString(w, page)
}
