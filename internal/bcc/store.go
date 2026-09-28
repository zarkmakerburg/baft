package bcc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zarkmakerburg/baft/internal/telemetry"
)

const (
	JobEnrollPeer = "enroll_peer"
	JobDeployBAFT  = "deploy_baft"
)

type Node struct {
	ID             string    `json:"id"`
	Alias          string    `json:"alias"`
	Address        string    `json:"address"`
	Role           string    `json:"role"`
	PublicKey      string    `json:"public_key,omitempty"`
	AgentTokenHash string    `json:"agent_token_hash,omitempty"`
	Health         string    `json:"health"`
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
	Telemetry       map[string]TelemetryCursor   `json:"telemetry,omitempty"`
	History         map[string][]HistoryPoint    `json:"history,omitempty"`
	NextJob         uint64                       `json:"next_job"`
	NextRateVersion uint64                       `json:"next_rate_version,omitempty"`
}

type Store struct {
	mu   sync.Mutex
	path string
	st   state
}

func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path)=="" { return nil, errors.New("state path is required") }
	s:=&Store{path:path,st:state{Nodes:map[string]Node{},Jobs:map[string]Job{},Finance:map[string]NodeFinance{},Policies:map[string]FinancePolicy{},RateHistory:map[string][]FinancePolicy{},Telemetry:map[string]TelemetryCursor{},History:map[string][]HistoryPoint{},NextJob:1,NextRateVersion:1}}
	b,err:=os.ReadFile(path)
	if err==nil {
		if err:=json.Unmarshal(b,&s.st);err!=nil{return nil,fmt.Errorf("decode BCC state: %w",err)}
		if s.st.Nodes==nil{s.st.Nodes=map[string]Node{}}
		if s.st.Jobs==nil{s.st.Jobs=map[string]Job{}}
		if s.st.Finance==nil{s.st.Finance=map[string]NodeFinance{}}
		if s.st.Policies==nil{s.st.Policies=map[string]FinancePolicy{}}
		if s.st.RateHistory==nil{s.st.RateHistory=map[string][]FinancePolicy{}}
		if s.st.Telemetry==nil{s.st.Telemetry=map[string]TelemetryCursor{}}
		if s.st.History==nil{s.st.History=map[string][]HistoryPoint{}}
		if s.st.NextJob==0{s.st.NextJob=1}
		if s.st.NextRateVersion==0{s.st.NextRateVersion=1}
		for id,p:=range s.st.Policies{
			if len(s.st.RateHistory[id])!=0{continue}
			if p.Currency==""{p.Currency="IRR"}
			if p.EffectiveFrom.IsZero(){p.EffectiveFrom=time.Unix(0,0).UTC()}
			if p.Version==0{p.Version=s.st.NextRateVersion;s.st.NextRateVersion++}
			s.st.RateHistory[id]=[]FinancePolicy{p}
		}
	} else if !errors.Is(err,os.ErrNotExist) {
		return nil,err
	}
	return s,nil
}

func tokenHash(v string) string {
	sum:=sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func (s *Store) saveLocked() error {
	if err:=os.MkdirAll(filepath.Dir(s.path),0700);err!=nil{return err}
	b,err:=json.MarshalIndent(s.st,"","  ");if err!=nil{return err}
	tmp:=s.path+".tmp"
	if err:=os.WriteFile(tmp,b,0600);err!=nil{return err}
	return os.Rename(tmp,s.path)
}

func (s *Store) UpsertNode(n Node, agentToken string) (Node,error) {
	if strings.TrimSpace(n.ID)==""||len(n.ID)>128{return Node{},errors.New("node id is required")}
	if strings.TrimSpace(n.Alias)==""{n.Alias=n.ID}
	if strings.TrimSpace(n.Address)==""{return Node{},errors.New("node address is required")}
	switch n.Role {case "foreign","worker","master":default:return Node{},errors.New("node role must be foreign, worker, or master")}
	s.mu.Lock();defer s.mu.Unlock()
	old,exists:=s.st.Nodes[n.ID]
	if agentToken!="" { n.AgentTokenHash=tokenHash(agentToken) } else if exists { n.AgentTokenHash=old.AgentTokenHash }
	if n.Health=="" { if exists { n.Health=old.Health } else { n.Health="unknown" } }
	if n.LastChecked.IsZero()&&exists{n.LastChecked=old.LastChecked;n.LatencyMS=old.LatencyMS}
	if n.LastChecked.IsZero()&&!exists{n.LatencyMS=-1}
	n.UpdatedAt=time.Now().UTC()
	s.st.Nodes[n.ID]=n
	return publicNode(n),s.saveLocked()
}

func publicNode(n Node) Node { n.AgentTokenHash=""; return n }

func (s *Store) ListNodes() []Node {
	s.mu.Lock();defer s.mu.Unlock()
	out:=make([]Node,0,len(s.st.Nodes))
	for _,n:=range s.st.Nodes{out=append(out,publicNode(n))}
	sort.Slice(out,func(i,j int)bool{return out[i].Alias<out[j].Alias})
	return out
}

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

func (s *Store) CreateEnrollmentJobs(workerID,publicKey string) ([]Job,error) {
	if workerID==""||publicKey==""{return nil,errors.New("worker id and public key are required")}
	s.mu.Lock();defer s.mu.Unlock()
	w,ok:=s.st.Nodes[workerID]
	if !ok||w.Role!="worker"{return nil,errors.New("worker must be registered first")}
	var out []Job
	for _,n:=range s.st.Nodes{
		if n.Role!="foreign"{continue}
		out=append(out,s.newJobLocked(Job{Type:JobEnrollPeer,NodeID:n.ID,WorkerID:workerID,PublicKey:publicKey}))
	}
	if len(out)==0{return nil,errors.New("no foreign nodes registered")}
	return out,s.saveLocked()
}

func validVersion(v string) bool {
	if v==""||len(v)>64{return false}
	for _,r:=range v{if !(r>='a'&&r<='z'||r>='A'&&r<='Z'||r>='0'&&r<='9'||r=='.'||r=='_'||r=='-'){return false}}
	return true
}

func (s *Store) CreateDeployJobs(nodeIDs []string,version string) ([]Job,error) {
	if !validVersion(version){return nil,errors.New("invalid BAFT version")}
	if len(nodeIDs)==0{return nil,errors.New("at least one node is required")}
	s.mu.Lock();defer s.mu.Unlock()
	seen:=map[string]struct{}{};out:=make([]Job,0,len(nodeIDs))
	for _,id:=range nodeIDs{
		if _,dup:=seen[id];dup{continue};seen[id]=struct{}{}
		if _,ok:=s.st.Nodes[id];!ok{return nil,fmt.Errorf("unknown node %s",id)}
		out=append(out,s.newJobLocked(Job{Type:JobDeployBAFT,NodeID:id,Version:version}))
	}
	return out,s.saveLocked()
}

func (s *Store) authorizedLocked(nodeID,token string) bool {
	n,ok:=s.st.Nodes[nodeID]
	return ok&&n.AgentTokenHash!=""&&token!=""&&n.AgentTokenHash==tokenHash(token)
}

func (s *Store) PullJobs(nodeID,token string) ([]Job,error) {
	s.mu.Lock();defer s.mu.Unlock()
	if !s.authorizedLocked(nodeID,token){return nil,errors.New("agent authentication failed")}
	var out []Job
	for id,j:=range s.st.Jobs{
		if j.NodeID!=nodeID||j.Status!="queued"{continue}
		j.Status="dispatched";j.UpdatedAt=time.Now().UTC();s.st.Jobs[id]=j;out=append(out,j)
	}
	sort.Slice(out,func(i,j int)bool{return out[i].CreatedAt.Before(out[j].CreatedAt)})
	if len(out)>0{if err:=s.saveLocked();err!=nil{return nil,err}}
	return out,nil
}

func (s *Store) AckJob(nodeID,token,jobID,status,message string) error {
	if status!="succeeded"&&status!="failed"{return errors.New("invalid job status")}
	s.mu.Lock();defer s.mu.Unlock()
	if !s.authorizedLocked(nodeID,token){return errors.New("agent authentication failed")}
	j,ok:=s.st.Jobs[jobID];if !ok||j.NodeID!=nodeID{return errors.New("job not found")}
	if j.Status!="dispatched"&&j.Status!="queued"{return errors.New("job already completed")}
	j.Status=status;j.Message=message;j.UpdatedAt=time.Now().UTC();s.st.Jobs[jobID]=j
	return s.saveLocked()
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

func (s *Store) SetFinancePolicyAt(nodeID string,costMicrosPerGiB,revenueMicrosPerGiB int64,currency string,effectiveFrom time.Time) error {
	if costMicrosPerGiB<0||revenueMicrosPerGiB<0{return errors.New("finance rates must be non-negative")}
	if costMicrosPerGiB>1_000_000_000||revenueMicrosPerGiB>1_000_000_000{return errors.New("finance rates are unreasonably large")}
	currency=strings.ToUpper(strings.TrimSpace(currency))
	if currency==""{currency="IRR"}
	if !validCurrency(currency){return errors.New("invalid currency code")}
	if effectiveFrom.IsZero(){effectiveFrom=time.Now().UTC()}
	effectiveFrom=effectiveFrom.UTC()

	s.mu.Lock();defer s.mu.Unlock()
	if _,ok:=s.st.Nodes[nodeID];!ok{return errors.New("node not found")}
	p:=FinancePolicy{
		NodeID:nodeID,CostMicrosPerGiB:costMicrosPerGiB,RevenueMicrosPerGiB:revenueMicrosPerGiB,
		Currency:currency,EffectiveFrom:effectiveFrom,Version:s.st.NextRateVersion,
	}
	s.st.NextRateVersion++
	h:=append(s.st.RateHistory[nodeID],p)
	sort.SliceStable(h,func(i,j int)bool{
		if h[i].EffectiveFrom.Equal(h[j].EffectiveFrom){return h[i].Version<h[j].Version}
		return h[i].EffectiveFrom.Before(h[j].EffectiveFrom)
	})
	s.st.RateHistory[nodeID]=h
	s.st.Policies[nodeID]=p
	return s.saveLocked()
}

func moneyForBytes(bytes uint64,rate int64) int64 {
	if rate<=0||bytes==0{return 0}
	const gib uint64 = 1 << 30
	whole:=bytes/gib
	rem:=bytes%gib
	if whole>uint64(math.MaxInt64/rate){return math.MaxInt64}
	base:=int64(whole)*rate
	fraction:=int64((rem*uint64(rate))/gib)
	if base>math.MaxInt64-fraction{return math.MaxInt64}
	return base+fraction
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
	if ok{return chosen,true}
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
	cost:=moneyForBytes(total,p.CostMicrosPerGiB)
	revenue:=moneyForBytes(total,p.RevenueMicrosPerGiB)
	profit:=revenue-cost

	f:=s.st.Finance[nodeID]
	f.NodeID=nodeID
	if ^uint64(0)-f.IngressBytes<ingressBytes||^uint64(0)-f.EgressBytes<egressBytes{return errors.New("traffic counter overflow")}
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
	if !s.authorizedLocked(nodeID,token){return NodeFinance{},errors.New("agent authentication failed")}
	if err:=s.appendFinanceLocked(nodeID,time.Now().UTC(),ingressBytes,egressBytes);err!=nil{return NodeFinance{},err}
	if err:=s.saveLocked();err!=nil{return NodeFinance{},err}
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
	n,ok:=s.st.Nodes[report.NodeID]
	if !ok||n.AgentTokenHash==""||tokenHash(token)!=n.AgentTokenHash{return NodeFinance{},false,errors.New("agent authentication failed")}
	if !telemetry.VerifyHashedToken(n.AgentTokenHash,signature,body){return NodeFinance{},false,errors.New("telemetry signature invalid")}

	prev:=s.st.Telemetry[report.NodeID]
	if prev.BootID==report.BootID && report.Sequence<=prev.Sequence {
		f:=s.st.Finance[report.NodeID];f.NodeID=report.NodeID
		return f,true,nil
	}
	var din,dout uint64
	if prev.BootID==report.BootID && prev.BootID!="" {
		if report.IngressBytes<prev.IngressBytes||report.EgressBytes<prev.EgressBytes||report.HandshakeErrors<prev.HandshakeErrors{
			return NodeFinance{},false,errors.New("telemetry cumulative counters moved backwards")
		}
		din=report.IngressBytes-prev.IngressBytes
		dout=report.EgressBytes-prev.EgressBytes
	}else{
		din=report.IngressBytes
		dout=report.EgressBytes
	}

	ts:=time.Unix(report.TimestampUnix,0).UTC()
	if err:=s.appendFinanceLocked(report.NodeID,ts,din,dout);err!=nil{return NodeFinance{},false,err}
	f:=s.st.Finance[report.NodeID];f.NodeID=report.NodeID
	rateMilli:=int64(0)
	if prev.BootID==report.BootID && !prev.LastTelemetry.IsZero() && ts.After(prev.LastTelemetry) {
		deltaErrors:=report.HandshakeErrors-prev.HandshakeErrors
		deltaMillis:=ts.Sub(prev.LastTelemetry).Milliseconds()
		if deltaMillis>0 { rateMilli=int64(deltaErrors)*60_000_000/deltaMillis }
	}
	routes:=append([]telemetry.RouteSnapshot(nil),report.Routes...)
	s.st.Telemetry[report.NodeID]=TelemetryCursor{
		NodeID:report.NodeID,BootID:report.BootID,Sequence:report.Sequence,
		IngressBytes:report.IngressBytes,EgressBytes:report.EgressBytes,
		ActiveSessions:report.ActiveSessions,HandshakeErrors:report.HandshakeErrors,
		NoiseLatencyMS:report.NoiseLatencyMS,HandshakeErrorRateMilliMin:rateMilli,Routes:routes,LastTelemetry:ts,
	}
	n=s.st.Nodes[report.NodeID]
	point:=HistoryPoint{
		Timestamp:ts,IngressBytes:report.IngressBytes,EgressBytes:report.EgressBytes,
		ActiveSessions:report.ActiveSessions,NoiseLatencyMS:report.NoiseLatencyMS,HandshakeErrorRateMilliMin:rateMilli,
		NodeHealth:n.Health,LatencyMS:n.LatencyMS,Routes:routes,
	}
	h:=s.st.History[report.NodeID]
	cutoff:=time.Now().UTC().Add(-7*24*time.Hour)
	keep:=h[:0]
	for _,p:=range h { if !p.Timestamp.Before(cutoff) { keep=append(keep,p) } }
	keep=append(keep,point)
	if len(keep)>10080 { keep=keep[len(keep)-10080:] }
	s.st.History[report.NodeID]=keep
	if err:=s.saveLocked();err!=nil{return NodeFinance{},false,err}
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
