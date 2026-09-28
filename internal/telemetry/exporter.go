package telemetry

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const DefaultQueueLimit = 120

type RouteSnapshot struct {
	RouteID    string `json:"route_id"`
	Status     string `json:"status"`
	LatencyMS  int64  `json:"latency_ms"`
	ErrorCount uint64 `json:"error_count"`
	ProbeKind  string `json:"probe_kind"`
}

type Snapshot struct {
	IngressBytes    uint64
	EgressBytes     uint64
	ActiveSessions  uint64
	HandshakeErrors uint64
	NoiseLatencyMS  int64
	Routes          []RouteSnapshot
}

type Report struct {
	NodeID          string `json:"node_id"`
	BootID          string `json:"boot_id"`
	Sequence        uint64 `json:"sequence"`
	IngressBytes    uint64 `json:"ingress_bytes"`
	EgressBytes     uint64 `json:"egress_bytes"`
	ActiveSessions  uint64 `json:"active_sessions"`
	HandshakeErrors uint64          `json:"handshake_errors"`
	NoiseLatencyMS  int64           `json:"noise_latency_ms"`
	Routes          []RouteSnapshot `json:"routes,omitempty"`
	TimestampUnix   int64           `json:"timestamp_unix"`
}

type Source func() Snapshot

type Status struct {
	PendingCount           int       `json:"pending_count"`
	OldestPendingSequence  uint64    `json:"oldest_pending_sequence,omitempty"`
	LatestSequence         uint64    `json:"latest_sequence"`
	SpoolHealth            string    `json:"spool_health"`
	LastSuccessfulDelivery time.Time `json:"last_successful_delivery,omitempty"`
	LastDeliveryError      string    `json:"last_delivery_error,omitempty"`
}

type Exporter struct {
	NodeID     string
	BootID     string
	BCCURL     string
	Token      string
	Interval   time.Duration
	QueueLimit int
	Source     Source
	Client     *http.Client
	Spool      *Spool

	flushMu sync.Mutex
	sampleMu sync.Mutex
	statusMu sync.Mutex
	baseIngress uint64
	baseEgress uint64
	baseHandshakes uint64
	lastSuccess time.Time
	lastError string
	spoolHealth string

}

func NewPersistent(nodeID,bccURL,token,spoolPath string,interval time.Duration,queueLimit int,source Source)(*Exporter,error){
	if strings.TrimSpace(nodeID)==""||strings.TrimSpace(bccURL)==""||strings.TrimSpace(token)==""{
		return nil,errors.New("telemetry node, BCC URL, and token are required")
	}
	if strings.TrimSpace(spoolPath)==""{return nil,errors.New("telemetry spool path is required")}
	if source==nil{return nil,errors.New("telemetry source is required")}
	if interval<=0{interval=60*time.Second}
	if queueLimit<=0{queueLimit=DefaultQueueLimit}
	spool,err:=OpenSpool(spoolPath,nodeID,queueLimit)
	if err!=nil{return nil,err}
	ingress,egress,handshakes:=spool.LastCounters()
	return &Exporter{
		NodeID:nodeID,BootID:spool.BootID(),BCCURL:strings.TrimRight(bccURL,"/"),
		Token:token,Interval:interval,QueueLimit:queueLimit,Source:source,
		Client:&http.Client{Timeout:10*time.Second},Spool:spool,
		baseIngress:ingress,baseEgress:egress,baseHandshakes:handshakes,spoolHealth:"ok",
	},nil
}

// New is retained for compatibility with callers that do not need restart
// recovery. Production Runtime uses NewPersistent with a configured spool path.
func New(nodeID,bccURL,token string,interval time.Duration,source Source)(*Exporter,error){
	dir,err:=os.MkdirTemp("","baft-telemetry-")
	if err!=nil{return nil,err}
	return NewPersistent(nodeID,bccURL,token,filepath.Join(dir,"spool.json"),interval,DefaultQueueLimit,source)
}

func signingKey(token string) []byte {
	sum:=sha256.Sum256([]byte(token))
	return sum[:]
}

func Sign(token string,body []byte) string {
	m:=hmac.New(sha256.New,signingKey(token))
	_,_=m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func VerifyHashedToken(tokenHashHex,signature string,body []byte) bool {
	key,err:=hex.DecodeString(tokenHashHex);if err!=nil||len(key)!=sha256.Size{return false}
	got,err:=hex.DecodeString(signature);if err!=nil{return false}
	m:=hmac.New(sha256.New,key);_,_=m.Write(body)
	return hmac.Equal(got,m.Sum(nil))
}

func addCounter(base,raw uint64)(uint64,error){
	if ^uint64(0)-base<raw{return 0,errors.New("telemetry cumulative counter overflow")}
	return base+raw,nil
}

func (e *Exporter) setError(err error){
	e.statusMu.Lock();defer e.statusMu.Unlock()
	if err==nil{e.lastError="";e.spoolHealth="ok";return}
	e.lastError=err.Error()
	if errors.Is(err,ErrSpoolFull){e.spoolHealth="degraded"}else{e.spoolHealth="error"}
}

func (e *Exporter) markSuccess(){
	e.statusMu.Lock();defer e.statusMu.Unlock()
	e.lastSuccess=time.Now().UTC()
	e.lastError=""
	e.spoolHealth="ok"
}

func (e *Exporter) sample(now time.Time) error {
	e.sampleMu.Lock()
	defer e.sampleMu.Unlock()
	s:=e.Source()
	ingress,err:=addCounter(e.baseIngress,s.IngressBytes);if err!=nil{e.setError(err);return err}
	egress,err:=addCounter(e.baseEgress,s.EgressBytes);if err!=nil{e.setError(err);return err}
	handshakes,err:=addCounter(e.baseHandshakes,s.HandshakeErrors);if err!=nil{e.setError(err);return err}
	seq:=e.Spool.NextSequence()
	r:=Report{
		NodeID:e.NodeID,BootID:e.BootID,Sequence:seq,
		IngressBytes:ingress,EgressBytes:egress,
		ActiveSessions:s.ActiveSessions,HandshakeErrors:handshakes,
		NoiseLatencyMS:s.NoiseLatencyMS,Routes:append([]RouteSnapshot(nil),s.Routes...),
		TimestampUnix:now.Unix(),
	}
	if err:=e.Spool.Enqueue(r);err!=nil{e.setError(err);return err}
	e.setError(nil)
	return nil
}

func (e *Exporter) QueueLength() int {
	if e==nil||e.Spool==nil{return 0}
	return e.Spool.PendingCount()
}

func (e *Exporter) Status() Status {
	if e==nil||e.Spool==nil{return Status{SpoolHealth:"error",LastDeliveryError:"exporter not configured"}}
	pending:=e.Spool.PendingCount()
	oldest,_:=e.Spool.OldestSequence()
	next:=e.Spool.NextSequence()
	e.statusMu.Lock()
	st:=Status{
		PendingCount:pending,OldestPendingSequence:oldest,SpoolHealth:e.spoolHealth,
		LastSuccessfulDelivery:e.lastSuccess,LastDeliveryError:e.lastError,
	}
	e.statusMu.Unlock()
	if next>0{st.LatestSequence=next-1}
	return st
}

func (e *Exporter) sendReport(ctx context.Context,r Report) error {
	body,err:=json.Marshal(r);if err!=nil{return err}
	req,err:=http.NewRequestWithContext(ctx,http.MethodPost,e.BCCURL+"/api/agent/traffic",bytes.NewReader(body))
	if err!=nil{return err}
	req.Header.Set("Content-Type","application/json")
	req.Header.Set("Authorization","Bearer "+e.Token)
	req.Header.Set("X-BAFT-Signature",Sign(e.Token,body))
	resp,err:=e.Client.Do(req);if err!=nil{return err}
	defer resp.Body.Close()
	if resp.StatusCode<200||resp.StatusCode>=300{return fmt.Errorf("telemetry BCC status %d",resp.StatusCode)}
	return nil
}

func (e *Exporter) flush(ctx context.Context) error {
	e.flushMu.Lock()
	defer e.flushMu.Unlock()
	for {
		r,ok:=e.Spool.First()
		if !ok{return nil}
		if err:=e.sendReport(ctx,r);err!=nil{e.setError(err);return err}
		if err:=e.Spool.Ack(r.BootID,r.Sequence);err!=nil{e.setError(err);return err}
		e.markSuccess()
	}
}

func (e *Exporter) FlushPending(ctx context.Context) error {
	if e==nil||e.Spool==nil{return errors.New("telemetry exporter is not configured")}
	return e.flush(ctx)
}

func (e *Exporter) SendOnce(ctx context.Context) error {
	if e==nil||e.Source==nil||e.Spool==nil{return errors.New("telemetry exporter is not configured")}
	// Drain old durable reports first so a full spool can recover as soon as
	// connectivity returns. A failure here still leaves every report durable.
	if err:=e.flush(ctx);err!=nil{
		// We still attempt to sample if capacity remains, preserving outage data.
		if err2:=e.sample(time.Now());err2!=nil{return err2}
		return err
	}
	if err:=e.sample(time.Now());err!=nil{return err}
	return e.flush(ctx)
}

func (e *Exporter) Run(ctx context.Context) {
	_ = e.flush(ctx)
	_ = e.SendOnce(ctx)
	t:=time.NewTicker(e.Interval);defer t.Stop()
	for{
		select{
		case <-ctx.Done():return
		case now:=<-t.C:
			_ = e.flush(ctx)
			if err:=e.sample(now);err==nil{_ = e.flush(ctx)}
		}
	}
}
