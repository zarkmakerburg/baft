package telemetry

import (
	"crypto/rand"
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
)

const spoolSchemaVersion = 1

var (
	ErrSpoolFull = errors.New("telemetry spool capacity reached")
	ErrSpoolCorrupt = errors.New("telemetry spool is corrupt")
)

type spoolState struct {
	SchemaVersion   int      `json:"schema_version"`
	NodeID          string   `json:"node_id"`
	BootID          string   `json:"boot_id"`
	NextSequence    uint64   `json:"next_sequence"`
	AckedSequence   uint64   `json:"acked_sequence"`
	LastIngress     uint64   `json:"last_ingress"`
	LastEgress      uint64   `json:"last_egress"`
	LastHandshakes  uint64   `json:"last_handshake_errors"`
	Pending         []Report `json:"pending"`
}

type spoolEnvelope struct {
	State    spoolState `json:"state"`
	Checksum string     `json:"checksum"`
}

type Spool struct {
	mu    sync.Mutex
	path  string
	limit int
	st    spoolState
}

func randomBootID()(string,error){
	b:=make([]byte,16)
	if _,err:=rand.Read(b);err!=nil{return "",err}
	return hex.EncodeToString(b),nil
}

func validateSpoolState(st spoolState,expectedNode string) error {
	if st.SchemaVersion!=spoolSchemaVersion{return fmt.Errorf("%w: unsupported schema %d",ErrSpoolCorrupt,st.SchemaVersion)}
	if strings.TrimSpace(st.NodeID)==""||st.NodeID!=expectedNode{return fmt.Errorf("%w: node binding mismatch",ErrSpoolCorrupt)}
	if strings.TrimSpace(st.BootID)==""{return fmt.Errorf("%w: missing boot id",ErrSpoolCorrupt)}
	if st.NextSequence==0{return fmt.Errorf("%w: next sequence is zero",ErrSpoolCorrupt)}
	prev:=st.AckedSequence
	for i,r:=range st.Pending{
		if r.NodeID!=st.NodeID||r.BootID!=st.BootID{return fmt.Errorf("%w: pending identity mismatch at %d",ErrSpoolCorrupt,i)}
		if r.Sequence!=prev+1{return fmt.Errorf("%w: pending sequence gap/order at %d",ErrSpoolCorrupt,i)}
		prev=r.Sequence
	}
	if len(st.Pending)>0&&st.NextSequence!=st.Pending[len(st.Pending)-1].Sequence+1{
		return fmt.Errorf("%w: next sequence mismatch",ErrSpoolCorrupt)
	}
	if len(st.Pending)==0&&st.NextSequence<=st.AckedSequence{
		return fmt.Errorf("%w: next sequence behind ack",ErrSpoolCorrupt)
	}
	return nil
}

func spoolChecksum(st spoolState)(string,error){
	b,err:=json.Marshal(st);if err!=nil{return "",err}
	sum:=sha256.Sum256(b)
	return hex.EncodeToString(sum[:]),nil
}

func writeSpoolAtomic(path string,data []byte) error {
	dir:=filepath.Dir(path)
	if err:=os.MkdirAll(dir,0700);err!=nil{return err}
	if err:=os.Chmod(dir,0700);err!=nil{return err}
	tmp:=path+".tmp"
	f,err:=os.OpenFile(tmp,os.O_CREATE|os.O_TRUNC|os.O_WRONLY,0600);if err!=nil{return err}
	if _,err=f.Write(data);err!=nil{_ = f.Close();_ = os.Remove(tmp);return err}
	if err=f.Sync();err!=nil{_ = f.Close();_ = os.Remove(tmp);return err}
	if err=f.Close();err!=nil{_ = os.Remove(tmp);return err}
	if err=os.Rename(tmp,path);err!=nil{_ = os.Remove(tmp);return err}
	if err=os.Chmod(path,0600);err!=nil{return err}
	d,err:=os.Open(dir);if err!=nil{return err}
	defer d.Close()
	return d.Sync()
}

func (s *Spool) saveLocked() error {
	sum,err:=spoolChecksum(s.st);if err!=nil{return err}
	raw,err:=json.MarshalIndent(spoolEnvelope{State:s.st,Checksum:sum},"","  ");if err!=nil{return err}
	return writeSpoolAtomic(s.path,raw)
}

func OpenSpool(path,nodeID string,limit int)(*Spool,error){
	if strings.TrimSpace(path)==""{return nil,errors.New("telemetry spool path is required")}
	if strings.TrimSpace(nodeID)==""{return nil,errors.New("telemetry node id is required")}
	if limit<=0{limit=DefaultQueueLimit}
	s:=&Spool{path:path,limit:limit}
	raw,err:=os.ReadFile(path)
	if errors.Is(err,os.ErrNotExist){
		boot,err:=randomBootID();if err!=nil{return nil,err}
		s.st=spoolState{SchemaVersion:spoolSchemaVersion,NodeID:nodeID,BootID:boot,NextSequence:1}
		if err:=s.saveLocked();err!=nil{return nil,err}
		return s,nil
	}
	if err!=nil{return nil,err}
	var env spoolEnvelope
	if err:=json.Unmarshal(raw,&env);err!=nil{return nil,fmt.Errorf("%w: invalid JSON",ErrSpoolCorrupt)}
	sum,err:=spoolChecksum(env.State);if err!=nil{return nil,err}
	if !strings.EqualFold(sum,env.Checksum){return nil,fmt.Errorf("%w: checksum mismatch",ErrSpoolCorrupt)}
	if err:=validateSpoolState(env.State,nodeID);err!=nil{return nil,err}
	if len(env.State.Pending)>limit{return nil,fmt.Errorf("%w: pending count %d exceeds configured limit %d",ErrSpoolCorrupt,len(env.State.Pending),limit)}
	s.st=env.State
	return s,nil
}

func (s *Spool) BootID() string { s.mu.Lock();defer s.mu.Unlock();return s.st.BootID }
func (s *Spool) NextSequence() uint64 { s.mu.Lock();defer s.mu.Unlock();return s.st.NextSequence }
func (s *Spool) PendingCount() int { s.mu.Lock();defer s.mu.Unlock();return len(s.st.Pending) }
func (s *Spool) OldestSequence()(uint64,bool){s.mu.Lock();defer s.mu.Unlock();if len(s.st.Pending)==0{return 0,false};return s.st.Pending[0].Sequence,true}
func (s *Spool) LastCounters()(uint64,uint64,uint64){s.mu.Lock();defer s.mu.Unlock();return s.st.LastIngress,s.st.LastEgress,s.st.LastHandshakes}

func (s *Spool) Enqueue(r Report) error {
	s.mu.Lock();defer s.mu.Unlock()
	if len(s.st.Pending)>=s.limit{return ErrSpoolFull}
	if r.NodeID!=s.st.NodeID||r.BootID!=s.st.BootID{return errors.New("telemetry report identity does not match spool")}
	if r.Sequence!=s.st.NextSequence{return fmt.Errorf("telemetry sequence %d does not match next %d",r.Sequence,s.st.NextSequence)}
	oldIngress,oldEgress,oldHandshakes:=s.st.LastIngress,s.st.LastEgress,s.st.LastHandshakes
	s.st.Pending=append(s.st.Pending,r)
	s.st.NextSequence++
	s.st.LastIngress=r.IngressBytes
	s.st.LastEgress=r.EgressBytes
	s.st.LastHandshakes=r.HandshakeErrors
	if err:=s.saveLocked();err!=nil{
		s.st.Pending=s.st.Pending[:len(s.st.Pending)-1]
		s.st.NextSequence--
		s.st.LastIngress,s.st.LastEgress,s.st.LastHandshakes=oldIngress,oldEgress,oldHandshakes
		return err
	}
	return nil
}

func (s *Spool) First()(Report,bool){
	s.mu.Lock();defer s.mu.Unlock()
	if len(s.st.Pending)==0{return Report{},false}
	return s.st.Pending[0],true
}

func (s *Spool) Ack(bootID string,sequence uint64) error {
	s.mu.Lock();defer s.mu.Unlock()
	if len(s.st.Pending)==0{return errors.New("telemetry ACK with empty spool")}
	r:=s.st.Pending[0]
	if r.BootID!=bootID||r.Sequence!=sequence{return errors.New("telemetry ACK is out of order")}
	old:=append([]Report(nil),s.st.Pending...)
	oldAck:=s.st.AckedSequence
	s.st.Pending=append([]Report(nil),s.st.Pending[1:]...)
	s.st.AckedSequence=sequence
	if err:=s.saveLocked();err!=nil{s.st.Pending=old;s.st.AckedSequence=oldAck;return err}
	return nil
}

func (s *Spool) Snapshot() spoolState {
	s.mu.Lock();defer s.mu.Unlock()
	out:=s.st
	out.Pending=append([]Report(nil),s.st.Pending...)
	sort.SliceStable(out.Pending,func(i,j int)bool{return out.Pending[i].Sequence<out.Pending[j].Sequence})
	return out
}

func (s *Spool) Path() string { return s.path }
