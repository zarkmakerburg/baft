package mesh

import (
	"errors"
	"fmt"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/recordshape"
)

type LinkSpec struct {
	DialerNodeID        string
	ListenerNodeID      string
	ListenerAddress     string
	ServerName          string
	DialerLocalListen   string
	ListenerTarget      string
	DialerNoiseKeyFile  string
	ListenerNoiseKeyFile string
	DialerPublicKey     string
	ListenerPublicKey   string
	DialerMetrics       string
	ListenerMetrics     string
	DialerSocket        string
	ListenerSocket      string
	TLS                 config.TLS
	Limits              config.Limits
}

func BuildLink(s LinkSpec) (config.Config,config.Config,error) {
	if s.DialerNodeID==""||s.ListenerNodeID==""{return config.Config{},config.Config{},errors.New("mesh node ids are required")}
	if s.ServerName==""{return config.Config{},config.Config{},errors.New("mesh server name is required")}
	if s.DialerNoiseKeyFile==""||s.ListenerNoiseKeyFile==""||s.DialerPublicKey==""||s.ListenerPublicKey==""{
		return config.Config{},config.Config{},errors.New("mesh Noise key material is required")
	}
	limits:=s.Limits
	if limits.MaxFlows==0 {
		limits=config.Limits{MaxFlows:256,DataMemoryMiB:256,ReceiveInitialKiB:64,ReceiveMaxMiB:16,ReplayMaxMiB:16}
	}
	dialerIdentity:="urn:baft:node:"+s.DialerNodeID
	listenerIdentity:="urn:baft:node:"+s.ListenerNodeID
	dialer:=config.Config{
		SchemaVersion:config.SchemaVersion,
		Noise:&config.Noise{KeyFile:s.DialerNoiseKeyFile,PeerPublicKey:s.ListenerPublicKey,RecordShaping:recordshape.Config{}},
		Node:config.Node{ID:s.DialerNodeID,Role:"dialer"},
		Peer:&config.Peer{Address:s.ListenerAddress,ServerName:s.ServerName,AllowedIdentity:listenerIdentity},
		TLS:s.TLS,
		Transport:config.Transport{Primary:"h2",Shards:1,Profile:"mesh-reliability"},
		Limits:limits,Recovery:config.Recovery{Enabled:false,RetentionSeconds:30},
		Routes:[]config.Route{{ID:"mesh-egress",Listen:s.DialerLocalListen,RemoteRoute:"mesh-forward",Direction:"outbound"}},
		Management:config.Management{UnixSocket:s.DialerSocket,MetricsListen:s.DialerMetrics},
		Logging:config.Logging{Level:"info",Payload:false},
	}
	listener:=config.Config{
		SchemaVersion:config.SchemaVersion,
		Noise:&config.Noise{KeyFile:s.ListenerNoiseKeyFile,PeerPublicKey:s.DialerPublicKey,RecordShaping:recordshape.Config{}},
		Node:config.Node{ID:s.ListenerNodeID,Role:"listener"},
		Server:&config.Server{Listen:s.ListenerAddress,ServerName:s.ServerName,AllowedPeerIdentities:[]string{dialerIdentity}},
		TLS:s.TLS,
		Transport:config.Transport{Primary:"h2",Shards:1,Profile:"mesh-reliability"},
		Limits:limits,Recovery:config.Recovery{Enabled:false,RetentionSeconds:30},
		Routes:[]config.Route{{ID:"mesh-forward",Direction:"inbound",Target:s.ListenerTarget,AllowedPeers:[]string{dialerIdentity}}},
		Management:config.Management{UnixSocket:s.ListenerSocket,MetricsListen:s.ListenerMetrics},
		Logging:config.Logging{Level:"info",Payload:false},
	}
	if err:=config.Validate(dialer);err!=nil{return config.Config{},config.Config{},fmt.Errorf("mesh dialer: %w",err)}
	if err:=config.Validate(listener);err!=nil{return config.Config{},config.Config{},fmt.Errorf("mesh listener: %w",err)}
	return dialer,listener,nil
}
