package bcc

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
)

const (
	SmartIngressProviderCloudflare = "cloudflare"

	SmartIngressProviderCreate   = "CREATE"
	SmartIngressProviderUpdate   = "UPDATE"
	SmartIngressProviderNoop     = "NOOP"
	SmartIngressProviderWithdraw = "WITHDRAW"
	SmartIngressProviderBlocked  = "BLOCKED"
)

// SmartIngressProviderBinding is non-secret provider location metadata for one
// explicit BAFT route. AccountID is required for Cloudflare pools. ZoneID is
// optional: when absent, the load balancer is planned at account scope.
type SmartIngressProviderBinding struct {
	RouteID   string `json:"route_id"`
	Provider  string `json:"provider"`
	EXNode    string `json:"ex_node"`
	AccountID string `json:"account_id"`
	ZoneID    string `json:"zone_id,omitempty"`
}

// SmartIngressProviderOrigin is a provider-neutral weighted endpoint. M-017B
// renders only explicit public IR IPs and never discovers or substitutes one.
type SmartIngressProviderOrigin struct {
	Name    string  `json:"name"`
	Address string  `json:"address"`
	Weight  float64 `json:"weight"`
	Enabled bool    `json:"enabled"`
}

// SmartIngressProviderRouteState is the deterministic provider representation
// of one route. Resource names are derived from the BAFT route+EX identity.
type SmartIngressProviderRouteState struct {
	RouteID             string                       `json:"route_id"`
	Provider            string                       `json:"provider"`
	EXNode              string                       `json:"ex_node"`
	ManagedKey          string                       `json:"managed_key"`
	AccountID           string                       `json:"account_id"`
	LoadBalancerScope   string                       `json:"load_balancer_scope"`
	LoadBalancerScopeID string                       `json:"load_balancer_scope_id"`
	Host                string                       `json:"host"`
	PoolName            string                       `json:"pool_name"`
	LoadBalancerKey     string                       `json:"load_balancer_key"`
	Origins             []SmartIngressProviderOrigin `json:"origins,omitempty"`
}

// SmartIngressProviderObserved is non-secret observed metadata supplied by the
// dry-run caller. M-017B never reads a provider over the network.
type SmartIngressProviderObserved struct {
	Exists bool `json:"exists"`
	SmartIngressProviderRouteState
}

type SmartIngressProviderOperation struct {
	RouteID    string                          `json:"route_id"`
	Provider   string                          `json:"provider"`
	EXNode     string                          `json:"ex_node,omitempty"`
	Generation uint64                          `json:"generation,omitempty"`
	Kind       string                          `json:"kind"`
	Reason     string                          `json:"reason"`
	Desired    *SmartIngressProviderRouteState `json:"desired,omitempty"`
	Observed   *SmartIngressProviderObserved   `json:"observed,omitempty"`
}

type SmartIngressProviderDryRunRequest struct {
	Provider string                         `json:"provider"`
	Bindings []SmartIngressProviderBinding  `json:"bindings"`
	Observed []SmartIngressProviderObserved `json:"observed,omitempty"`
}

type SmartIngressProviderDryRunResponse struct {
	Provider   string                          `json:"provider"`
	Operations []SmartIngressProviderOperation `json:"operations"`
}

// SmartIngressProviderAdapter is the provider-neutral planning contract.
// Implementations are pure: no network access and no store mutation.
type SmartIngressProviderAdapter interface {
	Provider() string
	Plan(SmartIngressView, SmartIngressProviderBinding, SmartIngressProviderObserved) SmartIngressProviderOperation
}

type cloudflareSmartIngressAdapter struct{}

func (cloudflareSmartIngressAdapter) Provider() string { return SmartIngressProviderCloudflare }

func providerResourceBase(routeID, exNode string) string {
	slug := sanitizeTopologyID(routeID)
	if slug == "" {
		slug = "route"
	}
	if len(slug) > 24 {
		slug = strings.Trim(slug[:24], "-")
	}
	return "baft-" + slug + "-" + sha256Short(routeID+"\x00"+exNode)
}

func providerOriginName(base, irNode string) string {
	slug := sanitizeTopologyID(irNode)
	if slug == "" {
		slug = "ir"
	}
	if len(slug) > 20 {
		slug = strings.Trim(slug[:20], "-")
	}
	return base + "-" + slug + "-" + sha256Short(irNode)
}

func validProviderScopeID(v string) bool {
	if v == "" || strings.TrimSpace(v) != v || len(v) > 128 {
		return false
	}
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

func normalizeCloudflareBinding(view SmartIngressView, in SmartIngressProviderBinding) (SmartIngressProviderBinding, string, string, string) {
	in.RouteID = strings.TrimSpace(in.RouteID)
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	in.EXNode = strings.TrimSpace(in.EXNode)
	in.AccountID = strings.TrimSpace(in.AccountID)
	in.ZoneID = strings.TrimSpace(in.ZoneID)

	switch {
	case in.RouteID == "" || in.RouteID != view.RouteID:
		return in, "", "", "provider binding route_id does not match Smart Ingress route"
	case in.Provider != SmartIngressProviderCloudflare:
		return in, "", "", "provider binding is not cloudflare"
	case in.EXNode == "" || in.EXNode != view.EXNode:
		return in, "", "", "provider binding EX does not match explicit route EX"
	case !validProviderScopeID(in.AccountID):
		return in, "", "", "provider binding account_id is missing or invalid"
	case in.ZoneID != "" && !validProviderScopeID(in.ZoneID):
		return in, "", "", "provider binding zone_id is invalid"
	}
	scope, scopeID := "account", in.AccountID
	if in.ZoneID != "" {
		scope, scopeID = "zone", in.ZoneID
	}
	return in, scope, scopeID, ""
}

func renderCloudflareDesired(view SmartIngressView, binding SmartIngressProviderBinding, scope, scopeID string) (SmartIngressProviderRouteState, string) {
	p := view.SmartIngressPlan
	if !view.Usable {
		return SmartIngressProviderRouteState{}, "Smart Ingress desired state is stale or unusable"
	}
	if p.State != SmartIngressReady || p.Action != SmartIngressActionPublish {
		return SmartIngressProviderRouteState{}, "Smart Ingress desired state is not READY/PUBLISH"
	}
	if p.Generation == 0 || p.DistributionGeneration == 0 {
		return SmartIngressProviderRouteState{}, "Smart Ingress desired generation is missing"
	}
	host, err := normalizeSmartIngressHost(p.Host)
	if err != nil || host == "" || host != p.Host {
		return SmartIngressProviderRouteState{}, "Smart Ingress hostname is invalid"
	}
	if len(p.Endpoints) == 0 {
		return SmartIngressProviderRouteState{}, "Smart Ingress has no publishable IR endpoints"
	}
	base := providerResourceBase(p.RouteID, p.EXNode)
	seenIR := map[string]bool{}
	origins := make([]SmartIngressProviderOrigin, 0, len(p.Endpoints))
	sum := 0
	for _, ep := range p.Endpoints {
		if ep.IRNode == "" || seenIR[ep.IRNode] {
			return SmartIngressProviderRouteState{}, "Smart Ingress IR endpoint identity is missing or duplicated"
		}
		seenIR[ep.IRNode] = true
		ip := net.ParseIP(ep.IP)
		if !isPublicIngressIP(ip) || ip.String() != ep.IP {
			return SmartIngressProviderRouteState{}, "Smart Ingress endpoint is not a canonical public IP"
		}
		if ep.Weight <= 0 || ep.Weight > 100 {
			return SmartIngressProviderRouteState{}, "Smart Ingress endpoint weight is outside 1..100"
		}
		sum += ep.Weight
		origins = append(origins, SmartIngressProviderOrigin{
			Name: providerOriginName(base, ep.IRNode), Address: ep.IP,
			Weight: float64(ep.Weight) / 100, Enabled: true,
		})
	}
	if sum != 100 {
		return SmartIngressProviderRouteState{}, "Smart Ingress endpoint weights do not sum to 100"
	}
	sort.Slice(origins, func(i, j int) bool {
		if origins[i].Name == origins[j].Name {
			return origins[i].Address < origins[j].Address
		}
		return origins[i].Name < origins[j].Name
	})
	return SmartIngressProviderRouteState{
		RouteID: p.RouteID, Provider: SmartIngressProviderCloudflare, EXNode: p.EXNode,
		ManagedKey: base, AccountID: binding.AccountID,
		LoadBalancerScope: scope, LoadBalancerScopeID: scopeID,
		Host: host, PoolName: base + "-pool", LoadBalancerKey: base + "-lb",
		Origins: origins,
	}, ""
}

func canonicalCloudflareWeight(v float64) (float64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
		return 0, false
	}
	hundredths := math.Round(v * 100)
	if math.Abs(v*100-hundredths) > 1e-9 {
		return 0, false
	}
	return hundredths / 100, true
}

func normalizeCloudflareObserved(in SmartIngressProviderObserved, binding SmartIngressProviderBinding, scope, scopeID, base string) (SmartIngressProviderObserved, string) {
	if !in.Exists {
		return SmartIngressProviderObserved{}, ""
	}
	in.RouteID = strings.TrimSpace(in.RouteID)
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	in.EXNode = strings.TrimSpace(in.EXNode)
	in.ManagedKey = strings.TrimSpace(in.ManagedKey)
	in.AccountID = strings.TrimSpace(in.AccountID)
	in.LoadBalancerScope = strings.ToLower(strings.TrimSpace(in.LoadBalancerScope))
	in.LoadBalancerScopeID = strings.TrimSpace(in.LoadBalancerScopeID)
	in.Host = strings.TrimSpace(strings.ToLower(in.Host))
	in.PoolName = strings.TrimSpace(in.PoolName)
	in.LoadBalancerKey = strings.TrimSpace(in.LoadBalancerKey)

	switch {
	case in.RouteID != binding.RouteID:
		return in, "observed provider route_id does not match binding"
	case in.Provider != SmartIngressProviderCloudflare:
		return in, "observed provider does not match cloudflare"
	case in.EXNode != binding.EXNode:
		return in, "observed provider EX does not match explicit route EX"
	case in.ManagedKey != base:
		return in, "observed resource is not owned by this BAFT route"
	case in.AccountID != binding.AccountID:
		return in, "observed provider account does not match binding"
	case in.LoadBalancerScope != scope || in.LoadBalancerScopeID != scopeID:
		return in, "observed load balancer scope does not match binding"
	case in.PoolName != base+"-pool" || in.LoadBalancerKey != base+"-lb":
		return in, "observed provider resource keys do not match BAFT managed keys"
	}
	host, err := normalizeSmartIngressHost(in.Host)
	if err != nil || host == "" || host != in.Host {
		return in, "observed provider hostname is invalid"
	}
	in.Host = host
	for i := range in.Origins {
		o := &in.Origins[i]
		o.Name = strings.TrimSpace(o.Name)
		o.Address = strings.TrimSpace(o.Address)
		if o.Name == "" || !strings.HasPrefix(o.Name, base+"-") {
			return in, "observed pool contains an unmanaged origin"
		}
		ip := net.ParseIP(o.Address)
		if !isPublicIngressIP(ip) || ip.String() != o.Address {
			return in, "observed origin address is not a canonical public IP"
		}
		w, ok := canonicalCloudflareWeight(o.Weight)
		if !ok {
			return in, "observed origin weight is outside Cloudflare 0.01 increments"
		}
		o.Weight = w
	}
	sort.Slice(in.Origins, func(i, j int) bool {
		if in.Origins[i].Name == in.Origins[j].Name {
			return in.Origins[i].Address < in.Origins[j].Address
		}
		return in.Origins[i].Name < in.Origins[j].Name
	})
	return in, ""
}

func sameProviderOrigins(a, b []SmartIngressProviderOrigin) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameCloudflareRouteState(a SmartIngressProviderRouteState, b SmartIngressProviderObserved) bool {
	return b.Exists &&
		a.RouteID == b.RouteID &&
		a.Provider == b.Provider &&
		a.EXNode == b.EXNode &&
		a.ManagedKey == b.ManagedKey &&
		a.AccountID == b.AccountID &&
		a.LoadBalancerScope == b.LoadBalancerScope &&
		a.LoadBalancerScopeID == b.LoadBalancerScopeID &&
		a.Host == b.Host &&
		a.PoolName == b.PoolName &&
		a.LoadBalancerKey == b.LoadBalancerKey &&
		sameProviderOrigins(a.Origins, b.Origins)
}

func (cloudflareSmartIngressAdapter) Plan(view SmartIngressView, binding SmartIngressProviderBinding, observed SmartIngressProviderObserved) SmartIngressProviderOperation {
	op := SmartIngressProviderOperation{
		RouteID: view.RouteID, Provider: SmartIngressProviderCloudflare,
		EXNode: view.EXNode, Generation: view.Generation,
	}
	binding, scope, scopeID, reason := normalizeCloudflareBinding(view, binding)
	if reason != "" {
		op.Kind, op.Reason = SmartIngressProviderBlocked, reason
		return op
	}
	base := providerResourceBase(view.RouteID, view.EXNode)
	observed, reason = normalizeCloudflareObserved(observed, binding, scope, scopeID, base)
	if reason != "" {
		op.Kind, op.Reason = SmartIngressProviderBlocked, reason
		return op
	}
	if observed.Exists {
		cp := observed
		op.Observed = &cp
	}

	if view.State == SmartIngressUnpublishable || view.Action == SmartIngressActionWithdraw {
		op.Kind = SmartIngressProviderWithdraw
		op.Reason = "Smart Ingress requires explicit withdrawal for this route"
		return op
	}

	desired, reason := renderCloudflareDesired(view, binding, scope, scopeID)
	if reason != "" {
		op.Kind, op.Reason = SmartIngressProviderBlocked, reason
		return op
	}
	op.Desired = &desired
	switch {
	case !observed.Exists:
		op.Kind, op.Reason = SmartIngressProviderCreate, "no managed provider resource is observed for this route"
	case sameCloudflareRouteState(desired, observed):
		op.Kind, op.Reason = SmartIngressProviderNoop, "observed provider state already matches desired state"
	default:
		op.Kind, op.Reason = SmartIngressProviderUpdate, "managed provider state differs from desired state"
	}
	return op
}

func PlanSmartIngressProviderDryRun(views []SmartIngressView, in SmartIngressProviderDryRunRequest) (SmartIngressProviderDryRunResponse, error) {
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	if provider != SmartIngressProviderCloudflare {
		return SmartIngressProviderDryRunResponse{}, fmt.Errorf("unsupported Smart Ingress provider %q", in.Provider)
	}
	viewByRoute := map[string]SmartIngressView{}
	for _, v := range views {
		if v.RouteID == "" {
			return SmartIngressProviderDryRunResponse{}, fmt.Errorf("Smart Ingress view has empty route_id")
		}
		if _, dup := viewByRoute[v.RouteID]; dup {
			return SmartIngressProviderDryRunResponse{}, fmt.Errorf("duplicate Smart Ingress route %q", v.RouteID)
		}
		viewByRoute[v.RouteID] = v
	}
	bindingByRoute := map[string]SmartIngressProviderBinding{}
	for _, b := range in.Bindings {
		id := strings.TrimSpace(b.RouteID)
		if id == "" {
			return SmartIngressProviderDryRunResponse{}, fmt.Errorf("provider binding route_id is required")
		}
		if _, dup := bindingByRoute[id]; dup {
			return SmartIngressProviderDryRunResponse{}, fmt.Errorf("duplicate provider binding for route %q", id)
		}
		b.RouteID = id
		bindingByRoute[id] = b
	}
	observedByRoute := map[string]SmartIngressProviderObserved{}
	for _, o := range in.Observed {
		id := strings.TrimSpace(o.RouteID)
		if id == "" {
			return SmartIngressProviderDryRunResponse{}, fmt.Errorf("observed provider route_id is required")
		}
		if _, dup := observedByRoute[id]; dup {
			return SmartIngressProviderDryRunResponse{}, fmt.Errorf("duplicate observed provider route %q", id)
		}
		o.RouteID = id
		observedByRoute[id] = o
	}

	ids := map[string]bool{}
	for id := range viewByRoute {
		ids[id] = true
	}
	for id := range bindingByRoute {
		ids[id] = true
	}
	for id := range observedByRoute {
		ids[id] = true
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)

	adapter := cloudflareSmartIngressAdapter{}
	out := SmartIngressProviderDryRunResponse{Provider: provider}
	for _, id := range ordered {
		view, ok := viewByRoute[id]
		if !ok {
			out.Operations = append(out.Operations, SmartIngressProviderOperation{
				RouteID: id, Provider: provider, Kind: SmartIngressProviderBlocked,
				Reason: "provider metadata exists for a route with no BAFT Smart Ingress desired state",
			})
			continue
		}
		binding, ok := bindingByRoute[id]
		if !ok {
			out.Operations = append(out.Operations, SmartIngressProviderOperation{
				RouteID: id, Provider: provider, EXNode: view.EXNode, Generation: view.Generation,
				Kind: SmartIngressProviderBlocked, Reason: "provider binding is missing",
			})
			continue
		}
		out.Operations = append(out.Operations, adapter.Plan(view, binding, observedByRoute[id]))
	}
	return out, nil
}

func decodeSmartIngressDryRun(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func (s *Server) smartIngressProviderDryRunAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	var in SmartIngressProviderDryRunRequest
	if err := decodeSmartIngressDryRun(r, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out, err := PlanSmartIngressProviderDryRun(s.store.SmartIngressSnapshot(s.now()), in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	counts := map[string]int{}
	for _, op := range out.Operations {
		counts[op.Kind]++
	}
	if err := s.auditAdmin(r, "ingress.smart.provider.dryrun", "cluster", "success", map[string]any{
		"provider": out.Provider, "operations": len(out.Operations),
		"create": counts[SmartIngressProviderCreate], "update": counts[SmartIngressProviderUpdate],
		"noop": counts[SmartIngressProviderNoop], "withdraw": counts[SmartIngressProviderWithdraw],
		"blocked": counts[SmartIngressProviderBlocked],
	}); err != nil {
		http.Error(w, "audit log failure", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
