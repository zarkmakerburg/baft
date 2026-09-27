package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/zarkmakerburg/baft/internal/protocol"
	"github.com/zarkmakerburg/baft/internal/routes"
)

const (
	defaultWindow = 64 * 1024
	dataChunk     = 32 * 1024
)

type Role uint8

const (
	Dialer Role = iota + 1
	Listener
)

type Carrier struct { In io.Reader; Out io.Writer }
type Options struct { NodeID string; ExpectedPeerNodeID string; ShardID uint8; ProfileID string; ProfileVersion uint32; ConfigRevision string }

type Peer struct {
	role Role
	carrier Carrier
	writer frameWriter
	peerID string
	routes *routes.Table
	dial func(context.Context,string,string)(net.Conn,error)
	nodeID string
	expectedPeerNodeID string
	bootID string
	sessionID string
	shardID uint8
	profileID string
	profileVersion uint32
	configRevision string
	epoch string
	readyCh chan struct{}
	localReady bool
	peerReady bool
	helloSeen bool
	mu sync.Mutex
	flows map[uint64]*flow
	nextID uint64
	closed bool
	wg sync.WaitGroup
}

type flow struct {
	id uint64
	routeID string
	nonce string
	conn net.Conn
	openDone chan error
	mu sync.Mutex
	peerMax uint64
	txNext uint64
	txAcked uint64
	rxNext uint64
	rxWritten uint64
	rxMax uint64
	creditWait chan struct{}
	finSent bool
	finRecv bool
	finAcked bool
	closed bool
}

type frameWriter struct { mu sync.Mutex; w io.Writer }
func (w *frameWriter) send(f protocol.Frame) error { w.mu.Lock(); defer w.mu.Unlock(); return protocol.Encode(w.w,f) }

func New(role Role,c Carrier,peerID string,table *routes.Table,opts Options)(*Peer,error){
	if c.In==nil||c.Out==nil{return nil,errors.New("carrier input/output are required")}
	if role!=Dialer&&role!=Listener{return nil,errors.New("invalid role")}
	if role==Listener&&(peerID==""||table==nil){return nil,errors.New("listener requires authenticated peer identity and route table")}
	if opts.NodeID==""||len(opts.NodeID)>64||opts.ExpectedPeerNodeID==""||len(opts.ExpectedPeerNodeID)>64{return nil,errors.New("node identities are required")}
	if opts.ShardID>7{return nil,errors.New("shard_id out of range")}
	if opts.ProfileID==""{opts.ProfileID="secure-fast"};if opts.ProfileVersion==0{opts.ProfileVersion=1};if opts.ConfigRevision==""{opts.ConfigRevision="dev"}
	bootID,err:=randomHex128();if err!=nil{return nil,err}
	p:=&Peer{role:role,carrier:c,writer:frameWriter{w:c.Out},peerID:peerID,routes:table,flows:make(map[uint64]*flow),nodeID:opts.NodeID,expectedPeerNodeID:opts.ExpectedPeerNodeID,bootID:bootID,shardID:opts.ShardID,profileID:opts.ProfileID,profileVersion:opts.ProfileVersion,configRevision:opts.ConfigRevision,epoch:"1",readyCh:make(chan struct{})}
	if role==Dialer{p.nextID=1;p.sessionID,err=randomHex128();if err!=nil{return nil,err}}else{p.nextID=2}
	p.dial=(&net.Dialer{Timeout:5*time.Second}).DialContext
	return p,nil
}

func randomHex128()(string,error){b:=make([]byte,16);if _,err:=rand.Read(b);err!=nil{return "",err};return hex.EncodeToString(b),nil}
func (p *Peer) Run(ctx context.Context)error{defer func(){p.closeAll();p.wg.Wait()}();if p.role==Dialer{if err:=p.sendHello();err!=nil{return err}};for{f,err:=protocol.Decode(p.carrier.In);if err!=nil{if ctx.Err()!=nil||errors.Is(err,io.EOF){return ctx.Err()};return err};if err:=p.handleFrame(ctx,f);err!=nil{return err}}}

func (p *Peer) OpenFlow(ctx context.Context,routeID string,conn net.Conn)error{
	if p.role!=Dialer{return errors.New("only dialer opens outbound flows in baseline")};if conn==nil||routeID==""||len(routeID)>64{return errors.New("valid route and connection required")};if err:=p.waitReady(ctx);err!=nil{return err};id,err:=p.allocateStreamID();if err!=nil{return err};nonceBytes:=make([]byte,16);if _,err:=rand.Read(nonceBytes);err!=nil{return err};fl:=newFlow(id,routeID,hex.EncodeToString(nonceBytes),conn);p.mu.Lock();p.flows[id]=fl;p.mu.Unlock();payload,err:=protocol.EncodeControl(protocol.OpenRequest{RouteID:routeID,OpenNonce:fl.nonce});if err!=nil{return err};if err:=p.writer.send(protocol.Frame{Type:protocol.TypeOpen,StreamID:id,Payload:payload});err!=nil{p.removeFlow(id);return err};select{case err:=<-fl.openDone:if err!=nil{p.removeFlow(id);_ = conn.Close();return err};case <-ctx.Done():p.removeFlow(id);_ = conn.Close();return ctx.Err()};if err:=p.grantReceive(fl);err!=nil{return err};p.startPump(ctx,fl);return nil
}

func newFlow(id uint64,routeID,nonce string,conn net.Conn)*flow{return &flow{id:id,routeID:routeID,nonce:nonce,conn:conn,openDone:make(chan error,1),creditWait:make(chan struct{})}}
func (p *Peer) allocateStreamID()(uint64,error){p.mu.Lock();defer p.mu.Unlock();if p.closed||p.nextID==0||p.nextID>^uint64(0)-2{return 0,errors.New("session cannot allocate another stream")};id:=p.nextID;p.nextID+=2;return id,nil}

func (p *Peer) handleFrame(ctx context.Context,fr protocol.Frame)error{
	switch fr.Type{case protocol.TypeHello:return p.handleHello(fr);case protocol.TypeHelloAck:return p.handleHelloAck(fr);case protocol.TypeReady:return p.handleReady(fr)}
	if !p.isReady(){return errors.New("application frame received before READY")}
	switch fr.Type{
	case protocol.TypeOpen:return p.handleOpen(ctx,fr)
	case protocol.TypeOpenOK:fl,err:=p.getFlow(fr.StreamID);if err!=nil{return err};select{case fl.openDone<-nil:default:};return nil
	case protocol.TypeOpenErr:fl,err:=p.getFlow(fr.StreamID);if err!=nil{return err};oe,err:=protocol.DecodeOpenError(fr.Payload);if err!=nil{return err};select{case fl.openDone<-errors.New(oe.Code):default:};return nil
	case protocol.TypeWindow:fl,err:=p.getFlow(fr.StreamID);if err!=nil{return err};return fl.onWindow(fr.Offset)
	case protocol.TypeAck:fl,err:=p.getFlow(fr.StreamID);if err!=nil{return err};return fl.onAck(fr.Offset)
	case protocol.TypeData:fl,err:=p.getFlow(fr.StreamID);if err!=nil{return err};return p.handleData(fl,fr)
	case protocol.TypeFin:fl,err:=p.getFlow(fr.StreamID);if err!=nil{return err};return p.handleFin(fl,fr.Offset)
	case protocol.TypeFinAck:fl,err:=p.getFlow(fr.StreamID);if err!=nil{return err};return fl.onFinAck(fr.Offset)
	case protocol.TypeReset:fl,err:=p.getFlow(fr.StreamID);if err!=nil{return err};fl.close();p.removeFlow(fr.StreamID);return nil
	default:return fmt.Errorf("unsupported frame in stage B session: 0x%02x",uint8(fr.Type))
	}
}

func (p *Peer) sendHello()error{p.mu.Lock();h:=protocol.Hello{ProtocolMin:1,ProtocolMax:1,NodeID:p.nodeID,BootID:p.bootID,SessionID:p.sessionID,ShardID:p.shardID,Epoch:p.epoch,Mode:"new",ResumeSnapshotID:nil,ProfileID:p.profileID,ProfileVersion:p.profileVersion,ConfigRevision:p.configRevision,Capabilities:[]string{}};p.mu.Unlock();payload,err:=protocol.EncodeControl(h);if err!=nil{return err};return p.writer.send(protocol.Frame{Type:protocol.TypeHello,Payload:payload})}
func (p *Peer) handleHello(fr protocol.Frame)error{if p.role!=Listener{return errors.New("dialer received HELLO")};h,err:=protocol.DecodeHello(fr.Payload);if err!=nil{return err};if h.NodeID!=p.expectedPeerNodeID{return errors.New("HELLO node_id does not match authenticated peer")};if h.Mode!="new"{return errors.New("resume is not implemented in stage B")};if h.ProfileID!=p.profileID||h.ProfileVersion!=p.profileVersion{return errors.New("profile mismatch")};p.mu.Lock();if p.helloSeen{p.mu.Unlock();return errors.New("duplicate HELLO")};p.helloSeen=true;p.sessionID=h.SessionID;p.epoch=h.Epoch;p.shardID=h.ShardID;ack:=protocol.HelloAck{SelectedProtocol:1,SessionID:p.sessionID,Epoch:p.epoch,PeerBootID:p.bootID,AcceptedProfile:protocol.AcceptedProfile{ID:p.profileID,Version:p.profileVersion},NegotiatedLimits:protocol.NegotiatedLimits{MaxFramePayloadBytes:protocol.MaxPayloadSize,MaxFlowsPerShard:64,ReceiveInitialBytes:defaultWindow,ReceiveMaxBytes:16*1024*1024,RetentionMS:30000}};p.mu.Unlock();payload,err:=protocol.EncodeControl(ack);if err!=nil{return err};if err:=p.writer.send(protocol.Frame{Type:protocol.TypeHelloAck,Payload:payload});err!=nil{return err};return p.sendReady()}
func (p *Peer) handleHelloAck(fr protocol.Frame)error{if p.role!=Dialer{return errors.New("listener received HELLO_ACK")};ack,err:=protocol.DecodeHelloAck(fr.Payload);if err!=nil{return err};p.mu.Lock();if p.helloSeen{p.mu.Unlock();return errors.New("duplicate HELLO_ACK")};if ack.SessionID!=p.sessionID||ack.Epoch!=p.epoch||ack.AcceptedProfile.ID!=p.profileID||ack.AcceptedProfile.Version!=p.profileVersion{p.mu.Unlock();return errors.New("HELLO_ACK state mismatch")};p.helloSeen=true;p.mu.Unlock();return p.sendReady()}
func (p *Peer) sendReady()error{payload,err:=protocol.EncodeControl(protocol.Ready{SnapshotID:nil});if err!=nil{return err};if err:=p.writer.send(protocol.Frame{Type:protocol.TypeReady,Payload:payload});err!=nil{return err};p.mu.Lock();p.localReady=true;p.markReadyLocked();p.mu.Unlock();return nil}
func (p *Peer) handleReady(fr protocol.Frame)error{r,err:=protocol.DecodeReady(fr.Payload);if err!=nil{return err};if r.SnapshotID!=nil{return errors.New("snapshot READY is unsupported before resume")};p.mu.Lock();p.peerReady=true;p.markReadyLocked();p.mu.Unlock();return nil}
func (p *Peer) markReadyLocked(){if p.localReady&&p.peerReady{select{case <-p.readyCh:default:close(p.readyCh)}}}
func (p *Peer) isReady()bool{p.mu.Lock();defer p.mu.Unlock();select{case <-p.readyCh:return true;default:return false}}
func (p *Peer) waitReady(ctx context.Context)error{select{case <-p.readyCh:return nil;case <-ctx.Done():return ctx.Err()}}

func (p *Peer) handleOpen(ctx context.Context,fr protocol.Frame)error{if p.role!=Listener{return errors.New("dialer received unexpected OPEN")};req,err:=protocol.DecodeOpen(fr.Payload);if err!=nil{return err};p.mu.Lock();if existing:=p.flows[fr.StreamID];existing!=nil{same:=existing.routeID==req.RouteID&&existing.nonce==req.OpenNonce;p.mu.Unlock();if !same{return errors.New("duplicate stream_id with different OPEN identity")};return p.writer.send(protocol.Frame{Type:protocol.TypeOpenOK,StreamID:fr.StreamID,Payload:[]byte("{}")})};p.mu.Unlock();target,err:=p.routes.Resolve(p.peerID,req.RouteID);if err!=nil{code:=err.Error();payload,_:=protocol.EncodeControl(protocol.OpenError{Code:code});return p.writer.send(protocol.Frame{Type:protocol.TypeOpenErr,StreamID:fr.StreamID,Payload:payload})};conn,err:=p.dial(ctx,"tcp",target);if err!=nil{payload,_:=protocol.EncodeControl(protocol.OpenError{Code:"TARGET_UNREACHABLE"});return p.writer.send(protocol.Frame{Type:protocol.TypeOpenErr,StreamID:fr.StreamID,Payload:payload})};fl:=newFlow(fr.StreamID,req.RouteID,req.OpenNonce,conn);p.mu.Lock();if p.closed{p.mu.Unlock();_ = conn.Close();return errors.New("session closed")};if old:=p.flows[fr.StreamID];old!=nil{p.mu.Unlock();_ = conn.Close();return errors.New("concurrent OPEN conflict")};p.flows[fr.StreamID]=fl;p.mu.Unlock();if err:=p.writer.send(protocol.Frame{Type:protocol.TypeOpenOK,StreamID:fr.StreamID,Payload:[]byte("{}")});err!=nil{fl.close();p.removeFlow(fr.StreamID);return err};if err:=p.grantReceive(fl);err!=nil{fl.close();p.removeFlow(fr.StreamID);return err};p.startPump(ctx,fl);return nil}
func (p *Peer) grantReceive(fl *flow)error{fl.mu.Lock();newMax:=fl.rxWritten+defaultWindow;if newMax<fl.rxWritten{fl.mu.Unlock();return errors.New("receive window overflow")};if newMax<=fl.rxMax{fl.mu.Unlock();return nil};fl.rxMax=newMax;fl.mu.Unlock();return p.writer.send(protocol.Frame{Type:protocol.TypeWindow,StreamID:fl.id,Offset:newMax})}
func (p *Peer) handleData(fl *flow,fr protocol.Frame)error{data,ack,duplicate,err:=fl.acceptData(fr.Offset,fr.Payload);if err!=nil{return err};if err:=p.writer.send(protocol.Frame{Type:protocol.TypeAck,StreamID:fl.id,Offset:ack});err!=nil{return err};if duplicate{return nil};if err:=writeConnFull(fl.conn,data);err!=nil{return err};fl.mu.Lock();fl.rxWritten+=uint64(len(data));fl.mu.Unlock();return p.grantReceive(fl)}
func (p *Peer) handleFin(fl *flow,finalOffset uint64)error{fl.mu.Lock();if finalOffset!=fl.rxNext{fl.mu.Unlock();return errors.New("FIN final_offset does not match received data")};fl.finRecv=true;fl.mu.Unlock();if cw,ok:=fl.conn.(interface{CloseWrite()error});ok{if err:=cw.CloseWrite();err!=nil{return err}};return p.writer.send(protocol.Frame{Type:protocol.TypeFinAck,StreamID:fl.id,Offset:finalOffset})}
func (p *Peer) startPump(ctx context.Context,fl *flow){p.wg.Add(1);go func(){defer p.wg.Done();p.pumpLocal(ctx,fl)}()}
func (p *Peer) pumpLocal(ctx context.Context,fl *flow){buf:=make([]byte,dataChunk);for{n,err:=fl.conn.Read(buf);if n>0{off,werr:=fl.reserveSend(ctx,uint64(n));if werr!=nil{return};payload:=append([]byte(nil),buf[:n]...);if werr:=p.writer.send(protocol.Frame{Type:protocol.TypeData,StreamID:fl.id,Offset:off,Payload:payload});werr!=nil{return}};if err!=nil{if errors.Is(err,io.EOF){fl.mu.Lock();final:=fl.txNext;fl.finSent=true;fl.mu.Unlock();_ = p.writer.send(protocol.Frame{Type:protocol.TypeFin,StreamID:fl.id,Offset:final})};return}}}
func (f *flow) reserveSend(ctx context.Context,n uint64)(uint64,error){for{f.mu.Lock();if f.closed{f.mu.Unlock();return 0,errors.New("flow closed")};if n<=^uint64(0)-f.txNext&&f.txNext+n<=f.peerMax{off:=f.txNext;f.txNext+=n;f.mu.Unlock();return off,nil};wait:=f.creditWait;f.mu.Unlock();select{case <-ctx.Done():return 0,ctx.Err();case <-wait:}}}
func (f *flow) onWindow(max uint64)error{f.mu.Lock();defer f.mu.Unlock();if max<f.peerMax{return errors.New("WINDOW moved backwards")};if max==f.peerMax{return nil};f.peerMax=max;close(f.creditWait);f.creditWait=make(chan struct{});return nil}
func (f *flow) onAck(ack uint64)error{f.mu.Lock();defer f.mu.Unlock();if ack>f.txNext{return errors.New("ACK exceeds tx_next")};if ack>f.txAcked{f.txAcked=ack};return nil}
func (f *flow) acceptData(offset uint64,payload []byte)([]byte,uint64,bool,error){f.mu.Lock();defer f.mu.Unlock();if uint64(len(payload))>^uint64(0)-offset{return nil,0,false,errors.New("DATA offset overflow")};end:=offset+uint64(len(payload));if end>f.rxMax{return nil,0,false,errors.New("FLOW_CONTROL_ERROR")};if offset>f.rxNext{return nil,0,false,errors.New("DATA gap is not allowed")};if end<=f.rxNext{return nil,f.rxNext,true,nil};skip:=uint64(0);if offset<f.rxNext{skip=f.rxNext-offset};data:=append([]byte(nil),payload[skip:]...);f.rxNext=end;return data,f.rxNext,false,nil}
func (f *flow) onFinAck(off uint64)error{f.mu.Lock();defer f.mu.Unlock();if !f.finSent||off!=f.txNext{return errors.New("invalid FIN_ACK")};f.finAcked=true;return nil}
func (f *flow) close(){f.mu.Lock();if f.closed{f.mu.Unlock();return};f.closed=true;close(f.creditWait);f.mu.Unlock();_ = f.conn.Close()}
func (p *Peer) getFlow(id uint64)(*flow,error){p.mu.Lock();fl:=p.flows[id];p.mu.Unlock();if fl==nil{return nil,errors.New("frame references unknown flow")};return fl,nil}
func (p *Peer) removeFlow(id uint64){p.mu.Lock();delete(p.flows,id);p.mu.Unlock()}
func (p *Peer) closeAll(){p.mu.Lock();if p.closed{p.mu.Unlock();return};p.closed=true;flows:=make([]*flow,0,len(p.flows));for _,fl:=range p.flows{flows=append(flows,fl)};p.flows=map[uint64]*flow{};p.mu.Unlock();for _,fl:=range flows{fl.close()}}
func writeConnFull(w io.Writer,p []byte)error{for len(p)>0{n,err:=w.Write(p);if n<0||n>len(p){return errors.New("invalid socket write count")};p=p[n:];if err!=nil{return err};if n==0{return io.ErrShortWrite}};return nil}
