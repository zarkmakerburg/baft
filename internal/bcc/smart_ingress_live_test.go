package bcc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type cloudflareFakeTransport struct {
	mu              sync.Mutex
	token           string
	pool            *cloudflarePool
	lb              *cloudflareLoadBalancer
	requests        int
	writes          int
	failBeforeWrite bool
	failAfterWrite  bool
}

func boolPtr(v bool) *bool { return &v }

func (f *cloudflareFakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if req.URL.Scheme != "https" || req.URL.Host != "api.cloudflare.com" {
		return nil, errors.New("unexpected provider origin")
	}
	if req.Header.Get("Authorization") != "Bearer "+f.token {
		return nil, errors.New("bad authorization")
	}
	path := req.URL.Path
	method := req.Method
	if method != http.MethodGet {
		f.writes++
		if f.failBeforeWrite {
			f.failBeforeWrite = false
			return nil, errors.New("injected ambiguous write failure")
		}
	}
	var result any
	switch {
	case method == http.MethodGet && strings.HasSuffix(path, "/load_balancers/pools"):
		if f.pool == nil {
			result = []cloudflarePool{}
		} else {
			result = []cloudflarePool{*f.pool}
		}
	case method == http.MethodGet && strings.HasSuffix(path, "/load_balancers"):
		if f.lb == nil {
			result = []cloudflareLoadBalancer{}
		} else {
			result = []cloudflareLoadBalancer{*f.lb}
		}
	case method == http.MethodPost && strings.HasSuffix(path, "/load_balancers/pools"):
		var in cloudflarePool
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			return nil, err
		}
		in.ID = "pool-1"
		f.pool = &in
		result = in
	case method == http.MethodPut && strings.Contains(path, "/load_balancers/pools/"):
		if f.pool == nil {
			return f.response(req, http.StatusNotFound, false, map[string]any{}), nil
		}
		var in cloudflarePool
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			return nil, err
		}
		in.ID = f.pool.ID
		f.pool = &in
		result = in
	case method == http.MethodPost && strings.HasSuffix(path, "/load_balancers"):
		var in cloudflareLoadBalancer
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			return nil, err
		}
		in.ID = "lb-1"
		f.lb = &in
		result = in
	case method == http.MethodPut && strings.Contains(path, "/load_balancers/"):
		if f.lb == nil {
			return f.response(req, http.StatusNotFound, false, map[string]any{}), nil
		}
		var in cloudflareLoadBalancer
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			return nil, err
		}
		in.ID = f.lb.ID
		f.lb = &in
		result = in
	case method == http.MethodPatch && strings.Contains(path, "/load_balancers/"):
		if f.lb == nil {
			return f.response(req, http.StatusNotFound, false, map[string]any{}), nil
		}
		var in struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			return nil, err
		}
		if in.Enabled != nil {
			f.lb.Enabled = in.Enabled
		}
		result = *f.lb
	default:
		return f.response(req, http.StatusNotFound, false, map[string]any{}), nil
	}
	if method != http.MethodGet && f.failAfterWrite {
		f.failAfterWrite = false
		return nil, errors.New("injected post-write ambiguity")
	}
	return f.response(req, http.StatusOK, true, result), nil
}

func (f *cloudflareFakeTransport) response(req *http.Request, status int, success bool, result any) *http.Response {
	raw, _ := json.Marshal(map[string]any{
		"success":     success,
		"result":      result,
		"errors":      []any{},
		"result_info": map[string]any{"page": 1, "total_pages": 1},
	})
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(raw)),
		Request:    req,
	}
}

func (f *cloudflareFakeTransport) counts() (requests, writes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, f.writes
}

func setupLiveProvider(t *testing.T) (*Store, *Server, SmartIngressView, SmartIngressProviderBinding, *cloudflareFakeTransport) {
	t.Helper()
	t.Setenv("BAFT_SMART_INGRESS_LIVE", "1")
	t.Setenv("BAFT_CLOUDFLARE_API_TOKEN_ENV", "M017C_CF_TOKEN")
	t.Setenv("M017C_CF_TOKEN", "m017c-super-secret-token")
	now := time.Unix(44000, 0).UTC()
	store, views := readySmartIngressViews(t, now)
	var view SmartIngressView
	for _, v := range views {
		if v.RouteID == "de" {
			view = v
			break
		}
	}
	if view.RouteID == "" {
		view = views[0]
	}
	binding := SmartIngressProviderBinding{
		RouteID: view.RouteID, Provider: SmartIngressProviderCloudflare, EXNode: view.EXNode,
		AccountID: "account-01", ZoneID: "zone-01",
	}
	app, err := NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	app.now = func() time.Time { return now.Add(20 * time.Second) }
	fake := &cloudflareFakeTransport{token: "m017c-super-secret-token"}
	app.smartIngressHTTPClient = &http.Client{Transport: fake, Timeout: time.Second}
	return store, app, view, binding, fake
}

func desiredCloudflareForTest(t *testing.T, view SmartIngressView, binding SmartIngressProviderBinding) SmartIngressProviderRouteState {
	t.Helper()
	op := (cloudflareSmartIngressAdapter{}).Plan(view, binding, SmartIngressProviderObserved{})
	if op.Kind != SmartIngressProviderCreate || op.Desired == nil {
		t.Fatalf("initial operation=%+v", op)
	}
	return *op.Desired
}

func installFakeObserved(t *testing.T, fake *cloudflareFakeTransport, desired SmartIngressProviderRouteState, drift bool) {
	t.Helper()
	origins := make([]cloudflareOrigin, 0, len(desired.Origins))
	for i, o := range desired.Origins {
		w := o.Weight
		if drift && i == 0 {
			w = 1
		}
		if drift && i == 1 {
			w = 0
		}
		origins = append(origins, cloudflareOrigin{
			Name: o.Name, Address: o.Address, Weight: w, Enabled: boolPtr(true),
		})
	}
	fake.pool = &cloudflarePool{
		ID: "pool-1", Name: desired.PoolName, Description: cloudflareManagedMarker(desired.ManagedKey),
		Enabled: boolPtr(true), Origins: origins,
	}
	fake.lb = &cloudflareLoadBalancer{
		ID: "lb-1", Name: desired.Host, Description: cloudflareManagedMarker(desired.ManagedKey),
		Enabled: boolPtr(true), DefaultPools: []string{"pool-1"}, FallbackPool: "pool-1",
	}
}

func liveProviderObserve(t *testing.T, app *Server, binding SmartIngressProviderBinding) (int, SmartIngressProviderLiveResponse) {
	t.Helper()
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/ingress/smart/provider/observe", "admin", SmartIngressProviderLiveRequest{
		Provider: SmartIngressProviderCloudflare, Binding: binding,
	}))
	var out SmartIngressProviderLiveResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return rr.Code, out
}

func liveProviderApply(t *testing.T, app *Server, binding SmartIngressProviderBinding, ack SmartIngressProviderApplyAck) (int, SmartIngressProviderLiveResponse, string) {
	t.Helper()
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/ingress/smart/provider/apply", "admin", SmartIngressProviderLiveRequest{
		Provider: SmartIngressProviderCloudflare, Binding: binding, Ack: &ack,
	}))
	var out SmartIngressProviderLiveResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return rr.Code, out, rr.Body.String()
}

func TestSmartIngressLiveDisabledMakesZeroProviderRequests(t *testing.T) {
	t.Setenv("BAFT_SMART_INGRESS_LIVE", "")
	t.Setenv("BAFT_CLOUDFLARE_API_TOKEN_ENV", "")
	now := time.Unix(45000, 0).UTC()
	store, views := readySmartIngressViews(t, now)
	binding := cloudflareBindings(views)[0]
	app, err := NewServer(store, "admin")
	if err != nil {
		t.Fatal(err)
	}
	fake := &cloudflareFakeTransport{token: "unused"}
	app.smartIngressHTTPClient = &http.Client{Transport: fake}
	code, _ := liveProviderObserve(t, app, binding)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", code)
	}
	if requests, _ := fake.counts(); requests != 0 {
		t.Fatalf("provider requests=%d want 0", requests)
	}
}

func TestSmartIngressApplyStaleAckMakesZeroProviderRequests(t *testing.T) {
	_, app, view, binding, fake := setupLiveProvider(t)
	bad := strings.Repeat("0", 64)
	code, _, _ := liveProviderApply(t, app, binding, SmartIngressProviderApplyAck{
		RouteID: view.RouteID, Generation: view.Generation, PlanDigest: bad,
	})
	if code != http.StatusConflict {
		t.Fatalf("status=%d want 409", code)
	}
	if requests, writes := fake.counts(); requests != 0 || writes != 0 {
		t.Fatalf("requests=%d writes=%d want 0/0", requests, writes)
	}
}

func TestSmartIngressLiveRejectsTokenFieldBeforeNetwork(t *testing.T) {
	_, app, _, binding, fake := setupLiveProvider(t)
	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/ingress/smart/provider/observe", "admin", map[string]any{
		"provider": SmartIngressProviderCloudflare,
		"binding":  binding,
		"token":    "must-not-be-accepted",
	}))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if requests, _ := fake.counts(); requests != 0 {
		t.Fatalf("provider requests=%d want 0", requests)
	}
}

func TestSmartIngressLiveUpdateRequiresReobserveBeforeAppliedGeneration(t *testing.T) {
	store, app, view, binding, fake := setupLiveProvider(t)
	desired := desiredCloudflareForTest(t, view, binding)
	installFakeObserved(t, fake, desired, true)

	code, plan := liveProviderObserve(t, app, binding)
	if code != http.StatusOK || plan.Operation.Kind != SmartIngressProviderUpdate {
		t.Fatalf("observe status=%d plan=%+v", code, plan)
	}
	before := store.SmartIngressSnapshot(app.now())
	var beforeApplied uint64
	for _, v := range before {
		if v.RouteID == view.RouteID {
			beforeApplied = v.AppliedGeneration
		}
	}
	if beforeApplied != 0 {
		t.Fatalf("applied before write=%d", beforeApplied)
	}

	code, out, body := liveProviderApply(t, app, binding, SmartIngressProviderApplyAck{
		RouteID: view.RouteID, Generation: view.Generation, PlanDigest: plan.PlanDigest,
	})
	if code != http.StatusOK || !out.Confirmed || out.Applied == nil {
		t.Fatalf("apply status=%d body=%s out=%+v", code, body, out)
	}
	if out.Applied.AppliedGeneration != view.Generation || out.Applied.ApplyStatus != SmartIngressApplyApplied {
		t.Fatalf("applied state=%+v", out.Applied)
	}
	if out.Operation.Kind != SmartIngressProviderNoop {
		t.Fatalf("post-apply operation=%+v want NOOP", out.Operation)
	}
	_, writes := fake.counts()
	if writes != 2 {
		t.Fatalf("provider writes=%d want pool+load-balancer updates", writes)
	}
}

func TestSmartIngressAmbiguousWriteDoesNotAdvanceWithoutConvergence(t *testing.T) {
	store, app, view, binding, fake := setupLiveProvider(t)
	desired := desiredCloudflareForTest(t, view, binding)
	installFakeObserved(t, fake, desired, true)

	code, plan := liveProviderObserve(t, app, binding)
	if code != http.StatusOK {
		t.Fatalf("observe status=%d", code)
	}
	fake.mu.Lock()
	fake.failBeforeWrite = true
	fake.mu.Unlock()

	code, _, body := liveProviderApply(t, app, binding, SmartIngressProviderApplyAck{
		RouteID: view.RouteID, Generation: view.Generation, PlanDigest: plan.PlanDigest,
	})
	if code != http.StatusBadGateway || !strings.Contains(body, "ambiguous") {
		t.Fatalf("status=%d body=%s", code, body)
	}
	for _, v := range store.SmartIngressSnapshot(app.now()) {
		if v.RouteID == view.RouteID && v.AppliedGeneration != 0 {
			t.Fatalf("ambiguous write advanced applied generation: %+v", v)
		}
	}
}

func TestSmartIngressPostWriteAmbiguityCanBeConfirmedByReobserve(t *testing.T) {
	_, app, view, binding, fake := setupLiveProvider(t)
	desired := desiredCloudflareForTest(t, view, binding)
	installFakeObserved(t, fake, desired, true)

	code, plan := liveProviderObserve(t, app, binding)
	if code != http.StatusOK {
		t.Fatalf("observe status=%d", code)
	}
	fake.mu.Lock()
	fake.failAfterWrite = true
	fake.mu.Unlock()

	code, out, body := liveProviderApply(t, app, binding, SmartIngressProviderApplyAck{
		RouteID: view.RouteID, Generation: view.Generation, PlanDigest: plan.PlanDigest,
	})
	if code != http.StatusOK || !out.Confirmed {
		t.Fatalf("status=%d body=%s out=%+v", code, body, out)
	}
}

func TestSmartIngressProviderTokenNeverPersistsOrAudits(t *testing.T) {
	store, app, view, binding, fake := setupLiveProvider(t)
	desired := desiredCloudflareForTest(t, view, binding)
	installFakeObserved(t, fake, desired, true)
	code, plan := liveProviderObserve(t, app, binding)
	if code != http.StatusOK {
		t.Fatalf("observe status=%d", code)
	}
	code, _, body := liveProviderApply(t, app, binding, SmartIngressProviderApplyAck{
		RouteID: view.RouteID, Generation: view.Generation, PlanDigest: plan.PlanDigest,
	})
	if code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", code, body)
	}
	secret := []byte("m017c-super-secret-token")
	stateBytes, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	auditBytes, err := os.ReadFile(store.Path() + ".audit.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stateBytes, secret) || bytes.Contains(auditBytes, secret) {
		t.Fatal("Cloudflare API token leaked to durable state or audit")
	}
	if strings.Contains(body, string(secret)) {
		t.Fatal("Cloudflare API token leaked to API response")
	}
}

func TestSmartIngressLiveCreateConvergesBeforeAppliedGeneration(t *testing.T) {
	store, app, view, binding, fake := setupLiveProvider(t)

	code, plan := liveProviderObserve(t, app, binding)
	if code != http.StatusOK || plan.Operation.Kind != SmartIngressProviderCreate {
		t.Fatalf("observe status=%d plan=%+v", code, plan)
	}
	code, out, body := liveProviderApply(t, app, binding, SmartIngressProviderApplyAck{
		RouteID: view.RouteID, Generation: view.Generation, PlanDigest: plan.PlanDigest,
	})
	if code != http.StatusOK || !out.Confirmed || out.Applied == nil {
		t.Fatalf("apply status=%d body=%s out=%+v", code, body, out)
	}
	if out.Applied.AppliedGeneration != view.Generation {
		t.Fatalf("applied generation=%d want %d", out.Applied.AppliedGeneration, view.Generation)
	}
	if requests, writes := fake.counts(); requests == 0 || writes != 2 {
		t.Fatalf("requests=%d writes=%d want provider reads and exactly two create writes", requests, writes)
	}
	for _, v := range store.SmartIngressSnapshot(app.now()) {
		if v.RouteID != view.RouteID && v.AppliedGeneration != 0 {
			t.Fatalf("route %s was changed by route %s apply: %+v", v.RouteID, view.RouteID, v)
		}
	}
}

func TestSmartIngressLiveNoopPerformsNoProviderWrite(t *testing.T) {
	_, app, view, binding, fake := setupLiveProvider(t)
	desired := desiredCloudflareForTest(t, view, binding)
	installFakeObserved(t, fake, desired, false)

	code, plan := liveProviderObserve(t, app, binding)
	if code != http.StatusOK || plan.Operation.Kind != SmartIngressProviderNoop {
		t.Fatalf("observe status=%d plan=%+v", code, plan)
	}
	beforeRequests, beforeWrites := fake.counts()
	code, out, body := liveProviderApply(t, app, binding, SmartIngressProviderApplyAck{
		RouteID: view.RouteID, Generation: view.Generation, PlanDigest: plan.PlanDigest,
	})
	if code != http.StatusOK || !out.Confirmed {
		t.Fatalf("apply status=%d body=%s", code, body)
	}
	afterRequests, afterWrites := fake.counts()
	if afterWrites != beforeWrites {
		t.Fatalf("NOOP performed provider write: before=%d after=%d", beforeWrites, afterWrites)
	}
	if afterRequests <= beforeRequests {
		t.Fatalf("NOOP did not re-observe provider state")
	}
}

func TestSmartIngressLiveWithdrawDisablesExactRouteOnly(t *testing.T) {
	store, app, oldView, binding, fake := setupLiveProvider(t)
	desired := desiredCloudflareForTest(t, oldView, binding)
	installFakeObserved(t, fake, desired, false)

	store.mu.Lock()
	route := store.st.EXRoutes[oldView.RouteID]
	route.Enabled = false
	store.st.EXRoutes[oldView.RouteID] = route
	store.mu.Unlock()
	if _, err := store.EvaluateSmartIngress(app.now()); err != nil {
		t.Fatal(err)
	}
	view, err := currentSmartIngressView(store, oldView.RouteID, app.now())
	if err != nil {
		t.Fatal(err)
	}
	if view.Action != SmartIngressActionWithdraw || view.Generation <= oldView.Generation {
		t.Fatalf("withdraw view=%+v old=%+v", view, oldView)
	}
	binding.EXNode = view.EXNode

	code, plan := liveProviderObserve(t, app, binding)
	if code != http.StatusOK || plan.Operation.Kind != SmartIngressProviderWithdraw {
		t.Fatalf("observe status=%d plan=%+v", code, plan)
	}
	code, out, body := liveProviderApply(t, app, binding, SmartIngressProviderApplyAck{
		RouteID: view.RouteID, Generation: view.Generation, PlanDigest: plan.PlanDigest,
	})
	if code != http.StatusOK || !out.Confirmed || out.Applied == nil {
		t.Fatalf("withdraw status=%d body=%s out=%+v", code, body, out)
	}
	if out.Applied.AppliedGeneration != view.Generation || out.Applied.AppliedHost != "" || len(out.Applied.AppliedEndpoints) != 0 {
		t.Fatalf("withdraw applied state=%+v", out.Applied)
	}
	fake.mu.Lock()
	enabled := fake.lb != nil && boolDefaultTrue(fake.lb.Enabled)
	fake.mu.Unlock()
	if enabled {
		t.Fatal("withdraw left exact route load balancer enabled")
	}
	_, writesAfterFirst := fake.counts()
	code, secondPlan := liveProviderObserve(t, app, binding)
	if code != http.StatusOK || secondPlan.Operation.Kind != SmartIngressProviderNoop {
		t.Fatalf("repeat observe status=%d plan=%+v want NOOP", code, secondPlan)
	}
	code, secondOut, body := liveProviderApply(t, app, binding, SmartIngressProviderApplyAck{
		RouteID: view.RouteID, Generation: view.Generation, PlanDigest: secondPlan.PlanDigest,
	})
	if code != http.StatusOK || !secondOut.Confirmed {
		t.Fatalf("repeat withdraw status=%d body=%s out=%+v", code, body, secondOut)
	}
	_, writesAfterSecond := fake.counts()
	if writesAfterSecond != writesAfterFirst {
		t.Fatalf("repeat confirmed withdraw wrote provider again: before=%d after=%d", writesAfterFirst, writesAfterSecond)
	}
	for _, v := range store.SmartIngressSnapshot(app.now()) {
		if v.RouteID != view.RouteID && v.AppliedGeneration != 0 {
			t.Fatalf("withdraw of %s changed route %s: %+v", view.RouteID, v.RouteID, v)
		}
	}
}

func TestSmartIngressConcurrentApplyHasOneProviderWriter(t *testing.T) {
	_, app, view, binding, fake := setupLiveProvider(t)
	desired := desiredCloudflareForTest(t, view, binding)
	installFakeObserved(t, fake, desired, true)
	code, plan := liveProviderObserve(t, app, binding)
	if code != http.StatusOK || plan.Operation.Kind != SmartIngressProviderUpdate {
		t.Fatalf("observe status=%d plan=%+v", code, plan)
	}
	ack := SmartIngressProviderApplyAck{
		RouteID: view.RouteID, Generation: view.Generation, PlanDigest: plan.PlanDigest,
	}
	start := make(chan struct{})
	type result struct {
		code int
		body string
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			code, _, body := liveProviderApply(t, app, binding, ack)
			results <- result{code: code, body: body}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for got := range results {
		if got.code != http.StatusOK {
			t.Fatalf("concurrent apply status=%d body=%s", got.code, got.body)
		}
	}
	_, writes := fake.counts()
	if writes != 2 {
		t.Fatalf("concurrent apply provider writes=%d want exactly one pool+LB writer", writes)
	}
}
