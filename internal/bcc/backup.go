package bcc

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	backupMagic = "BAFT-BCC-BACKUP"
	BackupSchemaVersion = 2
)

type BackupHeader struct {
	Magic          string    `json:"magic"`
	SchemaVersion  int       `json:"schema_version"`
	CreatedAt      time.Time `json:"created_at"`
	PayloadSHA256  string    `json:"payload_sha256"`
	AuditSequence  uint64    `json:"audit_sequence"`
	AuditHash      string    `json:"audit_hash"`
}

type encryptedBackup struct {
	Header     BackupHeader `json:"header"`
	Nonce      string       `json:"nonce"`
	Ciphertext string       `json:"ciphertext"`
}

type backupAlertState struct {
	ActiveAlerts                   map[string]Alert `json:"active_alerts"`
	TelemetryStaleAfterNanos       int64            `json:"telemetry_stale_after_nanos"`
	HandshakeErrorRateMilliPerMin  int64            `json:"handshake_error_rate_milli_per_min"`
	IntervalNanos                  int64            `json:"interval_nanos"`
}

type backupPayload struct {
	State  state            `json:"state"`
	Alerts backupAlertState `json:"alerts"`
	Audit  []AuditEntry     `json:"audit"`
}

type BackupRetention struct {
	Daily  int
	Weekly int
}

func BackupKeyFromEnv()([]byte,error){
	raw:=strings.TrimSpace(os.Getenv("BAFT_BCC_BACKUP_KEY"))
	if raw==""{return nil,errors.New("BAFT_BCC_BACKUP_KEY is not set")}
	key,err:=base64.StdEncoding.DecodeString(raw)
	if err!=nil{return nil,errors.New("BAFT_BCC_BACKUP_KEY must be base64")}
	if len(key)!=32{return nil,errors.New("BAFT_BCC_BACKUP_KEY must decode to exactly 32 bytes")}
	return key,nil
}

func newAEAD(key []byte)(cipher.AEAD,error){
	if len(key)!=32{return nil,errors.New("backup key must be exactly 32 bytes")}
	block,err:=aes.NewCipher(key);if err!=nil{return nil,err}
	return cipher.NewGCM(block)
}

func normalizeState(st *state){
	if st.Nodes==nil{st.Nodes=map[string]Node{}}
	if st.Jobs==nil{st.Jobs=map[string]Job{}}
	if st.Tunnels==nil{st.Tunnels=map[string]Tunnel{}}
	if st.Health==nil{st.Health=map[string]NodeHealthRecord{}}
	if st.Discovery==nil{st.Discovery=map[string]NodeDiscovery{}}
	if st.CertRotations==nil{st.CertRotations=map[string]CertRotation{}}
	if st.IRPool==nil{st.IRPool=map[string]IRPoolMember{}}
	if st.EXRoutes==nil{st.EXRoutes=map[string]ExplicitEXRoute{}}
	if st.TopologyBindings==nil{st.TopologyBindings=map[string]TopologyBinding{}}
	if st.IngressSelections==nil{st.IngressSelections=map[string]IngressSelection{}}
	if st.IngressDistributions==nil{st.IngressDistributions=map[string]IngressDistribution{}}
	if st.SmartIngressPlans==nil{st.SmartIngressPlans=map[string]SmartIngressPlan{}}
	if st.Finance==nil{st.Finance=map[string]NodeFinance{}}
	if st.Policies==nil{st.Policies=map[string]FinancePolicy{}}
	if st.RateHistory==nil{st.RateHistory=map[string][]FinancePolicy{}}
	if st.Telemetry==nil{st.Telemetry=map[string]TelemetryCursor{}}
	if st.History==nil{st.History=map[string][]HistoryPoint{}}
	if st.ActiveAlerts==nil{st.ActiveAlerts=map[string]Alert{}}
	if st.RetiredBootIDs==nil{st.RetiredBootIDs=map[string]map[string]bool{}}
	if st.NextJob==0{st.NextJob=1}
	if st.NextRateVersion==0{st.NextRateVersion=1}
	if st.NextTelemetryIngestID==0{
		var maxIngest uint64
		for _,cur:=range st.Telemetry{if cur.IngestID>maxIngest{maxIngest=cur.IngestID}}
		st.NextTelemetryIngestID=maxIngest+1
		if st.NextTelemetryIngestID==0{st.NextTelemetryIngestID=1}
	}
}

func cloneState(st state)(state,error){
	b,err:=json.Marshal(st);if err!=nil{return state{},err}
	var out state
	if err:=json.Unmarshal(b,&out);err!=nil{return state{},err}
	normalizeState(&out)
	return out,nil
}

func (s *Store) snapshotState()(state,error){
	s.mu.Lock();defer s.mu.Unlock()
	return cloneState(s.st)
}

func writeAtomic(path string,data []byte,mode os.FileMode) error {
	if err:=os.MkdirAll(filepath.Dir(path),0700);err!=nil{return err}
	tmp:=path+".tmp"
	f,err:=os.OpenFile(tmp,os.O_CREATE|os.O_TRUNC|os.O_WRONLY,mode);if err!=nil{return err}
	if _,err=f.Write(data);err!=nil{_ = f.Close();_ = os.Remove(tmp);return err}
	if err=f.Sync();err!=nil{_ = f.Close();_ = os.Remove(tmp);return err}
	if err=f.Close();err!=nil{_ = os.Remove(tmp);return err}
	if err=os.Rename(tmp,path);err!=nil{_ = os.Remove(tmp);return err}
	return fsyncDir(path)
}

func verifyAuditEntries(entries []AuditEntry) error {
	var prev string
	var seq uint64
	for _,e:=range entries{
		if e.Sequence!=seq+1{return fmt.Errorf("audit sequence discontinuity at %d",e.Sequence)}
		if e.PrevHash!=prev{return fmt.Errorf("audit hash chain mismatch at %d",e.Sequence)}
		want,err:=auditHash(e);if err!=nil{return err}
		if e.Hash!=want{return fmt.Errorf("audit entry hash mismatch at %d",e.Sequence)}
		seq=e.Sequence;prev=e.Hash
	}
	return nil
}

func (s *Server) snapshotBackupPayload()(backupPayload,AuditAnchor,error){
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	st,err:=s.store.snapshotState();if err!=nil{return backupPayload{},AuditAnchor{},err}
	entries,err:=s.audit.List(0);if err!=nil{return backupPayload{},AuditAnchor{},err}
	if err:=verifyAuditEntries(entries);err!=nil{return backupPayload{},AuditAnchor{},err}
	anchor:=AuditAnchor{}
	if len(entries)>0{
		last:=entries[len(entries)-1]
		anchor=AuditAnchor{Sequence:last.Sequence,Hash:last.Hash,Timestamp:s.now().UTC()}
	}

	s.alertMu.Lock()
	active:=make(map[string]Alert,len(s.activeAlerts))
	for k,v:=range s.activeAlerts{active[k]=v}
	ac:=s.alertConfig
	s.alertMu.Unlock()
	st.ActiveAlerts=cloneAlerts(active)

	return backupPayload{
		State:st,
		Alerts:backupAlertState{
			ActiveAlerts:active,
			TelemetryStaleAfterNanos:int64(ac.TelemetryStaleAfter),
			HandshakeErrorRateMilliPerMin:ac.HandshakeErrorRateMilliPerMin,
			IntervalNanos:int64(ac.Interval),
		},
		Audit:entries,
	},anchor,nil
}

func (s *Server) CreateBackupBytes(key []byte,createdAt time.Time)([]byte,BackupHeader,error){
	s.backupMu.Lock();defer s.backupMu.Unlock()
	if createdAt.IsZero(){createdAt=s.now().UTC()}else{createdAt=createdAt.UTC()}
	payload,anchor,err:=s.snapshotBackupPayload();if err!=nil{return nil,BackupHeader{},err}
	plain,err:=json.Marshal(payload);if err!=nil{return nil,BackupHeader{},err}
	sum:=sha256.Sum256(plain)
	header:=BackupHeader{
		Magic:backupMagic,SchemaVersion:BackupSchemaVersion,CreatedAt:createdAt,
		PayloadSHA256:hex.EncodeToString(sum[:]),AuditSequence:anchor.Sequence,AuditHash:anchor.Hash,
	}
	aad,err:=json.Marshal(header);if err!=nil{return nil,BackupHeader{},err}
	aead,err:=newAEAD(key);if err!=nil{return nil,BackupHeader{},err}
	nonce:=make([]byte,aead.NonceSize())
	if _,err:=rand.Read(nonce);err!=nil{return nil,BackupHeader{},err}
	ciphertext:=aead.Seal(nil,nonce,plain,aad)
	env:=encryptedBackup{
		Header:header,
		Nonce:base64.StdEncoding.EncodeToString(nonce),
		Ciphertext:base64.StdEncoding.EncodeToString(ciphertext),
	}
	out,err:=json.MarshalIndent(env,"","  ")
	if err!=nil{return nil,BackupHeader{},err}
	return out,header,nil
}

func (s *Server) BackupToFile(path string,key []byte,createdAt time.Time)(BackupHeader,error){
	data,header,err:=s.CreateBackupBytes(key,createdAt);if err!=nil{return BackupHeader{},err}
	if err:=writeAtomic(path,data,0600);err!=nil{return BackupHeader{},err}
	return header,nil
}

func decodeBackup(data,key []byte)(backupPayload,BackupHeader,error){
	var env encryptedBackup
	if err:=json.Unmarshal(data,&env);err!=nil{return backupPayload{},BackupHeader{},errors.New("invalid backup envelope")}
	if env.Header.Magic!=backupMagic{return backupPayload{},BackupHeader{},errors.New("invalid backup magic")}
	if env.Header.SchemaVersion!=BackupSchemaVersion{return backupPayload{},BackupHeader{},fmt.Errorf("unsupported backup schema version %d",env.Header.SchemaVersion)}
	aad,err:=json.Marshal(env.Header);if err!=nil{return backupPayload{},BackupHeader{},err}
	aead,err:=newAEAD(key);if err!=nil{return backupPayload{},BackupHeader{},err}
	nonce,err:=base64.StdEncoding.DecodeString(env.Nonce);if err!=nil{return backupPayload{},BackupHeader{},errors.New("invalid backup nonce")}
	if len(nonce)!=aead.NonceSize(){return backupPayload{},BackupHeader{},errors.New("invalid backup nonce size")}
	ciphertext,err:=base64.StdEncoding.DecodeString(env.Ciphertext);if err!=nil{return backupPayload{},BackupHeader{},errors.New("invalid backup ciphertext")}
	plain,err:=aead.Open(nil,nonce,ciphertext,aad);if err!=nil{return backupPayload{},BackupHeader{},errors.New("backup authentication failed")}
	sum:=sha256.Sum256(plain)
	if hex.EncodeToString(sum[:])!=env.Header.PayloadSHA256{return backupPayload{},BackupHeader{},errors.New("backup checksum mismatch")}
	var payload backupPayload
	if err:=json.Unmarshal(plain,&payload);err!=nil{return backupPayload{},BackupHeader{},errors.New("invalid backup payload")}
	normalizeState(&payload.State)
	if payload.Alerts.ActiveAlerts==nil{payload.Alerts.ActiveAlerts=map[string]Alert{}}
	if err:=verifyAuditEntries(payload.Audit);err!=nil{return backupPayload{},BackupHeader{},fmt.Errorf("backup audit invalid: %w",err)}
	if len(payload.Audit)==0{
		if env.Header.AuditSequence!=0||env.Header.AuditHash!=""{return backupPayload{},BackupHeader{},errors.New("backup audit header mismatch")}
	}else{
		last:=payload.Audit[len(payload.Audit)-1]
		if last.Sequence!=env.Header.AuditSequence||last.Hash!=env.Header.AuditHash{return backupPayload{},BackupHeader{},errors.New("backup audit header mismatch")}
	}
	return payload,env.Header,nil
}

func readBackupFile(path string,key []byte)(backupPayload,BackupHeader,error){
	b,err:=os.ReadFile(path);if err!=nil{return backupPayload{},BackupHeader{},err}
	return decodeBackup(b,key)
}

func auditContainsAnchor(entries []AuditEntry,seq uint64,hash string) bool {
	if seq==0&&hash==""{return true}
	if seq==0||hash==""||uint64(len(entries))<seq{return false}
	return entries[seq-1].Sequence==seq&&entries[seq-1].Hash==hash
}

func cursorAhead(cur,bak TelemetryCursor,hasBak bool) bool {
	if cur.NodeID==""{return false}
	if !hasBak{return true}
	if cur.IngestID!=0||bak.IngestID!=0{return cur.IngestID>bak.IngestID}
	// Compatibility for pre-schema-v2 in-memory state only. New v2 backups
	// always carry server-side ingestion ids and never rely on node clocks.
	if cur.BootID==bak.BootID{return cur.Sequence>bak.Sequence}
	return cur.LastTelemetry.After(bak.LastTelemetry)
}

func latestRateVersion(h []FinancePolicy) uint64 {
	var v uint64
	for _,p:=range h{if p.Version>v{v=p.Version}}
	return v
}

func mergeAntiRollback(restored *state,current state,createdAt time.Time){
	preserveLedgerNodes:=map[string]bool{}
	for id,cur:=range current.Telemetry{
		bak,ok:=restored.Telemetry[id]
		if !cursorAhead(cur,bak,ok){continue}
		restored.Telemetry[id]=cur
		if f,ok:=current.Finance[id];ok{
			restored.Finance[id]=f
			// The sub-micro remainder belongs to the totals it was carried from.
			if r,ok:=current.FinanceRemainders[id];ok{
				if restored.FinanceRemainders==nil{restored.FinanceRemainders=map[string]financeRemainder{}}
				restored.FinanceRemainders[id]=r
			}else{
				delete(restored.FinanceRemainders,id)
			}
		}
		if h,ok:=current.History[id];ok{restored.History[id]=append([]HistoryPoint(nil),h...)}
		if latestRateVersion(current.RateHistory[id])>latestRateVersion(restored.RateHistory[id]){
			restored.RateHistory[id]=append([]FinancePolicy(nil),current.RateHistory[id]...)
			if p,ok:=current.Policies[id];ok{restored.Policies[id]=p}
		}
		preserveLedgerNodes[id]=true
	}
	if len(preserveLedgerNodes)>0{
		ledger:=make([]FinanceLedgerEntry,0,len(restored.FinanceLedger)+len(current.FinanceLedger))
		for _,e:=range restored.FinanceLedger{if !preserveLedgerNodes[e.NodeID]{ledger=append(ledger,e)}}
		for _,e:=range current.FinanceLedger{if preserveLedgerNodes[e.NodeID]{ledger=append(ledger,e)}}
		sort.SliceStable(ledger,func(i,j int)bool{return ledger[i].Timestamp.Before(ledger[j].Timestamp)})
		restored.FinanceLedger=ledger
	}

	for id,cur:=range current.Nodes{
		bak,exists:=restored.Nodes[id]
		if !exists{continue}
		preserveSecurity:=cur.Revoked
		if cur.UpdatedAt.After(createdAt) &&
			(cur.AgentTokenHash!=bak.AgentTokenHash||cur.PreviousAgentTokenHash!=bak.PreviousAgentTokenHash||!cur.PreviousAgentTokenUntil.Equal(bak.PreviousAgentTokenUntil)){
			preserveSecurity=true
		}
		if preserveSecurity{
			bak.AgentTokenHash=cur.AgentTokenHash
			bak.PreviousAgentTokenHash=cur.PreviousAgentTokenHash
			bak.PreviousAgentTokenUntil=cur.PreviousAgentTokenUntil
			bak.Revoked=cur.Revoked
			bak.RevokedAt=cur.RevokedAt
			bak.RevokeReason=cur.RevokeReason
			if cur.Revoked{bak.Health="down"}
			if cur.UpdatedAt.After(bak.UpdatedAt){bak.UpdatedAt=cur.UpdatedAt}
			restored.Nodes[id]=bak
		}
	}
	for nodeID,ids:=range current.RetiredBootIDs{
		if restored.RetiredBootIDs[nodeID]==nil{restored.RetiredBootIDs[nodeID]=map[string]bool{}}
		for bootID,v:=range ids{if v{restored.RetiredBootIDs[nodeID][bootID]=true}}
	}
	if current.NextRateVersion>restored.NextRateVersion{restored.NextRateVersion=current.NextRateVersion}
	if current.NextTelemetryIngestID>restored.NextTelemetryIngestID{restored.NextTelemetryIngestID=current.NextTelemetryIngestID}
}

func auditBytes(entries []AuditEntry)([]byte,error){
	var out []byte
	for _,e:=range entries{
		b,err:=json.Marshal(e);if err!=nil{return nil,err}
		out=append(out,b...)
		out=append(out,'\n')
	}
	return out,nil
}

func (s *Server) RestoreFromFile(path string,key []byte) error {
	return s.restoreTransactional(path,key)
}

func prunePrefix(dir,prefix string,keep int) error {
	if keep<0{keep=0}
	entries,err:=os.ReadDir(dir)
	if errors.Is(err,os.ErrNotExist){return nil}
	if err!=nil{return err}
	var names []string
	for _,e:=range entries{if !e.IsDir()&&strings.HasPrefix(e.Name(),prefix)&&strings.HasSuffix(e.Name(),".baftbak"){names=append(names,e.Name())}}
	sort.Strings(names)
	if len(names)<=keep{return nil}
	for _,name:=range names[:len(names)-keep]{if err:=os.Remove(filepath.Join(dir,name));err!=nil{return err}}
	return nil
}

func (s *Server) ScheduledBackup(dir string,key []byte,now time.Time,ret BackupRetention)([]string,error){
	if ret.Daily<=0{ret.Daily=7}
	if ret.Weekly<=0{ret.Weekly=4}
	if now.IsZero(){now=s.now().UTC()}else{now=now.UTC()}
	data,_,err:=s.CreateBackupBytes(key,now);if err!=nil{return nil,err}
	if err:=os.MkdirAll(dir,0700);err!=nil{return nil,err}
	stamp:=now.Format("20060102T150405Z")
	var paths []string
	daily:=filepath.Join(dir,"daily-"+stamp+".baftbak")
	if err:=writeAtomic(daily,data,0600);err!=nil{return nil,err}
	paths=append(paths,daily)
	if now.Weekday()==time.Monday{
		weekly:=filepath.Join(dir,"weekly-"+stamp+".baftbak")
		if err:=writeAtomic(weekly,data,0600);err!=nil{return nil,err}
		paths=append(paths,weekly)
	}
	if err:=prunePrefix(dir,"daily-",ret.Daily);err!=nil{return nil,err}
	if err:=prunePrefix(dir,"weekly-",ret.Weekly);err!=nil{return nil,err}
	return paths,nil
}

func (s *Server) StartBackupLoop(ctx context.Context,dir string,key []byte,interval time.Duration,ret BackupRetention){
	if interval<=0{interval=24*time.Hour}
	_,_ = s.ScheduledBackup(dir,key,s.now().UTC(),ret)
	t:=time.NewTicker(interval);defer t.Stop()
	for{
		select{
		case <-ctx.Done():return
		case now:=<-t.C:_,_ = s.ScheduledBackup(dir,key,now.UTC(),ret)
		}
	}
}
