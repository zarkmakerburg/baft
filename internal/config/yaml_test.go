package config

import (
	"strings"
	"testing"
)

const validYAML = `schema_version: 1
node:
  id: ir-01
  role: dialer
peer:
  address: 192.0.2.20:443
  server_name: ex.example
  allowed_identity: urn:baft:node:ex-01
tls:
  min_version: "1.3"
  ca_file: /etc/baft/pki/ca.pem
  cert_file: /etc/baft/pki/ir.pem
  key_file: /etc/baft/pki/ir.key
  session_tickets: false
transport:
  primary: h2
  h3_enabled: false
  shards: 4
  profile: secure-fast
limits:
  max_flows: 256
  data_memory_mib: 256
  receive_initial_kib: 64
  receive_max_mib: 16
  replay_max_mib: 16
recovery:
  enabled: false
  retention_seconds: 30
routes:
  - id: service-main
    listen: 127.0.0.1:1443
    remote_route: service-main
    direction: outbound
    traffic_class: interactive
management:
  unix_socket: /run/baft/admin.sock
  metrics_listen: 127.0.0.1:9191
logging:
  level: info
  payload: false
`

func TestDecodeYAMLValid(t *testing.T) {
	cfg, err := DecodeYAML(strings.NewReader(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Node.ID != "ir-01" || cfg.Transport.Shards != 4 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestDecodeYAMLRejectsUnknownField(t *testing.T) {
	bad := strings.Replace(validYAML, "logging:\n", "unexpected: true\nlogging:\n", 1)
	if _, err := DecodeYAML(strings.NewReader(bad)); err == nil {
		t.Fatal("expected unknown field rejection")
	}
}

func TestDecodeYAMLRejectsDuplicateKey(t *testing.T) {
	if _, err := DecodeYAML(strings.NewReader("schema_version: 1\nschema_version: 1\n")); err == nil {
		t.Fatal("expected duplicate YAML key rejection")
	}
}

func TestDecodeYAMLRejectsAnchorAlias(t *testing.T) {
	if _, err := DecodeYAML(strings.NewReader("a: &x 1\nb: *x\n")); err == nil {
		t.Fatal("expected anchor/alias rejection")
	}
}

func TestDecodeYAMLRejectsMultipleDocuments(t *testing.T) {
	if _, err := DecodeYAML(strings.NewReader("schema_version: 1\n---\nschema_version: 1\n")); err == nil {
		t.Fatal("expected multiple document rejection")
	}
}
