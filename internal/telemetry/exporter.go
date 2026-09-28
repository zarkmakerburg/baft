package telemetry

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	Routes          []RouteSnapshot `json:"routes,omitempty"`
	TimestampUnix   int64           `json:"timestamp_unix"`
}

type Source func() Snapshot

type Exporter struct {
	NodeID     string
	BootID     string
	BCCURL     string
	Token      string
	Interval   time.Duration
	QueueLimit int
	Source     Source
	Client     *http.Client

	flushMu sync.Mutex
	mu      sync.Mutex
	nextSeq uint64
	queue   []Report
}

func New(nodeID,bccURL,token string,interval time.Duration,source Source)(*Exporter,error){
	if strings.TrimSpace(nodeID)==""||strings.TrimSpace(bccURL)==""||strings.TrimSpace(token)==""{
		return nil,errors.New("telemetry node, BCC URL, and token are required")
	}
	if source==nil{return nil,errors.New("telemetry source is required")}
	if interval<=0{interval=60*time.Second}
	boot:=make([]byte,16)
	if _,err:=rand.Read(boot);err!=nil{return nil,err}
	return &Exporter{
		NodeID:nodeID,BootID:hex.EncodeToString(boot),BCCURL:strings.TrimRight(bccURL,"/"),
		Token:token,Interval:interval,QueueLimit:DefaultQueueLimit,Source:source,
		Client:&http.Client{Timeout:10*time.Second},
	},nil
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

func (e *Exporter) sample(now time.Time) {
	s:=e.Source()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nextSeq++
	r:=Report{
		NodeID:e.NodeID,BootID:e.BootID,Sequence:e.nextSeq,
		IngressBytes:s.IngressBytes,EgressBytes:s.EgressBytes,
		ActiveSessions:s.ActiveSessions,HandshakeErrors:s.HandshakeErrors,
		Routes:append([]RouteSnapshot(nil),s.Routes...),
		TimestampUnix:now.Unix(),
	}
	limit:=e.QueueLimit
	if limit<=0{limit=DefaultQueueLimit}
	if len(e.queue)>=limit {
		copy(e.queue,e.queue[len(e.queue)-limit+1:])
		e.queue=e.queue[:limit-1]
	}
	e.queue=append(e.queue,r)
}

func (e *Exporter) QueueLength() int {
	e.mu.Lock();defer e.mu.Unlock()
	return len(e.queue)
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
		e.mu.Lock()
		if len(e.queue)==0{e.mu.Unlock();return nil}
		r:=e.queue[0]
		e.mu.Unlock()
		if err:=e.sendReport(ctx,r);err!=nil{return err}
		e.mu.Lock()
		if len(e.queue)>0&&e.queue[0].Sequence==r.Sequence&&e.queue[0].BootID==r.BootID {
			e.queue=append([]Report(nil),e.queue[1:]...)
		}
		e.mu.Unlock()
	}
}

func (e *Exporter) SendOnce(ctx context.Context) error {
	if e==nil||e.Source==nil{return errors.New("telemetry exporter is not configured")}
	e.sample(time.Now())
	return e.flush(ctx)
}

func (e *Exporter) Run(ctx context.Context) {
	_ = e.SendOnce(ctx)
	t:=time.NewTicker(e.Interval);defer t.Stop()
	for{
		select{
		case <-ctx.Done():return
		case now:=<-t.C:
			e.sample(now)
			_ = e.flush(ctx)
		}
	}
}
