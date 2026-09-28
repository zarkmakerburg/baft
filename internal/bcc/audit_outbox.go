package bcc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
)

const auditAnchorOutboxVersion = 1

type auditAnchorOutboxState struct {
	Version      int           `json:"version"`
	AckedThrough uint64        `json:"acked_through"`
	Pending      []AuditAnchor `json:"pending"`
}

type AuditAnchorOutbox struct {
	mu   sync.Mutex
	path string
	st   auditAnchorOutboxState
}

func OpenAuditAnchorOutbox(path string)(*AuditAnchorOutbox,error){
	if path==""{return nil,errors.New("audit anchor outbox path is required")}
	o:=&AuditAnchorOutbox{path:path,st:auditAnchorOutboxState{Version:auditAnchorOutboxVersion}}
	raw,err:=os.ReadFile(path)
	if errors.Is(err,os.ErrNotExist){return o,nil}
	if err!=nil{return nil,err}
	if err:=json.Unmarshal(raw,&o.st);err!=nil{return nil,fmt.Errorf("decode audit anchor outbox: %w",err)}
	if o.st.Version!=auditAnchorOutboxVersion{return nil,fmt.Errorf("unsupported audit anchor outbox version %d",o.st.Version)}
	sort.SliceStable(o.st.Pending,func(i,j int)bool{return o.st.Pending[i].Sequence<o.st.Pending[j].Sequence})
	return o,nil
}

func (o *AuditAnchorOutbox) saveLocked() error {
	raw,err:=json.MarshalIndent(o.st,"","  ");if err!=nil{return err}
	return writeAtomic(o.path,raw,0600)
}

func (o *AuditAnchorOutbox) enqueueLocked(a AuditAnchor) error {
	if a.Sequence==0||a.Hash==""{return errors.New("audit anchor is empty")}
	if a.Sequence<=o.st.AckedThrough{return nil}
	for _,p:=range o.st.Pending{
		if p.Sequence==a.Sequence{
			if p.Hash!=a.Hash{return fmt.Errorf("conflicting pending audit anchor at sequence %d",a.Sequence)}
			return nil
		}
	}
	o.st.Pending=append(o.st.Pending,a)
	sort.SliceStable(o.st.Pending,func(i,j int)bool{return o.st.Pending[i].Sequence<o.st.Pending[j].Sequence})
	return nil
}

func (o *AuditAnchorOutbox) Enqueue(a AuditAnchor) error {
	o.mu.Lock();defer o.mu.Unlock()
	if err:=o.enqueueLocked(a);err!=nil{return err}
	return o.saveLocked()
}

func (o *AuditAnchorOutbox) ReconcileSecurityAudit(entries []AuditEntry) error {
	o.mu.Lock();defer o.mu.Unlock()
	changed:=false
	for _,e:=range entries{
		if e.Sequence<=o.st.AckedThrough{continue}
		if e.Action!="node.revoke"&&e.Action!="node.token.rotate"{continue}
		before:=len(o.st.Pending)
		if err:=o.enqueueLocked(AuditAnchor{Sequence:e.Sequence,Hash:e.Hash,Timestamp:e.Timestamp});err!=nil{return err}
		if len(o.st.Pending)!=before{changed=true}
	}
	if !changed{return nil}
	return o.saveLocked()
}

func (o *AuditAnchorOutbox) First()(AuditAnchor,bool) {
	o.mu.Lock();defer o.mu.Unlock()
	if len(o.st.Pending)==0{return AuditAnchor{},false}
	return o.st.Pending[0],true
}

func (o *AuditAnchorOutbox) Ack(a AuditAnchor) error {
	o.mu.Lock();defer o.mu.Unlock()
	if len(o.st.Pending)==0{return nil}
	first:=o.st.Pending[0]
	if first.Sequence!=a.Sequence||first.Hash!=a.Hash{return errors.New("audit anchor ACK is out of order")}
	o.st.Pending=append([]AuditAnchor(nil),o.st.Pending[1:]...)
	if a.Sequence>o.st.AckedThrough{o.st.AckedThrough=a.Sequence}
	return o.saveLocked()
}

func (o *AuditAnchorOutbox) Pending() []AuditAnchor {
	o.mu.Lock();defer o.mu.Unlock()
	return append([]AuditAnchor(nil),o.st.Pending...)
}

func (o *AuditAnchorOutbox) AckedThrough() uint64 {
	o.mu.Lock();defer o.mu.Unlock()
	return o.st.AckedThrough
}
