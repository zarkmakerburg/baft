package bcc

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/pbkdf2"
)

func (r *accessRig) loginFrom(ip, user, pass string) *httptest.ResponseRecorder {
	form := url.Values{"username": {user}, "password": {pass}}
	req := httptest.NewRequest(http.MethodPost, r.base()+"login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = ip + ":4000"
	return r.do(req)
}

func TestSecurityHeadersOnEveryKindOfResponse(t *testing.T) {
	r := newAccessRig(t)
	c, _ := r.session()
	dash := httptest.NewRequest(http.MethodGet, r.base(), nil)
	dash.AddCookie(c)
	cases := map[string]*httptest.ResponseRecorder{
		"welcome":   r.do(httptest.NewRequest(http.MethodGet, r.base(), nil)),
		"sign-in":   r.do(httptest.NewRequest(http.MethodGet, r.base()+"login", nil)),
		"dashboard": r.do(dash),
		"api":       r.do(authReq(http.MethodGet, "/api/nodes", "admin-secret", nil)),
		"not found": r.do(httptest.NewRequest(http.MethodGet, "/nothing", nil)),
	}
	for name, rr := range cases {
		h := rr.Header()
		for k, want := range map[string]string{
			"X-Content-Type-Options":       "nosniff",
			"X-Frame-Options":              "DENY",
			"Referrer-Policy":              "no-referrer",
			"Cross-Origin-Opener-Policy":   "same-origin",
			"Cross-Origin-Resource-Policy": "same-origin",
			"Cache-Control":                "no-store",
		} {
			if h.Get(k) != want {
				t.Errorf("%s: %s = %q, want %q", name, k, h.Get(k), want)
			}
		}
		csp := h.Get("Content-Security-Policy")
		for _, must := range []string{"default-src 'none'", "frame-ancestors 'none'", "base-uri 'none'", "form-action 'self'", "connect-src 'self'"} {
			if !strings.Contains(csp, must) {
				t.Errorf("%s: CSP lacks %q: %s", name, must, csp)
			}
		}
		if h.Get("Permissions-Policy") == "" || h.Get("X-Request-ID") == "" {
			t.Errorf("%s: missing Permissions-Policy or X-Request-ID", name)
		}
		if strings.Contains(csp, "'unsafe-eval'") || strings.Contains(csp, "http:") || strings.Contains(csp, "*") {
			t.Errorf("%s: CSP is too loose: %s", name, csp)
		}
	}
	if cases["welcome"].Header().Get("Strict-Transport-Security") == "" {
		t.Error("HSTS missing when secure cookies (TLS) are configured")
	}
}

func TestHSTSOnlyOverTLS(t *testing.T) {
	store, _ := OpenStore(t.TempDir() + "/s.json")
	app, _ := NewServer(store, "admin")
	plain := httptest.NewRecorder()
	app.Handler().ServeHTTP(plain, authReq(http.MethodGet, "/api/nodes", "admin", nil))
	if plain.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS sent over plain HTTP")
	}
	req := authReq(http.MethodGet, "/api/nodes", "admin", nil)
	req.TLS = &tls.ConnectionState{}
	secure := httptest.NewRecorder()
	app.Handler().ServeHTTP(secure, req)
	if !strings.HasPrefix(secure.Header().Get("Strict-Transport-Security"), "max-age=") {
		t.Error("HSTS missing over TLS")
	}
}

func TestRequestIDsAreFreshAndReachTheAuditLog(t *testing.T) {
	r := newAccessRig(t)
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodGet, "/nothing", nil)
		req.Header.Set("X-Request-ID", "attacker-chosen")
		id := r.do(req).Header().Get("X-Request-ID")
		if id == "" || id == "attacker-chosen" || seen[id] {
			t.Fatalf("request id %q is empty, client-chosen or repeated", id)
		}
		seen[id] = true
	}
	// An admin action and a sign-in both carry the ID that the response had.
	rr := r.do(authReq(http.MethodPost, "/api/nodes", "admin-secret", map[string]string{"ID": "n1", "Address": "127.0.0.1:1", "Role": "foreign"}))
	nodeReq := rr.Header().Get("X-Request-ID")
	bad := r.login(r.creds.Username, "wrong")
	loginReq := bad.Header().Get("X-Request-ID")
	entries, err := r.app.audit.List(0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range entries {
		if id, ok := e.Details["request_id"].(string); ok {
			got[e.Action+"/"+e.Outcome] = id
		}
	}
	if got["node.upsert/success"] != nodeReq || got["access.login/failure"] != loginReq {
		t.Fatalf("audit request ids %v, want node=%s login=%s", got, nodeReq, loginReq)
	}
	if err := r.app.audit.Verify(); err != nil {
		t.Fatalf("audit chain: %v", err)
	}
}

func TestAuditNamesTheJobsAnActionCreated(t *testing.T) {
	r := newAccessRig(t)
	for _, n := range []map[string]string{{"ID": "ex-1", "Address": "127.0.0.1:1", "Role": "foreign"}, {"ID": "ir-1", "Address": "127.0.0.1:2", "Role": "worker"}} {
		if rr := r.do(authReq(http.MethodPost, "/api/nodes", "admin-secret", n)); rr.Code != http.StatusCreated {
			t.Fatalf("node: %d %s", rr.Code, rr.Body.String())
		}
	}
	if rr := r.do(authReq(http.MethodPost, "/api/deploy", "admin-secret", map[string]any{"node_ids": []string{"ex-1"}, "version": "v1.0.0"})); rr.Code != http.StatusAccepted {
		t.Fatalf("deploy: %d", rr.Code)
	}
	if rr := r.do(authReq(http.MethodPost, "/api/tunnels", "admin-secret", map[string]any{"ex_node": "ex-1", "ir_node": "ir-1"})); rr.Code != http.StatusAccepted {
		t.Fatalf("tunnel: %d %s", rr.Code, rr.Body.String())
	}
	entries, _ := r.app.audit.List(0)
	var deploy, tunnel *AuditEntry
	for i := range entries {
		switch entries[i].Action {
		case "deploy.create":
			deploy = &entries[i]
		case "tunnel.create":
			tunnel = &entries[i]
		}
	}
	if deploy == nil || fmt.Sprint(deploy.Details["job_ids"]) == "[]" || deploy.Details["job_ids"] == nil {
		t.Fatalf("deploy audit lacks job ids: %+v", deploy)
	}
	if tunnel == nil || tunnel.Details["tunnel_id"] == nil || tunnel.Details["job_id"] == nil {
		t.Fatalf("tunnel audit lacks tunnel/job id: %+v", tunnel)
	}
}

func TestUsernameLimiterBlocksBeforeAnyHashing(t *testing.T) {
	r := newAccessRig(t)
	for i := 0; i < 10; i++ {
		// A different address each time, so only the per-username limit can act.
		if rr := r.loginFrom(fmt.Sprintf("198.51.100.%d", i+1), r.creds.Username, "wrong"); rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i, rr.Code)
		}
	}
	rr := r.loginFrom("198.51.100.200", r.creds.Username, r.creds.Password)
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("a username past its limit = %d", rr.Code)
	}
	r.clock = r.clock.Add(16 * time.Minute)
	if rr := r.loginFrom("198.51.100.201", r.creds.Username, r.creds.Password); rr.Code != http.StatusSeeOther {
		t.Fatalf("after the window = %d", rr.Code)
	}
}

func TestGlobalLoginCeilingStopsADistributedGuess(t *testing.T) {
	r := newAccessRig(t)
	for i := 0; i < 30; i++ {
		r.loginFrom(fmt.Sprintf("203.0.113.%d", i+1), fmt.Sprintf("guess-%d", i), "wrong")
	}
	rr := r.loginFrom("203.0.113.250", r.creds.Username, r.creds.Password)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("after a distributed burst the correct credentials got %d, want 429", rr.Code)
	}
	r.clock = r.clock.Add(61 * time.Second)
	if rr := r.loginFrom("203.0.113.251", r.creds.Username, r.creds.Password); rr.Code != http.StatusSeeOther {
		t.Fatalf("after the window = %d", rr.Code)
	}
}

func TestFailureWindowKeysAreBounded(t *testing.T) {
	f := newFailureWindow(3, time.Minute, 50)
	now := time.Now()
	for i := 0; i < 5000; i++ {
		f.fail(fmt.Sprintf("k%d", i), now)
	}
	if f.size() > 50 {
		t.Fatalf("%d keys kept, limit is 50", f.size())
	}
	if userKey("  "+strings.Repeat("A", 10_000)) != userKey(strings.Repeat("a", 256)) {
		t.Error("user keys are not normalised and bounded")
	}
}

func TestPasswordHashingIsArgon2idAndAcceptsLegacyPBKDF2(t *testing.T) {
	h, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "argon2id$v=19$m=65536,t=3,p=4$") {
		t.Fatalf("new hashes are not Argon2id 64 MiB / 3 / 4: %s", h)
	}
	if !verifyPassword(h, "correct horse battery staple") || verifyPassword(h, "correct horse battery stapl3") {
		t.Fatal("Argon2id hash does not verify exactly the right password")
	}
	again, _ := hashPassword("correct horse battery staple")
	if again == h {
		t.Fatal("two hashes of one password are identical: the salt is not random")
	}
	// A hash written by the previous version still works until regeneration.
	salt := []byte("0123456789abcdef")
	key := pbkdf2.Key([]byte("legacy-pass"), salt, pbkdf2Iterations, 32, sha256.New)
	enc := base64.RawStdEncoding
	legacy := fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations, enc.EncodeToString(salt), enc.EncodeToString(key))
	if !verifyPassword(legacy, "legacy-pass") || verifyPassword(legacy, "other") {
		t.Fatal("legacy PBKDF2 hash no longer verifies")
	}
	if err := (AccessFile{SchemaVersion: accessSchemaVersion, Generation: 1, SecretPath: strings.Repeat("a", 32), Username: "op-x", PasswordHash: legacy}).validate(); err != nil {
		t.Fatalf("legacy access file rejected: %v", err)
	}
}

func TestDamagedHashParametersCannotDemandUnboundedWork(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef"))
	sum := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	start := time.Now()
	for _, bad := range []string{
		"argon2id$v=19$m=4194304,t=3,p=4$" + salt + "$" + sum, // 4 GiB
		"argon2id$v=19$m=65536,t=1000,p=4$" + salt + "$" + sum,
		"argon2id$v=19$m=65536,t=3,p=200$" + salt + "$" + sum,
		"argon2id$v=19$m=1024,t=3,p=4$" + salt + "$" + sum, // too weak
		"argon2id$v=18$m=65536,t=3,p=4$" + salt + "$" + sum,
		"argon2id$v=19$m=65536,t=3,p=4$" + salt + "$short",
		"argon2id$v=19$x=1$" + salt + "$" + sum,
		"md5$whatever", "", "$$$$",
	} {
		if verifyPassword(bad, "x") {
			t.Errorf("accepted %q", bad)
		}
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("rejecting bad parameters was not immediate")
	}
}

func TestConcurrentHashingIsBounded(t *testing.T) {
	s := newHashSlots(2)
	if !s.acquire(time.Second) || !s.acquire(time.Second) {
		t.Fatal("could not take the free slots")
	}
	if s.acquire(20 * time.Millisecond) {
		t.Fatal("a third concurrent hash was allowed")
	}
	s.release()
	if !s.acquire(time.Second) {
		t.Fatal("a released slot was not reusable")
	}
}
