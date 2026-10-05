package bcc

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func cloudflareBindings(views []SmartIngressView) []SmartIngressProviderBinding {
	out := make([]SmartIngressProviderBinding, 0, len(views))
	for _, v := range views {
		out = append(out, SmartIngressProviderBinding{
			RouteID: v.RouteID, Provider: SmartIngressProviderCloudflare, EXNode: v.EXNode,
			AccountID: "account-01", ZoneID: "zone-01",
		})
	}
	return out
}

func providerOpsByRoute(ops []SmartIngressProviderOperation) map[string]SmartIngressProviderOperation {
	out := map[string]SmartIngressProviderOperation{}
	for _, op := range ops {
		out[op.RouteID] = op
	}
	return out
}

func observedFromOperations(ops []SmartIngressProviderOperation) []SmartIngressProviderObserved {
	out := make([]SmartIngressProviderObserved, 0, len(ops))
	for _, op := range ops {
		if op.Desired == nil {
			continue
		}
		out = append(out, SmartIngressProviderObserved{
			Exists: true, SmartIngressProviderRouteState: *op.Desired,
		})
	}
	return out
}

func readySmartIngressViews(t *testing.T, now time.Time) (*Store, []SmartIngressView) {
	t.Helper()
	s := smartIngressStore(t, now)
	at := now.Add(10 * time.Second)
	if _, err := s.EvaluateSmartIngress(at); err != nil {
		t.Fatal(err)
	}
	return s, s.SmartIngressSnapshot(at)
}

func TestCloudflareDryRunTwoIRFiveEXIsolatedAndDeterministic(t *testing.T) {
	now := time.Unix(32000, 0).UTC()
	_, views := readySmartIngressViews(t, now)
	bindings := cloudflareBindings(views)
	for i, j := 0, len(bindings)-1; i < j; i, j = i+1, j-1 {
		bindings[i], bindings[j] = bindings[j], bindings[i]
	}
	req := SmartIngressProviderDryRunRequest{Provider: SmartIngressProviderCloudflare, Bindings: bindings}
	first, err := PlanSmartIngressProviderDryRun(views, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Operations) != 5 {
		t.Fatalf("operations=%d want 5: %+v", len(first.Operations), first.Operations)
	}
	last := ""
	for _, op := range first.Operations {
		if op.RouteID < last {
			t.Fatalf("operations not sorted: %q before %q", last, op.RouteID)
		}
		last = op.RouteID
		if op.Kind != SmartIngressProviderCreate || op.Desired == nil {
			t.Fatalf("route %s operation=%+v want CREATE with desired state", op.RouteID, op)
		}
		if len(op.Desired.Origins) != 2 {
			t.Fatalf("route %s origins=%+v want 2", op.RouteID, op.Desired.Origins)
		}
		total := 0.0
		for _, origin := range op.Desired.Origins {
			total += origin.Weight
			if !origin.Enabled {
				t.Fatalf("route %s origin disabled: %+v", op.RouteID, origin)
			}
		}
		if math.Abs(total-1) > 1e-9 {
			t.Fatalf("route %s provider weight total=%f want 1.00", op.RouteID, total)
		}
	}

	reversedViews := append([]SmartIngressView(nil), views...)
	for i, j := 0, len(reversedViews)-1; i < j; i, j = i+1, j-1 {
		reversedViews[i], reversedViews[j] = reversedViews[j], reversedViews[i]
	}
	secondBindings := append([]SmartIngressProviderBinding(nil), bindings...)
	for i, j := 0, len(secondBindings)-1; i < j; i, j = i+1, j-1 {
		secondBindings[i], secondBindings[j] = secondBindings[j], secondBindings[i]
	}
	second, err := PlanSmartIngressProviderDryRun(reversedViews, SmartIngressProviderDryRunRequest{
		Provider: SmartIngressProviderCloudflare, Bindings: secondBindings,
	})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if !bytes.Equal(a, b) {
		t.Fatalf("dry-run JSON is not byte-stable\nfirst=%s\nsecond=%s", a, b)
	}
}

func TestCloudflareWeightMappingExact(t *testing.T) {
	view := SmartIngressView{SmartIngressPlan: SmartIngressPlan{
		RouteID: "de", EXNode: "ex-1", Host: "de.ingress.example.test",
		State: SmartIngressReady, Action: SmartIngressActionPublish,
		Generation: 7, DistributionGeneration: 4,
		Endpoints: []SmartIngressEndpoint{
			{IRNode: "ir-1", IP: "1.1.1.1", Weight: 65},
			{IRNode: "ir-2", IP: "8.8.8.8", Weight: 35},
		},
	}, Usable: true}
	binding := SmartIngressProviderBinding{
		RouteID: "de", Provider: SmartIngressProviderCloudflare, EXNode: "ex-1",
		AccountID: "account-01", ZoneID: "zone-01",
	}
	out, err := PlanSmartIngressProviderDryRun([]SmartIngressView{view}, SmartIngressProviderDryRunRequest{
		Provider: SmartIngressProviderCloudflare, Bindings: []SmartIngressProviderBinding{binding},
	})
	if err != nil {
		t.Fatal(err)
	}
	op := out.Operations[0]
	if op.Kind != SmartIngressProviderCreate || op.Desired == nil {
		t.Fatalf("operation=%+v", op)
	}
	got := map[string]float64{}
	total := 0.0
	for _, origin := range op.Desired.Origins {
		got[origin.Address] = origin.Weight
		total += origin.Weight
	}
	if got["1.1.1.1"] != 0.65 || got["8.8.8.8"] != 0.35 || math.Abs(total-1) > 1e-9 {
		t.Fatalf("weights=%+v total=%f; want 0.65/0.35 total 1.00", got, total)
	}
}

func TestCloudflareRouteOnlyWeightChangeChangesOnlyThatRoute(t *testing.T) {
	now := time.Unix(33000, 0).UTC()
	_, views := readySmartIngressViews(t, now)
	bindings := cloudflareBindings(views)
	base, err := PlanSmartIngressProviderDryRun(views, SmartIngressProviderDryRunRequest{
		Provider: SmartIngressProviderCloudflare, Bindings: bindings,
	})
	if err != nil {
		t.Fatal(err)
	}
	observed := observedFromOperations(base.Operations)
	changed := append([]SmartIngressView(nil), views...)
	for i := range changed {
		if changed[i].RouteID != "tr" {
			continue
		}
		changed[i].Generation++
		changed[i].DistributionGeneration++
		changed[i].Endpoints = []SmartIngressEndpoint{
			{IRNode: "ir-1", IP: "1.1.1.1", Weight: 65},
			{IRNode: "ir-2", IP: "8.8.8.8", Weight: 35},
		}
		changed[i].Usable = true
	}
	out, err := PlanSmartIngressProviderDryRun(changed, SmartIngressProviderDryRunRequest{
		Provider: SmartIngressProviderCloudflare, Bindings: bindings, Observed: observed,
	})
	if err != nil {
		t.Fatal(err)
	}
	ops := providerOpsByRoute(out.Operations)
	for route, op := range ops {
		want := SmartIngressProviderNoop
		if route == "tr" {
			want = SmartIngressProviderUpdate
		}
		if op.Kind != want {
			t.Fatalf("route %s kind=%s want %s: %+v", route, op.Kind, want, op)
		}
	}
}

func TestCloudflareUnavailableEXWithdrawsOnlyExactRoute(t *testing.T) {
	now := time.Unix(34000, 0).UTC()
	_, views := readySmartIngressViews(t, now)
	bindings := cloudflareBindings(views)
	base, err := PlanSmartIngressProviderDryRun(views, SmartIngressProviderDryRunRequest{
		Provider: SmartIngressProviderCloudflare, Bindings: bindings,
	})
	if err != nil {
		t.Fatal(err)
	}
	observed := observedFromOperations(base.Operations)
	changed := append([]SmartIngressView(nil), views...)
	for i := range changed {
		if changed[i].RouteID != "uk" {
			continue
		}
		changed[i].Generation++
		changed[i].State = SmartIngressUnpublishable
		changed[i].Action = SmartIngressActionWithdraw
		changed[i].Endpoints = nil
		changed[i].Usable = false
	}
	out, err := PlanSmartIngressProviderDryRun(changed, SmartIngressProviderDryRunRequest{
		Provider: SmartIngressProviderCloudflare, Bindings: bindings, Observed: observed,
	})
	if err != nil {
		t.Fatal(err)
	}
	ops := providerOpsByRoute(out.Operations)
	for route, op := range ops {
		want := SmartIngressProviderNoop
		if route == "uk" {
			want = SmartIngressProviderWithdraw
		}
		if op.Kind != want {
			t.Fatalf("route %s kind=%s want %s: %+v", route, op.Kind, want, op)
		}
	}
	if ops["uk"].EXNode != "ex-3" {
		t.Fatalf("withdraw substituted EX: %+v", ops["uk"])
	}
}

func TestCloudflareMissingBindingAndUnmanagedObservedBlock(t *testing.T) {
	now := time.Unix(35000, 0).UTC()
	_, views := readySmartIngressViews(t, now)
	bindings := cloudflareBindings(views)
	var withoutDE []SmartIngressProviderBinding
	for _, b := range bindings {
		if b.RouteID != "de" {
			withoutDE = append(withoutDE, b)
		}
	}
	out, err := PlanSmartIngressProviderDryRun(views, SmartIngressProviderDryRunRequest{
		Provider: SmartIngressProviderCloudflare, Bindings: withoutDE,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := providerOpsByRoute(out.Operations)["de"]; got.Kind != SmartIngressProviderBlocked {
		t.Fatalf("missing binding operation=%+v want BLOCKED", got)
	}

	base, err := PlanSmartIngressProviderDryRun(views, SmartIngressProviderDryRunRequest{
		Provider: SmartIngressProviderCloudflare, Bindings: bindings,
	})
	if err != nil {
		t.Fatal(err)
	}
	observed := observedFromOperations(base.Operations)
	for i := range observed {
		if observed[i].RouteID == "de" {
			observed[i].ManagedKey = "external-unmanaged-resource"
		}
	}
	out, err = PlanSmartIngressProviderDryRun(views, SmartIngressProviderDryRunRequest{
		Provider: SmartIngressProviderCloudflare, Bindings: bindings, Observed: observed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := providerOpsByRoute(out.Operations)["de"]; got.Kind != SmartIngressProviderBlocked {
		t.Fatalf("unmanaged conflict operation=%+v want BLOCKED", got)
	}
}

func TestCloudflareExactDesiredObservedIsNoop(t *testing.T) {
	now := time.Unix(36000, 0).UTC()
	_, views := readySmartIngressViews(t, now)
	bindings := cloudflareBindings(views)
	base, err := PlanSmartIngressProviderDryRun(views, SmartIngressProviderDryRunRequest{
		Provider: SmartIngressProviderCloudflare, Bindings: bindings,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := PlanSmartIngressProviderDryRun(views, SmartIngressProviderDryRunRequest{
		Provider: SmartIngressProviderCloudflare, Bindings: bindings,
		Observed: observedFromOperations(base.Operations),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range out.Operations {
		if op.Kind != SmartIngressProviderNoop {
			t.Fatalf("route %s kind=%s want NOOP: %+v", op.RouteID, op.Kind, op)
		}
	}
}

func TestCloudflareFailClosedInvalidDesiredAndBindingInputs(t *testing.T) {
	baseView := SmartIngressView{SmartIngressPlan: SmartIngressPlan{
		RouteID: "de", EXNode: "ex-1", Host: "de.ingress.example.test",
		State: SmartIngressReady, Action: SmartIngressActionPublish,
		Generation: 3, DistributionGeneration: 2,
		Endpoints: []SmartIngressEndpoint{
			{IRNode: "ir-1", IP: "1.1.1.1", Weight: 50},
			{IRNode: "ir-2", IP: "8.8.8.8", Weight: 50},
		},
	}, Usable: true}
	baseBinding := SmartIngressProviderBinding{
		RouteID: "de", Provider: SmartIngressProviderCloudflare, EXNode: "ex-1",
		AccountID: "account-01", ZoneID: "zone-01",
	}

	tests := []struct {
		name   string
		mutate func(*SmartIngressView, *SmartIngressProviderBinding)
	}{
		{"stale desired", func(v *SmartIngressView, _ *SmartIngressProviderBinding) { v.Usable = false }},
		{"invalid hostname", func(v *SmartIngressView, _ *SmartIngressProviderBinding) { v.Host = "not a hostname" }},
		{"private endpoint", func(v *SmartIngressView, _ *SmartIngressProviderBinding) { v.Endpoints[0].IP = "10.0.0.1" }},
		{"bad weight sum", func(v *SmartIngressView, _ *SmartIngressProviderBinding) { v.Endpoints[0].Weight = 40 }},
		{"mismatched EX", func(_ *SmartIngressView, b *SmartIngressProviderBinding) { b.EXNode = "ex-2" }},
		{"provider mismatch", func(_ *SmartIngressView, b *SmartIngressProviderBinding) { b.Provider = "other" }},
		{"invalid account scope", func(_ *SmartIngressView, b *SmartIngressProviderBinding) { b.AccountID = "bad scope id" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := baseView
			v.Endpoints = append([]SmartIngressEndpoint(nil), baseView.Endpoints...)
			b := baseBinding
			tc.mutate(&v, &b)
			out, err := PlanSmartIngressProviderDryRun([]SmartIngressView{v}, SmartIngressProviderDryRunRequest{
				Provider: SmartIngressProviderCloudflare, Bindings: []SmartIngressProviderBinding{b},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Operations) != 1 || out.Operations[0].Kind != SmartIngressProviderBlocked || out.Operations[0].Desired != nil {
				t.Fatalf("%s produced non-blocked provider intent: %+v", tc.name, out.Operations)
			}
		})
	}
}

func TestCloudflareUnknownExternalRouteIsBlocked(t *testing.T) {
	view := SmartIngressView{SmartIngressPlan: SmartIngressPlan{
		RouteID: "de", EXNode: "ex-1", Host: "de.ingress.example.test",
		State: SmartIngressReady, Action: SmartIngressActionPublish,
		Generation: 1, DistributionGeneration: 1,
		Endpoints: []SmartIngressEndpoint{{IRNode: "ir-1", IP: "1.1.1.1", Weight: 100}},
	}, Usable: true}
	bindings := []SmartIngressProviderBinding{
		{RouteID: "de", Provider: SmartIngressProviderCloudflare, EXNode: "ex-1", AccountID: "account-01"},
		{RouteID: "ghost", Provider: SmartIngressProviderCloudflare, EXNode: "ex-x", AccountID: "account-01"},
	}
	observed := []SmartIngressProviderObserved{{
		Exists: true,
		SmartIngressProviderRouteState: SmartIngressProviderRouteState{
			RouteID: "ghost", Provider: SmartIngressProviderCloudflare, EXNode: "ex-x",
			ManagedKey: "external", AccountID: "account-01", LoadBalancerScope: "account",
			LoadBalancerScopeID: "account-01", Host: "ghost.example.test",
			PoolName: "external-pool", LoadBalancerKey: "external-lb",
		},
	}}
	out, err := PlanSmartIngressProviderDryRun([]SmartIngressView{view}, SmartIngressProviderDryRunRequest{
		Provider: SmartIngressProviderCloudflare, Bindings: bindings, Observed: observed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := providerOpsByRoute(out.Operations)["ghost"]; got.Kind != SmartIngressProviderBlocked {
		t.Fatalf("unknown external route operation=%+v want BLOCKED", got)
	}
}

type denyOutboundRoundTripper struct{}

func (denyOutboundRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	panic("M-017B attempted outbound HTTP")
}

func TestSmartIngressProviderDryRunAPIAdminOnlyNoNetworkNoApplyMutation(t *testing.T) {
	now := time.Unix(37000, 0).UTC()
	s, views := readySmartIngressViews(t, now)
	at := now.Add(10 * time.Second)
	before := smartByRoute(s.SmartIngressSnapshot(at))
	bindings := cloudflareBindings(views)

	app, err := NewServer(s, "admin")
	if err != nil {
		t.Fatal(err)
	}
	app.now = func() time.Time { return at }
	app.httpClient = &http.Client{Transport: denyOutboundRoundTripper{}}

	oldTransport, oldClient := http.DefaultTransport, http.DefaultClient
	http.DefaultTransport = denyOutboundRoundTripper{}
	http.DefaultClient = &http.Client{Transport: denyOutboundRoundTripper{}}
	defer func() {
		http.DefaultTransport, http.DefaultClient = oldTransport, oldClient
	}()

	rr := httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/ingress/smart/provider/dry-run", "", SmartIngressProviderDryRunRequest{
		Provider: SmartIngressProviderCloudflare, Bindings: bindings,
	}))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated dry-run status=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/ingress/smart/provider/dry-run", "admin", SmartIngressProviderDryRunRequest{
		Provider: SmartIngressProviderCloudflare, Bindings: bindings,
	}))
	if rr.Code != http.StatusOK {
		t.Fatalf("dry-run status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got SmartIngressProviderDryRunResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Operations) != 5 {
		t.Fatalf("dry-run operations=%d want 5", len(got.Operations))
	}

	after := smartByRoute(s.SmartIngressSnapshot(at))
	for route := range before {
		if before[route].AppliedGeneration != after[route].AppliedGeneration ||
			before[route].AppliedHost != after[route].AppliedHost ||
			before[route].ApplyStatus != after[route].ApplyStatus {
			t.Fatalf("dry-run mutated applied state for %s: before=%+v after=%+v", route, before[route], after[route])
		}
	}

	entries, err := app.audit.List(200)
	if err != nil {
		t.Fatal(err)
	}
	auditJSON, _ := json.Marshal(entries)
	auditText := string(auditJSON)
	if !strings.Contains(auditText, "ingress.smart.provider.dryrun") {
		t.Fatalf("dry-run audit missing: %s", auditText)
	}
	for _, forbidden := range []string{"1.1.1.1", "8.8.8.8", "account-01", "zone-01"} {
		if strings.Contains(auditText, forbidden) {
			t.Fatalf("dry-run audit leaked endpoint/provider metadata %q: %s", forbidden, auditText)
		}
	}

	rr = httptest.NewRecorder()
	app.Handler().ServeHTTP(rr, authReq(http.MethodPost, "/api/ingress/smart/provider/dry-run", "admin", map[string]any{
		"provider": SmartIngressProviderCloudflare,
		"bindings": bindings,
		"token":    "must-never-be-accepted",
	}))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "unknown field") {
		t.Fatalf("credential-shaped unknown field was not rejected: status=%d body=%s", rr.Code, rr.Body.String())
	}
}
