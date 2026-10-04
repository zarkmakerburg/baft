package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"crypto/tls"
	"time"

	carrierh2 "github.com/zarkmakerburg/baft/internal/carrier/h2"
	carrierws "github.com/zarkmakerburg/baft/internal/carrier/ws"
	"github.com/zarkmakerburg/baft/internal/carrier/utlsdial"
	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/recovery"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
	"github.com/zarkmakerburg/baft/internal/session"
)

var errRecoverySnapshotTransient = errors.New("recovery snapshot exchange transient")

type openedRuntimeCarrier struct {
	carrier   session.Carrier
	transport string
	// closeFn tears down the underlying physical carrier (transport-agnostic:
	// HTTP/2 client + pipe + body, or a WebSocket connection).
	closeFn func()
}

func (o *openedRuntimeCarrier) close() {
	if o==nil{return}
	if o.closeFn!=nil{o.closeFn()}
}

func recoveryRetention(cfg config.Config) time.Duration {
	if cfg.Recovery.RetentionSeconds<=0{return 30*time.Second}
	return time.Duration(cfg.Recovery.RetentionSeconds)*time.Second
}

func (r *Runtime) registerSession(p *session.Peer) error {
	if p==nil{return errors.New("nil session")}
	id:=p.SessionID()
	if id==""{return errors.New("session id unavailable")}
	r.sessionMu.Lock()
	if r.sessions==nil{r.sessions=map[string]*session.Peer{}}
	if old:=r.sessions[id];old!=nil&&old!=p{r.sessionMu.Unlock();return errors.New("duplicate live session id")}
	r.sessions[id]=p
	r.sessionMu.Unlock()
	r.recordLogicalSessionLifecycle("LOGICAL_SESSION_REGISTER","",p)
	return nil
}

func (r *Runtime) unregisterSession(p *session.Peer) {
	if p==nil{return}
	id:=p.SessionID()
	if id==""{return}
	r.sessionMu.Lock()
	removed:=r.sessions[id]==p
	if removed{delete(r.sessions,id)}
	r.sessionMu.Unlock()
	if removed{r.recordLogicalSessionLifecycle("LOGICAL_SESSION_UNREGISTER","registry owner retired",p)}
}

func (r *Runtime) sessionByID(id string)*session.Peer{
	r.sessionMu.Lock();defer r.sessionMu.Unlock()
	return r.sessions[id]
}

func (r *Runtime) recoveryFail(stage string) error {
	r.recoveryFaultMu.RLock()
	fn:=r.recoveryFault
	r.recoveryFaultMu.RUnlock()
	if fn==nil{return nil}
	return fn(stage)
}

func (r *Runtime) nextRecoveryCandidate(shard int) string {
	n:=r.recoverySeq.Add(1)
	return fmt.Sprintf("shard-%d-replacement-%d",shard,n)
}

func primaryTransport(cfg config.Config) string {
	if cfg.Transport.Primary == "" {
		return "h2"
	}
	return cfg.Transport.Primary
}

// nextRecoveryTransport keeps transport choice subordinate to the existing
// logical Session/ECRL authority. With no fallback, recovery preserves the
// historical behavior. With one configured, a failed current carrier moves to
// the alternate physical path; a later failure can move back.
func nextRecoveryTransport(cfg config.Config, current, lastAttempt string) string {
	primary := primaryTransport(cfg)
	fallback := cfg.Transport.Fallback
	if fallback == "" {
		return primary
	}
	basis := lastAttempt
	if basis == "" {
		basis = current
	}
	if basis == "" {
		basis = primary
	}
	if basis == primary {
		return fallback
	}
	if basis == fallback {
		return primary
	}
	return fallback
}

func (r *Runtime) openRuntimeCarrier(ctx context.Context,cfg config.Config,tlsCfg *tls.Config,transport string)(*openedRuntimeCarrier,error){
	return r.dialCarrierTransport(ctx,cfg,tlsCfg,transport)
}

// wsCarrierHeaders makes the WebSocket upgrade blend into ordinary browser
// traffic (a Chrome-like User-Agent and an Origin for the configured host).
func wsCarrierHeaders(serverName string) http.Header {
	h := http.Header{}
	h.Set("User-Agent", fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s.0.0.0 Safari/537.36", utlsdial.DefaultChromeMajor))
	if serverName != "" {
		h.Set("Origin", "https://"+serverName)
	}
	return h
}

// dialCarrier opens the configured primary carrier for initial Session setup.
func (r *Runtime) dialCarrier(ctx context.Context,cfg config.Config,tlsCfg *tls.Config)(*openedRuntimeCarrier,error){
	return r.dialCarrierTransport(ctx,cfg,tlsCfg,primaryTransport(cfg))
}

// dialCarrierTransport opens exactly one requested physical transport. The
// caller chooses primary vs fallback; this function never changes Session
// identity, recovery epoch, or authority ownership.
func (r *Runtime) dialCarrierTransport(ctx context.Context,cfg config.Config,tlsCfg *tls.Config,transport string)(*openedRuntimeCarrier,error){
	if transport=="ws"{
		if cfg.Noise==nil{return nil,errors.New("ws carrier requires noise")}
		nc,e:=noiseConfig(cfg);if e!=nil{return nil,e}
		// The WebSocket carrier speaks HTTP/1.1; offer that in ALPN rather than
		// the HTTP/2 the shared client config carries.
		wsTLS:=tlsCfg.Clone();wsTLS.NextProtos=[]string{"http/1.1"}
		d:=&carrierws.Dialer{Endpoint:"wss://"+cfg.Peer.Address,TLSConfig:wsTLS,Header:wsCarrierHeaders(cfg.Peer.ServerName)}
		if cfg.Transport.UTLS{
			// Present a browser-fidelity ClientHello to blend the IR->carrier TLS
			// into ordinary HTTPS; the server is still verified (and the inner
			// Noise handshake remains the end-to-end authenticator).
			roots:=tlsCfg.RootCAs
			d.TLSHandshake=func(hctx context.Context,conn net.Conn,serverName string)(net.Conn,error){
				return utlsdial.Handshake(hctx,conn,utlsdial.Config{ServerName:serverName,RootCAs:roots,NextProtos:[]string{"http/1.1"}})
			}
		}
		conn,secure,err:=d.OpenNoise(ctx,nc)
		if err!=nil{r.handshakeErrors.Add(1);return nil,err}
		return &openedRuntimeCarrier{carrier:session.Carrier{In:secure,Out:secure},transport:"ws",closeFn:func(){_ = conn.Close()}},nil
	}
	if transport!="h2"{return nil,fmt.Errorf("unsupported carrier transport %q",transport)}
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
	body:=resp.Body
	return &openedRuntimeCarrier{carrier:carrier,transport:"h2",closeFn:func(){if pw!=nil{_ = pw.Close()};if body!=nil{_ = body.Close()};client.CloseIdleConnections()}},nil
}

func (r *Runtime) recoverDialerShard(ctx context.Context,cfg config.Config,tlsCfg *tls.Config,index int,sh *dialerShard) error {
	if sh.peer.NeedsExactTransactionResolution(){
		resolved,err:=r.resolveDialerCommitUncertainty(ctx,cfg,tlsCfg,sh)
		if err!=nil{sh.peer.EnsureRecoverySignal(err);return err}
		if resolved{sh.peer.ClearPeerExactRequirement();return nil}
		if err:=r.recoveryFail("after_not_committed_resolution");err!=nil{return err}
	}

	candidate:=r.nextRecoveryCandidate(index)
	local,err:=sh.peer.BeginRecovery(candidate)
	if err!=nil{return err}
	published:=false
	defer func(){if !published{sh.peer.AbortRecovery(candidate)}}()
	if err:=r.recoveryFail("candidate_setup");err!=nil{sh.peer.RecordRecoveryFailure("candidate_setup");return fmt.Errorf("candidate setup: %w",err)}

	transport:=sh.recoveryTransport(cfg)
	o,err:=r.openRuntimeCarrier(ctx,cfg,tlsCfg,transport)
	if err!=nil{sh.peer.RecordRecoveryFailure("candidate_setup");return fmt.Errorf("candidate setup (%s): %w",transport,err)}
	keepCarrier:=false
	defer func(){if !keepCarrier{o.close()}}()
	if err:=r.recoveryFail("snapshot_exchange");err!=nil{sh.peer.RecordRecoveryFailure("snapshot_exchange");return fmt.Errorf("snapshot exchange: %w",err)}

	if err:=session.EncodeRecoveryOffer(o.carrier.Out,local);err!=nil{
		sh.peer.RecordRecoveryFailure("snapshot_exchange")
		return fmt.Errorf("%w: write offer: %v",errRecoverySnapshotTransient,err)
	}
	fr,err:=protocol.Decode(o.carrier.In);if err!=nil{
		// The listener may have rejected an asynchronous first snapshot before
		// it could return its offer. When the old carrier is already dead this
		// is safe to retry pre-commit with a fresh snapshot/candidate.
		sh.peer.RecordRecoveryFailure("snapshot_exchange")
		return fmt.Errorf("%w: read peer offer: %v",errRecoverySnapshotTransient,err)
	}
	peer,err:=session.DecodeRecoveryOffer(fr);if err!=nil{
		sh.peer.RecordRecoveryFailure("snapshot_exchange")
		return fmt.Errorf("snapshot exchange: %w",err)
	}
	if peer.ResolvePrevious{
		// The listener still owes proof for the last finalized transaction
		// (typically a FIN written on a carrier that then failed). A fresh
		// epoch would be refused until retention expires; resolve that exact
		// transaction first, which re-sends the obligation on a new carrier.
		if err:=sh.peer.DeferToPreviousTransaction(candidate);err!=nil{return fmt.Errorf("listener requires the previous transaction: %w",err)}
		return fmt.Errorf("%w: listener requires the previous transaction to be resolved first",session.ErrCommitUncertain)
	}
	// A Flow the listener has already closed can never be reconciled. Close
	// this side's copy and retry with a fresh snapshot.
	if closed:=sh.peer.ClosePeerClosedFlows(peer.ClosedStreams);closed>0||peer.Rejected{
		sh.peer.RecordRecoveryFailure("snapshot_exchange")
		return fmt.Errorf("%w: listener rejected the snapshot, closed %d flows it had already closed",errRecoverySnapshotTransient,closed)
	}
	if err:=sh.peer.ReconcileRecovery(candidate,peer);err!=nil{
		if errors.Is(err,recovery.ErrStateMismatch){
			return fmt.Errorf("%w: reconcile: %v",errRecoverySnapshotTransient,err)
		}
		return err
	}

	prepared,err:=sh.peer.PrepareRecoveryCommit(ctx,candidate,o.carrier)
	if err!=nil{return err}
	if err:=sh.peer.MarkRecoveryPrepared(prepared);err!=nil{return err}
	if err:=session.EncodeRecoveryControl(o.carrier.Out,prepared);err!=nil{return fmt.Errorf("prepared exchange: %w",err)}
	fr,err=protocol.Decode(o.carrier.In);if err!=nil{return fmt.Errorf("prepared exchange: %w",err)}
	peerPrepared,err:=session.DecodeRecoveryControl(fr);if err!=nil{return fmt.Errorf("prepared exchange: %w",err)}
	if err:=sh.peer.ValidateRecoveryControl(peerPrepared,session.RecoveryPhasePrepared);err!=nil{return err}

	ready:=prepared;ready.Phase=session.RecoveryPhaseCommitReady
	if err:=session.EncodeRecoveryControl(o.carrier.Out,ready);err!=nil{return fmt.Errorf("commit readiness: %w",err)}
	fr,err=protocol.Decode(o.carrier.In);if err!=nil{return fmt.Errorf("commit readiness: %w",err)}
	peerReady,err:=session.DecodeRecoveryControl(fr);if err!=nil{return fmt.Errorf("commit readiness: %w",err)}
	if err:=sh.peer.ValidateRecoveryControl(peerReady,session.RecoveryPhaseCommitReady);err!=nil{return err}
	if err:=sh.peer.MarkRecoveryCommitReady(prepared);err!=nil{return err}

	if err:=r.recoveryFail("before_commit");err!=nil{sh.peer.RecordRecoveryFailure("commit");return fmt.Errorf("before commit: %w",err)}
	commitCtl:=prepared;commitCtl.Phase=session.RecoveryPhaseCommit
	commitCopies:=r.recoveryControlCopiesForTest("dialer_commit_send",&commitCtl)
	if err:=session.EncodeRecoveryControl(o.carrier.Out,commitCtl);err!=nil{return fmt.Errorf("commit send: %w",err)}
	if err:=sh.peer.MarkCommitSent(commitCtl);err!=nil{return err}
	for i:=1;i<commitCopies;i++{
		if err:=session.EncodeRecoveryControl(o.carrier.Out,commitCtl);err!=nil{
			_ = sh.peer.MarkCommitUncertain(commitCtl)
			return fmt.Errorf("%w: duplicate commit send: %v",session.ErrCommitUncertain,err)
		}
	}
	if err:=r.recoveryFail("after_commit_sent_before_ack");err!=nil{
		_ = sh.peer.MarkCommitUncertain(commitCtl)
		return fmt.Errorf("%w: %v",session.ErrCommitUncertain,err)
	}
	ackReads:=r.recoveryControlCopiesForTest("dialer_commit_ack_reads",&commitCtl)
	if ackReads<commitCopies{ackReads=commitCopies}
	for i:=0;i<ackReads;i++{
		fr,err=protocol.Decode(o.carrier.In);if err!=nil{
			_ = sh.peer.MarkCommitUncertain(commitCtl)
			return fmt.Errorf("%w: commit ack: %v",session.ErrCommitUncertain,err)
		}
		peerAck,err:=session.DecodeRecoveryControl(fr);if err!=nil{
			_ = sh.peer.MarkCommitUncertain(commitCtl)
			return fmt.Errorf("%w: decode commit ack: %v",session.ErrCommitUncertain,err)
		}
		if err:=sh.peer.ValidateRecoveryControl(peerAck,session.RecoveryPhaseCommitAck);err!=nil{
			_ = sh.peer.MarkCommitUncertain(commitCtl)
			return fmt.Errorf("%w: validate commit ack: %v",session.ErrCommitUncertain,err)
		}
	}

	res,err:=sh.peer.PublishRecoveryCommit(commitCtl)
	if err!=nil{return err}
	if !res.Committed{return errors.New("recovery authority was not committed")}
	published=true
	sh.replaceCarrier(o);keepCarrier=true

	if err:=r.finishDialerFinalization(ctx,sh,o,commitCtl);err!=nil{return err}
	return nil
}

func (r *Runtime) finishDialerFinalization(ctx context.Context,sh *dialerShard,o *openedRuntimeCarrier,commitCtl session.RecoveryControl) error {
	finalCtl:=commitCtl
	finalCtl.Phase=session.RecoveryPhaseFinalize
	finalCtl.Status=session.RecoveryResolutionNone
	if err:=sh.peer.MarkFinalizationStarted(finalCtl);err!=nil{return err}

	copies:=r.recoveryControlCopiesForTest("dialer_finalize_send",&finalCtl)
	if copies<1{copies=1}
	for i:=0;i<copies;i++{
		if err:=session.EncodeRecoveryControl(o.carrier.Out,finalCtl);err!=nil{
			_ = sh.peer.MarkFinalizationUncertain(finalCtl)
			sh.peer.EnsureRecoverySignal(err)
			return fmt.Errorf("%w: FINALIZE write: %v",session.ErrFinalizationUncertain,err)
		}
	}
	// Preserve the previous test hook name while exposing an explicit finalizer
	// stage for deterministic asymmetric-final-ACK tests.
	if err:=r.recoveryFail("after_final_ack_send");err!=nil{
		_ = sh.peer.MarkFinalizationUncertain(finalCtl);sh.peer.EnsureRecoverySignal(err)
		return fmt.Errorf("%w: %v",session.ErrFinalizationUncertain,err)
	}
	if err:=r.recoveryFail("after_finalize_send");err!=nil{
		_ = sh.peer.MarkFinalizationUncertain(finalCtl);sh.peer.EnsureRecoverySignal(err)
		return fmt.Errorf("%w: %v",session.ErrFinalizationUncertain,err)
	}

	ackNeed:=r.recoveryControlCopiesForTest("dialer_finalize_ack_reads",&finalCtl)
	if ackNeed<1{ackNeed=1}
	acks:=0
	for acks<ackNeed{
		fr,err:=protocol.Decode(o.carrier.In)
		if err!=nil{
			_ = sh.peer.MarkFinalizationUncertain(finalCtl);sh.peer.EnsureRecoverySignal(err)
			return fmt.Errorf("%w: FINALIZE_ACK: %v",session.ErrFinalizationUncertain,err)
		}
		ctl,err:=session.DecodeRecoveryControl(fr);if err!=nil{
			_ = sh.peer.MarkFinalizationUncertain(finalCtl);sh.peer.EnsureRecoverySignal(err)
			return fmt.Errorf("%w: FINALIZE_ACK decode: %v",session.ErrFinalizationUncertain,err)
		}
		switch ctl.Phase{
		case session.RecoveryPhaseFinalizeAck:
			if err:=sh.peer.ValidateRecoveryControl(ctl,session.RecoveryPhaseFinalizeAck);err!=nil{
				_ = sh.peer.MarkFinalizationUncertain(finalCtl);return err
			}
			acks++
		case session.RecoveryPhaseCommitAck:
			if err:=sh.peer.ValidateRecoveryControl(ctl,session.RecoveryPhaseCommitAck);err!=nil{return err}
		case session.RecoveryPhaseStatusReply:
			if err:=sh.peer.ValidateStatusReply(ctl);err!=nil{return err}
		default:
			_ = sh.peer.MarkFinalizationUncertain(finalCtl)
			return recovery.ErrStateMismatch
		}
	}
	if err:=sh.peer.CompleteRecoveryFinalization(finalCtl);err!=nil{return err}
	if err:=sh.peer.FinalizeRecoveryCommit(ctx,finalCtl);err!=nil{return err}
	return nil
}

func (r *Runtime) resolveDialerCommitUncertainty(ctx context.Context,cfg config.Config,tlsCfg *tls.Config,sh *dialerShard)(bool,error){
	query,err:=sh.peer.CommitStatusQuery()
	if err!=nil{return false,err}
	if sh.peer.RecoveryTransactionState()==session.RecoveryTxnFinalized{
		release:=sh.peer.FreezeForExactResolution()
		defer release()
	}
	transport:=sh.recoveryTransport(cfg)
	o,err:=r.openRuntimeCarrier(ctx,cfg,tlsCfg,transport)
	if err!=nil{return false,fmt.Errorf("%w: status carrier (%s): %v",session.ErrCommitUncertain,transport,err)}
	keepCarrier:=false
	defer func(){if !keepCarrier{o.close()}}()

	queryCopies:=r.recoveryControlCopiesForTest("dialer_status_query_send",&query)
	if queryCopies<1{queryCopies=1}
	for i:=0;i<queryCopies;i++{
		if err:=session.EncodeRecoveryControl(o.carrier.Out,query);err!=nil{return false,fmt.Errorf("%w: status query: %v",session.ErrCommitUncertain,err)}
	}
	if err:=r.recoveryFail("status_query_after_send");err!=nil{return false,fmt.Errorf("%w: %v",session.ErrCommitUncertain,err)}
	fr,err:=protocol.Decode(o.carrier.In);if err!=nil{return false,fmt.Errorf("%w: status reply: %v",session.ErrCommitUncertain,err)}
	reply,err:=session.DecodeRecoveryControl(fr);if err!=nil{return false,fmt.Errorf("%w: status reply decode: %v",session.ErrCommitUncertain,err)}
	if err:=sh.peer.ValidateStatusReply(reply);err!=nil{return false,fmt.Errorf("%w: status reply validation: %v",session.ErrCommitUncertain,err)}

	switch reply.Status{
	case session.RecoveryResolutionNotCommitted:
		if err:=sh.peer.ResolveNotCommitted(reply);err!=nil{return false,err}
		return false,nil

	case session.RecoveryResolutionCommitted,session.RecoveryResolutionFinalized:
		if reply.Status==session.RecoveryResolutionFinalized{
			if err:=sh.peer.NoteFinalizedResolution(reply);err!=nil{return false,err}
		}else{
			if err:=sh.peer.NoteCommittedResolution(reply);err!=nil{return false,err}
		}
		commitCtl:=query
		commitCtl.Phase=session.RecoveryPhaseCommit
		commitCtl.Status=session.RecoveryResolutionNone
		if sh.peer.RecoveryEpoch()<commitCtl.NextEpoch{
			if err:=sh.peer.RebindPreparedRecovery(ctx,commitCtl,o.carrier);err!=nil{return false,err}
			res,err:=sh.peer.PublishRecoveryCommit(commitCtl);if err!=nil{return false,err}
			if !res.Committed{return false,errors.New("status resolution did not publish local authority")}
		}else{
			if err:=sh.peer.RebindCommittedCarrier(ctx,commitCtl,o.carrier);err!=nil{return false,err}
			// Normalize a locally uncertain exact commit before the distributed
			// finalization handshake.
			if _,err:=sh.peer.PublishRecoveryCommit(commitCtl);err!=nil{return false,err}
		}
		sh.replaceCarrier(o);keepCarrier=true

		// COMMITTED and FINALIZED status both converge through the same exact
		// idempotent FINALIZE/FINALIZE_ACK exchange. A FINALIZED reply is proof
		// that the peer has already accepted this transaction; the duplicate
		// FINALIZE simply binds the new physical carrier safely.
		if err:=r.finishDialerFinalization(ctx,sh,o,commitCtl);err!=nil{return false,err}
		return true,nil

	case session.RecoveryResolutionConflict,session.RecoveryResolutionUnknown:
		_ = sh.peer.NoteResolutionConflict(reply)
		return false,session.ErrCommitUncertain
	default:
		return false,recovery.ErrStateMismatch
	}
}

func (r *Runtime) handleIncomingRecovery(hctx context.Context,cfg config.Config,in io.Reader,out io.Writer,peer carrierh2.PeerInfo,first protocol.Frame)(bool,error){
	if first.Type==protocol.TypeResumeDone{
		ctl,err:=session.DecodeRecoveryControl(first);if err!=nil{return true,err}
		if ctl.Phase==session.RecoveryPhaseStatusQuery{
			return true,r.handleCommitStatusResolution(hctx,in,out,peer,ctl)
		}
		return true,recovery.ErrStateMismatch
	}
	if first.Type!=protocol.TypeResumeState{return false,nil}
	remote,err:=session.DecodeRecoveryOffer(first);if err!=nil{return true,err}
	p:=r.sessionByID(remote.Snapshot.SessionID)
	if p==nil{return true,errors.New("recovery session not found")}
	if p.PeerIdentity()!=peer.Identity{return true,errors.New("recovery peer identity mismatch")}
	p.RecordRecoveryDiagnosticForTest("HANDLER_CREATED",session.SenderStopUnknown,nil,0)

	// Flows the dialer has already closed, or whose unanswered OPEN it
	// abandoned when the carrier failed, cannot be recovered. Close this
	// side's copies before the snapshot so both sides describe one Flow set.
	p.ClosePeerClosedFlows(remote.ClosedStreams)
	local,err:=p.BeginRecovery(remote.CandidateID)
	if err!=nil{
		if errors.Is(err,session.ErrCommitUncertain)&&p.PreviousTransactionPinned(){
			// Tell the dialer why, so it resolves the pinned transaction
			// instead of retrying fresh epochs that are refused the same way.
			_ = session.EncodeRecoveryOffer(out,session.RecoveryOffer{CandidateID:remote.CandidateID,NextEpoch:remote.NextEpoch,Snapshot:recovery.Snapshot{SessionID:remote.Snapshot.SessionID},ResolvePrevious:true})
		}
		return true,err
	}
	published:=false
	var prepared session.RecoveryControl
	preparedKnown:=false
	defer func(){
		if published{return}
		if preparedKnown{_ = p.RememberNotCommitted(prepared);return}
		p.AbortRecovery(remote.CandidateID)
	}()
	if local.NextEpoch!=remote.NextEpoch{return true,errors.New("recovery epoch mismatch")}
	if err:=p.ReconcileRecovery(remote.CandidateID,remote);err!=nil{
		if errors.Is(err,recovery.ErrStateMismatch){
			// The dialer may still hold a Flow this side has closed. Send the
			// tombstones so its next attempt can converge; without them the
			// same mismatch repeats until retention expires.
			rejected:=local;rejected.Rejected=true
			_ = session.EncodeRecoveryOffer(out,rejected)
		}
		return true,err
	}
	if err:=session.EncodeRecoveryOffer(out,local);err!=nil{return true,fmt.Errorf("snapshot exchange: %w",err)}

	prepared,err=p.PrepareRecoveryCommit(hctx,remote.CandidateID,session.Carrier{In:in,Out:out})
	if err!=nil{return true,err}
	preparedKnown=true
	if err:=p.MarkRecoveryPrepared(prepared);err!=nil{return true,err}
	fr,err:=protocol.Decode(in);if err!=nil{return true,fmt.Errorf("prepared exchange: %w",err)}
	peerPrepared,err:=session.DecodeRecoveryControl(fr);if err!=nil{return true,fmt.Errorf("prepared exchange: %w",err)}
	if err:=p.ValidateRecoveryControl(peerPrepared,session.RecoveryPhasePrepared);err!=nil{return true,err}
	if err:=session.EncodeRecoveryControl(out,prepared);err!=nil{return true,fmt.Errorf("prepared exchange: %w",err)}

	fr,err=protocol.Decode(in);if err!=nil{return true,fmt.Errorf("commit readiness: %w",err)}
	ready,err:=session.DecodeRecoveryControl(fr);if err!=nil{return true,fmt.Errorf("commit readiness: %w",err)}
	if err:=p.ValidateRecoveryControl(ready,session.RecoveryPhaseCommitReady);err!=nil{return true,err}
	if err:=p.MarkRecoveryCommitReady(prepared);err!=nil{return true,err}
	if err:=r.recoveryFail("listener_before_commit");err!=nil{p.RecordRecoveryFailure("commit");return true,fmt.Errorf("listener before commit: %w",err)}
	readyAck:=prepared;readyAck.Phase=session.RecoveryPhaseCommitReady
	if err:=session.EncodeRecoveryControl(out,readyAck);err!=nil{return true,fmt.Errorf("commit readiness: %w",err)}

	for {
		fr,err=protocol.Decode(in);if err!=nil{
			if published{
				commitCtl:=prepared;commitCtl.Phase=session.RecoveryPhaseCommit
				_ = p.MarkCommitUncertain(commitCtl)
				return true,fmt.Errorf("%w: listener commit exchange: %v",session.ErrCommitUncertain,err)
			}
			return true,fmt.Errorf("commit exchange: %w",err)
		}
		ctl,err:=session.DecodeRecoveryControl(fr);if err!=nil{return true,err}
		switch ctl.Phase {
		case session.RecoveryPhaseCommit:
			if err:=p.ValidateRecoveryControl(ctl,session.RecoveryPhaseCommit);err!=nil{return true,err}
			if err:=r.recoveryFail("before_listener_publish");err!=nil{return true,fmt.Errorf("before listener publish: %w",err)}
			res,err:=p.PublishRecoveryCommit(ctl)
			if err!=nil{return true,err}
			if !res.Committed{return true,errors.New("listener authority was not committed")}
			published=true
			if err:=r.recoveryFail("after_listener_publish_before_commit_ack");err!=nil{
				_ = p.MarkCommitUncertain(ctl)
				return true,fmt.Errorf("%w: %v",session.ErrCommitUncertain,err)
			}
			ack:=ctl;ack.Phase=session.RecoveryPhaseCommitAck
			if err:=r.recoveryFail("before_commit_ack_write");err!=nil{
				_ = p.MarkCommitUncertain(ctl)
				return true,fmt.Errorf("%w: %v",session.ErrCommitUncertain,err)
			}
			ackCopies:=r.recoveryControlCopiesForTest("listener_commit_ack_send",&ack)
			for i:=0;i<ackCopies;i++{
				if err:=session.EncodeRecoveryControl(out,ack);err!=nil{
					_ = p.MarkCommitUncertain(ctl)
					return true,fmt.Errorf("%w: commit ack: %v",session.ErrCommitUncertain,err)
				}
			}
			if err:=r.recoveryFail("after_commit_ack_write");err!=nil{
				_ = p.MarkCommitUncertain(ctl)
				return true,fmt.Errorf("%w: %v",session.ErrCommitUncertain,err)
			}
		case session.RecoveryPhaseCommitAck:
			// Duplicate/legacy final COMMIT_ACK carries no finalization proof.
			// It is idempotently validated but cannot transition to FINALIZED.
			if !published{return true,recovery.ErrStateMismatch}
			if err:=p.ValidateRecoveryControl(ctl,session.RecoveryPhaseCommitAck);err!=nil{return true,err}
			continue
		case session.RecoveryPhaseFinalize:
			if !published{return true,recovery.ErrStateMismatch}
			if err:=p.ValidateRecoveryControl(ctl,session.RecoveryPhaseFinalize);err!=nil{return true,err}
			if err:=p.MarkFinalizationStarted(ctl);err!=nil{return true,err}
			if err:=r.recoveryFail("before_listener_finalize_process");err!=nil{
				_ = p.MarkFinalizationUncertain(ctl)
				return true,fmt.Errorf("%w: %v",session.ErrFinalizationUncertain,err)
			}
			// Listener accepts this exact transaction before proving acceptance
			// back to the Dialer. Duplicate FINALIZE remains idempotent.
			if err:=p.CompleteRecoveryFinalization(ctl);err!=nil{return true,err}
			if err:=r.recoveryFail("after_listener_finalize_before_ack");err!=nil{return true,err}
			ack:=ctl;ack.Phase=session.RecoveryPhaseFinalizeAck
			copies:=r.recoveryControlCopiesForTest("listener_finalize_ack_send",&ack)
			if copies<1{copies=1}
			for i:=0;i<copies;i++{
				if err:=session.EncodeRecoveryControl(out,ack);err!=nil{return true,fmt.Errorf("FINALIZE_ACK: %w",err)}
			}
			if err:=r.recoveryFail("after_listener_finalize_ack_write");err!=nil{return true,err}
			generation,err:=p.FinalizeRecoveryCommitWithGeneration(hctx,ctl)
			if err!=nil{return true,err}
			if generation==0{return true,errors.New("recovery transaction carrier generation unavailable")}
			p.RecordRecoveryDiagnosticForTest("HANDLER_FINALIZE_COMPLETED",session.SenderStopUnknown,nil,generation)
			owner,ok:=p.RecoveryCarrierOwnerForGeneration(ctl,generation)
			if !ok{return true,session.ErrStaleRecoveryIncarnation}
			<-hctx.Done()
			p.RecordRecoveryDiagnosticForTest("HANDLER_CTX_DONE",session.SenderStopContextDone,hctx.Err(),generation)
			p.RecordRecoveryDiagnosticForTest("OWNER_FENCE_ATTEMPT",session.SenderStopRecoveryOwnerFence,hctx.Err(),generation)
			if p.FenceRecoveryCarrierOwner(owner){
				p.RecordRecoveryDiagnosticForTest("OWNER_FENCE_RESULT_APPLIED",session.SenderStopRecoveryOwnerFence,hctx.Err(),generation)
			}else{
				p.RecordRecoveryDiagnosticForTest("OWNER_FENCE_RESULT_REJECTED",session.SenderStopRecoveryOwnerFence,hctx.Err(),generation)
			}
			p.RecordRecoveryDiagnosticForTest("HANDLER_RETURN",session.SenderStopRecoveryOwnerFence,nil,generation)
			return true,nil
		default:
			return true,recovery.ErrStateMismatch
		}
	}
}

func (r *Runtime) handleCommitStatusResolution(hctx context.Context,in io.Reader,out io.Writer,peer carrierh2.PeerInfo,query session.RecoveryControl) error {
	p:=r.sessionByID(query.SessionID)
	if p==nil{
		r.recordStatusLookupMiss(query,peer.Identity,"recovery status session not found")
		return errors.New("recovery status session not found")
	}
	r.recordLogicalSessionLifecycle("STATUS_LOOKUP_HIT","",p)
	if p.PeerIdentity()!=peer.Identity{return errors.New("recovery status peer identity mismatch")}
	p.RecordRecoveryDiagnosticForTest("HANDLER_CREATED",session.SenderStopUnknown,nil,0)
	reply,err:=p.EvaluateCommitStatus(query);if err!=nil{return err}

	commitCtl:=query
	commitCtl.Phase=session.RecoveryPhaseCommit
	commitCtl.Status=session.RecoveryResolutionNone
	if reply.Status==session.RecoveryResolutionCommitted||reply.Status==session.RecoveryResolutionFinalized{
		if err:=p.RebindCommittedCarrier(hctx,commitCtl,session.Carrier{In:in,Out:out});err!=nil{return err}
	}
	if reply.Status==session.RecoveryResolutionNotCommitted{
		if err:=p.RememberNotCommitted(query);err!=nil{return err}
	}
	if err:=r.recoveryFail("before_status_reply");err!=nil{return err}
	replyCopies:=r.recoveryControlCopiesForTest("listener_status_reply_send",&reply)
	if replyCopies<1{replyCopies=1}
	for i:=0;i<replyCopies;i++{if err:=session.EncodeRecoveryControl(out,reply);err!=nil{return err}}
	if err:=r.recoveryFail("after_status_reply_write");err!=nil{return err}

	switch reply.Status{
	case session.RecoveryResolutionCommitted,session.RecoveryResolutionFinalized:
		for {
			fr,err:=protocol.Decode(in)
			if err!=nil{
				if p.RecoveryTransactionState()!=session.RecoveryTxnFinalized{
					finalCtl:=commitCtl;finalCtl.Phase=session.RecoveryPhaseFinalize
					_ = p.MarkFinalizationUncertain(finalCtl)
				}
				return fmt.Errorf("%w: resolution finalization: %v",session.ErrFinalizationUncertain,err)
			}
			ctl,err:=session.DecodeRecoveryControl(fr);if err!=nil{return err}
			switch ctl.Phase{
			case session.RecoveryPhaseStatusQuery:
				dupReply,err:=p.EvaluateCommitStatus(ctl);if err!=nil{return err}
				if dupReply.Status!=session.RecoveryResolutionCommitted&&dupReply.Status!=session.RecoveryResolutionFinalized{return session.ErrCommitUncertain}
				copies:=r.recoveryControlCopiesForTest("listener_status_reply_send",&dupReply)
				if copies<1{copies=1}
				for i:=0;i<copies;i++{if err:=session.EncodeRecoveryControl(out,dupReply);err!=nil{return err}}
				continue
			case session.RecoveryPhaseCommitAck:
				if err:=p.ValidateRecoveryControl(ctl,session.RecoveryPhaseCommitAck);err!=nil{return err}
				if err:=session.EncodeRecoveryControl(out,ctl);err!=nil{return err}
				continue
			case session.RecoveryPhaseFinalize:
				if err:=p.ValidateRecoveryControl(ctl,session.RecoveryPhaseFinalize);err!=nil{return err}
				if err:=p.MarkFinalizationStarted(ctl);err!=nil{return err}
				if err:=r.recoveryFail("before_resolution_finalize_process");err!=nil{
					_ = p.MarkFinalizationUncertain(ctl)
					return fmt.Errorf("%w: %v",session.ErrFinalizationUncertain,err)
				}
				if err:=p.CompleteRecoveryFinalization(ctl);err!=nil{return err}
				ack:=ctl;ack.Phase=session.RecoveryPhaseFinalizeAck
				copies:=r.recoveryControlCopiesForTest("listener_finalize_ack_send",&ack)
				if copies<1{copies=1}
				for i:=0;i<copies;i++{if err:=session.EncodeRecoveryControl(out,ack);err!=nil{return err}}
				if err:=r.recoveryFail("after_resolution_finalize_ack_write");err!=nil{return err}
				generation,err:=p.FinalizeRecoveryCommitWithGeneration(hctx,ctl)
				if err!=nil{return err}
				if generation==0{return errors.New("recovery resolution carrier generation unavailable")}
				p.RecordRecoveryDiagnosticForTest("HANDLER_FINALIZE_COMPLETED",session.SenderStopUnknown,nil,generation)
				owner,ok:=p.RecoveryCarrierOwnerForGeneration(ctl,generation)
				if !ok{return session.ErrStaleRecoveryIncarnation}
				<-hctx.Done()
				p.RecordRecoveryDiagnosticForTest("HANDLER_CTX_DONE",session.SenderStopContextDone,hctx.Err(),generation)
				p.RecordRecoveryDiagnosticForTest("OWNER_FENCE_ATTEMPT",session.SenderStopRecoveryOwnerFence,hctx.Err(),generation)
				if p.FenceRecoveryCarrierOwner(owner){
					p.RecordRecoveryDiagnosticForTest("OWNER_FENCE_RESULT_APPLIED",session.SenderStopRecoveryOwnerFence,hctx.Err(),generation)
				}else{
					p.RecordRecoveryDiagnosticForTest("OWNER_FENCE_RESULT_REJECTED",session.SenderStopRecoveryOwnerFence,hctx.Err(),generation)
				}
				p.RecordRecoveryDiagnosticForTest("HANDLER_RETURN",session.SenderStopRecoveryOwnerFence,nil,generation)
				return nil
			default:
				return recovery.ErrStateMismatch
			}
		}
	case session.RecoveryResolutionNotCommitted:
		return nil
	case session.RecoveryResolutionConflict,session.RecoveryResolutionUnknown:
		return session.ErrCommitUncertain
	default:
		return recovery.ErrStateMismatch
	}
}
