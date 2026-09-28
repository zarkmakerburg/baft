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
	"time"
)

type Report struct {
	NodeID       string `json:"node_id"`
	BootID       string `json:"boot_id"`
	IngressBytes uint64 `json:"ingress_bytes"`
	EgressBytes  uint64 `json:"egress_bytes"`
	TimestampUnix int64 `json:"timestamp_unix"`
}

type Source func() (ingressBytes, egressBytes uint64)

type Exporter struct {
	NodeID   string
	BootID   string
	BCCURL   string
	Token    string
	Interval time.Duration
	Source   Source
	Client   *http.Client
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
		Token:token,Interval:interval,Source:source,
		Client:&http.Client{Timeout:10*time.Second},
	},nil
}

func Sign(token string,body []byte) string {
	m:=hmac.New(sha256.New,[]byte(token))
	_,_=m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func Verify(token,signature string,body []byte) bool {
	got,err:=hex.DecodeString(signature);if err!=nil{return false}
	m:=hmac.New(sha256.New,[]byte(token));_,_=m.Write(body)
	return hmac.Equal(got,m.Sum(nil))
}

func (e *Exporter) SendOnce(ctx context.Context) error {
	if e==nil||e.Source==nil{return errors.New("telemetry exporter is not configured")}
	in,out:=e.Source()
	body,err:=json.Marshal(Report{NodeID:e.NodeID,BootID:e.BootID,IngressBytes:in,EgressBytes:out,TimestampUnix:time.Now().Unix()})
	if err!=nil{return err}
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

func (e *Exporter) Run(ctx context.Context) {
	_ = e.SendOnce(ctx)
	t:=time.NewTicker(e.Interval);defer t.Stop()
	for{
		select{
		case <-ctx.Done():return
		case <-t.C:_ = e.SendOnce(ctx)
		}
	}
}
