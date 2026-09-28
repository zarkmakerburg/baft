package bcc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
	NodeID    string    `json:"node_id"`
	RouteID   string    `json:"route_id,omitempty"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

type Server struct {
	store *Store
	adminToken string
	probeTimeout time.Duration
	alertConfig AlertConfig
	alertMu sync.Mutex
	activeAlerts map[string]bool
	httpClient *http.Client
}

func NewServer(store *Store,adminToken string) (*Server,error) {
	if store==nil{return nil,errors.New("store is required")}
	if strings.TrimSpace(adminToken)==""{return nil,errors.New("admin token is required")}
	return &Server{
		store:store,adminToken:adminToken,probeTimeout:1500*time.Millisecond,
		alertConfig:AlertConfig{TelemetryStaleAfter:3*time.Minute,HandshakeErrorRateMilliPerMin:5000,Interval:15*time.Second},
		activeAlerts:map[string]bool{},httpClient:&http.Client{Timeout:5*time.Second},
	},nil
}

func bearer(r *http.Request) string {
	const p="Bearer "
	v:=r.Header.Get("Authorization")
	if !strings.HasPrefix(v,p){return ""}
	return strings.TrimSpace(strings.TrimPrefix(v,p))
}

func (s *Server) admin(w http.ResponseWriter,r *http.Request) bool {
	if bearer(r)!=s.adminToken{http.Error(w,"unauthorized",http.StatusUnauthorized);return false}
	return true
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
	m.HandleFunc("/api/deploy",s.deploy)
	m.HandleFunc("/api/agent/jobs",s.agentJobs)
	m.HandleFunc("/api/agent/ack",s.agentAck)
	m.HandleFunc("/api/agent/traffic",s.agentTraffic)
	m.HandleFunc("/api/finance",s.finance)
	m.HandleFunc("/api/monitoring",s.monitoring)
	m.HandleFunc("/api/history",s.history)
	return m
}

func (s *Server) nodes(w http.ResponseWriter,r *http.Request){
	switch r.Method{
	case http.MethodGet:
		writeJSON(w,http.StatusOK,s.store.ListNodes())
	case http.MethodPost:
		if !s.admin(w,r){return}
		var in struct{ID,Alias,Address,Role,PublicKey,AgentToken string}
		if err:=decodeJSON(r,&in);err!=nil{http.Error(w,err.Error(),400);return}
		n,err:=s.store.UpsertNode(Node{ID:in.ID,Alias:in.Alias,Address:in.Address,Role:in.Role,PublicKey:in.PublicKey},in.AgentToken)
		if err!=nil{http.Error(w,err.Error(),400);return}
		writeJSON(w,http.StatusCreated,n)
	default:http.Error(w,"method not allowed",405)
	}
}

func (s *Server) jobs(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodGet{http.Error(w,"method not allowed",405);return}
	if !s.admin(w,r){return}
	writeJSON(w,http.StatusOK,s.store.ListJobs())
}

func (s *Server) enroll(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodPost{http.Error(w,"method not allowed",405);return}
	if !s.admin(w,r){return}
	var in struct{WorkerID string `json:"worker_id"`; PublicKey string `json:"public_key"`}
	if err:=decodeJSON(r,&in);err!=nil{http.Error(w,err.Error(),400);return}
	jobs,err:=s.store.CreateEnrollmentJobs(in.WorkerID,in.PublicKey)
	if err!=nil{http.Error(w,err.Error(),400);return}
	writeJSON(w,http.StatusAccepted,jobs)
}

func (s *Server) deploy(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodPost{http.Error(w,"method not allowed",405);return}
	if !s.admin(w,r){return}
	var in struct{NodeIDs []string `json:"node_ids"`; Version string `json:"version"`}
	if err:=decodeJSON(r,&in);err!=nil{http.Error(w,err.Error(),400);return}
	jobs,err:=s.store.CreateDeployJobs(in.NodeIDs,in.Version)
	if err!=nil{http.Error(w,err.Error(),400);return}
	writeJSON(w,http.StatusAccepted,jobs)
}

func (s *Server) agentJobs(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodGet{http.Error(w,"method not allowed",405);return}
	nodeID:=r.URL.Query().Get("node_id")
	jobs,err:=s.store.PullJobs(nodeID,bearer(r))
	if err!=nil{http.Error(w,err.Error(),http.StatusUnauthorized);return}
	writeJSON(w,http.StatusOK,jobs)
}

func (s *Server) agentAck(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodPost{http.Error(w,"method not allowed",405);return}
	var in struct{NodeID,JobID,Status,Message string}
	if err:=decodeJSON(r,&in);err!=nil{http.Error(w,err.Error(),400);return}
	if err:=s.store.AckJob(in.NodeID,bearer(r),in.JobID,in.Status,in.Message);err!=nil{http.Error(w,err.Error(),400);return}
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
	if err!=nil{http.Error(w,err.Error(),http.StatusUnauthorized);return}
	writeJSON(w,http.StatusAccepted,map[string]any{"finance":f,"duplicate":duplicate,"sequence":in.Sequence})
}

func (s *Server) finance(w http.ResponseWriter,r *http.Request){
	if !s.admin(w,r){return}
	switch r.Method{
	case http.MethodGet:
		writeJSON(w,http.StatusOK,s.store.FinanceSnapshot())
	case http.MethodPost:
		var in struct{
			NodeID string `json:"node_id"`
			CostMicrosPerGiB int64 `json:"cost_micros_per_gib"`
			RevenueMicrosPerGiB int64 `json:"revenue_micros_per_gib"`
		}
		if err:=decodeJSON(r,&in);err!=nil{http.Error(w,err.Error(),400);return}
		if err:=s.store.SetFinancePolicy(in.NodeID,in.CostMicrosPerGiB,in.RevenueMicrosPerGiB);err!=nil{http.Error(w,err.Error(),400);return}
		writeJSON(w,http.StatusOK,map[string]bool{"ok":true})
	default:http.Error(w,"method not allowed",405)
	}
}


func (s *Server) monitoring(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodGet{http.Error(w,"method not allowed",405);return}
	if !s.admin(w,r){return}
	writeJSON(w,http.StatusOK,s.store.MonitoringSnapshot(time.Now().UTC(),s.alertConfig.TelemetryStaleAfter))
}

func (s *Server) history(w http.ResponseWriter,r *http.Request){
	if r.Method!=http.MethodGet{http.Error(w,"method not allowed",405);return}
	if !s.admin(w,r){return}
	nodeID:=strings.TrimSpace(r.URL.Query().Get("node_id"))
	if nodeID==""{http.Error(w,"node_id is required",400);return}
	writeJSON(w,http.StatusOK,s.store.History(nodeID,time.Now().UTC()))
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
	now:=time.Now().UTC()
	view:=s.store.MonitoringSnapshot(now,s.alertConfig.TelemetryStaleAfter)
	current:=map[string]Alert{}
	for _,n:=range view{
		if !n.LastSeen.IsZero()&&now.Sub(n.LastSeen)>s.alertConfig.TelemetryStaleAfter{
			key:="telemetry_stale:"+n.NodeID
			current[key]=Alert{Type:"telemetry_stale",NodeID:n.NodeID,Message:"telemetry has exceeded stale threshold",Timestamp:now}
		}
		if n.HandshakeErrorRateMilliMin>=s.alertConfig.HandshakeErrorRateMilliPerMin{
			key:="handshake_error_rate:"+n.NodeID
			current[key]=Alert{Type:"handshake_error_rate",NodeID:n.NodeID,Message:"handshake error rate exceeded threshold",Timestamp:now}
		}
		for _,route:=range n.Routes{
			if route.Status=="down"{
				key:="route_down:"+n.NodeID+":"+route.RouteID
				current[key]=Alert{Type:"route_down",NodeID:n.NodeID,RouteID:route.RouteID,Message:"route reported down",Timestamp:now}
			}
		}
	}

	s.alertMu.Lock()
	defer s.alertMu.Unlock()
	for key:=range s.activeAlerts{
		if _,ok:=current[key];!ok{delete(s.activeAlerts,key)}
	}
	for key,alert:=range current{
		if s.activeAlerts[key]{continue}
		if s.alertConfig.WebhookURL!=""{
			if err:=s.sendWebhook(ctx,alert);err!=nil{return err}
		}
		s.activeAlerts[key]=true
	}
	return nil
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
