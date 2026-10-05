package bcc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cloudflareAPIBaseURL       = "https://api.cloudflare.com/client/v4"
	cloudflareResponseMaxBytes = 1 << 20
	cloudflareListPageSize     = 50
	cloudflareListMaxPages     = 20
)

var providerSecretEnvNameRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

type SmartIngressProviderApplyAck struct {
	RouteID    string `json:"route_id"`
	Generation uint64 `json:"generation"`
	PlanDigest string `json:"plan_digest"`
}

type SmartIngressProviderLiveRequest struct {
	Provider string                        `json:"provider"`
	Binding  SmartIngressProviderBinding   `json:"binding"`
	Ack      *SmartIngressProviderApplyAck `json:"ack,omitempty"`
}

type SmartIngressProviderLiveResponse struct {
	Provider   string                        `json:"provider"`
	Operation  SmartIngressProviderOperation `json:"operation"`
	PlanDigest string                        `json:"plan_digest"`
	Confirmed  bool                          `json:"confirmed,omitempty"`
	Applied    *SmartIngressView             `json:"applied,omitempty"`
}

type cloudflareAPIError struct {
	Code int `json:"code"`
}

type cloudflareResultInfo struct {
	Page       int `json:"page"`
	TotalPages int `json:"total_pages"`
}

type cloudflareEnvelope struct {
	Success    bool                  `json:"success"`
	Result     json.RawMessage       `json:"result"`
	Errors     []cloudflareAPIError  `json:"errors"`
	ResultInfo *cloudflareResultInfo `json:"result_info,omitempty"`
}

type cloudflareOrigin struct {
	Name    string  `json:"name"`
	Address string  `json:"address"`
	Weight  float64 `json:"weight"`
	Enabled *bool   `json:"enabled,omitempty"`
}

type cloudflarePool struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	Enabled        *bool              `json:"enabled,omitempty"`
	Origins        []cloudflareOrigin `json:"origins"`
	OriginSteering struct {
		Policy string `json:"policy"`
	} `json:"origin_steering,omitempty"`
}

type cloudflareLoadBalancer struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Enabled      *bool    `json:"enabled,omitempty"`
	DefaultPools []string `json:"default_pools"`
	FallbackPool string   `json:"fallback_pool"`
}

type cloudflareLiveObservation struct {
	Observed            SmartIngressProviderObserved
	PoolPresent         bool
	LoadBalancerPresent bool
}

func boolDefaultTrue(v *bool) bool {
	return v == nil || *v
}

func cloudflareManagedMarker(base string) string {
	return "baft-smart-ingress:" + base
}

func (s *Server) smartIngressRouteLock(routeID string) *sync.Mutex {
	s.smartIngressApplyMu.Lock()
	defer s.smartIngressApplyMu.Unlock()
	if s.smartIngressRouteLocks == nil {
		s.smartIngressRouteLocks = map[string]*sync.Mutex{}
	}
	m := s.smartIngressRouteLocks[routeID]
	if m == nil {
		m = &sync.Mutex{}
		s.smartIngressRouteLocks[routeID] = m
	}
	return m
}

func (s *Server) cloudflareToken() (string, error) {
	if !s.smartIngressLiveEnabled {
		return "", errors.New("Smart Ingress live provider access is disabled")
	}
	envName := strings.TrimSpace(s.smartIngressTokenEnv)
	if !providerSecretEnvNameRE.MatchString(envName) {
		return "", errors.New("Cloudflare API token secret reference is not configured")
	}
	token, ok := os.LookupEnv(envName)
	if !ok || strings.TrimSpace(token) == "" {
		return "", errors.New("Cloudflare API token secret source is empty")
	}
	if len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		return "", errors.New("Cloudflare API token secret source is invalid")
	}
	return token, nil
}

func currentSmartIngressView(store *Store, routeID string, now time.Time) (SmartIngressView, error) {
	routeID = strings.TrimSpace(routeID)
	now = now.UTC()
	store.mu.Lock()
	defer store.mu.Unlock()

	persisted, ok := store.st.SmartIngressPlans[routeID]
	if !ok || persisted.RouteID == "" {
		return SmartIngressView{}, fmt.Errorf("Smart Ingress route %q does not exist", routeID)
	}
	route, exists := store.st.EXRoutes[routeID]
	live := store.chooseSmartIngressLocked(route, exists, persisted, now)
	if !sameSmartIngressDesired(persisted, live) {
		return SmartIngressView{}, fmt.Errorf("Smart Ingress route %q desired state is stale; evaluate it before provider access", routeID)
	}
	return SmartIngressView{
		SmartIngressPlan: persisted,
		Usable:           persisted.State == SmartIngressReady,
	}, nil
}

type providerPlanDigestMaterial struct {
	Provider               string                      `json:"provider"`
	Binding                SmartIngressProviderBinding `json:"binding"`
	RouteID                string                      `json:"route_id"`
	EXNode                 string                      `json:"ex_node"`
	Host                   string                      `json:"host,omitempty"`
	State                  string                      `json:"state"`
	Action                 string                      `json:"action"`
	Endpoints              []SmartIngressEndpoint      `json:"endpoints,omitempty"`
	DistributionGeneration uint64                      `json:"distribution_generation,omitempty"`
	Generation             uint64                      `json:"generation"`
}

func smartIngressProviderPlanDigest(view SmartIngressView, binding SmartIngressProviderBinding) (string, SmartIngressProviderBinding, error) {
	binding, _, _, reason := normalizeCloudflareBinding(view, binding)
	if reason != "" {
		return "", binding, errors.New(reason)
	}
	material := providerPlanDigestMaterial{
		Provider:               SmartIngressProviderCloudflare,
		Binding:                binding,
		RouteID:                view.RouteID,
		EXNode:                 view.EXNode,
		Host:                   view.Host,
		State:                  view.State,
		Action:                 view.Action,
		Endpoints:              append([]SmartIngressEndpoint(nil), view.Endpoints...),
		DistributionGeneration: view.DistributionGeneration,
		Generation:             view.Generation,
	}
	raw, err := json.Marshal(material)
	if err != nil {
		return "", binding, err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), binding, nil
}

func validPlanDigest(v string) bool {
	if len(v) != sha256.Size*2 || strings.ToLower(v) != v {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}

func samePlanDigest(a, b string) bool {
	if !validPlanDigest(a) || !validPlanDigest(b) || len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) cloudflareRequest(ctx context.Context, method, path, token string, body any, out any) (cloudflareResultInfo, error) {
	var resultInfo cloudflareResultInfo
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "://") {
		return resultInfo, errors.New("invalid Cloudflare API path")
	}
	fullURL := cloudflareAPIBaseURL + path
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return resultInfo, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, reader)
	if err != nil {
		return resultInfo, errors.New("build Cloudflare request failed")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := s.smartIngressHTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return resultInfo, errors.New("Cloudflare request transport failure")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, cloudflareResponseMaxBytes+1))
	if err != nil {
		return resultInfo, errors.New("Cloudflare response read failure")
	}
	if len(raw) > cloudflareResponseMaxBytes {
		return resultInfo, errors.New("Cloudflare response exceeds size limit")
	}
	var envelope cloudflareEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return resultInfo, errors.New("Cloudflare response is not valid JSON")
	}
	if envelope.ResultInfo != nil {
		resultInfo = *envelope.ResultInfo
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.Success {
		codes := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			if e.Code != 0 {
				codes = append(codes, strconv.Itoa(e.Code))
			}
		}
		if len(codes) > 0 {
			return resultInfo, fmt.Errorf("Cloudflare API rejected request (status %d, codes %s)", resp.StatusCode, strings.Join(codes, ","))
		}
		return resultInfo, fmt.Errorf("Cloudflare API rejected request (status %d)", resp.StatusCode)
	}
	if out != nil {
		if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
			return resultInfo, errors.New("Cloudflare response result is missing")
		}
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return resultInfo, errors.New("Cloudflare response result has unexpected shape")
		}
	}
	return resultInfo, nil
}

func cloudflarePoolsPath(accountID string, page int) string {
	v := url.Values{}
	v.Set("page", strconv.Itoa(page))
	v.Set("per_page", strconv.Itoa(cloudflareListPageSize))
	return "/accounts/" + url.PathEscape(accountID) + "/load_balancers/pools?" + v.Encode()
}

func cloudflareLoadBalancersPath(scope, scopeID string, page int) string {
	root := "accounts"
	if scope == "zone" {
		root = "zones"
	}
	v := url.Values{}
	v.Set("page", strconv.Itoa(page))
	v.Set("per_page", strconv.Itoa(cloudflareListPageSize))
	return "/" + root + "/" + url.PathEscape(scopeID) + "/load_balancers?" + v.Encode()
}

func cloudflareLoadBalancerBasePath(scope, scopeID string) string {
	root := "accounts"
	if scope == "zone" {
		root = "zones"
	}
	return "/" + root + "/" + url.PathEscape(scopeID) + "/load_balancers"
}

func (s *Server) listCloudflarePools(ctx context.Context, token, accountID string) ([]cloudflarePool, error) {
	var all []cloudflarePool
	for page := 1; page <= cloudflareListMaxPages; page++ {
		var batch []cloudflarePool
		info, err := s.cloudflareRequest(ctx, http.MethodGet, cloudflarePoolsPath(accountID, page), token, nil, &batch)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if info.TotalPages == 0 || page >= info.TotalPages {
			return all, nil
		}
	}
	return nil, errors.New("Cloudflare pool listing exceeds safety page limit")
}

func (s *Server) listCloudflareLoadBalancers(ctx context.Context, token, scope, scopeID string) ([]cloudflareLoadBalancer, error) {
	var all []cloudflareLoadBalancer
	for page := 1; page <= cloudflareListMaxPages; page++ {
		var batch []cloudflareLoadBalancer
		info, err := s.cloudflareRequest(ctx, http.MethodGet, cloudflareLoadBalancersPath(scope, scopeID, page), token, nil, &batch)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if info.TotalPages == 0 || page >= info.TotalPages {
			return all, nil
		}
	}
	return nil, errors.New("Cloudflare load balancer listing exceeds safety page limit")
}

func (s *Server) observeCloudflare(ctx context.Context, token string, view SmartIngressView, binding SmartIngressProviderBinding) (cloudflareLiveObservation, error) {
	binding, scope, scopeID, reason := normalizeCloudflareBinding(view, binding)
	if reason != "" {
		return cloudflareLiveObservation{}, errors.New(reason)
	}
	base := providerResourceBase(view.RouteID, view.EXNode)
	marker := cloudflareManagedMarker(base)
	poolName := base + "-pool"

	pools, err := s.listCloudflarePools(ctx, token, binding.AccountID)
	if err != nil {
		return cloudflareLiveObservation{}, err
	}
	var pool *cloudflarePool
	for i := range pools {
		p := &pools[i]
		nameMatch := p.Name == poolName
		markerMatch := p.Description == marker
		if nameMatch != markerMatch {
			return cloudflareLiveObservation{}, errors.New("Cloudflare pool name is occupied by an unmanaged resource")
		}
		if nameMatch && markerMatch {
			if pool != nil {
				return cloudflareLiveObservation{}, errors.New("multiple managed Cloudflare pools match one BAFT route")
			}
			cp := *p
			pool = &cp
		}
	}

	lbs, err := s.listCloudflareLoadBalancers(ctx, token, scope, scopeID)
	if err != nil {
		return cloudflareLiveObservation{}, err
	}
	var lb *cloudflareLoadBalancer
	for i := range lbs {
		v := &lbs[i]
		hostMatch := v.Name == view.Host && view.Host != ""
		markerMatch := v.Description == marker
		if hostMatch && !markerMatch {
			return cloudflareLiveObservation{}, errors.New("Cloudflare load balancer hostname is occupied by an unmanaged resource")
		}
		if markerMatch {
			if lb != nil {
				return cloudflareLiveObservation{}, errors.New("multiple managed Cloudflare load balancers match one BAFT route")
			}
			cp := *v
			lb = &cp
		}
	}
	if lb != nil && pool == nil {
		return cloudflareLiveObservation{}, errors.New("managed Cloudflare load balancer exists without its managed pool")
	}

	live := cloudflareLiveObservation{}
	if pool != nil {
		if p := strings.ToLower(strings.TrimSpace(pool.OriginSteering.Policy)); p != "" && p != "random" {
			return cloudflareLiveObservation{}, errors.New("managed Cloudflare pool uses unsupported origin steering")
		}
		live.PoolPresent = true
		live.Observed.PoolID = strings.TrimSpace(pool.ID)
		if live.Observed.PoolID == "" {
			return cloudflareLiveObservation{}, errors.New("managed Cloudflare pool has no id")
		}
		poolEnabled := boolDefaultTrue(pool.Enabled)
		live.Observed.PoolEnabled = &poolEnabled
	}
	if lb != nil {
		live.LoadBalancerPresent = true
		live.Observed.LoadBalancerID = strings.TrimSpace(lb.ID)
		if live.Observed.LoadBalancerID == "" {
			return cloudflareLiveObservation{}, errors.New("managed Cloudflare load balancer has no id")
		}
		if pool != nil {
			wiringMatches := len(lb.DefaultPools) == 1 &&
				lb.DefaultPools[0] == live.Observed.PoolID &&
				lb.FallbackPool == live.Observed.PoolID
			live.Observed.LoadBalancerPoolWiringMatches = &wiringMatches
		}
	}
	if pool == nil || lb == nil {
		return live, nil
	}

	enabled := boolDefaultTrue(lb.Enabled)
	live.Observed.Exists = true
	live.Observed.LoadBalancerEnabled = &enabled
	live.Observed.SmartIngressProviderRouteState = SmartIngressProviderRouteState{
		RouteID:             view.RouteID,
		Provider:            SmartIngressProviderCloudflare,
		EXNode:              view.EXNode,
		ManagedKey:          base,
		AccountID:           binding.AccountID,
		LoadBalancerScope:   scope,
		LoadBalancerScopeID: scopeID,
		Host:                strings.ToLower(strings.TrimSpace(lb.Name)),
		PoolName:            strings.TrimSpace(pool.Name),
		LoadBalancerKey:     base + "-lb",
	}
	for _, origin := range pool.Origins {
		live.Observed.Origins = append(live.Observed.Origins, SmartIngressProviderOrigin{
			Name:    strings.TrimSpace(origin.Name),
			Address: strings.TrimSpace(origin.Address),
			Weight:  origin.Weight,
			Enabled: boolDefaultTrue(origin.Enabled),
		})
	}
	sort.Slice(live.Observed.Origins, func(i, j int) bool {
		if live.Observed.Origins[i].Name == live.Observed.Origins[j].Name {
			return live.Observed.Origins[i].Address < live.Observed.Origins[j].Address
		}
		return live.Observed.Origins[i].Name < live.Observed.Origins[j].Name
	})
	return live, nil
}

func cloudflarePoolBody(desired SmartIngressProviderRouteState) map[string]any {
	origins := make([]map[string]any, 0, len(desired.Origins))
	for _, o := range desired.Origins {
		origins = append(origins, map[string]any{
			"name": o.Name, "address": o.Address, "weight": o.Weight, "enabled": o.Enabled,
		})
	}
	return map[string]any{
		"name":            desired.PoolName,
		"description":     cloudflareManagedMarker(desired.ManagedKey),
		"enabled":         true,
		"origins":         origins,
		"origin_steering": map[string]any{"policy": "random"},
	}
}

func cloudflareLoadBalancerBody(desired SmartIngressProviderRouteState, poolID string) map[string]any {
	return map[string]any{
		"name":          desired.Host,
		"description":   cloudflareManagedMarker(desired.ManagedKey),
		"enabled":       true,
		"default_pools": []string{poolID},
		"fallback_pool": poolID,
	}
}

func (s *Server) writeCloudflarePool(ctx context.Context, token string, desired SmartIngressProviderRouteState, poolID string) (string, error) {
	path := "/accounts/" + url.PathEscape(desired.AccountID) + "/load_balancers/pools"
	method := http.MethodPost
	if poolID != "" {
		method = http.MethodPatch
		path += "/" + url.PathEscape(poolID)
	}
	var result struct {
		ID string `json:"id"`
	}
	if _, err := s.cloudflareRequest(ctx, method, path, token, cloudflarePoolBody(desired), &result); err != nil {
		return "", err
	}
	if poolID != "" {
		return poolID, nil
	}
	if strings.TrimSpace(result.ID) == "" {
		return "", errors.New("Cloudflare created pool without an id")
	}
	return strings.TrimSpace(result.ID), nil
}

func (s *Server) writeCloudflareLoadBalancer(ctx context.Context, token string, desired SmartIngressProviderRouteState, lbID, poolID string) error {
	path := cloudflareLoadBalancerBasePath(desired.LoadBalancerScope, desired.LoadBalancerScopeID)
	method := http.MethodPost
	if lbID != "" {
		method = http.MethodPatch
		path += "/" + url.PathEscape(lbID)
	}
	var result struct {
		ID string `json:"id"`
	}
	_, err := s.cloudflareRequest(ctx, method, path, token, cloudflareLoadBalancerBody(desired, poolID), &result)
	return err
}

func (s *Server) withdrawCloudflareLoadBalancer(ctx context.Context, token string, observed cloudflareLiveObservation, binding SmartIngressProviderBinding, view SmartIngressView) error {
	if !observed.LoadBalancerPresent || observed.Observed.LoadBalancerID == "" {
		return nil
	}
	_, scope, scopeID, reason := normalizeCloudflareBinding(view, binding)
	if reason != "" {
		return errors.New(reason)
	}
	path := cloudflareLoadBalancerBasePath(scope, scopeID) + "/" + url.PathEscape(observed.Observed.LoadBalancerID)
	var result struct {
		ID string `json:"id"`
	}
	_, err := s.cloudflareRequest(ctx, http.MethodPatch, path, token, map[string]any{"enabled": false}, &result)
	return err
}

func cloudflareWithdrawConverged(observed cloudflareLiveObservation) bool {
	if !observed.LoadBalancerPresent {
		return true
	}
	return observed.Observed.LoadBalancerEnabled != nil && !*observed.Observed.LoadBalancerEnabled
}

func (s *Server) executeCloudflareOperation(ctx context.Context, token string, in SmartIngressProviderLiveRequest, view SmartIngressView, binding SmartIngressProviderBinding, digest string, op SmartIngressProviderOperation, observed cloudflareLiveObservation) error {
	switch op.Kind {
	case SmartIngressProviderNoop:
		return nil
	case SmartIngressProviderBlocked:
		return errors.New("provider operation is blocked")
	case SmartIngressProviderWithdraw:
		return s.withdrawCloudflareLoadBalancer(ctx, token, observed, binding, view)
	case SmartIngressProviderCreate, SmartIngressProviderUpdate:
		if op.Desired == nil {
			return errors.New("provider operation has no desired state")
		}
		poolID, err := s.writeCloudflarePool(ctx, token, *op.Desired, observed.Observed.PoolID)
		if err != nil {
			return err
		}

		// CREATE/UPDATE spans two provider writes. Re-fence against the live
		// desired state before issuing the second write so a concurrent Smart
		// Ingress evaluation cannot make the load-balancer write stale.
		currentView, currentBinding, currentDigest, err := s.liveProviderPlan(in)
		if err != nil || currentView.Generation != view.Generation ||
			!samePlanDigest(currentDigest, digest) || currentBinding != binding {
			return errors.New("desired state changed between provider writes")
		}
		return s.writeCloudflareLoadBalancer(ctx, token, *op.Desired, observed.Observed.LoadBalancerID, poolID)
	default:
		return fmt.Errorf("unsupported provider operation %q", op.Kind)
	}
}

func (s *Store) ConfirmSmartIngressApplied(routeID string, generation uint64, now time.Time, audit AuditEntry) (SmartIngressPlan, bool, error) {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.st.SmartIngressPlans[routeID]
	if !ok || p.RouteID == "" {
		return SmartIngressPlan{}, false, errors.New("Smart Ingress route is missing")
	}
	if p.Generation != generation {
		return SmartIngressPlan{}, false, errors.New("Smart Ingress generation changed before provider confirmation")
	}
	route, exists := s.st.EXRoutes[routeID]
	live := s.chooseSmartIngressLocked(route, exists, p, now)
	if !sameSmartIngressDesired(p, live) {
		return SmartIngressPlan{}, false, errors.New("Smart Ingress desired state changed before durable provider confirmation")
	}
	already := p.AppliedGeneration == p.Generation && p.ApplyStatus == SmartIngressApplyApplied
	if p.State == SmartIngressReady && p.Action == SmartIngressActionPublish {
		already = already && p.AppliedHost == p.Host && sameSmartIngressEndpoints(p.AppliedEndpoints, p.Endpoints)
	} else {
		already = already && p.AppliedHost == "" && len(p.AppliedEndpoints) == 0
	}
	if already {
		return p, false, nil
	}
	before := p
	p.AppliedGeneration = p.Generation
	p.AppliedAt = now
	p.ApplyStatus = SmartIngressApplyApplied
	if p.State == SmartIngressReady && p.Action == SmartIngressActionPublish {
		p.AppliedHost = p.Host
		p.AppliedEndpoints = append([]SmartIngressEndpoint(nil), p.Endpoints...)
	} else {
		p.AppliedHost = ""
		p.AppliedEndpoints = nil
	}
	s.st.SmartIngressPlans[routeID] = p
	audit.Timestamp = now
	audit.Action = "ingress.smart.provider.apply"
	audit.Target = routeID
	audit.Outcome = "success"
	if audit.Actor == "" {
		audit.Actor = "admin"
	}
	if _, err := s.enqueueSecurityAuditLocked(audit); err != nil {
		s.st.SmartIngressPlans[routeID] = before
		return SmartIngressPlan{}, false, err
	}
	if err := s.saveLocked(); err != nil {
		s.st.SmartIngressPlans[routeID] = before
		return SmartIngressPlan{}, false, err
	}
	return p, true, nil
}

func sameSmartIngressEndpoints(a, b []SmartIngressEndpoint) bool {
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

func providerOperationForLive(view SmartIngressView, binding SmartIngressProviderBinding, observed cloudflareLiveObservation) SmartIngressProviderOperation {
	op := (cloudflareSmartIngressAdapter{}).Plan(view, binding, observed.Observed)
	if (view.State == SmartIngressUnpublishable || view.Action == SmartIngressActionWithdraw) && cloudflareWithdrawConverged(observed) {
		op.Kind = SmartIngressProviderNoop
		op.Reason = "provider state is already withdrawn for this exact route"
	}
	return op
}

func (s *Server) liveProviderPlan(in SmartIngressProviderLiveRequest) (SmartIngressView, SmartIngressProviderBinding, string, error) {
	if strings.ToLower(strings.TrimSpace(in.Provider)) != SmartIngressProviderCloudflare {
		return SmartIngressView{}, SmartIngressProviderBinding{}, "", errors.New("unsupported Smart Ingress provider")
	}
	view, err := currentSmartIngressView(s.store, in.Binding.RouteID, s.now())
	if err != nil {
		return SmartIngressView{}, SmartIngressProviderBinding{}, "", err
	}
	digest, binding, err := smartIngressProviderPlanDigest(view, in.Binding)
	if err != nil {
		return SmartIngressView{}, SmartIngressProviderBinding{}, "", err
	}
	return view, binding, digest, nil
}

func (s *Server) smartIngressProviderObserveAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	var in SmartIngressProviderLiveRequest
	if err := decodeSmartIngressDryRun(r, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if in.Ack != nil {
		http.Error(w, "observe request must not include apply acknowledgement", http.StatusBadRequest)
		return
	}
	view, binding, digest, err := s.liveProviderPlan(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token, err := s.cloudflareToken()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	observed, err := s.observeCloudflare(ctx, token, view, binding)
	if err != nil {
		s.auditFailure(w, r, "ingress.smart.provider.observe", view.RouteID, map[string]any{
			"provider": SmartIngressProviderCloudflare, "generation": view.Generation,
		}, err, http.StatusBadGateway)
		return
	}
	op := providerOperationForLive(view, binding, observed)
	if err := s.auditAdmin(r, "ingress.smart.provider.observe", view.RouteID, "success", map[string]any{
		"provider": SmartIngressProviderCloudflare, "generation": view.Generation, "operation": op.Kind,
	}); err != nil {
		http.Error(w, "audit log failure", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, SmartIngressProviderLiveResponse{
		Provider: SmartIngressProviderCloudflare, Operation: op, PlanDigest: digest,
	})
}

func (s *Server) smartIngressProviderApplyAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.admin(w, r) {
		return
	}
	var in SmartIngressProviderLiveRequest
	if err := decodeSmartIngressDryRun(r, &in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if in.Ack == nil {
		http.Error(w, "explicit apply acknowledgement is required", http.StatusBadRequest)
		return
	}
	view, binding, digest, err := s.liveProviderPlan(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if in.Ack.RouteID != view.RouteID || in.Ack.Generation != view.Generation || !samePlanDigest(in.Ack.PlanDigest, digest) {
		http.Error(w, "apply acknowledgement is stale or does not match current desired state", http.StatusConflict)
		return
	}

	routeLock := s.smartIngressRouteLock(view.RouteID)
	routeLock.Lock()
	defer routeLock.Unlock()

	view, binding, digest, err = s.liveProviderPlan(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if in.Ack.RouteID != view.RouteID || in.Ack.Generation != view.Generation || !samePlanDigest(in.Ack.PlanDigest, digest) {
		http.Error(w, "desired state changed before apply", http.StatusConflict)
		return
	}

	token, err := s.cloudflareToken()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	observed, err := s.observeCloudflare(ctx, token, view, binding)
	if err != nil {
		s.auditFailure(w, r, "ingress.smart.provider.apply", view.RouteID, map[string]any{
			"provider": SmartIngressProviderCloudflare, "generation": view.Generation, "phase": "observe",
		}, err, http.StatusBadGateway)
		return
	}
	// Provider observation can itself take long enough for the live desired
	// state to change. Fence once more immediately before any provider write.
	writeView, writeBinding, writeDigest, err := s.liveProviderPlan(in)
	if err != nil || writeView.Generation != view.Generation ||
		!samePlanDigest(writeDigest, digest) || writeBinding != binding {
		http.Error(w, "desired state changed during provider observation", http.StatusConflict)
		return
	}

	op := providerOperationForLive(view, binding, observed)
	if op.Kind == SmartIngressProviderBlocked {
		http.Error(w, "provider reconciliation is blocked: "+op.Reason, http.StatusConflict)
		return
	}
	if err := s.auditAdmin(r, "ingress.smart.provider.apply.start", view.RouteID, "success", map[string]any{
		"provider": SmartIngressProviderCloudflare, "generation": view.Generation, "operation": op.Kind,
	}); err != nil {
		http.Error(w, "audit log failure", http.StatusInternalServerError)
		return
	}

	writeErr := s.executeCloudflareOperation(ctx, token, in, view, binding, digest, op, observed)
	post, observeErr := s.observeCloudflare(ctx, token, view, binding)
	converged := false
	if observeErr == nil {
		if view.State == SmartIngressUnpublishable || view.Action == SmartIngressActionWithdraw {
			converged = cloudflareWithdrawConverged(post)
		} else {
			converged = providerOperationForLive(view, binding, post).Kind == SmartIngressProviderNoop
		}
	}
	if !converged {
		errText := "provider convergence was not confirmed"
		if writeErr != nil {
			errText = "provider write outcome is ambiguous; convergence was not confirmed"
		}
		if observeErr != nil {
			errText += "; re-observation failed"
		}
		_ = s.auditAdmin(r, "ingress.smart.provider.apply.result", view.RouteID, "failure", map[string]any{
			"provider": SmartIngressProviderCloudflare, "generation": view.Generation, "operation": op.Kind,
			"reason": errText,
		})
		http.Error(w, errText, http.StatusBadGateway)
		return
	}

	currentView, currentBinding, currentDigest, err := s.liveProviderPlan(in)
	if err != nil || currentView.Generation != view.Generation || !samePlanDigest(currentDigest, digest) ||
		currentBinding != binding {
		http.Error(w, "desired state changed after provider write; applied generation was not advanced", http.StatusConflict)
		return
	}
	appliedPlan, changed, err := s.store.ConfirmSmartIngressApplied(view.RouteID, view.Generation, s.now(), AuditEntry{
		Actor: "admin", RemoteIP: s.clientIP(r),
		Details: withRequest(r, map[string]any{
			"provider": SmartIngressProviderCloudflare, "generation": view.Generation, "operation": op.Kind,
		}),
	})
	if err != nil {
		http.Error(w, "provider converged but durable applied-state confirmation failed", http.StatusInternalServerError)
		return
	}
	if changed {
		if err := s.FlushSecurityAuditIntents(); err != nil {
			w.Header().Set("X-BAFT-Audit-State", "pending")
		} else {
			_ = s.SendAuditAnchor(r.Context())
		}
	}
	appliedView := SmartIngressView{SmartIngressPlan: appliedPlan, Usable: currentView.Usable}
	postOp := providerOperationForLive(currentView, binding, post)
	writeJSON(w, http.StatusOK, SmartIngressProviderLiveResponse{
		Provider: SmartIngressProviderCloudflare, Operation: postOp, PlanDigest: digest,
		Confirmed: true, Applied: &appliedView,
	})
}
