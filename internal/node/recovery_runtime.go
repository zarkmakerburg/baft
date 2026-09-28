package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"crypto/tls"
	"time"

	carrierh2 "github.com/zarkmakerburg/baft/internal/carrier/h2"
	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
	"github.com/zarkmakerburg/baft/internal/session"
)

type openedRuntimeCarrier struct {
	carrier session.Carrier
	client  *carrierh2.Client
	pw      *io.PipeWriter
	body    io.ReadCloser
}

func (o *openedRuntimeCarrier) close() {
	if o==nil{return}
	if o.pw!=nil{_ = o.pw.Close()}
	if o.body!=nil{_ = o.body.Close()}
	if o.client!=nil{o.client.CloseIdleConnections()}
}

func recoveryRetention(cfg config.Config) time.Duration {
	if cfg.Recovery.RetentionSeconds<=0{return 30*time.Second}
	return time.Duration(cfg.Recovery.RetentionSeconds)*time.Second
}

func (r *Runtime) registerSession(p *session.Peer) error {
	if p==nil{return errors.New("nil session")}
	id:=p.SessionID()
	if id==""{return errors.New("session id unavailable")}
	r.sessionMu.Lock();defer r.sessionMu.Unlock()
	if r.sessions==nil{r.sessions=map[string]*session.Peer{}}
	if old:=r.sessions[id];old!=nil&&old!=p{return errors.New("duplicate live session id")}
	r.sessions[id]=p
	return nil
}

func (r *Runtime) unregisterSession(p *session.Peer) {
	if p==nil{return}
	id:=p.SessionID()
	if id==""{return}
	r.sessionMu.Lock()
	if r.sessions[id]==p{delete(r.sessions,id)}
	r.sessionMu.Unlock()
}

func (r *Runtime) sessionByID(id string)*session.Peer{
	r.sessionMu.Lock();defer r.sessionMu.Unlock()
	return r.sessions[id]
}

func (r *Runtime) recoveryFail(stage string) error {
	if r.recoveryFault==nil{return nil}
	return r.recoveryFault(stage)
}

func (r *Runtime) nextRecoveryCandidate(shard int) string {
	n:=r.recoverySeq.Add(1)
	return fmt.Sprintf("shard-%d-replacement-%d",shard,n)
}

func (r *Runtime) openRuntimeCarrier(ctx context.Context,cfg config.Config,tlsCfg *tls.Config)(*openedRuntimeCarrier,error){
	client,err:=carrierh2.NewClient("https://"+cfg.Peer.Address,tlsCfg)
	if err!=nil{return nil,err}
	var pw *io.PipeWriter
	var resp *http.Response
	var secure *securityinternal.Conn
	if cfg.Noise!=nil{
		nc,e:=noiseConfig(cfg);if e!=nil{client.CloseIdleConnections();return nil,e}
		resp,pw,secure,err=client.OpenNoise(ctx,nc)
		if err!=nil{r.handshakeErrors.Add(1)}
	}else{
		pr,writer:=io.Pipe();pw=writer
		resp,err=client.Open(ctx,pr)
	}
	if err!=nil{
		if pw!=nil{_ = pw.Close()}
		client.CloseIdleConnections()
		return nil,err
	}
	carrier:=session.Carrier{In:resp.Body,Out:pw}
	if secure!=nil{carrier=session.Carrier{In:secure,Out:secure}}
	return &openedRuntimeCarrier{carrier:carrier,client:client,pw:pw,body:resp.Body},nil
}

func (r *Runtime) recoverDialerShard(ctx context.Context,cfg config.Config,tlsCfg *tls.Config,index int,sh *dialerShard) error {
	candidate:=r.nextRecoveryCandidate(index)
	local,err:=sh.peer.BeginRecovery(candidate)
	if err!=nil{return err}
	published:=false
	defer func(){if !published{sh.peer.AbortRecovery(candidate)}}()
	if err:=r.recoveryFail("candidate_setup");err!=nil{sh.peer.RecordRecoveryFailure("candidate_setup");return fmt.Errorf("candidate setup: %w",err)}

	o,err:=r.openRuntimeCarrier(ctx,cfg,tlsCfg)
	if err!=nil{sh.peer.RecordRecoveryFailure("candidate_setup");return fmt.Errorf("candidate setup: %w",err)}
	keepCarrier:=false
	defer func(){if !keepCarrier{o.close()}}()
	if err:=r.recoveryFail("snapshot_exchange");err!=nil{sh.peer.RecordRecoveryFailure("snapshot_exchange");return fmt.Errorf("snapshot exchange: %w",err)}

	if err:=session.EncodeRecoveryOffer(o.carrier.Out,local);err!=nil{sh.peer.RecordRecoveryFailure("snapshot_exchange");return fmt.Errorf("snapshot exchange: %w",err)}
	fr,err:=protocol.Decode(o.carrier.In);if err!=nil{sh.peer.RecordRecoveryFailure("snapshot_exchange");return fmt.Errorf("snapshot exchange: %w",err)}
	peer,err:=session.DecodeRecoveryOffer(fr);if err!=nil{sh.peer.RecordRecoveryFailure("snapshot_exchange");return fmt.Errorf("snapshot exchange: %w",err)}
	if err:=sh.peer.ReconcileRecovery(candidate,peer);err!=nil{return err}

	prepared,err:=sh.peer.PrepareRecoveryCommit(ctx,candidate,o.carrier)
	if err!=nil{return err}
	if err:=session.EncodeRecoveryControl(o.carrier.Out,prepared);err!=nil{return fmt.Errorf("prepared exchange: %w",err)}
	fr,err=protocol.Decode(o.carrier.In);if err!=nil{return fmt.Errorf("prepared exchange: %w",err)}
	peerPrepared,err:=session.DecodeRecoveryControl(fr);if err!=nil{return fmt.Errorf("prepared exchange: %w",err)}
	if err:=sh.peer.ValidateRecoveryControl(peerPrepared,session.RecoveryPhasePrepared);err!=nil{return err}

	ready:=prepared;ready.Phase=session.RecoveryPhaseCommitReady
	if err:=session.EncodeRecoveryControl(o.carrier.Out,ready);err!=nil{return fmt.Errorf("commit readiness: %w",err)}
	fr,err=protocol.Decode(o.carrier.In);if err!=nil{return fmt.Errorf("commit readiness: %w",err)}
	peerReady,err:=session.DecodeRecoveryControl(fr);if err!=nil{return fmt.Errorf("commit readiness: %w",err)}
	if err:=sh.peer.ValidateRecoveryControl(peerReady,session.RecoveryPhaseCommitReady);err!=nil{return err}

	// Both endpoints are reconciled and PREPARED. This is the last dialer
	// failure point at which neither authority has changed.
	if err:=r.recoveryFail("before_commit");err!=nil{sh.peer.RecordRecoveryFailure("commit");return fmt.Errorf("before commit: %w",err)}

	commitCtl:=prepared;commitCtl.Phase=session.RecoveryPhaseCommit
	if err:=session.EncodeRecoveryControl(o.carrier.Out,commitCtl);err!=nil{return fmt.Errorf("commit send: %w",err)}
	fr,err=protocol.Decode(o.carrier.In);if err!=nil{return fmt.Errorf("commit ack: %w",err)}
	peerAck,err:=session.DecodeRecoveryControl(fr);if err!=nil{return fmt.Errorf("commit ack: %w",err)}
	if err:=sh.peer.ValidateRecoveryControl(peerAck,session.RecoveryPhaseCommitAck);err!=nil{return err}

	res,err:=sh.peer.PublishRecoveryCommit(commitCtl)
	if err!=nil{return err}
	if !res.Committed{return errors.New("recovery authority was not committed")}
	published=true
	sh.replaceCarrier(o);keepCarrier=true

	// Final ACK tells the listener that both authorities now name the same
	// transaction. Duplicate ACK is harmless and improves ACK-loss recovery.
	finalAck:=commitCtl;finalAck.Phase=session.RecoveryPhaseCommitAck
	if err:=session.EncodeRecoveryControl(o.carrier.Out,finalAck);err!=nil{
		return sh.peer.MarkPostCommitFailure(fmt.Errorf("final commit ack: %w",err),commitCtl)
	}
	_ = session.EncodeRecoveryControl(o.carrier.Out,finalAck)

	if err:=sh.peer.FinalizeRecoveryCommit(ctx,commitCtl);err!=nil{return err}
	return nil
}

func (r *Runtime) handleIncomingRecovery(hctx context.Context,cfg config.Config,in io.Reader,out io.Writer,peer carrierh2.PeerInfo,first protocol.Frame)(bool,error){
	if first.Type!=protocol.TypeResumeState{return false,nil}
	remote,err:=session.DecodeRecoveryOffer(first);if err!=nil{return true,err}
	p:=r.sessionByID(remote.Snapshot.SessionID)
	if p==nil{return true,errors.New("recovery session not found")}
	if p.PeerIdentity()!=peer.Identity{return true,errors.New("recovery peer identity mismatch")}

	local,err:=p.BeginRecovery(remote.CandidateID)
	if err!=nil{return true,err}
	published:=false
	defer func(){if !published{p.AbortRecovery(remote.CandidateID)}}()
	if local.NextEpoch!=remote.NextEpoch{return true,errors.New("recovery epoch mismatch")}
	if err:=p.ReconcileRecovery(remote.CandidateID,remote);err!=nil{return true,err}
	if err:=session.EncodeRecoveryOffer(out,local);err!=nil{return true,fmt.Errorf("snapshot exchange: %w",err)}

	prepared,err:=p.PrepareRecoveryCommit(hctx,remote.CandidateID,session.Carrier{In:in,Out:out})
	if err!=nil{return true,err}
	fr,err:=protocol.Decode(in);if err!=nil{return true,fmt.Errorf("prepared exchange: %w",err)}
	peerPrepared,err:=session.DecodeRecoveryControl(fr);if err!=nil{return true,fmt.Errorf("prepared exchange: %w",err)}
	if err:=p.ValidateRecoveryControl(peerPrepared,session.RecoveryPhasePrepared);err!=nil{return true,err}
	if err:=session.EncodeRecoveryControl(out,prepared);err!=nil{return true,fmt.Errorf("prepared exchange: %w",err)}

	fr,err=protocol.Decode(in);if err!=nil{return true,fmt.Errorf("commit readiness: %w",err)}
	ready,err:=session.DecodeRecoveryControl(fr);if err!=nil{return true,fmt.Errorf("commit readiness: %w",err)}
	if err:=p.ValidateRecoveryControl(ready,session.RecoveryPhaseCommitReady);err!=nil{return true,err}
	if err:=r.recoveryFail("listener_before_commit");err!=nil{p.RecordRecoveryFailure("commit");return true,fmt.Errorf("listener before commit: %w",err)}
	readyAck:=prepared;readyAck.Phase=session.RecoveryPhaseCommitReady
	if err:=session.EncodeRecoveryControl(out,readyAck);err!=nil{return true,fmt.Errorf("commit readiness: %w",err)}

	for {
		fr,err=protocol.Decode(in);if err!=nil{
			if published{return true,p.MarkPostCommitFailure(fmt.Errorf("commit exchange: %w",err),prepared)}
			return true,fmt.Errorf("commit exchange: %w",err)
		}
		ctl,err:=session.DecodeRecoveryControl(fr);if err!=nil{return true,err}
		switch ctl.Phase {
		case session.RecoveryPhaseCommit:
			if err:=p.ValidateRecoveryControl(ctl,session.RecoveryPhaseCommit);err!=nil{return true,err}
			res,err:=p.PublishRecoveryCommit(ctl)
			if err!=nil{return true,err}
			if !res.Committed{return true,errors.New("listener authority was not committed")}
			published=true
			ack:=ctl;ack.Phase=session.RecoveryPhaseCommitAck
			if err:=session.EncodeRecoveryControl(out,ack);err!=nil{return true,p.MarkPostCommitFailure(fmt.Errorf("commit ack: %w",err),ctl)}
			// If the first ACK is lost, a duplicate COMMIT is accepted below
			// and receives the same idempotent ACK without another epoch/replay.
		case session.RecoveryPhaseCommitAck:
			if !published{return true,recovery.ErrStateMismatch}
			if err:=p.ValidateRecoveryControl(ctl,session.RecoveryPhaseCommitAck);err!=nil{return true,err}
			if err:=p.FinalizeRecoveryCommit(hctx,ctl);err!=nil{return true,err}
			return true,nil
		default:
			return true,recovery.ErrStateMismatch
		}
	}
}

