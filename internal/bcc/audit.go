package bcc

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type AuditEntry struct {
	Sequence  uint64         `json:"sequence"`
	IntentID  string         `json:"intent_id,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Actor     string         `json:"actor"`
	RemoteIP  string         `json:"remote_ip"`
	Action    string         `json:"action"`
	Target    string         `json:"target,omitempty"`
	Outcome   string         `json:"outcome"`
	Details   map[string]any `json:"details,omitempty"`
	PrevHash  string         `json:"prev_hash,omitempty"`
	Hash      string         `json:"hash"`
}

type AuditLog struct {
	mu       sync.Mutex
	path     string
	lastHash string
	nextSeq  uint64
}

func OpenAuditLog(path string) (*AuditLog,error) {
	if path==""{return nil,errors.New("audit path is required")}
	a:=&AuditLog{path:path,nextSeq:1}
	f,err:=os.Open(path)
	if errors.Is(err,os.ErrNotExist){return a,nil}
	if err!=nil{return nil,err}
	defer f.Close()
	s:=bufio.NewScanner(f)
	var prev string
	var seq uint64
	for s.Scan(){
		var e AuditEntry
		if err:=json.Unmarshal(s.Bytes(),&e);err!=nil{return nil,fmt.Errorf("decode audit entry: %w",err)}
		if e.Sequence!=seq+1{return nil,fmt.Errorf("audit sequence discontinuity at %d",e.Sequence)}
		if e.PrevHash!=prev{return nil,fmt.Errorf("audit hash chain mismatch at %d",e.Sequence)}
		want,err:=auditHash(e);if err!=nil{return nil,err}
		if e.Hash!=want{return nil,fmt.Errorf("audit entry hash mismatch at %d",e.Sequence)}
		prev=e.Hash;seq=e.Sequence
	}
	if err:=s.Err();err!=nil{return nil,err}
	a.lastHash=prev;a.nextSeq=seq+1
	return a,nil
}

func auditHash(e AuditEntry)(string,error){
	e.Hash=""
	b,err:=json.Marshal(e);if err!=nil{return "",err}
	sum:=sha256.Sum256(b)
	return hex.EncodeToString(sum[:]),nil
}

func (a *AuditLog) Append(e AuditEntry)(AuditEntry,error){
	if a==nil{return AuditEntry{},errors.New("audit log is not configured")}
	a.mu.Lock();defer a.mu.Unlock()
	if e.Timestamp.IsZero(){e.Timestamp=time.Now().UTC()}else{e.Timestamp=e.Timestamp.UTC()}
	e.Sequence=a.nextSeq
	e.PrevHash=a.lastHash
	h,err:=auditHash(e);if err!=nil{return AuditEntry{},err}
	e.Hash=h
	if err:=os.MkdirAll(filepath.Dir(a.path),0700);err!=nil{return AuditEntry{},err}
	f,err:=os.OpenFile(a.path,os.O_CREATE|os.O_WRONLY|os.O_APPEND,0600);if err!=nil{return AuditEntry{},err}
	b,err:=json.Marshal(e);if err==nil{_,err=f.Write(append(b,'\n'))}
	if err==nil{err=f.Sync()}
	closeErr:=f.Close()
	if err!=nil{return AuditEntry{},err}
	if closeErr!=nil{return AuditEntry{},closeErr}
	if err:=fsyncDir(a.path);err!=nil{return AuditEntry{},err}
	a.lastHash=e.Hash;a.nextSeq++
	return e,nil
}

func (a *AuditLog) List(limit int)([]AuditEntry,error){
	if a==nil{return nil,errors.New("audit log is not configured")}
	a.mu.Lock();defer a.mu.Unlock()
	f,err:=os.Open(a.path)
	if errors.Is(err,os.ErrNotExist){return []AuditEntry{},nil}
	if err!=nil{return nil,err}
	defer f.Close()
	var out []AuditEntry
	s:=bufio.NewScanner(f)
	for s.Scan(){
		var e AuditEntry
		if err:=json.Unmarshal(s.Bytes(),&e);err!=nil{return nil,err}
		out=append(out,e)
	}
	if err:=s.Err();err!=nil{return nil,err}
	if limit>0&&len(out)>limit{out=out[len(out)-limit:]}
	return out,nil
}

func (a *AuditLog) Verify() error {
	if a==nil{return errors.New("audit log is not configured")}
	_,err:=OpenAuditLog(a.path)
	return err
}
