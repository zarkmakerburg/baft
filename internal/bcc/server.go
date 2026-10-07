package bcc

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

type AlertConfig struct {
	WebhookURL                    string
	TelemetryStaleAfter           time.Duration
	HandshakeErrorRateMilliPerMin int64
	Interval                      time.Duration
}

type Alert struct {
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	NodeID    string    `json:"node_id"`
	NodeAlias string    `json:"node_alias"`
	RouteID   string    `json:"route_id,omitempty"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
	// Severity is "warning" for a confirmed DEGRADED layer and "critical" for a
	// confirmed DOWN one. Health is the layer state behind it and Evidence the
	// raw observation that fed that layer; neither is an alert trigger by itself.
	Severity string `json:"severity,omitempty"`
	Health   string `json:"health,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	// EvidenceFields is the structured evidence (node_unreachable carries the
	// address, previous and current state, failure duration, last successful
	// reachability, transition reason and event id).
	EvidenceFields map[string]string `json:"evidence_fields,omitempty"`
	// Unnotified: the alert is recorded but its notification was withheld
	// because it belongs to a larger incident (CorrelatedWith names it).
	// CorrelatedAlerts, on the root alert, lists what it covers.
	Unnotified       bool     `json:"unnotified,omitempty"`
	CorrelatedWith   string   `json:"correlated_with,omitempty"`
	CorrelatedAlerts []string `json:"correlated_alerts,omitempty"`
}

type Server struct {
	store *Store
	adminToken string
	audit *AuditLog
	guard *IPGuard
	trustedProxies map[string]struct{}
	auditAnchorWebhook string
	auditAnchorInterval time.Duration
	auditAnchorRetryBase time.Duration
	auditAnchorRetryMax time.Duration
	anchorOutbox *AuditAnchorOutbox
	anchorWake chan struct{}
	probeTimeout time.Duration
	alertConfig AlertConfig
	alertMu sync.Mutex
	activeAlerts map[string]Alert
	httpClient *http.Client
	smartIngressHTTPClient *http.Client
	smartIngressLiveEnabled bool
	smartIngressTokenEnv string
	smartIngressApplyMu sync.Mutex
	smartIngressRouteLocks map[string]*sync.Mutex
	mutationMu sync.Mutex
	securityAuditMu sync.Mutex
	backupMu sync.Mutex
	backupDir string
	backupKey []byte
	restoreFault func(string) error
	access *accessGate
	jobKey ed25519.PrivateKey
	boot *bootstrapState
	loginLim *loginLimiter
	hashing hashSlots
	now func() time.Time
}

func NewServer(store *Store,adminToken string) (*Server,error) {
	if store==nil{return nil,errors.New("store is required")}
	if strings.TrimSpace(adminToken)==""{return nil,errors.New("admin token is required")}
	audit,err:=OpenAuditLog(store.path+".audit.jsonl")
	if err!=nil{return nil,fmt.Errorf("open audit log: %w",err)}
	outbox,err:=OpenAuditAnchorOutbox(store.path+".audit-anchor-outbox.json")
	if err!=nil{return nil,fmt.Errorf("open audit anchor outbox: %w",err)}
	initialAlerts:=store.ActiveAlertsSnapshot()
	srv:=&Server{
		store:store,adminToken:adminToken,audit:audit,guard:newIPGuard(SecurityConfig{}),trustedProxies:map[string]struct{}{},probeTimeout:1500*time.Millisecond,
		alertConfig:AlertConfig{TelemetryStaleAfter:3*time.Minute,HandshakeErrorRateMilliPerMin:5000,Interval:15*time.Second},
		activeAlerts:initialAlerts,httpClient:&http.Client{Timeout:5*time.Second},
		smartIngressHTTPClient:&http.Client{Timeout:5*time.Second,CheckRedirect:func(*http.Request,[]*http.Request)error{return http.ErrUseLastResponse}},
		smartIngressLiveEnabled:strings.TrimSpace(os.Getenv("BAFT_SMART_INGRESS_LIVE"))=="1",smartIngressTokenEnv:strings.TrimSpace(os.Getenv("BAFT_CLOUDFLARE_API_TOKEN_ENV")),smartIngressRouteLocks:map[string]*sync.Mutex{},
		auditAnchorRetryBase:time.Second,auditAnchorRetryMax:time.Minute,anchorOutbox:outbox,anchorWake:make(chan struct{},1),
		now:func() time.Time{return time.Now().UTC()},
		loginLim:newLoginLimiter(),hashing:newHashSlots(2),
	}
	if err:=srv.FlushSecurityAuditIntents();err!=nil{return nil,fmt.Errorf("reconcile security audit intents: %w",err)}
	entries,err:=audit.List(0)
	if err!=nil{return nil,fmt.Errorf("read audit for outbox reconcile: %w",err)}
	if err:=outbox.ReconcileSecurityAudit(entries);err!=nil{return nil,fmt.Errorf("reconcile audit anchor outbox: %w",err)}
	return srv,nil
}

func bearer(r *http.Request) string {
	const p="Bearer "
	v:=r.Header.Get("Authorization")
	if !strings.HasPrefix(v,p){return ""}
	return strings.TrimSpace(strings.TrimPrefix(v,p))
}

func (s *Server) admin(w http.ResponseWriter,r *http.Request) bool {
	if s.sessionAdmin(r){return true}
	got:=bearer(r)
	ok:=len(got)==len(s.adminToken)&&subtle.ConstantTimeCompare([]byte(got),[]byte(s.adminToken))==1
	if !ok{
		s.guard.AuthFailure(s.clientIP(r),s.now())
		http.Error(w,"unauthorized",http.StatusUnauthorized)
		return false
	}
	s.guard.AuthSuccess(s.clientIP(r))
	return true
}

func (s *Server) ConfigureSecurity(cfg SecurityConfig){
	s.guard=newIPGuard(cfg)
}

func (s *Server) ConfigureTrustedProxies(values []string) error {
	m,err:=parseTrustedProxies(values)
	if err!=nil{return err}
	s.trustedProxies=m
	return nil
}

func (s *Server) clientIP(r *http.Request) string {
	return clientIP(r,s.trustedProxies)
}

func (s *Server) auditAdmin(r *http.Request,action,target,outcome string,details map[string]any) error {
	_,err:=s.audit.Append(AuditEntry{
		Timestamp:s.now().UTC(),Actor:"admin",RemoteIP:s.clientIP(r),
		Action:action,Target:target,Outcome:outcome,Details:withRequest(r,details),
	})
	return err
}

func (s *Server) auditFailure(w http.ResponseWriter,r *http.Request,action,target string,details map[string]any,err error,status int){
	if aerr:=s.auditAdmin(r,action,target,"failure",details);aerr!=nil{
		http.Error(w,"audit log failure",http.StatusInternalServerError);return
	}
	http.Error(w,err.Error(),status)
}

func (s *Server) agentAuthResult(r *http.Request,err error){
	if errors.Is(err,ErrAgentAuthentication){s.guard.AuthFailure(s.clientIP(r),s.now());return}
	if err==nil{s.guard.AuthSuccess(s.clientIP(r))}
}

func writeJSON(w http.ResponseWriter,status int,v any){
	w.Header().Set("Content-Type","application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(r *http.Request,v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body,1<<20)).Decode(v)
}

func (s *Server) Handler() http.Handler {
	m:=http.NewServeMux()
	m.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){
		if r.URL.Path!="/"{http.NotFound(w,r);return}
		w.Header().Set("Content-Type","text/html; charset=utf-8")
		_,_=w.Write([]byte(dashboardHTML))
	})
	m.HandleFunc("/api/nodes",s.nodes)
	m.HandleFunc("/api/jobs",s.jobs)
	m.HandleFunc("/api/enroll",s.enroll)
	m.HandleFunc("/api/bootstrap",s.bootstrap)
	m.HandleFunc("/api/tunnels",s.tunnels)
	m.HandleFunc("/api/path-probes",s.pathProbes)
	m.HandleFunc("/api/path-probes/cancel",s.pathProbeCancel)
	m.HandleFunc("/api/path-discovery",s.pathDiscoveryAPI)
	m.HandleFunc("/api/path-graph",s.pathGraphAPI)
	m.HandleFunc("/api/path-matrix",s.pathMatrixAPI)
	m.HandleFunc("/api/route-doctor",s.routeDoctorAPI)
	m.HandleFunc("/api/ssh-migration",s.sshMigrationAPI)
	m.HandleFunc("/api/tunnels/cancel",s.tunnelCancel)
	m.HandleFunc("/api/tunnels/plan",s.tunnelPlan)
	m.HandleFunc("/api/tunnels/decommission/plan",s.tunnelDecommissionPlan)
	m.HandleFunc("/api/tunnels/decommission",s.tunnelDecommission)
	m.HandleFunc("/api/tunnels/drift",s.tunnelDrift)
	m.HandleFunc("/api/tunnels/rotate-cert",s.certRotationStart)
	m.HandleFunc("/api/topology",s.topologyAPI)
	m.HandleFunc("/api/topology/reconcile",s.topologyReconcileAPI)
	m.HandleFunc("/api/ingress",s.ingressAPI)
	m.HandleFunc("/api/ingress/evaluate",s.ingressEvaluateAPI)
	m.HandleFunc("/api/ingress/distribution",s.distributionAPI)
	m.HandleFunc("/api/ingress/distribution/evaluate",s.distributionEvaluateAPI)
	m.HandleFunc("/api/ingress/smart",s.smartIngressAPI)
	m.HandleFunc("/api/ingress/smart/evaluate",s.smartIngressEvaluateAPI)
	m.HandleFunc("/api/ingress/smart/provider/dry-run",s.smartIngressProviderDryRunAPI)
	m.HandleFunc("/api/ingress/smart/provider/observe",s.smartIngressProviderObserveAPI)
	m.HandleFunc("/api/ingress/smart/provider/apply",s.smartIngressProviderApplyAPI)
	m.HandleFunc("/api/cert-rotations",s.certRotations)
	m.HandleFunc("/api/cert-rotations/cancel",s.certRotationCancel)
	m.HandleFunc("/api/bootstrap/hostkey",s.bootstrapHostKey)
	m.HandleFunc("/api/deploy",s.deploy)
	m.HandleFunc("/api/agent/jobs",s.agentJobs)
	m.HandleFunc("/api/agent/ack",s.agentAck)
	m.HandleFunc("/api/agent/traffic",s.agentTraffic)
	m.HandleFunc("/api/finance",s.finance)
	m.HandleFunc("/api/finance/report",s.financeReport)
	m.HandleFunc("/api/monitoring",s.monitoring)
	m.HandleFunc("/api/health",s.healthAPI)
	m.HandleFunc("/api/discovery",s.discoveryAPI)
	m.HandleFunc("/api/history",s.history)
	m.HandleFunc("/api/audit",s.auditEntries)
	m.HandleFunc("/api/backups",s.backups)
	m.HandleFunc("/api/backups/restore-preview",s.restorePreviewAPI)
	m.HandleFunc("/api/nodes/revoke",s.revokeNode)
	m.HandleFunc("/api/nodes/rotate-token",s.rotateNodeToken)
	if s.access!=nil{
		return s.harden(s.guard.middleware(s.now,s.clientIP,true,s.accessHandler(m)))
	}
	return s.harden(s.guard.middleware(s.now,s.clientIP,false,m))
}

func (s *Server) nodes(w http.ResponseWriter,r *http.Request){
	switch r.Method{
	case http.MethodGet:
		// The node list is the cluster topology; it is admin-only like every
		// other read endpoint.
		if !s.admin(w,r){return}
		writeJSON(w,http.StatusOK,s.store.ListNodes())
	case http.MethodPost:
		if !s.admin(w,r){return}
		s.mutationMu.Lock();defer s.mutationMu.Unlock()
		var in struct{
			ID,Alias,Address,Role,PublicKey string
			PathIPv4 string `json:"path_ipv4"`
			PathIPv6 string `json:"path_ipv6"`
			AgentTokenEnv string
			AgentToken string
			AgentTokenEnvSnake string `json:"agent_token_env"`
			AgentTokenSnake string `json:"agent_token"`
		}
		if err:=decodeJSON(r,&in);err!=nil{http.Error(w,err.Error(),400);return}
		if in.AgentToken!=""||in.AgentTokenSnake!=""{
			http.Error(w,"raw agent token is forbidden; use agent token environment variable",400);return
		}
		envName:=strings.TrimSpace(in.AgentTokenEnv)
		if envName==""{envName=strings.TrimSpace(in.AgentTokenEnvSnake)}
		agentToken:=""
		if envName!=""{
			if strings.ContainsAny(envName,"=\x00"){http.Error(w,"invalid agent token env name",400);return}
			var ok bool
			agentToken,ok=os.LookupEnv(envName)
			if !ok||agentToken==""{http.Error(w,"agent token environment variable is empty",400);return}
		}
		details:=map[string]any{"alias":in.Alias,"address":in.Address,"path_ipv4":in.PathIPv4,"path_ipv6":in.PathIPv6,"role":in.Role,"public_key":in.PublicKey,"agent_token_env":envName}
		n,err:=s.store.UpsertNode(Node{ID:in.ID,Alias:in.Alias,Address:in.Address,PathIPv4:in.PathIPv4,PathIPv6:in.PathIPv6,Role:in.Role,PublicKey:in.PublicKey},agentToken)
		if err!=nil{s.auditFailure(w,r,"node.upsert",in.ID,details,err,http.StatusBadRequest);return}
		if err:=s.auditAdmin(r,"node.upsert",in.ID,"success",details);err!=nil{http.Error(w,"audit log failure",500);return}
		writeJSON(w,http.StatusCreated,n)
	default:http.Error(w,"method not allowed",405)
	}
}

func (s *Server) jobs(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodGet{http.Error(w,"method not allowed",405);return}
	if !s.admin(w,r){return}
	jobs:=s.store.ListJobs()
	for i:=range jobs{jobs[i]=publicJob(jobs[i])}
	writeJSON(w,http.StatusOK,jobs)
}

// enroll was replaced by the tunnel builder; the route stays so old tools get
// a clear answer instead of a 404.
func (s *Server) enroll(w http.ResponseWriter,r *http.Request){
	if !s.admin(w,r){return}
	http.Error(w,"retired: build tunnels with POST /api/tunnels",http.StatusGone)
}

func (s *Server) deploy(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodPost{http.Error(w,"method not allowed",405);return}
	if !s.admin(w,r){return}
	s.mutationMu.Lock();defer s.mutationMu.Unlock()
	var in struct{NodeIDs []string `json:"node_ids"`; Version string `json:"version"`}
	if err:=decodeJSON(r,&in);err!=nil{http.Error(w,err.Error(),400);return}
	details:=map[string]any{"node_ids":append([]string(nil),in.NodeIDs...),"version":in.Version}
	jobs,err:=s.store.CreateDeployJobs(in.NodeIDs,in.Version)
	if err!=nil{s.auditFailure(w,r,"deploy.create","cluster",details,err,http.StatusBadRequest);return}
	jobIDs:=make([]string,0,len(jobs));for _,j:=range jobs{jobIDs=append(jobIDs,j.ID)}
	details["job_ids"]=jobIDs
	if err:=s.auditAdmin(r,"deploy.create","cluster","success",details);err!=nil{http.Error(w,"audit log failure",500);return}
	writeJSON(w,http.StatusAccepted,jobs)
}

func (s *Server) agentJobs(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodGet{http.Error(w,"method not allowed",405);return}
	nodeID:=r.URL.Query().Get("node_id")
	jobs,err:=s.store.PullJobs(nodeID,bearer(r))
	s.agentAuthResult(r,err)
	if err!=nil{http.Error(w,err.Error(),http.StatusUnauthorized);return}
	signed,err:=s.signAgentJobs(jobs)
	if err!=nil{http.Error(w,"job signing failed",http.StatusInternalServerError);return}
	writeJSON(w,http.StatusOK,signed)
}

func (s *Server) agentAck(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodPost{http.Error(w,"method not allowed",405);return}
	var in struct{NodeID,JobID,Status,Message,Output string}
	if err:=decodeJSON(r,&in);err!=nil{http.Error(w,err.Error(),400);return}
	err:=s.store.AckJobOutput(in.NodeID,bearer(r),in.JobID,in.Status,in.Message,in.Output)
	s.agentAuthResult(r,err)
	if err!=nil{
		status:=http.StatusBadRequest
		if errors.Is(err,ErrAgentAuthentication){status=http.StatusUnauthorized}
		http.Error(w,err.Error(),status);return
	}
	s.AdvanceTunnels()
	writeJSON(w,http.StatusOK,map[string]bool{"ok":true})
}

func (s *Server) ProbeOnce(ctx context.Context){
	for _,n:=range s.store.ListNodes(){
		d:=net.Dialer{Timeout:s.probeTimeout}
		start:=time.Now()
		conn,err:=d.DialContext(ctx,"tcp",n.Address)
		health:="down"
		latency:=int64(-1)
		if err==nil{
			health="up"
			latency=time.Since(start).Milliseconds()
			_ = conn.Close()
		}
		_ = s.store.SetHealth(n.ID,health,latency,time.Now())
	}
	s.EvaluateHealthOnce()
}

func (s *Server) StartHealthLoop(ctx context.Context,interval time.Duration){
	if interval<=0{interval=10*time.Second}
	s.ProbeOnce(ctx)
	t:=time.NewTicker(interval);defer t.Stop()
	for{
		select{
		case <-ctx.Done():return
		case <-t.C:s.ProbeOnce(ctx)
		}
	}
}


func (s *Server) agentTraffic(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodPost{http.Error(w,"method not allowed",405);return}
	defer r.Body.Close()
	body,err:=io.ReadAll(io.LimitReader(r.Body,1<<20))
	if err!=nil{http.Error(w,err.Error(),400);return}
	var in telemetry.Report
	if err:=json.Unmarshal(body,&in);err!=nil{http.Error(w,err.Error(),400);return}
	f,duplicate,err:=s.store.ApplyTelemetry(bearer(r),r.Header.Get("X-BAFT-Signature"),body,in)
	s.agentAuthResult(r,err)
	if err!=nil{http.Error(w,err.Error(),http.StatusUnauthorized);return}
	writeJSON(w,http.StatusAccepted,map[string]any{"finance":f,"duplicate":duplicate,"sequence":in.Sequence})
}

func (s *Server) finance(w http.ResponseWriter,r *http.Request){
	if !s.admin(w,r){return}
	switch r.Method{
	case http.MethodGet:
		writeJSON(w,http.StatusOK,s.store.FinanceSnapshot())
	case http.MethodPost:
		s.mutationMu.Lock();defer s.mutationMu.Unlock()
		var in struct{
			NodeID string `json:"node_id"`
			CostMicrosPerGiB int64 `json:"cost_micros_per_gib"`
			RevenueMicrosPerGiB int64 `json:"revenue_micros_per_gib"`
			Currency string `json:"currency"`
			EffectiveFrom string `json:"effective_from,omitempty"`
		}
		if err:=decodeJSON(r,&in);err!=nil{http.Error(w,err.Error(),400);return}
		effective:=s.now().UTC()
		if strings.TrimSpace(in.EffectiveFrom)!=""{
			parsed,err:=time.Parse(time.RFC3339,in.EffectiveFrom)
			if err!=nil{http.Error(w,"effective_from must be RFC3339",400);return}
			effective=parsed.UTC()
		}
		details:=map[string]any{"cost_micros_per_gib":in.CostMicrosPerGiB,"revenue_micros_per_gib":in.RevenueMicrosPerGiB,"currency":in.Currency,"effective_from":effective.Format(time.RFC3339)}
		if err:=s.store.SetFinancePolicyAt(in.NodeID,in.CostMicrosPerGiB,in.RevenueMicrosPerGiB,in.Currency,effective);err!=nil{s.auditFailure(w,r,"finance.rate.change",in.NodeID,details,err,http.StatusBadRequest);return}
		if err:=s.auditAdmin(r,"finance.rate.change",in.NodeID,"success",details);err!=nil{http.Error(w,"audit log failure",500);return}
		h:=s.store.RateHistory(in.NodeID)
		writeJSON(w,http.StatusOK,map[string]any{"ok":true,"rate":h[len(h)-1]})
	default:http.Error(w,"method not allowed",405)
	}
}

func (s *Server) financeReport(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodGet{http.Error(w,"method not allowed",405);return}
	if !s.admin(w,r){return}
	q:=r.URL.Query()
	period:=q.Get("period")
	if period==""{period="daily"}
	tz:=q.Get("tz")
	if tz==""{tz="Asia/Tehran"}
	rows,err:=s.store.FinanceReport(period,q.Get("from"),q.Get("to"),tz)
	if err!=nil{http.Error(w,err.Error(),400);return}
	if q.Get("format")=="csv"{
		b,err:=FinanceReportCSV(rows);if err!=nil{http.Error(w,err.Error(),500);return}
		w.Header().Set("Content-Type","text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition","attachment; filename=baft-finance-"+period+".csv")
		w.WriteHeader(http.StatusOK)
		_,_=w.Write(b)
		return
	}
	writeJSON(w,http.StatusOK,map[string]any{"period":period,"timezone":tz,"rows":rows})
}


func (s *Server) monitoring(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodGet{http.Error(w,"method not allowed",405);return}
	if !s.admin(w,r){return}
	writeJSON(w,http.StatusOK,s.store.MonitoringSnapshot(s.now().UTC(),s.alertConfig.TelemetryStaleAfter))
}

func (s *Server) history(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodGet{http.Error(w,"method not allowed",405);return}
	if !s.admin(w,r){return}
	nodeID:=strings.TrimSpace(r.URL.Query().Get("node_id"))
	if nodeID==""{http.Error(w,"node_id is required",400);return}
	writeJSON(w,http.StatusOK,s.store.History(nodeID,s.now().UTC()))
}

func (s *Server) ConfigureAlerts(cfg AlertConfig) error {
	if cfg.TelemetryStaleAfter<=0{cfg.TelemetryStaleAfter=3*time.Minute}
	if cfg.HandshakeErrorRateMilliPerMin<=0{cfg.HandshakeErrorRateMilliPerMin=5000}
	if cfg.Interval<=0{cfg.Interval=15*time.Second}
	if cfg.WebhookURL!=""{
		u,err:=url.Parse(cfg.WebhookURL)
		if err!=nil||u.Host==""||(u.Scheme!="http"&&u.Scheme!="https"){return errors.New("alert webhook must be an absolute http/https URL")}
	}
	s.alertConfig=cfg
	return nil
}

func (s *Server) StartAlertLoop(ctx context.Context) {
	_ = s.EvaluateAlertsOnce(ctx)
	t:=time.NewTicker(s.alertConfig.Interval);defer t.Stop()
	for{
		select{
		case <-ctx.Done():return
		case <-t.C:_ = s.EvaluateAlertsOnce(ctx)
		}
	}
}

func (s *Server) EvaluateAlertsOnce(ctx context.Context) error {
	return s.evaluateAlertsAt(ctx,s.now().UTC())
}

func (s *Server) sendWebhook(ctx context.Context,alert Alert) error {
	body,err:=json.Marshal(alert);if err!=nil{return err}
	req,err:=http.NewRequestWithContext(ctx,http.MethodPost,s.alertConfig.WebhookURL,bytes.NewReader(body));if err!=nil{return err}
	req.Header.Set("Content-Type","application/json")
	resp,err:=s.httpClient.Do(req);if err!=nil{return err}
	defer resp.Body.Close()
	if resp.StatusCode<200||resp.StatusCode>=300{return fmt.Errorf("alert webhook status %d",resp.StatusCode)}
	return nil
}


func (s *Server) auditEntries(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodGet{http.Error(w,"method not allowed",405);return}
	if !s.admin(w,r){return}
	entries,err:=s.audit.List(200)
	if err!=nil{http.Error(w,"audit read failed",500);return}
	writeJSON(w,http.StatusOK,entries)
}

func (s *Server) revokeNode(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodPost{http.Error(w,"method not allowed",405);return}
	if !s.admin(w,r){return}
	s.mutationMu.Lock();defer s.mutationMu.Unlock()
	var in struct{NodeID string `json:"node_id"`; Reason string `json:"reason"`}
	if err:=decodeJSON(r,&in);err!=nil{http.Error(w,err.Error(),400);return}
	details:=map[string]any{"reason":strings.TrimSpace(in.Reason)}
	now:=s.now()
	n,err:=s.store.RevokeNode(strings.TrimSpace(in.NodeID),in.Reason,now,AuditEntry{Timestamp:now.UTC(),Actor:"admin",RemoteIP:s.clientIP(r),Details:withRequest(r,details)})
	if err!=nil{s.auditFailure(w,r,"node.revoke",in.NodeID,details,err,http.StatusBadRequest);return}
	if err:=s.FlushSecurityAuditIntents();err!=nil{
		w.Header().Set("X-BAFT-Audit-State","pending")
	}else{
		_ = s.SendAuditAnchor(r.Context())
	}
	writeJSON(w,http.StatusOK,n)
}

func (s *Server) rotateNodeToken(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodPost{http.Error(w,"method not allowed",405);return}
	if !s.admin(w,r){return}
	s.mutationMu.Lock();defer s.mutationMu.Unlock()
	var in struct{
		NodeID string `json:"node_id"`
		AgentTokenEnv string `json:"agent_token_env"`
		GraceSeconds int64 `json:"grace_seconds"`
	}
	if err:=decodeJSON(r,&in);err!=nil{http.Error(w,err.Error(),400);return}
	envName:=strings.TrimSpace(in.AgentTokenEnv)
	if envName==""||strings.ContainsAny(envName,"=\x00"){http.Error(w,"valid agent_token_env is required",400);return}
	newToken,ok:=os.LookupEnv(envName)
	if !ok||strings.TrimSpace(newToken)==""{http.Error(w,"agent token environment variable is empty",400);return}
	grace:=time.Duration(in.GraceSeconds)*time.Second
	details:=map[string]any{"agent_token_env":envName,"grace_seconds":in.GraceSeconds}
	now:=s.now()
	n,err:=s.store.RotateAgentToken(strings.TrimSpace(in.NodeID),newToken,now,grace,AuditEntry{Timestamp:now.UTC(),Actor:"admin",RemoteIP:s.clientIP(r),Details:withRequest(r,details)})
	if err!=nil{s.auditFailure(w,r,"node.token.rotate",in.NodeID,details,err,http.StatusBadRequest);return}
	if err:=s.FlushSecurityAuditIntents();err!=nil{
		w.Header().Set("X-BAFT-Audit-State","pending")
	}else{
		_ = s.SendAuditAnchor(r.Context())
	}
	writeJSON(w,http.StatusOK,n)
}
