package bcc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const restoreJournalVersion = 1

type restoreJournal struct {
	Version       int    `json:"version"`
	Phase         string `json:"phase"`
	HadState      bool   `json:"had_state"`
	HadAudit      bool   `json:"had_audit"`
	OldStateB64   string `json:"old_state_b64,omitempty"`
	OldAuditB64   string `json:"old_audit_b64,omitempty"`
	StateStage    string `json:"state_stage"`
	AuditStage    string `json:"audit_stage"`
}

func fsyncDir(path string) error {
	dir:=filepath.Dir(path)
	f,err:=os.Open(dir);if err!=nil{return err}
	defer f.Close()
	return f.Sync()
}

func readMaybe(path string)([]byte,bool,error){
	b,err:=os.ReadFile(path)
	if errors.Is(err,os.ErrNotExist){return nil,false,nil}
	if err!=nil{return nil,false,err}
	return b,true,nil
}

func restoreJournalPath(statePath string) string { return statePath+".restore-journal.json" }

func cleanupRestoreArtifacts(j restoreJournal,statePath string){
	_ = os.Remove(j.StateStage)
	_ = os.Remove(j.AuditStage)
	_ = os.Remove(restoreJournalPath(statePath))
	_ = fsyncDir(statePath)
}

func rollbackFromJournal(j restoreJournal,statePath string) error {
	auditPath:=statePath+".audit.jsonl"
	if j.HadState {
		old,err:=base64.StdEncoding.DecodeString(j.OldStateB64);if err!=nil{return err}
		if err:=writeAtomic(statePath,old,0600);err!=nil{return err}
	}else{
		if err:=os.Remove(statePath);err!=nil&&!errors.Is(err,os.ErrNotExist){return err}
	}
	if j.HadAudit {
		old,err:=base64.StdEncoding.DecodeString(j.OldAuditB64);if err!=nil{return err}
		if err:=writeAtomic(auditPath,old,0600);err!=nil{return err}
	}else{
		if err:=os.Remove(auditPath);err!=nil&&!errors.Is(err,os.ErrNotExist){return err}
	}
	cleanupRestoreArtifacts(j,statePath)
	return nil
}

func recoverRestoreTransaction(statePath string) error {
	journalPath:=restoreJournalPath(statePath)
	raw,err:=os.ReadFile(journalPath)
	if errors.Is(err,os.ErrNotExist){return nil}
	if err!=nil{return err}
	var j restoreJournal
	if err:=json.Unmarshal(raw,&j);err!=nil{return fmt.Errorf("decode restore journal: %w",err)}
	if j.Version!=restoreJournalVersion{return fmt.Errorf("unsupported restore journal version %d",j.Version)}
	if j.Phase=="committed"{
		cleanupRestoreArtifacts(j,statePath)
		return nil
	}
	if j.Phase!="prepared"{return fmt.Errorf("invalid restore journal phase %q",j.Phase)}
	return rollbackFromJournal(j,statePath)
}

func buildRestoreAudit(base []AuditEntry,at time.Time,target string,header BackupHeader)([]AuditEntry,error){
	out:=append([]AuditEntry(nil),base...)
	e:=AuditEntry{
		Timestamp:at.UTC(),Actor:"system",RemoteIP:"local",
		Action:"backup.restore",Target:target,Outcome:"success",
		Details:map[string]any{
			"backup_created_at":header.CreatedAt.Format(time.RFC3339),
			"schema_version":header.SchemaVersion,
			"audit_hash":header.AuditHash,
		},
	}
	e.Sequence=uint64(len(out))+1
	if len(out)>0{e.PrevHash=out[len(out)-1].Hash}
	h,err:=auditHash(e);if err!=nil{return nil,err}
	e.Hash=h
	out=append(out,e)
	return out,nil
}

func validateStagedState(path string,want state) error {
	got,err:=readStateDB(path);if err!=nil{return err}
	if !bytes.Equal(canonicalStateJSON(got),canonicalStateJSON(want)){return errors.New("staged state differs from the restored state")}
	return nil
}

func (s *Server) restoreFail(stage string) error {
	if s.restoreFault==nil{return nil}
	return s.restoreFault(stage)
}

func (s *Server) restoreTransactional(path string,key []byte) error {
	s.backupMu.Lock();defer s.backupMu.Unlock()
	s.mutationMu.Lock();defer s.mutationMu.Unlock()

	// Current audit integrity is a precondition and is checked before the
	// backup is decrypted or any candidate state is prepared.
	if err:=s.audit.Verify();err!=nil{return fmt.Errorf("current audit verification failed: %w",err)}

	payload,header,err:=readBackupFile(path,key)
	if err!=nil{return err}

	current,err:=s.store.snapshotState();if err!=nil{return err}
	currentAudit,err:=s.audit.List(0);if err!=nil{return err}
	if len(currentAudit)>0&&!auditContainsAnchor(currentAudit,header.AuditSequence,header.AuditHash){
		return errors.New("restore refused: current audit does not extend backup anchor")
	}

	restored,err:=cloneState(payload.State);if err!=nil{return err}
	mergeAntiRollback(&restored,current,header.CreatedAt)
	restored.ActiveAlerts=cloneAlerts(payload.Alerts.ActiveAlerts)

	baseAudit:=payload.Audit
	if len(currentAudit)>0{baseAudit=currentAudit}
	candidateAudit,err:=buildRestoreAudit(baseAudit,s.now().UTC(),filepath.Base(path),header)
	if err!=nil{return err}
	if err:=verifyAuditEntries(candidateAudit);err!=nil{return fmt.Errorf("candidate audit invalid: %w",err)}

	auditData,err:=auditBytes(candidateAudit);if err!=nil{return err}

	stateStage:=s.store.path+".restore-state.stage"
	auditStage:=s.audit.path+".restore-audit.stage"
	_ = os.Remove(stateStage);_ = os.Remove(auditStage)

	if err:=s.restoreFail("state_stage_write");err!=nil{return err}
	if err:=writeStateDB(stateStage,restored);err!=nil{_ = os.Remove(stateStage);return err}
	if err:=validateStagedState(stateStage,restored);err!=nil{_ = os.Remove(stateStage);return fmt.Errorf("staged state verify: %w",err)}

	if err:=s.restoreFail("audit_stage_write");err!=nil{_ = os.Remove(stateStage);return err}
	if err:=writeAtomic(auditStage,auditData,0600);err!=nil{_ = os.Remove(stateStage);return err}
	if err:=s.restoreFail("audit_verify");err!=nil{_ = os.Remove(stateStage);_ = os.Remove(auditStage);return err}
	stagedAudit,err:=OpenAuditLog(auditStage)
	if err!=nil{_ = os.Remove(stateStage);_ = os.Remove(auditStage);return fmt.Errorf("staged audit open: %w",err)}
	if err:=stagedAudit.Verify();err!=nil{_ = os.Remove(stateStage);_ = os.Remove(auditStage);return fmt.Errorf("staged audit verify: %w",err)}

	oldState,hadState,err:=readMaybe(s.store.path);if err!=nil{return err}
	oldAudit,hadAudit,err:=readMaybe(s.audit.path);if err!=nil{return err}
	j:=restoreJournal{
		Version:restoreJournalVersion,Phase:"prepared",HadState:hadState,HadAudit:hadAudit,
		StateStage:stateStage,AuditStage:auditStage,
	}
	if hadState{j.OldStateB64=base64.StdEncoding.EncodeToString(oldState)}
	if hadAudit{j.OldAuditB64=base64.StdEncoding.EncodeToString(oldAudit)}
	jraw,err:=json.Marshal(j);if err!=nil{return err}
	if err:=writeAtomic(restoreJournalPath(s.store.path),jraw,0600);err!=nil{return err}

	if err:=s.restoreFail("before_commit");err!=nil{
		_ = rollbackFromJournal(j,s.store.path)
		return err
	}

	if err:=os.Rename(stateStage,s.store.path);err!=nil{
		_ = rollbackFromJournal(j,s.store.path)
		return err
	}
	if err:=fsyncDir(s.store.path);err!=nil{
		_ = rollbackFromJournal(j,s.store.path)
		return err
	}
	if err:=s.restoreFail("after_state_commit");err!=nil{
		_ = rollbackFromJournal(j,s.store.path)
		return err
	}
	if err:=os.Rename(auditStage,s.audit.path);err!=nil{
		_ = rollbackFromJournal(j,s.store.path)
		return err
	}
	if err:=fsyncDir(s.audit.path);err!=nil{
		_ = rollbackFromJournal(j,s.store.path)
		return err
	}

	reopened,err:=OpenAuditLog(s.audit.path)
	if err!=nil{
		_ = rollbackFromJournal(j,s.store.path)
		return fmt.Errorf("committed audit reopen: %w",err)
	}
	if err:=reopened.Verify();err!=nil{
		_ = rollbackFromJournal(j,s.store.path)
		return fmt.Errorf("committed audit verify: %w",err)
	}

	j.Phase="committed"
	jraw,err=json.Marshal(j);if err!=nil{
		_ = rollbackFromJournal(j,s.store.path)
		return err
	}
	if err:=writeAtomic(restoreJournalPath(s.store.path),jraw,0600);err!=nil{
		_ = rollbackFromJournal(j,s.store.path)
		return err
	}

	// RAM becomes visible only after both files are committed and verified.
	s.store.mu.Lock()
	s.store.st=restored
	s.store.mu.Unlock()
	s.audit=reopened

	s.alertMu.Lock()
	s.activeAlerts=cloneAlerts(payload.Alerts.ActiveAlerts)
	if payload.Alerts.TelemetryStaleAfterNanos>0{s.alertConfig.TelemetryStaleAfter=time.Duration(payload.Alerts.TelemetryStaleAfterNanos)}
	if payload.Alerts.HandshakeErrorRateMilliPerMin>0{s.alertConfig.HandshakeErrorRateMilliPerMin=payload.Alerts.HandshakeErrorRateMilliPerMin}
	if payload.Alerts.IntervalNanos>0{s.alertConfig.Interval=time.Duration(payload.Alerts.IntervalNanos)}
	s.alertMu.Unlock()

	cleanupRestoreArtifacts(j,s.store.path)
	return nil
}
