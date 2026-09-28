package bcc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	NodeID               string `json:"node_id"`
	CostMicrosPerGiB     int64  `json:"cost_micros_per_gib"`
	RevenueMicrosPerGiB  int64  `json:"revenue_micros_per_gib"`
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
	HandshakeErrors uint64    `json:"handshake_errors"`
	LastTelemetry   time.Time `json:"last_telemetry"`
}

type state struct {
	Nodes    map[string]Node          `json:"nodes"`
	Jobs     map[string]Job           `json:"jobs"`
	Finance   map[string]NodeFinance      `json:"finance,omitempty"`
	Policies  map[string]FinancePolicy    `json:"finance_policies,omitempty"`
	Telemetry map[string]TelemetryCursor  `json:"telemetry,omitempty"`
	NextJob   uint64                      `json:"next_job"`
}

type Store struct {
	mu   sync.Mutex
	path string
	st   state
}

func OpenStore(path string) (*Store, error) {
	if strings.TrimSpace(path)=="" { return nil, errors.New("state path is required") }
	s:=&Store{path:path,st:state{Nodes:map[string]Node{},Jobs:map[string]Job{},Finance:map[string]NodeFinance{},Policies:map[string]FinancePolicy{},Telemetry:map[string]TelemetryCursor{},NextJob:1}}
	b,err:=os.ReadFile(path)
	if err==nil {
		if err:=json.Unmarshal(b,&s.st);err!=nil{return nil,fmt.Errorf("decode BCC state: %w",err)}
		if s.st.Nodes==nil{s.st.Nodes=map[string]Node{}}
		if s.st.Jobs==nil{s.st.Jobs=map[string]Job{}}
		if s.st.Finance==nil{s.st.Finance=map[string]NodeFinance{}}
		if s.st.Policies==nil{s.st.Policies=map[string]FinancePolicy{}}
		if s.st.Telemetry==nil{s.st.Telemetry=map[string]TelemetryCursor{}}
		if s.st.NextJob==0{s.st.NextJob=1}
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
	if n.LastChecked.IsZero()&&exists{n.LastChecked=old.LastChecked}
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

func (s *Store) SetHealth(nodeID,health string,checked time.Time) error {
	if health!="up"&&health!="down"&&health!="unknown"{return errors.New("invalid health")}
	s.mu.Lock();defer s.mu.Unlock()
	n,ok:=s.st.Nodes[nodeID];if !ok{return errors.New("node not found")}
	n.Health=health;n.LastChecked=checked.UTC();s.st.Nodes[nodeID]=n
	return s.saveLocked()
}


func (s *Store) SetFinancePolicy(nodeID string,costMicrosPerGiB,revenueMicrosPerGiB int64) error {
	if costMicrosPerGiB<0||revenueMicrosPerGiB<0{return errors.New("finance rates must be non-negative")}
	if costMicrosPerGiB>1_000_000_000||revenueMicrosPerGiB>1_000_000_000{return errors.New("finance rates are unreasonably large")}
	s.mu.Lock();defer s.mu.Unlock()
	if _,ok:=s.st.Nodes[nodeID];!ok{return errors.New("node not found")}
	s.st.Policies[nodeID]=FinancePolicy{NodeID:nodeID,CostMicrosPerGiB:costMicrosPerGiB,RevenueMicrosPerGiB:revenueMicrosPerGiB}
	f:=s.st.Finance[nodeID];f.NodeID=nodeID
	s.recalculateFinanceLocked(&f)
	s.st.Finance[nodeID]=f
	return s.saveLocked()
}

func moneyForBytes(bytes uint64,rate int64) int64 {
	if rate<=0||bytes==0{return 0}
	const gib uint64 = 1 << 30
	whole:=bytes/gib
	rem:=bytes%gib
	if whole>uint64((1<<63-1)/rate){return 1<<63-1}
	base:=int64(whole)*rate
	fraction:=int64((rem*uint64(rate))/gib)
	if base>(1<<63-1)-fraction{return 1<<63-1}
	return base+fraction
}

func (s *Store) recalculateFinanceLocked(f *NodeFinance) {
	p:=s.st.Policies[f.NodeID]
	total:=f.IngressBytes+f.EgressBytes
	f.CostMicros=moneyForBytes(total,p.CostMicrosPerGiB)
	f.RevenueMicros=moneyForBytes(total,p.RevenueMicrosPerGiB)
	f.ProfitMicros=f.RevenueMicros-f.CostMicros
}

func (s *Store) AddTraffic(nodeID,token string,ingressBytes,egressBytes uint64) (NodeFinance,error) {
	s.mu.Lock();defer s.mu.Unlock()
	if !s.authorizedLocked(nodeID,token){return NodeFinance{},errors.New("agent authentication failed")}
	f:=s.st.Finance[nodeID];f.NodeID=nodeID
	if ^uint64(0)-f.IngressBytes<ingressBytes||^uint64(0)-f.EgressBytes<egressBytes{return NodeFinance{},errors.New("traffic counter overflow")}
	f.IngressBytes+=ingressBytes;f.EgressBytes+=egressBytes;f.UpdatedAt=time.Now().UTC()
	s.recalculateFinanceLocked(&f)
	s.st.Finance[nodeID]=f
	if err:=s.saveLocked();err!=nil{return NodeFinance{},err}
	return f,nil
}

func (s *Store) FinanceSnapshot() []NodeFinance {
	s.mu.Lock();defer s.mu.Unlock()
	out:=make([]NodeFinance,0,len(s.st.Nodes))
	for id:=range s.st.Nodes{
		f:=s.st.Finance[id];f.NodeID=id
		s.recalculateFinanceLocked(&f)
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
		f:=s.st.Finance[report.NodeID];f.NodeID=report.NodeID;s.recalculateFinanceLocked(&f)
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

	f:=s.st.Finance[report.NodeID];f.NodeID=report.NodeID
	if ^uint64(0)-f.IngressBytes<din||^uint64(0)-f.EgressBytes<dout{return NodeFinance{},false,errors.New("traffic counter overflow")}
	f.IngressBytes+=din;f.EgressBytes+=dout;f.UpdatedAt=time.Now().UTC()
	s.recalculateFinanceLocked(&f)
	s.st.Finance[report.NodeID]=f
	s.st.Telemetry[report.NodeID]=TelemetryCursor{
		NodeID:report.NodeID,BootID:report.BootID,Sequence:report.Sequence,
		IngressBytes:report.IngressBytes,EgressBytes:report.EgressBytes,
		ActiveSessions:report.ActiveSessions,HandshakeErrors:report.HandshakeErrors,
		LastTelemetry:time.Unix(report.TimestampUnix,0).UTC(),
	}
	if err:=s.saveLocked();err!=nil{return NodeFinance{},false,err}
	return f,false,nil
}

func (s *Store) TelemetrySnapshot(nodeID string) (TelemetryCursor,bool) {
	s.mu.Lock();defer s.mu.Unlock()
	v,ok:=s.st.Telemetry[nodeID]
	return v,ok
}
