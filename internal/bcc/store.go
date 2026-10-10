package bcc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

const (
	// jobEnrollPeerLegacy was retired by the tunnel builder; old queued jobs of
	// this type are failed instead of served.
	jobEnrollPeerLegacy = "enroll_peer"
	JobDeployBAFT  = "deploy_baft"
)

var ErrAgentAuthentication = errors.New("agent authentication failed")

type Node struct {
	ID             string    `json:"id"`
	Alias          string    `json:"alias"`
	Address        string    `json:"address"`
	PathIPv4       string    `json:"path_ipv4,omitempty"`
	PathIPv6       string    `json:"path_ipv6,omitempty"`
	Role           string    `json:"role"`
	PublicKey      string    `json:"public_key,omitempty"`
	AgentTokenHash           string    `json:"agent_token_hash,omitempty"`
	PreviousAgentTokenHash   string    `json:"previous_agent_token_hash,omitempty"`
	PreviousAgentTokenUntil  time.Time `json:"previous_agent_token_until,omitempty"`
	Revoked                  bool      `json:"revoked,omitempty"`
	RevokedAt                time.Time `json:"revoked_at,omitempty"`
	RevokeReason             string    `json:"revoke_reason,omitempty"`
	// AgentSeen is the last time the node's agent authenticated to BCC.
	// AppliedGeneration is the legacy singleton generation. Multi-instance
	// tunnels keep independent generation baselines in AppliedGenerations.
	AgentSeen                time.Time      `json:"agent_seen,omitempty"`
	AppliedGeneration        int            `json:"applied_generation,omitempty"`
	AppliedGenerations       map[string]int `json:"applied_generations,omitempty"`
	Health                   string         `json:"health"`
	LastChecked    time.Time `json:"last_checked,omitempty"`
	LatencyMS      int64     `json:"latency_ms"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Job struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	NodeID    string    `json:"node_id"`
	WorkerID  string    `json:"worker_id,omitempty"`
	PublicKey string    `json:"public_key,omitempty"`
	Version   string    `json:"version,omitempty"`
	// Params carries a tunnel job's parameters; Output is a secret the agent
	// returned (a pairing code) until BCC hands it to the next step.
	Params    map[string]string `json:"params,omitempty"`
	Output    string    `json:"output,omitempty"`
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type FinancePolicy struct {
	NodeID              string    `json:"node_id"`
	CostMicrosPerGiB    int64     `json:"cost_micros_per_gib"`
	RevenueMicrosPerGiB int64     `json:"revenue_micros_per_gib"`
	Currency            string    `json:"currency"`
	EffectiveFrom       time.Time `json:"effective_from"`
	Version             uint64    `json:"version"`
}

type FinanceLedgerEntry struct {
	NodeID         string    `json:"node_id"`
	Timestamp      time.Time `json:"timestamp"`
	IngressBytes   uint64    `json:"ingress_bytes"`
	EgressBytes    uint64    `json:"egress_bytes"`
	CostMicros     int64     `json:"cost_micros"`
	RevenueMicros  int64     `json:"revenue_micros"`
	ProfitMicros   int64     `json:"profit_micros"`
	Currency       string    `json:"currency"`
	RateVersion    uint64    `json:"rate_version"`
	RateEffective  time.Time `json:"rate_effective_from"`
}

// financeRemainder is money not yet large enough to book, in 1/GiB micros.
type financeRemainder struct {
	Cost    uint64 `json:"cost"`
	Revenue uint64 `json:"revenue"`
}

type NodeFinance struct {
	NodeID        string    `json:"node_id"`
	IngressBytes  uint64    `json:"ingress_bytes"`
	EgressBytes   uint64    `json:"egress_bytes"`
	CostMicros    int64     `json:"cost_micros"`
	RevenueMicros int64     `json:"revenue_micros"`
	ProfitMicros  int64     `json:"profit_micros"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type TelemetryCursor struct {
	NodeID          string    `json:"node_id"`
	BootID          string    `json:"boot_id"`
	Sequence        uint64    `json:"sequence"`
	IngestID        uint64    `json:"ingest_id"`
	IngressBytes    uint64    `json:"ingress_bytes"`
	EgressBytes     uint64    `json:"egress_bytes"`
	ActiveSessions  uint64    `json:"active_sessions"`
	HandshakeErrors             uint64                    `json:"handshake_errors"`
	NoiseLatencyMS              int64                     `json:"noise_latency_ms"`
	HandshakeErrorRateMilliMin  int64                     `json:"handshake_error_rate_milli_per_min"`
	Routes                      []telemetry.RouteSnapshot `json:"routes,omitempty"`
	LastTelemetry               time.Time                 `json:"last_telemetry"`
}

type HistoryPoint struct {
	Timestamp                   time.Time                 `json:"timestamp"`
	IngressBytes                uint64                    `json:"ingress_bytes"`
	EgressBytes                 uint64                    `json:"egress_bytes"`
	ActiveSessions              uint64                    `json:"active_sessions"`
	NoiseLatencyMS              int64                     `json:"noise_latency_ms"`
	HandshakeErrorRateMilliMin  int64                     `json:"handshake_error_rate_milli_per_min"`
	NodeHealth                  string                    `json:"node_health"`
	LatencyMS                   int64                     `json:"latency_ms"`
	Routes                      []telemetry.RouteSnapshot `json:"routes,omitempty"`
}

type MonitoringNode struct {
	NodeID                     string                    `json:"node_id"`
	Alias                      string                    `json:"alias"`
	Address                    string                    `json:"address"`
	Role                       string                    `json:"role"`
	Status                     string                    `json:"status"`
	HealthCheckStatus          string                    `json:"health_check_status"`
	LatencyMS                  int64                     `json:"latency_ms"`
	LastSeen                   time.Time                 `json:"last_seen,omitempty"`
	ActiveSessions             uint64                    `json:"active_sessions"`
	HandshakeErrors            uint64                    `json:"handshake_errors"`
	NoiseLatencyMS             int64                     `json:"noise_latency_ms"`
	HandshakeErrorRateMilliMin int64                     `json:"handshake_error_rate_milli_per_min"`
	Routes                     []telemetry.RouteSnapshot `json:"routes,omitempty"`
}

type state struct {
	Nodes    map[string]Node          `json:"nodes"`
	Jobs     map[string]Job           `json:"jobs"`
	Finance   map[string]NodeFinance      `json:"finance,omitempty"`
	Policies        map[string]FinancePolicy     `json:"finance_policies,omitempty"`
	RateHistory     map[string][]FinancePolicy   `json:"finance_rate_history,omitempty"`
	FinanceLedger   []FinanceLedgerEntry         `json:"finance_ledger,omitempty"`
	// FinanceRemainders carries each node's sub-micro money between traffic
	// reports; state written before this field existed starts from zero.
	FinanceRemainders map[string]financeRemainder `json:"finance_remainders,omitempty"`
	Telemetry       map[string]TelemetryCursor   `json:"telemetry,omitempty"`
	History         map[string][]HistoryPoint    `json:"history,omitempty"`
	ActiveAlerts    map[string]Alert             `json:"active_alerts,omitempty"`
	RetiredBootIDs  map[string]map[string]bool   `json:"retired_boot_ids,omitempty"`
	Tunnels         map[string]Tunnel            `json:"tunnels,omitempty"`
	// Health is the layered health of each node (state machines + history).
	Health          map[string]NodeHealthRecord  `json:"health,omitempty"`
	// Discovery is the last report-only inventory of each node.
	Discovery       map[string]NodeDiscovery     `json:"discovery,omitempty"`
	// CertRotations are the certificate rotations of built tunnels (A4).
	CertRotations   map[string]CertRotation      `json:"cert_rotations,omitempty"`
	// SecurityAuditIntents are audit entries committed in the same SQLite
	// transaction as security-sensitive state changes and drained afterward.
	SecurityAuditIntents map[string]AuditEntry   `json:"security_audit_intents,omitempty"`
	// Desired topology (M-014): IR pool members, explicit EX routes, and stable edge bindings.
	IRPool           map[string]IRPoolMember       `json:"ir_pool,omitempty"`
	EXRoutes         map[string]ExplicitEXRoute    `json:"ex_routes,omitempty"`
	TopologyBindings map[string]TopologyBinding    `json:"topology_bindings,omitempty"`
	// IngressSelections are M-015's durable ACTIVE/STANDBY decisions per explicit EX route.
	IngressSelections map[string]IngressSelection `json:"ingress_selections,omitempty"`
	// IngressDistributions are M-016's durable new-connection weight vectors.
	IngressDistributions map[string]IngressDistribution `json:"ingress_distributions,omitempty"`
	// SmartIngressPlans are M-017's durable provider-independent publication intents.
	SmartIngressPlans map[string]SmartIngressPlan `json:"smart_ingress_plans,omitempty"`
	PathProbes       map[string]PathProbe         `json:"path_probes,omitempty"`
	PathDiscoveries  map[string]PathDiscovery     `json:"path_discoveries,omitempty"`
	RouteDoctorRuns map[string]RouteDoctorRun    `json:"route_doctor_runs,omitempty"`
	SSHMigrations map[string]SSHMigration `json:"ssh_migrations,omitempty"`
	ChangeLedger []ChangeRecord `json:"change_ledger,omitempty"`
	NextChangeSequence uint64 `json:"next_change_sequence,omitempty"`
	NextJob         uint64                       `json:"next_job"`
	NextRateVersion       uint64                 `json:"next_rate_version,omitempty"`
	NextTelemetryIngestID uint64                 `json:"next_telemetry_ingest_id,omitempty"`
}

type Store struct {
	mu          sync.Mutex
	reconcileMu sync.Mutex
	path        string
	st          state
	// Set when a commit's outcome cannot be established from durable state.
	// Keep the process from overwriting a possibly committed transaction.
	persistenceUncertain bool
	// DriftEvery is how often active tunnels are checked for drift; zero
	// disables the automatic check (a check can still be requested).
	DriftEvery time.Duration
}

func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path)=="" { return nil, errors.New("state path is required") }
	if err:=recoverRestoreTransaction(path);err!=nil{return nil,fmt.Errorf("recover interrupted restore: %w",err)}
	s:=&Store{path:path,st:state{Nodes:map[string]Node{},Jobs:map[string]Job{},Finance:map[string]NodeFinance{},Policies:map[string]FinancePolicy{},RateHistory:map[string][]FinancePolicy{},Telemetry:map[string]TelemetryCursor{},History:map[string][]HistoryPoint{},ActiveAlerts:map[string]Alert{},RetiredBootIDs:map[string]map[string]bool{},SecurityAuditIntents:map[string]AuditEntry{},IngressSelections:map[string]IngressSelection{},IngressDistributions:map[string]IngressDistribution{},SmartIngressPlans:map[string]SmartIngressPlan{},PathProbes:map[string]PathProbe{},PathDiscoveries:map[string]PathDiscovery{},RouteDoctorRuns:map[string]RouteDoctorRun{},SSHMigrations:map[string]SSHMigration{},NextJob:1,NextRateVersion:1,NextTelemetryIngestID:1}}
	b,err:=os.ReadFile(path)
	switch {
	case err==nil&&isSQLiteFile(b):
		st,err:=readStateDB(path)
		if err!=nil{return nil,fmt.Errorf("open BCC state database: %w",err)}
		s.st=st
	case err==nil&&len(bytes.TrimSpace(b))>0:
		// A JSON state file from before the SQLite store: load it, then
		// convert it in place (the original is kept as <path>.json.bak).
		if err:=json.Unmarshal(b,&s.st);err!=nil{return nil,fmt.Errorf("decode BCC state: %w",err)}
		upgradeLoadedState(&s.st)
		if err:=migrateJSONState(path,b,s.st);err!=nil{return nil,fmt.Errorf("migrate BCC state to SQLite: %w",err)}
	case err==nil:
		// empty file: fresh state
	case !errors.Is(err,os.ErrNotExist):
		return nil,err
	}
	return s,nil
}

// upgradeLoadedState fills defaults and backfills rate history for state
// written by older builds.
func upgradeLoadedState(st *state){
	normalizeState(st)
	for id,p:=range st.Policies{
		if len(st.RateHistory[id])!=0{continue}
		if p.Currency==""{p.Currency="IRR"}
		if p.EffectiveFrom.IsZero(){p.EffectiveFrom=time.Unix(0,0).UTC()}
		if p.Version==0{p.Version=st.NextRateVersion;st.NextRateVersion++}
		st.RateHistory[id]=[]FinancePolicy{p}
	}
}

func tokenHash(v string) string {
	sum:=sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func (s *Store) saveLocked() error {
	if s.persistenceUncertain {
		return errStateCommitUncertain
	}
	return s.recordStateWrite(writeStateDB(s.path, s.st))
}

func (s *Store) recordStateWrite(err error) error {
	if errors.Is(err, errStateCommitUncertain) {
		s.persistenceUncertain = true
	}
	return err
}

// PersistenceUncertain reports a failed commit whose durable outcome could not
// be read back. Callers must not serve the in-memory snapshot as authoritative.
func (s *Store) PersistenceUncertain() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistenceUncertain
}

func (s *Store) UpsertNode(n Node, agentToken string, audits ...AuditEntry) (Node,error) {
	if len(audits)>1{return Node{},errors.New("at most one audit intent is allowed")}
	if strings.TrimSpace(n.ID)==""||len(n.ID)>128{return Node{},errors.New("node id is required")}
	if strings.TrimSpace(n.Alias)==""{n.Alias=n.ID}
	if strings.TrimSpace(n.Address)==""{return Node{},errors.New("node address is required")}
	if n.PathIPv4!=""{ip:=net.ParseIP(strings.TrimSpace(n.PathIPv4));if ip==nil||ip.To4()==nil{return Node{},errors.New("path_ipv4 must be a literal IPv4 address")};n.PathIPv4=ip.To4().String()}
	if n.PathIPv6!=""{ip:=net.ParseIP(strings.TrimSpace(n.PathIPv6));if ip==nil||ip.To4()!=nil||ip.To16()==nil{return Node{},errors.New("path_ipv6 must be a literal IPv6 address")};n.PathIPv6=ip.String()}
	switch n.Role {case "foreign","worker","master":default:return Node{},errors.New("node role must be foreign, worker, or master")}
	s.mu.Lock();defer s.mu.Unlock()
	var before state
	if len(audits)==1{
		var err error
		before,err=cloneState(s.st)
		if err!=nil{return Node{},err}
	}
	old,exists:=s.st.Nodes[n.ID]
	if agentToken!="" { n.AgentTokenHash=tokenHash(agentToken) } else if exists { n.AgentTokenHash=old.AgentTokenHash }
	if exists {
		n.PreviousAgentTokenHash=old.PreviousAgentTokenHash
		n.PreviousAgentTokenUntil=old.PreviousAgentTokenUntil
		n.Revoked=old.Revoked
		n.RevokedAt=old.RevokedAt
		n.RevokeReason=old.RevokeReason
		if n.AgentSeen.IsZero(){n.AgentSeen=old.AgentSeen}
		if n.PathIPv4==""{n.PathIPv4=old.PathIPv4}
		if n.PathIPv6==""{n.PathIPv6=old.PathIPv6}
		n.AppliedGeneration=old.AppliedGeneration
		if len(old.AppliedGenerations)>0{
			n.AppliedGenerations=make(map[string]int,len(old.AppliedGenerations))
			for k,v:=range old.AppliedGenerations{n.AppliedGenerations[k]=v}
		}
	}
	if n.Health=="" { if exists { n.Health=old.Health } else { n.Health="unknown" } }
	if n.LastChecked.IsZero()&&exists{n.LastChecked=old.LastChecked;n.LatencyMS=old.LatencyMS}
	if n.LastChecked.IsZero()&&!exists{n.LatencyMS=-1}
	n.UpdatedAt=time.Now().UTC()
	s.st.Nodes[n.ID]=n
	if len(audits)==1{
		a:=audits[0]
		a.Timestamp,a.Action,a.Target,a.Outcome=n.UpdatedAt,"node.upsert",n.ID,"success"
		if a.Actor==""{a.Actor="admin"}
		if _,err:=s.enqueueSecurityAuditLocked(a);err!=nil{s.st=before;return Node{},err}
	}
	if err:=s.saveLocked();err!=nil{
		if len(audits)==1{s.st=before}else if exists{s.st.Nodes[n.ID]=old}else{delete(s.st.Nodes,n.ID)}
		return Node{},err
	}
	return publicNode(n),nil
}

func publicNode(n Node) Node {
	n.AgentTokenHash=""
	n.PreviousAgentTokenHash=""
	return n
}

// GetNode returns a node record (without its token hashes).
func (s *Store) GetNode(id string) (Node, bool) {
	s.mu.Lock();defer s.mu.Unlock()
	n,ok:=s.st.Nodes[id]
	return publicNode(n),ok
}

func (s *Store) ListNodes() []Node {
	s.mu.Lock();defer s.mu.Unlock()
	out:=make([]Node,0,len(s.st.Nodes))
	for _,n:=range s.st.Nodes{out=append(out,publicNode(n))}
	sort.Slice(out,func(i,j int)bool{return out[i].Alias<out[j].Alias})
	return out
}

// Path is where the state database lives.
func (s *Store) Path() string { return s.path }

func (s *Store) ListJobs() []Job {
	s.mu.Lock();defer s.mu.Unlock()
	out:=make([]Job,0,len(s.st.Jobs))
	for _,j:=range s.st.Jobs{out=append(out,j)}
	sort.Slice(out,func(i,j int)bool{return out[i].CreatedAt.After(out[j].CreatedAt)})
	return out
}

func (s *Store) newJobLocked(j Job) Job {
	j.ID=fmt.Sprintf("job-%08d",s.st.NextJob);s.st.NextJob++
	j.Status="queued";j.CreatedAt=time.Now().UTC();j.UpdatedAt=j.CreatedAt
	s.st.Jobs[j.ID]=j
	return j
}

// Deploys name a signed release tag, the only thing an agent will install.
var deployVersionRe=regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

func validVersion(v string) bool {
	return len(v)<=64&&deployVersionRe.MatchString(v)
}

func (s *Store) CreateDeployJobs(nodeIDs []string,version string,audits ...AuditEntry) ([]Job,error) {
	if !validVersion(version){return nil,errors.New("invalid BAFT version")}
	if len(nodeIDs)==0{return nil,errors.New("at least one node is required")}
	if len(audits)>1{return nil,errors.New("at most one audit intent is allowed")}
	s.mu.Lock();defer s.mu.Unlock()
	var before state
	if len(audits)==1{
		var err error
		before,err=cloneState(s.st)
		if err!=nil{return nil,err}
	}
	seen:=map[string]struct{}{};ids:=make([]string,0,len(nodeIDs))
	for _,id:=range nodeIDs{
		if _,dup:=seen[id];dup{continue};seen[id]=struct{}{}
		if _,ok:=s.st.Nodes[id];!ok{return nil,fmt.Errorf("unknown node %s",id)}
		ids=append(ids,id)
	}
	startJob:=s.st.NextJob
	out:=make([]Job,0,len(ids))
	for _,id:=range ids{out=append(out,s.newJobLocked(Job{Type:JobDeployBAFT,NodeID:id,Version:version}))}
	if len(audits)==1{
		a:=audits[0]
		a.Action,a.Target,a.Outcome="deploy.create","cluster","success"
		if a.Actor==""{a.Actor="admin"}
		jobIDs:=make([]string,0,len(out));for _,job:=range out{jobIDs=append(jobIDs,job.ID)}
		a.Details=mergeAuditDetails(a.Details,map[string]any{"job_ids":jobIDs})
		if _,err:=s.enqueueSecurityAuditLocked(a);err!=nil{s.st=before;return nil,err}
	}
	if err:=s.saveLocked();err!=nil{
		if len(audits)==1{s.st=before}else{
			for _,job:=range out{delete(s.st.Jobs,job.ID)}
			s.st.NextJob=startJob
		}
		return nil,err
	}
	return out,nil
}

func (s *Store) authorizedHashLocked(nodeID,token string,now time.Time)(string,bool) {
	n,ok:=s.st.Nodes[nodeID]
	if !ok||n.Revoked||token==""{return "",false}
	h:=tokenHash(token)
	if n.AgentTokenHash!=""&&n.AgentTokenHash==h{return h,true}
	if n.PreviousAgentTokenHash!=""&&n.PreviousAgentTokenHash==h&&now.UTC().Before(n.PreviousAgentTokenUntil){return h,true}
	return "",false
}

func (s *Store) authorizedLocked(nodeID,token string) bool {
	_,ok:=s.authorizedHashLocked(nodeID,token,time.Now().UTC())
	return ok
}

func (s *Store) RotateAgentToken(nodeID,newToken string,now time.Time,grace time.Duration,audits ...AuditEntry)(Node,error){
	if strings.TrimSpace(newToken)==""{return Node{},errors.New("new agent token is required")}
	if grace<0||grace>15*time.Minute{return Node{},errors.New("token rotation grace must be between 0 and 15 minutes")}
	if len(audits)>1{return Node{},errors.New("at most one security audit intent is allowed")}
	now=now.UTC()
	s.mu.Lock();defer s.mu.Unlock()
	n,ok:=s.st.Nodes[nodeID];if !ok{return Node{},errors.New("node not found")}
	if n.Revoked{return Node{},errors.New("node is revoked")}
	before,err:=cloneState(s.st);if err!=nil{return Node{},err}
	oldHash:=n.AgentTokenHash
	n.AgentTokenHash=tokenHash(newToken)
	if grace>0&&oldHash!=""{
		n.PreviousAgentTokenHash=oldHash
		n.PreviousAgentTokenUntil=now.Add(grace)
	}else{
		n.PreviousAgentTokenHash=""
		n.PreviousAgentTokenUntil=time.Time{}
	}
	n.UpdatedAt=now
	s.st.Nodes[nodeID]=n
	if len(audits)==1{
		a:=audits[0]
		a.Timestamp,a.Action,a.Target,a.Outcome=now,"node.token.rotate",nodeID,"success"
		if a.Actor==""{a.Actor="admin"}
		if _,err:=s.enqueueSecurityAuditLocked(a);err!=nil{s.st=before;return Node{},err}
	}
	if err:=s.saveLocked();err!=nil{s.st=before;return Node{},err}
	return publicNode(n),nil
}

func (s *Store) RevokeNode(nodeID,reason string,now time.Time,audits ...AuditEntry)(Node,error){
	if len(audits)>1{return Node{},errors.New("at most one security audit intent is allowed")}
	now=now.UTC()
	s.mu.Lock();defer s.mu.Unlock()
	n,ok:=s.st.Nodes[nodeID];if !ok{return Node{},errors.New("node not found")}
	before,err:=cloneState(s.st);if err!=nil{return Node{},err}
	n.Revoked=true
	n.RevokedAt=now
	n.RevokeReason=strings.TrimSpace(reason)
	n.PreviousAgentTokenHash=""
	n.PreviousAgentTokenUntil=time.Time{}
	n.Health="down"
	n.UpdatedAt=now
	s.st.Nodes[nodeID]=n
	if len(audits)==1{
		a:=audits[0]
		a.Timestamp,a.Action,a.Target,a.Outcome=now,"node.revoke",nodeID,"success"
		if a.Actor==""{a.Actor="admin"}
		if _,err:=s.enqueueSecurityAuditLocked(a);err!=nil{s.st=before;return Node{},err}
	}
	if err:=s.saveLocked();err!=nil{s.st=before;return Node{},err}
	return publicNode(n),nil
}

// noteAgentSeenLocked records agent contact in memory; it reaches disk with the
// next save, or sooner when the stored value is already stale, so a polling
// agent does not rewrite the state on every request.
func (s *Store) noteAgentSeenLocked(nodeID string,now time.Time){
	n,ok:=s.st.Nodes[nodeID];if !ok{return}
	stale:=now.Sub(n.AgentSeen)>5*time.Minute
	n.AgentSeen=now;s.st.Nodes[nodeID]=n
	if stale{_ = s.saveLocked()}
}

func (s *Store) PullJobs(nodeID,token string) ([]Job,error) {
	s.mu.Lock();defer s.mu.Unlock()
	if !s.authorizedLocked(nodeID,token){return nil,ErrAgentAuthentication}
	before,err:=cloneState(s.st);if err!=nil{return nil,err}
	s.noteAgentSeenLocked(nodeID,time.Now().UTC())
	var out []Job
	changed:=false
	for id,j:=range s.st.Jobs{
		if j.NodeID!=nodeID||j.Status!="queued"{continue}
		changed=true
		if j.Type==jobEnrollPeerLegacy{j.Status="failed";j.Message="retired job type: build tunnels with /api/tunnels";j.UpdatedAt=time.Now().UTC();s.st.Jobs[id]=j;continue}
		j.Status="dispatched";j.UpdatedAt=time.Now().UTC();s.st.Jobs[id]=j;out=append(out,j)
	}
	sort.Slice(out,func(i,j int)bool{return out[i].CreatedAt.Before(out[j].CreatedAt)})
	if changed{if err:=s.saveLocked();err!=nil{s.st=before;return nil,err}}
	return out,nil
}

func (s *Store) AckJob(nodeID,token,jobID,status,message string) error {
	return s.AckJobOutput(nodeID,token,jobID,status,message,"")
}

// maxJobOutput bounds the secret output an agent can attach to an ack.
const maxJobOutput=16<<10

// AckJobOutput is AckJob plus the output a tunnel job returns.
func (s *Store) AckJobOutput(nodeID,token,jobID,status,message,output string) error {
	if status!="succeeded"&&status!="failed"{return errors.New("invalid job status")}
	if len(output)>maxJobOutput{return errors.New("job output too large")}
	s.mu.Lock();defer s.mu.Unlock()
	if !s.authorizedLocked(nodeID,token){return ErrAgentAuthentication}
	j,ok:=s.st.Jobs[jobID];if !ok||j.NodeID!=nodeID{return errors.New("job not found")}
	if j.Status!="dispatched"&&j.Status!="queued"{return errors.New("job already completed")}
	previous:=j
	j.Status=status;j.Message=message;j.UpdatedAt=time.Now().UTC()
	if status=="succeeded"{j.Output=output}
	wipeSecretParams(&j)
	s.st.Jobs[jobID]=j
	if err:=s.saveLocked();err!=nil{s.st.Jobs[jobID]=previous;return err}
	return nil
}

func (s *Store) SetHealth(nodeID,health string,latencyMS int64,checked time.Time) error {
	if health!="up"&&health!="down"&&health!="unknown"{return errors.New("invalid health")}
	s.mu.Lock();defer s.mu.Unlock()
	n,ok:=s.st.Nodes[nodeID];if !ok{return errors.New("node not found")}
	n.Health=health;n.LatencyMS=latencyMS;n.LastChecked=checked.UTC();s.st.Nodes[nodeID]=n
	return s.saveLocked()
}


func validCurrency(v string) bool {
	if v==""{return false}
	if len(v)<3||len(v)>8{return false}
	for _,r:=range v{
		if !(r>='A'&&r<='Z'){return false}
	}
	return true
}

func (s *Store) SetFinancePolicy(nodeID string,costMicrosPerGiB,revenueMicrosPerGiB int64) error {
	// Legacy/test helper: effective from Unix epoch so historical fixture
	// timestamps remain deterministic. Production API uses SetFinancePolicyAt.
	return s.SetFinancePolicyAt(nodeID,costMicrosPerGiB,revenueMicrosPerGiB,"IRR",time.Unix(0,0).UTC())
}

func (s *Store) SetFinancePolicyAt(nodeID string,costMicrosPerGiB,revenueMicrosPerGiB int64,currency string,effectiveFrom time.Time,audits ...AuditEntry) error {
	if len(audits)>1{return errors.New("at most one audit intent is allowed")}
	if costMicrosPerGiB<0||revenueMicrosPerGiB<0{return errors.New("finance rates must be non-negative")}
	if costMicrosPerGiB>1_000_000_000||revenueMicrosPerGiB>1_000_000_000{return errors.New("finance rates are unreasonably large")}
	currency=strings.ToUpper(strings.TrimSpace(currency))
	if currency==""{currency="IRR"}
	if !validCurrency(currency){return errors.New("invalid currency code")}
	if effectiveFrom.IsZero(){effectiveFrom=time.Now().UTC()}
	effectiveFrom=effectiveFrom.UTC()

	s.mu.Lock();defer s.mu.Unlock()
	if _,ok:=s.st.Nodes[nodeID];!ok{return errors.New("node not found")}
	var before state
	if len(audits)==1{
		var err error
		before,err=cloneState(s.st)
		if err!=nil{return err}
	}
	p:=FinancePolicy{
		NodeID:nodeID,CostMicrosPerGiB:costMicrosPerGiB,RevenueMicrosPerGiB:revenueMicrosPerGiB,
		Currency:currency,EffectiveFrom:effectiveFrom,Version:s.st.NextRateVersion,
	}
	previousVersion:=s.st.NextRateVersion
	previousPolicy,hadPolicy:=s.st.Policies[nodeID]
	previousHistory,hadHistory:=s.st.RateHistory[nodeID]
	s.st.NextRateVersion++
	// Sorting an appended slice can otherwise overwrite the old backing array.
	h:=append(append([]FinancePolicy(nil),previousHistory...),p)
	sort.SliceStable(h,func(i,j int)bool{
		if h[i].EffectiveFrom.Equal(h[j].EffectiveFrom){return h[i].Version<h[j].Version}
		return h[i].EffectiveFrom.Before(h[j].EffectiveFrom)
	})
	s.st.RateHistory[nodeID]=h
	s.st.Policies[nodeID]=p
	if len(audits)==1{
		a:=audits[0]
		a.Action,a.Target,a.Outcome="finance.rate.change",nodeID,"success"
		if a.Actor==""{a.Actor="admin"}
		a.Details=mergeAuditDetails(a.Details,map[string]any{"rate_version":p.Version})
		if _,err:=s.enqueueSecurityAuditLocked(a);err!=nil{s.st=before;return err}
	}
	if err:=s.saveLocked();err!=nil{
		if len(audits)==1{s.st=before}else{
			s.st.NextRateVersion=previousVersion
			if hadPolicy{s.st.Policies[nodeID]=previousPolicy}else{delete(s.st.Policies,nodeID)}
			if hadHistory{s.st.RateHistory[nodeID]=previousHistory}else{delete(s.st.RateHistory,nodeID)}
		}
		return err
	}
	return nil
}

// moneyForBytes returns the whole micros owed for bytes at rate, plus the new
// sub-micro remainder. carry and the returned remainder are in units of
// 1/GiB micro. Threading the remainder through successive reports makes their
// sum equal the money for the summed bytes, wherever the reports split them.
// Rates are capped at 1e9 by SetFinancePolicyAt, so rem*rate+carry fits.
func moneyForBytes(bytes uint64,rate int64,carry uint64) (int64,uint64) {
	const gib uint64 = 1 << 30
	carry%=gib
	if rate<=0||bytes==0{return 0,carry}
	whole:=bytes/gib
	rem:=bytes%gib
	if whole>uint64(math.MaxInt64/rate){return math.MaxInt64,carry}
	base:=int64(whole)*rate
	num:=rem*uint64(rate)+carry
	fraction:=int64(num/gib)
	if base>math.MaxInt64-fraction{return math.MaxInt64,carry}
	return base+fraction,num%gib
}

func satAdd(a,b int64) int64 {
	if b>0&&a>math.MaxInt64-b{return math.MaxInt64}
	if b<0&&a<math.MinInt64-b{return math.MinInt64}
	return a+b
}

func (s *Store) rateAtLocked(nodeID string,at time.Time) (FinancePolicy,bool) {
	h:=s.st.RateHistory[nodeID]
	var chosen FinancePolicy
	ok:=false
	for _,p:=range h{
		if p.EffectiveFrom.After(at){break}
		chosen=p;ok=true
	}
	if len(h)>0{
		if ok{return chosen,true}
		return FinancePolicy{NodeID:nodeID,Currency:"IRR"},false
	}
	// Backward-compatibility only for persisted pre-versioning state.
	p,ok:=s.st.Policies[nodeID]
	if ok{
		if p.Currency==""{p.Currency="IRR"}
		return p,true
	}
	return FinancePolicy{NodeID:nodeID,Currency:"IRR"},false
}

func (s *Store) appendFinanceLocked(nodeID string,at time.Time,ingressBytes,egressBytes uint64) error {
	if ingressBytes==0&&egressBytes==0{return nil}
	if ^uint64(0)-ingressBytes<egressBytes{return errors.New("traffic total overflow")}
	total:=ingressBytes+egressBytes
	p,_:=s.rateAtLocked(nodeID,at)
	if p.Currency==""{p.Currency="IRR"}
	carried:=s.st.FinanceRemainders[nodeID]
	cost,costRemainder:=moneyForBytes(total,p.CostMicrosPerGiB,carried.Cost)
	revenue,revenueRemainder:=moneyForBytes(total,p.RevenueMicrosPerGiB,carried.Revenue)
	profit:=revenue-cost

	f:=s.st.Finance[nodeID]
	f.NodeID=nodeID
	if ^uint64(0)-f.IngressBytes<ingressBytes||^uint64(0)-f.EgressBytes<egressBytes{return errors.New("traffic counter overflow")}
	if s.st.FinanceRemainders==nil{s.st.FinanceRemainders=map[string]financeRemainder{}}
	s.st.FinanceRemainders[nodeID]=financeRemainder{Cost:costRemainder,Revenue:revenueRemainder}
	f.IngressBytes+=ingressBytes
	f.EgressBytes+=egressBytes
	f.CostMicros=satAdd(f.CostMicros,cost)
	f.RevenueMicros=satAdd(f.RevenueMicros,revenue)
	f.ProfitMicros=satAdd(f.ProfitMicros,profit)
	f.UpdatedAt=at.UTC()
	s.st.Finance[nodeID]=f
	s.st.FinanceLedger=append(s.st.FinanceLedger,FinanceLedgerEntry{
		NodeID:nodeID,Timestamp:at.UTC(),IngressBytes:ingressBytes,EgressBytes:egressBytes,
		CostMicros:cost,RevenueMicros:revenue,ProfitMicros:profit,Currency:p.Currency,
		RateVersion:p.Version,RateEffective:p.EffectiveFrom,
	})
	return nil
}

func (s *Store) AddTraffic(nodeID,token string,ingressBytes,egressBytes uint64) (NodeFinance,error) {
	s.mu.Lock();defer s.mu.Unlock()
	if !s.authorizedLocked(nodeID,token){return NodeFinance{},ErrAgentAuthentication}
	beforeFinance,hadFinance:=s.st.Finance[nodeID]
	beforeRemainder,hadRemainder:=s.st.FinanceRemainders[nodeID]
	hadRemainderMap:=s.st.FinanceRemainders!=nil
	beforeLedger:=s.st.FinanceLedger
	if err:=s.appendFinanceLocked(nodeID,time.Now().UTC(),ingressBytes,egressBytes);err!=nil{return NodeFinance{},err}
	if err:=s.saveLocked();err!=nil{
		if hadFinance{s.st.Finance[nodeID]=beforeFinance}else{delete(s.st.Finance,nodeID)}
		if hadRemainder{s.st.FinanceRemainders[nodeID]=beforeRemainder}else{delete(s.st.FinanceRemainders,nodeID)}
		if !hadRemainderMap{s.st.FinanceRemainders=nil}
		s.st.FinanceLedger=beforeLedger
		return NodeFinance{},err
	}
	return s.st.Finance[nodeID],nil
}

func (s *Store) FinanceSnapshot() []NodeFinance {
	s.mu.Lock();defer s.mu.Unlock()
	out:=make([]NodeFinance,0,len(s.st.Nodes))
	for id:=range s.st.Nodes{
		f:=s.st.Finance[id];f.NodeID=id
		out=append(out,f)
	}
	sort.Slice(out,func(i,j int)bool{return out[i].NodeID<out[j].NodeID})
	return out
}

func (s *Store) ApplyTelemetry(token,signature string,body []byte,report telemetry.Report) (NodeFinance,bool,error) {
	if report.NodeID==""||report.BootID==""||report.Sequence==0{return NodeFinance{},false,errors.New("invalid telemetry identity")}
	s.mu.Lock();defer s.mu.Unlock()
	_,ok:=s.st.Nodes[report.NodeID]
	if !ok{return NodeFinance{},false,ErrAgentAuthentication}
	authHash,authorized:=s.authorizedHashLocked(report.NodeID,token,time.Now().UTC())
	if !authorized{return NodeFinance{},false,ErrAgentAuthentication}
	if !telemetry.VerifyHashedToken(authHash,signature,body){return NodeFinance{},false,ErrAgentAuthentication}

	prev:=s.st.Telemetry[report.NodeID]
	retired:=s.st.RetiredBootIDs[report.NodeID]
	if retired!=nil&&retired[report.BootID]{
		f:=s.st.Finance[report.NodeID];f.NodeID=report.NodeID
		return f,true,nil
	}
	if prev.BootID==report.BootID && report.Sequence<=prev.Sequence {
		f:=s.st.Finance[report.NodeID];f.NodeID=report.NodeID
		return f,true,nil
	}
	// A failed database write must not advance the replay cursor or finance
	// ledger in RAM: a retried report must be applied exactly once.
	before,err:=cloneState(s.st);if err!=nil{return NodeFinance{},false,err}
	var din,dout uint64
	if prev.BootID==report.BootID && prev.BootID!="" {
		if report.IngressBytes<prev.IngressBytes||report.EgressBytes<prev.EgressBytes||report.HandshakeErrors<prev.HandshakeErrors{
			return NodeFinance{},false,errors.New("telemetry cumulative counters moved backwards")
		}
		din=report.IngressBytes-prev.IngressBytes
		dout=report.EgressBytes-prev.EgressBytes
	}else{
		if prev.BootID!=""&&prev.BootID!=report.BootID{
			if s.st.RetiredBootIDs[report.NodeID]==nil{s.st.RetiredBootIDs[report.NodeID]=map[string]bool{}}
			s.st.RetiredBootIDs[report.NodeID][prev.BootID]=true
		}
		din=report.IngressBytes
		dout=report.EgressBytes
	}

	ts:=time.Unix(report.TimestampUnix,0).UTC()
	ingestID:=s.st.NextTelemetryIngestID
	if ingestID==0{ingestID=1}
	if ingestID==^uint64(0){return NodeFinance{},false,errors.New("telemetry ingestion id exhausted")}
	s.st.NextTelemetryIngestID=ingestID+1
	if err:=s.appendFinanceLocked(report.NodeID,ts,din,dout);err!=nil{s.st=before;return NodeFinance{},false,err}
	f:=s.st.Finance[report.NodeID];f.NodeID=report.NodeID
	rateMilli:=int64(0)
	if prev.BootID==report.BootID && !prev.LastTelemetry.IsZero() && ts.After(prev.LastTelemetry) {
		deltaErrors:=report.HandshakeErrors-prev.HandshakeErrors
		deltaMillis:=ts.Sub(prev.LastTelemetry).Milliseconds()
		if deltaMillis>0 { rateMilli=int64(deltaErrors)*60_000_000/deltaMillis }
	}
	routes:=append([]telemetry.RouteSnapshot(nil),report.Routes...)
	s.st.Telemetry[report.NodeID]=TelemetryCursor{
		NodeID:report.NodeID,BootID:report.BootID,Sequence:report.Sequence,IngestID:ingestID,
		IngressBytes:report.IngressBytes,EgressBytes:report.EgressBytes,
		ActiveSessions:report.ActiveSessions,HandshakeErrors:report.HandshakeErrors,
		NoiseLatencyMS:report.NoiseLatencyMS,HandshakeErrorRateMilliMin:rateMilli,Routes:routes,LastTelemetry:ts,
	}
	nodeState:=s.st.Nodes[report.NodeID]
	point:=HistoryPoint{
		Timestamp:ts,IngressBytes:report.IngressBytes,EgressBytes:report.EgressBytes,
		ActiveSessions:report.ActiveSessions,NoiseLatencyMS:report.NoiseLatencyMS,HandshakeErrorRateMilliMin:rateMilli,
		NodeHealth:nodeState.Health,LatencyMS:nodeState.LatencyMS,Routes:routes,
	}
	h:=s.st.History[report.NodeID]
	cutoff:=time.Now().UTC().Add(-7*24*time.Hour)
	keep:=h[:0]
	for _,p:=range h { if !p.Timestamp.Before(cutoff) { keep=append(keep,p) } }
	keep=append(keep,point)
	if len(keep)>10080 { keep=keep[len(keep)-10080:] }
	s.st.History[report.NodeID]=keep
	if err:=s.saveLocked();err!=nil{s.st=before;return NodeFinance{},false,err}
	return f,false,nil
}

func (s *Store) TelemetrySnapshot(nodeID string) (TelemetryCursor,bool) {
	s.mu.Lock();defer s.mu.Unlock()
	v,ok:=s.st.Telemetry[nodeID]
	return v,ok
}


func (s *Store) MonitoringSnapshot(now time.Time,staleAfter time.Duration) []MonitoringNode {
	if staleAfter<=0{staleAfter=3*time.Minute}
	s.mu.Lock();defer s.mu.Unlock()
	out:=make([]MonitoringNode,0,len(s.st.Nodes))
	for id,n:=range s.st.Nodes{
		cur,hasTelemetry:=s.st.Telemetry[id]
		status:="unknown"
		if n.Health=="down"{status="down"}
		if hasTelemetry {
			if now.Sub(cur.LastTelemetry)>=staleAfter { status="down" } else if n.Health!="down" { status="up" }
		}
		routes:=append([]telemetry.RouteSnapshot(nil),cur.Routes...)
		if hasTelemetry&&now.Sub(cur.LastTelemetry)>=staleAfter {
			for i:=range routes { routes[i].Status="unknown" }
		}
		out=append(out,MonitoringNode{
			NodeID:id,Alias:n.Alias,Address:n.Address,Role:n.Role,Status:status,
			HealthCheckStatus:n.Health,LatencyMS:n.LatencyMS,LastSeen:cur.LastTelemetry,
			ActiveSessions:cur.ActiveSessions,HandshakeErrors:cur.HandshakeErrors,NoiseLatencyMS:cur.NoiseLatencyMS,
			HandshakeErrorRateMilliMin:cur.HandshakeErrorRateMilliMin,Routes:routes,
		})
	}
	sort.Slice(out,func(i,j int)bool{return out[i].Alias<out[j].Alias})
	return out
}

func (s *Store) History(nodeID string,now time.Time) []HistoryPoint {
	s.mu.Lock();defer s.mu.Unlock()
	cutoff:=now.UTC().Add(-7*24*time.Hour)
	src:=s.st.History[nodeID]
	out:=make([]HistoryPoint,0,len(src))
	for _,p:=range src{
		if p.Timestamp.Before(cutoff){continue}
		cp:=p;cp.Routes=append([]telemetry.RouteSnapshot(nil),p.Routes...)
		out=append(out,cp)
	}
	return out
}


func cloneAlerts(src map[string]Alert) map[string]Alert {
	out:=make(map[string]Alert,len(src))
	for k,v:=range src{out[k]=v}
	return out
}

func (s *Store) ActiveAlertsSnapshot() map[string]Alert {
	s.mu.Lock();defer s.mu.Unlock()
	return cloneAlerts(s.st.ActiveAlerts)
}

func (s *Store) SetActiveAlerts(alerts map[string]Alert) error {
	s.mu.Lock();defer s.mu.Unlock()
	s.st.ActiveAlerts=cloneAlerts(alerts)
	return s.saveLocked()
}


func (s *Store) RetiredBootIDs(nodeID string) []string {
	s.mu.Lock();defer s.mu.Unlock()
	m:=s.st.RetiredBootIDs[nodeID]
	out:=make([]string,0,len(m))
	for id:=range m{out=append(out,id)}
	sort.Strings(out)
	return out
}
