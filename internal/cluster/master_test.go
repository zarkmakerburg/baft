package cluster

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/recordshape"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

func sixMasterConfigs(t *testing.T) []config.Config {
	t.Helper()
	cfgs := make([]config.Config, 0, RequiredForeignNodes)
	for i := 0; i < RequiredForeignNodes; i++ {
		kp, err := securityinternal.GenerateKeyPair()
		if err != nil {
			t.Fatal(err)
		}
		pub, err := securityinternal.EncodePublicKey(kp.Public)
		if err != nil {
			t.Fatal(err)
		}
		cfgs = append(cfgs, config.Config{
			SchemaVersion: config.SchemaVersion,
			Noise: &config.Noise{
				KeyFile:       "/etc/baft/keys/ir-noise.json",
				PeerPublicKey: pub,
				RecordShaping: recordshape.Config{},
			},
			Node: config.Node{ID: "ir-master", Role: "dialer"},
			Peer: &config.Peer{
				Address:         fmt.Sprintf("192.0.2.%d:443", i+10),
				ServerName:      fmt.Sprintf("ex-%d.example", i+1),
				AllowedIdentity: fmt.Sprintf("urn:baft:node:ex-%02d", i+1),
			},
			TLS: config.TLS{
				MinVersion:     "1.3",
				CAFile:         "/etc/baft/pki/ca.pem",
				CertFile:       "/etc/baft/pki/ir.pem",
				KeyFile:        "/etc/baft/pki/ir.key",
				SessionTickets: false,
			},
			Transport: config.Transport{Primary: "h2", Shards: 1, Profile: "secure-fast"},
			Limits: config.Limits{
				MaxFlows:          64,
				DataMemoryMiB:     64,
				ReceiveInitialKiB: 64,
				ReceiveMaxMiB:     8,
				ReplayMaxMiB:      8,
			},
			Recovery: config.Recovery{Enabled: false, RetentionSeconds: 30},
			Routes: []config.Route{{
				ID:          "service-main",
				Listen:      fmt.Sprintf("127.0.0.1:%d", 14001+i),
				RemoteRoute: "service-main",
				Direction:   "outbound",
			}},
			Management: config.Management{
				UnixSocket:    fmt.Sprintf("/run/baft/admin-%d.sock", i+1),
				MetricsListen: fmt.Sprintf("127.0.0.1:%d", 9201+i),
			},
			Logging: config.Logging{Level: "info", Payload: false},
		})
	}
	return cfgs
}

func TestValidateMasterConfigsRequiresSixNoisePeers(t *testing.T) {
	cfgs := sixMasterConfigs(t)
	if err := ValidateMasterConfigs(cfgs); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMasterConfigs(cfgs[:5]); err == nil {
		t.Fatal("five peers accepted")
	}
	bad := append([]config.Config(nil), cfgs...)
	bad[3].Noise = nil
	if err := ValidateMasterConfigs(bad); err == nil {
		t.Fatal("peer without Noise accepted")
	}
}

func TestValidateMasterConfigsRejectsListenerCollisions(t *testing.T) {
	cfgs := sixMasterConfigs(t)
	cfgs[5].Routes[0].Listen = cfgs[0].Routes[0].Listen
	if err := ValidateMasterConfigs(cfgs); err == nil {
		t.Fatal("duplicate local listener accepted")
	}
}

type blockingRunner struct {
	fail error
}

func (r *blockingRunner) Run(ctx context.Context, _ config.Config) error {
	if r.fail != nil {
		return r.fail
	}
	<-ctx.Done()
	return nil
}

func TestMasterFailFastCancelsPeerSet(t *testing.T) {
	cfgs := sixMasterConfigs(t)
	n := 0
	m := &Master{newRuntime: func() runtimeRunner {
		n++
		if n == 4 {
			return &blockingRunner{fail: fmt.Errorf("synthetic failure")}
		}
		return &blockingRunner{}
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := m.Run(ctx, cfgs)
	if err == nil || !strings.Contains(err.Error(), "foreign node") {
		t.Fatalf("unexpected error: %v", err)
	}
}
