package node

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	carrierh2 "github.com/zarkmakerburg/baft/internal/carrier/h2"
	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/recovery"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
	"github.com/zarkmakerburg/baft/internal/session"
)

func recoveryRetention(cfg config.Config) time.Duration {
	d:=time.Duration(cfg.Recovery.RetentionSeconds)*time.Second
	if d<=0 { d=30*time.Second }
	if d>300*time.Second { d=300*time.Second }
	return d
}

func (r *Runtime) registerSession(p *session.Peer) error {
	if p==nil{return errors.New("nil session")}
	id:=p.SessionID()
	if id==""{return errors.New("session id is empty")}
	r.sessionMu.Lock();defer r.sessionMu.Unlock()
	if old:=r.sessions[id];old!=nil&&old!=p{return errors.New("duplicate live session id")}
	r.sessions[id]=p
	return nil
}
func (r *Runtime) unregisterSession(p *session.Peer) {
	if p==nil{return}
	id:=p.SessionID()
	r.sessionMu.Lock()
	if r.sessions[id]==p{delete(r.sessions,id)}
	r.sessionMu.Unlock()
}
func (r *Runtime) lookupSession(id string)*session.Peer{
	r.sessionMu.Lock();defer r.sessionMu.Unlock();return r.sessions[id]
}

type openedRuntimeCarrier struct {
	client *carrierh2.Client
	pw *io.PipeWriter
	body io.ReadCloser
	carrier session.Carrier
}

func (o *openedRuntimeCarrier) close(){
	if o==nil{return}
	if o.pw!=nil{_ = o.pw.Close()}
	if o.body!=nil{_ = o.body.Close()}
	if o.client!=nil{o.client.CloseIdleConnections()}
}

func openRuntimeCarrier(ctx context.Context,cfg config.Config,tlsCfg *tls.Config)(*openedRuntimeCarrier,error){
	client,err:=carrierh2.NewClient("https://"+cfg.Peer.Address,tlsCfg)
	if err!=nil{return nil,err}
	var pw *io.PipeWriter
	var resp *http.Response
	var secure *securityinternal.Conn
	if cfg.Noise!=nil{
		nc,e:=noiseConfig(cfg);if e!=nil{client.CloseIdleConnections();return nil,e}
		resp,pw,secure,err=client.OpenNoise(ctx,nc)
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
	return &openedRuntimeCarrier{client:client,pw:pw,body:resp.Body,carrier:carrier},nil
}

func (r *Runtime) handleIncomingRecovery(ctx context.Context,cfg config.Config,in io.Reader,out io.Writer,peer carrierh2.PeerInfo,first protocol.Frame)(bool,error){
	if !cfg.Recovery.Enabled||first.Type!=protocol.TypeResumeState{return false,nil}
	remote,err:=session.DecodeRecoveryOffer(first);if err!=nil{return true,err}
	p:=r.lookupSession(remote.Snapshot.SessionID)
	if p==nil{return true,recovery.ErrStateMismatch}
	if p.PeerIdentity()!=peer.Identity{return true,errors.New("recovery authenticated peer identity mismatch")}
	local,err:=p.BeginRecovery(remote.CandidateID)
	if err!=nil{return true,err}
	committed:=false
	defer func(){if !committed{p.AbortRecovery(remote.CandidateID)}}()
	if local.NextEpoch!=remote.NextEpoch{return true,recovery.ErrStaleEpoch}
	if err:=p.ReconcileRecovery(remote.CandidateID,remote);err!=nil{return true,err}
	if err:=session.EncodeRecoveryOffer(out,local);err!=nil{return true,err}
	doneFrame,err:=protocol.Decode(in);if err!=nil{return true,err}
	done,err:=session.DecodeRecoveryDone(doneFrame);if err!=nil{return true,err}
	if done.CandidateID!=remote.CandidateID||done.NextEpoch!=remote.NextEpoch{return true,recovery.ErrStateMismatch}
	// The ACK is fully written before this endpoint publishes the candidate as
	// owner. Any replay frames therefore follow the recovery barrier on wire.
	if err:=session.EncodeRecoveryDone(out,done);err!=nil{return true,err}
	if err:=p.CommitRecovery(ctx,remote.CandidateID,session.Carrier{In:in,Out:out});err!=nil{return true,err}
	committed=true
	<-ctx.Done()
	return true,ctx.Err()
}

func (r *Runtime) recoverDialerShard(ctx context.Context,cfg config.Config,tlsCfg *tls.Config,index int,sh *dialerShard) error{
	deadline:=time.Now().Add(recoveryRetention(cfg))
	for {
		if err:=ctx.Err();err!=nil{return err}
		if time.Now().After(deadline){return fmt.Errorf("%w: replacement deadline",session.ErrCarrierUnavailable)}
		candidate:=fmt.Sprintf("shard-%d-carrier-%d",index,r.recoverySeq.Add(1)+1)
		opened,err:=openRuntimeCarrier(ctx,cfg,tlsCfg)
		if err!=nil{time.Sleep(50*time.Millisecond);continue}

		local,err:=sh.peer.BeginRecovery(candidate)
		if err!=nil{opened.close();return err}
		ok:=false
		func(){
			defer func(){if !ok{sh.peer.AbortRecovery(candidate);opened.close()}}()
			if err=session.EncodeRecoveryOffer(opened.carrier.Out,local);err!=nil{return}
			var fr protocol.Frame
			fr,err=protocol.Decode(opened.carrier.In);if err!=nil{return}
			var remote session.RecoveryOffer
			remote,err=session.DecodeRecoveryOffer(fr);if err!=nil{return}
			if remote.CandidateID!=candidate||remote.NextEpoch!=local.NextEpoch{err=recovery.ErrStateMismatch;return}
			if err=sh.peer.ReconcileRecovery(candidate,remote);err!=nil{return}
			done:=session.RecoveryDone{CandidateID:candidate,NextEpoch:local.NextEpoch}
			if err=session.EncodeRecoveryDone(opened.carrier.Out,done);err!=nil{return}
			var doneFr protocol.Frame
			doneFr,err=protocol.Decode(opened.carrier.In);if err!=nil{return}
			var ack session.RecoveryDone
			ack,err=session.DecodeRecoveryDone(doneFr);if err!=nil{return}
			if ack!=done{err=recovery.ErrStateMismatch;return}
			if err=sh.peer.CommitRecovery(ctx,candidate,opened.carrier);err!=nil{return}
			sh.replaceCarrier(opened)
			ok=true
		}()
		if ok{return nil}
		if errors.Is(err,recovery.ErrPeerRestarted)||errors.Is(err,recovery.ErrStateMismatch)||errors.Is(err,recovery.ErrLeaseConflict){return err}
		time.Sleep(50*time.Millisecond)
	}
}

type shardCarrierState struct {
	mu sync.Mutex
	opened *openedRuntimeCarrier
}
