package bcc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

type AuditAnchor struct {
	Sequence  uint64    `json:"sequence"`
	Hash      string    `json:"hash"`
	Timestamp time.Time `json:"timestamp"`
}

func (a *AuditLog) CurrentAnchor() AuditAnchor {
	if a==nil{return AuditAnchor{}}
	a.mu.Lock();defer a.mu.Unlock()
	return AuditAnchor{Sequence:a.nextSeq-1,Hash:a.lastHash,Timestamp:time.Now().UTC()}
}

func (a *AuditLog) VerifyAgainstAnchors(anchors []AuditAnchor) error {
	if a==nil{return errors.New("audit log is not configured")}
	entries,err:=a.List(0);if err!=nil{return err}
	bySeq:=make(map[uint64]string,len(entries))
	for _,e:=range entries{bySeq[e.Sequence]=e.Hash}
	for _,anchor:=range anchors{
		if anchor.Sequence==0&&anchor.Hash==""{continue}
		got,ok:=bySeq[anchor.Sequence]
		if !ok{return fmt.Errorf("audit anchor sequence %d is not present",anchor.Sequence)}
		if got!=anchor.Hash{return fmt.Errorf("audit anchor mismatch at sequence %d",anchor.Sequence)}
	}
	return a.Verify()
}

func ReadAuditAnchors(path string)([]AuditAnchor,error){
	f,err:=os.Open(path);if err!=nil{return nil,err}
	defer f.Close()
	var out []AuditAnchor
	s:=bufio.NewScanner(f)
	for s.Scan(){
		if len(bytes.TrimSpace(s.Bytes()))==0{continue}
		var a AuditAnchor
		if err:=json.Unmarshal(s.Bytes(),&a);err!=nil{return nil,err}
		out=append(out,a)
	}
	if err:=s.Err();err!=nil{return nil,err}
	return out,nil
}

func validateExternalWebhook(raw string) error {
	if raw==""{return nil}
	u,err:=url.Parse(raw)
	if err!=nil||u.Host==""{return errors.New("webhook must be an absolute URL")}
	if u.Scheme=="https"{return nil}
	if u.Scheme!="http"{return errors.New("webhook must use https except on loopback")}
	host:=u.Hostname()
	ip:=net.ParseIP(host)
	if host!="localhost"&&(ip==nil||!ip.IsLoopback()){return errors.New("http webhook is allowed only on loopback")}
	return nil
}

func (s *Server) ConfigureAuditAnchoring(webhook string,interval time.Duration) error {
	if err:=validateExternalWebhook(webhook);err!=nil{return err}
	if interval<=0{interval=time.Hour}
	s.auditAnchorWebhook=webhook
	s.auditAnchorInterval=interval
	return nil
}

func (s *Server) SendAuditAnchor(ctx context.Context) error {
	if s.auditAnchorWebhook==""{return nil}
	anchor:=s.audit.CurrentAnchor()
	body,err:=json.Marshal(anchor);if err!=nil{return err}
	req,err:=http.NewRequestWithContext(ctx,http.MethodPost,s.auditAnchorWebhook,bytes.NewReader(body));if err!=nil{return err}
	req.Header.Set("Content-Type","application/json")
	resp,err:=s.httpClient.Do(req);if err!=nil{return err}
	defer resp.Body.Close()
	if resp.StatusCode<200||resp.StatusCode>=300{return fmt.Errorf("audit anchor webhook status %d",resp.StatusCode)}
	return nil
}

func (s *Server) StartAuditAnchorLoop(ctx context.Context){
	if s.auditAnchorWebhook==""{return}
	_ = s.SendAuditAnchor(ctx)
	t:=time.NewTicker(s.auditAnchorInterval);defer t.Stop()
	for{
		select{
		case <-ctx.Done():return
		case <-t.C:_ = s.SendAuditAnchor(ctx)
		}
	}
}
