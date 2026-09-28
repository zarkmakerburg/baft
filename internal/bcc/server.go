package bcc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	store *Store
	adminToken string
	probeTimeout time.Duration
}

func NewServer(store *Store,adminToken string) (*Server,error) {
	if store==nil{return nil,errors.New("store is required")}
	if strings.TrimSpace(adminToken)==""{return nil,errors.New("admin token is required")}
	return &Server{store:store,adminToken:adminToken,probeTimeout:1500*time.Millisecond},nil
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
		c,err:=d.DialContext(ctx,"tcp",n.Address)
		health:="down"
		if err==nil{health="up";_ = c.Close()}
		_ = s.store.SetHealth(n.ID,health,time.Now())
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
	var in struct{
		NodeID string `json:"node_id"`
		IngressBytes uint64 `json:"ingress_bytes"`
		EgressBytes uint64 `json:"egress_bytes"`
	}
	if err:=decodeJSON(r,&in);err!=nil{http.Error(w,err.Error(),400);return}
	f,err:=s.store.AddTraffic(in.NodeID,bearer(r),in.IngressBytes,in.EgressBytes)
	if err!=nil{http.Error(w,err.Error(),http.StatusUnauthorized);return}
	writeJSON(w,http.StatusAccepted,f)
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
