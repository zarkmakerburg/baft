package bcc

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type accessRig struct {
	t     *testing.T
	app   *Server
	h     http.Handler
	file  string
	creds AccessCredentials
	clock time.Time
}

func newAccessRig(t *testing.T) *accessRig {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewServer(store, "admin-secret")
	if err != nil {
		t.Fatal(err)
	}
	r := &accessRig{t: t, app: app, file: filepath.Join(dir, "state.json.access.json"), clock: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)}
	app.now = func() time.Time { return r.clock }
	if r.creds, err = RotateAccess(r.file, true, r.clock); err != nil {
		t.Fatal(err)
	}
	if err := app.ConfigureAccess(r.file, true); err != nil {
		t.Fatal(err)
	}
	r.h = app.Handler()
	return r
}

func (r *accessRig) do(req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	r.h.ServeHTTP(rr, req)
	return rr
}

func (r *accessRig) base() string { return "/" + r.creds.SecretPath + "/" }

func (r *accessRig) login(user, pass string) *httptest.ResponseRecorder {
	form := url.Values{"username": {user}, "password": {pass}}
	req := httptest.NewRequest(http.MethodPost, r.base()+"login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r.do(req)
}

func (r *accessRig) session() (*http.Cookie, string) {
	r.t.Helper()
	rr := r.login(r.creds.Username, r.creds.Password)
	if rr.Code != http.StatusSeeOther {
		r.t.Fatalf("login status %d: %s", rr.Code, rr.Body.String())
	}
	var c *http.Cookie
	for _, ck := range rr.Result().Cookies() {
		if ck.Name == sessionCookie {
			c = ck
		}
	}
	if c == nil {
		r.t.Fatal("login set no session cookie")
	}
	req := httptest.NewRequest(http.MethodGet, r.base(), nil)
	req.AddCookie(c)
	page := r.do(req).Body.String()
	m := regexp.MustCompile(`C="([0-9A-Za-z]{32})"`).FindStringSubmatch(page)
	if m == nil {
		r.t.Fatalf("dashboard has no CSRF token:\n%.300s", page)
	}
	return c, m[1]
}

func TestRotateAccessStoresOnlyAHash(t *testing.T) {
	file := filepath.Join(t.TempDir(), "access.json")
	creds, err := RotateAccess(file, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(file)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("access file mode %o", st.Mode().Perm())
	}
	b, _ := os.ReadFile(file)
	if strings.Contains(string(b), creds.Password) {
		t.Fatal("access file contains the password")
	}
	if len(creds.SecretPath) != 32 || len(creds.Password) != 24 || !strings.HasPrefix(creds.Username, "op-") {
		t.Fatalf("weak credentials %+v", creds)
	}
	a, err := ReadAccessFile(file)
	if err != nil || !verifyPassword(a.PasswordHash, creds.Password) || verifyPassword(a.PasswordHash, creds.Password+"x") {
		t.Fatalf("password hash does not verify: %v", err)
	}
	if _, err := RotateAccess(file, true, time.Now()); err == nil {
		t.Fatal("init replaced an existing access file")
	}
	again, err := RotateAccess(file, false, time.Now())
	if err != nil || again.Generation != 2 || again.SecretPath == creds.SecretPath || again.Username == creds.Username || again.Password == creds.Password {
		t.Fatalf("regenerate did not rotate all three: %+v %v", again, err)
	}
	os.Chmod(file, 0o644)
	if _, err := ReadAccessFile(file); err == nil {
		t.Fatal("group-readable access file accepted")
	}
}

func TestOnlyTheSecretPathServesTheDashboard(t *testing.T) {
	r := newAccessRig(t)
	for _, p := range []string{"/", "/index.html", "/" + strings.Repeat("A", 32) + "/", "/" + r.creds.SecretPath[:31] + "/"} {
		if rr := r.do(httptest.NewRequest(http.MethodGet, p, nil)); rr.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rr.Code)
		}
	}
	// Without a session the secret path shows a static welcome page that links to
	// the sign-in form; no dashboard content and no data.
	rr := r.do(httptest.NewRequest(http.MethodGet, r.base(), nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `href="`+r.base()+`login"`) || strings.Contains(rr.Body.String(), "Recent Jobs") || strings.Contains(rr.Body.String(), "/api/") {
		t.Fatalf("secret path without a session should show only the welcome page: %d", rr.Code)
	}
	if login := r.do(httptest.NewRequest(http.MethodGet, r.base()+"login", nil)); login.Code != 200 || !strings.Contains(login.Body.String(), `name="password"`) || strings.Contains(login.Body.String(), "Recent Jobs") {
		t.Fatalf("sign-in page: %d", login.Code)
	}
	if rr.Header().Get("Cache-Control") != "no-store" || rr.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("missing security headers")
	}
	// The bearer-token API keeps working for automation and agents.
	if rr := r.do(authReq(http.MethodGet, "/api/nodes", "admin-secret", nil)); rr.Code != 200 {
		t.Fatalf("bearer API = %d", rr.Code)
	}
}

func TestLoginSessionAndCSRF(t *testing.T) {
	r := newAccessRig(t)
	if rr := r.login(r.creds.Username, "wrong"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", rr.Code)
	}
	if rr := r.login("op-nobody", r.creds.Password); rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong username = %d", rr.Code)
	}
	c, csrf := r.session()
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != r.base() {
		t.Fatalf("weak session cookie %+v", c)
	}

	get := httptest.NewRequest(http.MethodGet, r.base()+"api/nodes", nil)
	get.AddCookie(c)
	if rr := r.do(get); rr.Code != 200 {
		t.Fatalf("session GET = %d", rr.Code)
	}
	post := func(token string) int {
		req := authReq(http.MethodPost, r.base()+"api/nodes", "", map[string]string{"ID": "ex-1", "Alias": "ex", "Address": "127.0.0.1:1", "Role": "foreign"})
		req.AddCookie(c)
		if token != "" {
			req.Header.Set(csrfHeader, token)
		}
		return r.do(req).Code
	}
	if code := post(""); code != http.StatusUnauthorized {
		t.Fatalf("POST without CSRF = %d", code)
	}
	if code := post(csrf + "x"); code != http.StatusUnauthorized {
		t.Fatalf("POST with a wrong CSRF = %d", code)
	}
	if code := post(csrf); code != http.StatusCreated {
		t.Fatalf("POST with CSRF = %d", code)
	}
	// The session only counts through the secret path.
	root := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
	root.AddCookie(c)
	if rr := r.do(root); rr.Code != http.StatusUnauthorized {
		t.Fatalf("session accepted on the root API: %d", rr.Code)
	}

	entries, err := r.app.audit.List(0)
	if err != nil {
		t.Fatal(err)
	}
	var fails, oks int
	for _, e := range entries {
		if e.Action == "access.login" {
			if e.Outcome == "failure" {
				fails++
			} else {
				oks++
			}
		}
	}
	if fails != 2 || oks != 1 {
		t.Fatalf("audit has %d failed and %d good logins", fails, oks)
	}
}

func TestRegenerateEndsSessionsAndOldCredentials(t *testing.T) {
	r := newAccessRig(t)
	c, _ := r.session()
	old := r.creds
	next, err := RotateAccess(r.file, false, r.clock)
	if err != nil {
		t.Fatal(err)
	}
	// Make sure the new file's mtime differs, then let the reload interval pass.
	future := time.Now().Add(time.Minute)
	os.Chtimes(r.file, future, future)
	r.clock = r.clock.Add(2 * time.Second)

	req := httptest.NewRequest(http.MethodGet, "/"+old.SecretPath+"/api/nodes", nil)
	req.AddCookie(c)
	if rr := r.do(req); rr.Code != http.StatusNotFound {
		t.Fatalf("old secret path = %d, want 404", rr.Code)
	}
	r.creds = next
	req = httptest.NewRequest(http.MethodGet, r.base()+"api/nodes", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.Value})
	if rr := r.do(req); rr.Code != http.StatusUnauthorized {
		t.Fatalf("old session survived regeneration: %d", rr.Code)
	}
	if rr := r.login(old.Username, old.Password); rr.Code != http.StatusUnauthorized {
		t.Fatalf("old credentials work on the new path: %d", rr.Code)
	}
	r.session()
}

func TestLogoutAndIdleTimeout(t *testing.T) {
	r := newAccessRig(t)
	c, _ := r.session()
	out := httptest.NewRequest(http.MethodPost, r.base()+"logout", nil)
	out.AddCookie(c)
	if rr := r.do(out); rr.Code != http.StatusSeeOther {
		t.Fatalf("logout = %d", rr.Code)
	}
	req := httptest.NewRequest(http.MethodGet, r.base()+"api/nodes", nil)
	req.AddCookie(c)
	if rr := r.do(req); rr.Code != http.StatusUnauthorized {
		t.Fatalf("session survived logout: %d", rr.Code)
	}

	c, _ = r.session()
	r.clock = r.clock.Add(sessionIdleTimeout + time.Second)
	req = httptest.NewRequest(http.MethodGet, r.base()+"api/nodes", nil)
	req.AddCookie(c)
	if rr := r.do(req); rr.Code != http.StatusUnauthorized {
		t.Fatalf("idle session still valid: %d", rr.Code)
	}
}

func TestRepeatedLoginFailuresAreBlocked(t *testing.T) {
	r := newAccessRig(t)
	for i := 0; i < 5; i++ {
		r.login(r.creds.Username, "wrong")
	}
	if rr := r.login(r.creds.Username, r.creds.Password); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("login after 5 failures = %d, want 429", rr.Code)
	}
}

func TestLoginAndWelcomePagesRenderWithoutFormatArtifacts(t *testing.T) {
	// The templates are full of literal % (CSS); a fmt verb would corrupt them.
	for name, page := range map[string]string{
		"welcome":      renderWelcome("/secret/login"),
		"login":        renderLogin("/secret/login", `bad <b>"input"</b>`),
		"login-no-err": renderLogin("/secret/login", ""),
	} {
		if strings.Contains(page, "%!") || strings.Contains(page, "{{") {
			t.Errorf("%s page has format artifacts or unfilled placeholders", name)
		}
		if !strings.Contains(page, "%") {
			t.Errorf("%s page lost its CSS percentages", name)
		}
	}
	if !strings.Contains(renderWelcome("/s/login"), `href="/s/login"`) || !strings.Contains(renderLogin("/s/login", ""), `action="/s/login"`) {
		t.Error("login URL not filled in")
	}
	if page := renderLogin("/s/login", `<script>x</script>`); strings.Contains(page, "<script>x") {
		t.Error("error text is not escaped")
	}
}
