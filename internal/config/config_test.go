package config

import (
	"strings"
	"testing"
)

func validIR() Config {
	return Config{
		SchemaVersion: 1,
		Node:          Node{ID: "ir-01", Role: "dialer"},
		Peer:          &Peer{Address: "192.0.2.20:443", ServerName: "ex.example", AllowedIdentity: "urn:baft:node:ex-01"},
		TLS:           TLS{MinVersion: "1.3", CAFile: "/etc/baft/pki/ca.pem", CertFile: "/etc/baft/pki/ir.pem", KeyFile: "/etc/baft/pki/ir.key"},
		Transport:     Transport{Primary: "h2", Shards: 4, Profile: "secure-fast"},
		Limits:        Limits{MaxFlows: 256, DataMemoryMiB: 256, ReceiveInitialKiB: 64, ReceiveMaxMiB: 16, ReplayMaxMiB: 16},
		Recovery:      Recovery{Enabled: false, RetentionSeconds: 30},
		Routes:        []Route{{ID: "service-main", Listen: "127.0.0.1:1443", RemoteRoute: "service-main", Direction: "outbound", TrafficClass: "interactive"}},
		Management:    Management{UnixSocket: "/run/baft/admin.sock", MetricsListen: "127.0.0.1:9191"},
		Logging:       Logging{Level: "info", Payload: false},
	}
}

func TestValidateIR(t *testing.T) {
	if err := Validate(validIR()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsRecoveryBefore03(t *testing.T) {
	c := validIR()
	c.Recovery.Enabled = true
	if err := Validate(c); err == nil {
		t.Fatal("expected recovery to be rejected")
	}
}

func TestDecodeJSONRejectsUnknownField(t *testing.T) {
	js := `{"schema_version":1,"unexpected":true}`
	if _, err := DecodeJSON(strings.NewReader(js)); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestValidateRejectsPublicServiceListener(t *testing.T) {
	c := validIR()
	c.Routes[0].Listen = "0.0.0.0:1443"
	if err := Validate(c); err == nil {
		t.Fatal("expected public listener to be rejected by baseline schema")
	}
}
