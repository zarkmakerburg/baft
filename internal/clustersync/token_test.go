package clustersync

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

func testConfigs(t *testing.T) []config.Config {
	t.Helper()
	out := make([]config.Config, 0, RequiredNodes)
	for i := 0; i < RequiredNodes; i++ {
		kp, err := securityinternal.GenerateKeyPair()
		if err != nil { t.Fatal(err) }
		pub, err := securityinternal.EncodePublicKey(kp.Public)
		if err != nil { t.Fatal(err) }
		out = append(out, config.Config{
			SchemaVersion:1,
			Noise:&config.Noise{KeyFile:"/tmp/ir-noise", PeerPublicKey:pub, RecordShaping:recordshape.Config{}},
			Node:config.Node{ID:"ir-master", Role:"dialer"},
			Peer:&config.Peer{
				Address:fmt.Sprintf("192.0.2.%d:443", 20+i),
				ServerName:fmt.Sprintf("ex-%02d.example", i+1),
				AllowedIdentity:fmt.Sprintf("urn:baft:node:ex-%02d", i+1),
			},
			TLS:config.TLS{MinVersion:"1.3", CAFile:"/tmp/ca", CertFile:"/tmp/cert", KeyFile:"/tmp/key", SessionTickets:false},
			Transport:config.Transport{Primary:"h2", Shards:1, Profile:"secure-fast"},
			Limits:config.Limits{MaxFlows:64, DataMemoryMiB:64, ReceiveInitialKiB:64, ReceiveMaxMiB:8, ReplayMaxMiB:8},
			Recovery:config.Recovery{Enabled:false, RetentionSeconds:30},
			Routes:[]config.Route{{ID:"service-main", Listen:fmt.Sprintf("127.0.0.1:%d", 15000+i), RemoteRoute:"service-main", Direction:"outbound"}},
			Management:config.Management{UnixSocket:fmt.Sprintf("/tmp/admin-%d.sock",i), MetricsListen:fmt.Sprintf("127.0.0.1:%d", 9300+i)},
			Logging:config.Logging{Level:"info", Payload:false},
		})
	}
	return out
}

func TestTokenRoundTripSixNodes(t *testing.T) {
	workerPriv, workerPub, err := GenerateWorkerKeyPair()
	if err != nil { t.Fatal(err) }
	signPub, signPriv, err := GenerateSigningKeyPair()
	if err != nil { t.Fatal(err) }
	now := time.Unix(1700000000,0)
	m, err := ManifestFromConfigs("goldapp-baft",1,15*time.Minute,now,testConfigs(t))
	if err != nil { t.Fatal(err) }
	token, err := Seal(m,workerPub,signPriv)
	if err != nil { t.Fatal(err) }
	engine, err := NewEngine(workerPriv,signPub,"goldapp-baft")
	if err != nil { t.Fatal(err) }
	snap, changed, err := engine.Apply(token,now.Add(time.Second))
	if err != nil { t.Fatal(err) }
	if !changed || len(snap.Routes)!=6 { t.Fatalf("changed=%v routes=%d",changed,len(snap.Routes)) }
	if snap.Revision!=m.Revision { t.Fatal("revision mismatch") }
	t.Logf("PASS token decrypted+verified generation=%d routes=%d revision=%s",snap.Generation,len(snap.Routes),snap.Revision)
}

func TestTokenTamperRejected(t *testing.T) {
	workerPriv, workerPub, _ := GenerateWorkerKeyPair()
	signPub, signPriv, _ := GenerateSigningKeyPair()
	now := time.Unix(1700000000,0)
	m,_ := ManifestFromConfigs("goldapp-baft",1,15*time.Minute,now,testConfigs(t))
	token,_ := Seal(m,workerPub,signPriv)
	engine,_ := NewEngine(workerPriv,signPub,"goldapp-baft")

	raw := strings.TrimPrefix(token,TokenPrefix)
	idx := len(raw)/2
	repl := byte('A')
	if raw[idx]=='A' { repl='B' }
	tampered := TokenPrefix + raw[:idx] + string(repl) + raw[idx+1:]
	if _,_,err := engine.Apply(tampered,now.Add(time.Second)); err==nil {
		t.Fatal("tampered token accepted")
	}
}

func TestWorkerDetectsSignedNodeUpdateAndSyncs(t *testing.T) {
	workerPriv, workerPub, err := GenerateWorkerKeyPair()
	if err != nil { t.Fatal(err) }
	signPub, signPriv, err := GenerateSigningKeyPair()
	if err != nil { t.Fatal(err) }
	engine, err := NewEngine(workerPriv,signPub,"goldapp-baft")
	if err != nil { t.Fatal(err) }
	now := time.Unix(1700000000,0)

	cfgs1 := testConfigs(t)
	m1, _ := ManifestFromConfigs("goldapp-baft",1,15*time.Minute,now,cfgs1)
	t1, _ := Seal(m1,workerPub,signPriv)
	s1, changed, err := engine.Apply(t1,now.Add(time.Second))
	if err != nil || !changed { t.Fatalf("first apply changed=%v err=%v",changed,err) }

	cfgs2 := testConfigs(t)
	cfgs2[2].Peer.Address = "198.51.100.77:443"
	m2, _ := ManifestFromConfigs("goldapp-baft",2,15*time.Minute,now.Add(time.Minute),cfgs2)
	t2, _ := Seal(m2,workerPub,signPriv)
	s2, changed, err := engine.Apply(t2,now.Add(61*time.Second))
	if err != nil { t.Fatal(err) }
	if !changed { t.Fatal("signed update not detected") }
	if s1.Revision==s2.Revision { t.Fatal("revision did not change") }
	if s2.Routes[2].Address!="198.51.100.77:443" { t.Fatal("worker did not sync node update") }
	if len(s2.Routes)!=6 { t.Fatalf("routes=%d",len(s2.Routes)) }
	t.Logf("PASS worker sync generation=%d changed_node=3 new_address=%s",s2.Generation,s2.Routes[2].Address)
}

func TestRollbackAndWrongSignerRejected(t *testing.T) {
	workerPriv, workerPub, _ := GenerateWorkerKeyPair()
	signPub, signPriv, _ := GenerateSigningKeyPair()
	engine,_ := NewEngine(workerPriv,signPub,"goldapp-baft")
	now := time.Unix(1700000000,0)
	cfgs := testConfigs(t)

	m2,_ := ManifestFromConfigs("goldapp-baft",2,15*time.Minute,now,cfgs)
	t2,_ := Seal(m2,workerPub,signPriv)
	if _,_,err := engine.Apply(t2,now.Add(time.Second)); err!=nil { t.Fatal(err) }

	m1,_ := ManifestFromConfigs("goldapp-baft",1,15*time.Minute,now,cfgs)
	t1,_ := Seal(m1,workerPub,signPriv)
	if _,_,err := engine.Apply(t1,now.Add(2*time.Second)); !errors.Is(err,ErrRollback) { t.Fatalf("rollback err=%v",err) }

	_, wrongPriv, err := ed25519.GenerateKey(nil)
	if err != nil { t.Fatal(err) }
	bad,_ := Seal(m2,workerPub,wrongPriv)
	if _,_,err := engine.Apply(bad,now.Add(time.Second)); !errors.Is(err,ErrSignature) { t.Fatalf("signature err=%v",err) }
}
