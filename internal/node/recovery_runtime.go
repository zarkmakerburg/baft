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
	committed:=false
	defer func(){if !committed{sh.peer.AbortRecovery(candidate)}}()
	if err:=r.recoveryFail("candidate_setup");err!=nil{return fmt.Errorf("candidate setup: %w",err)}

	o,err:=r.openRuntimeCarrier(ctx,cfg,tlsCfg)
	if err!=nil{return fmt.Errorf("candidate setup: %w",err)}
	defer func(){if !committed{o.close()}}()
	if err:=r.recoveryFail("snapshot_exchange");err!=nil{return fmt.Errorf("snapshot exchange: %w",err)}

	if err:=session.EncodeRecoveryOffer(o.carrier.Out,local);err!=nil{return fmt.Errorf("snapshot exchange: %w",err)}
	fr,err:=protocol.Decode(o.carrier.In);if err!=nil{return fmt.Errorf("snapshot exchange: %w",err)}
	peer,err:=session.DecodeRecoveryOffer(fr);if err!=nil{return fmt.Errorf("snapshot exchange: %w",err)}
	if err:=sh.peer.ReconcileRecovery(candidate,peer);err!=nil{return err}

	if err:=session.EncodeRecoveryDone(o.carrier.Out,session.RecoveryDone{CandidateID:candidate,NextEpoch:local.NextEpoch});err!=nil{
		return fmt.Errorf("pre-commit barrier: %w",err)
	}
	fr,err=protocol.Decode(o.carrier.In);if err!=nil{return fmt.Errorf("pre-commit barrier: %w",err)}
	done,err:=session.DecodeRecoveryDone(fr);if err!=nil{return fmt.Errorf("pre-commit barrier: %w",err)}
	if done.CandidateID!=candidate||done.NextEpoch!=local.NextEpoch{return errors.New("recovery commit barrier mismatch")}
	if err:=r.recoveryFail("before_commit");err!=nil{return fmt.Errorf("before commit: %w",err)}

	if err:=sh.peer.CommitRecovery(ctx,candidate,o.carrier);err!=nil{return err}
	sh.replaceCarrier(o)
	committed=true
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
	committed:=false
	defer func(){if !committed{p.AbortRecovery(remote.CandidateID)}}()
	if local.NextEpoch!=remote.NextEpoch{return true,errors.New("recovery epoch mismatch")}
	if err:=p.ReconcileRecovery(remote.CandidateID,remote);err!=nil{return true,err}
	if err:=session.EncodeRecoveryOffer(out,local);err!=nil{return true,fmt.Errorf("snapshot exchange: %w",err)}

	fr,err:=protocol.Decode(in);if err!=nil{return true,fmt.Errorf("pre-commit barrier: %w",err)}
	done,err:=session.DecodeRecoveryDone(fr);if err!=nil{return true,fmt.Errorf("pre-commit barrier: %w",err)}
	if done.CandidateID!=remote.CandidateID||done.NextEpoch!=remote.NextEpoch{return true,errors.New("recovery commit barrier mismatch")}
	if err:=session.EncodeRecoveryDone(out,session.RecoveryDone{CandidateID:remote.CandidateID,NextEpoch:remote.NextEpoch});err!=nil{
		return true,fmt.Errorf("pre-commit barrier: %w",err)
	}
	if err:=p.CommitRecovery(hctx,remote.CandidateID,session.Carrier{In:in,Out:out});err!=nil{return true,err}
	committed=true

	// The existing session goroutine owns candidate reads after Commit. Keep
	// this authenticated HTTP/2 request alive until its carrier is replaced,
	// closed, or the runtime shuts down.
	<-hctx.Done()
	return true,nil
}
