package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestVersionJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	var v versionInfo
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Name != "baft" || v.Version != version {
		t.Fatalf("unexpected version: %#v", v)
	}
}

func TestConfigValidateCommand(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	const cfg = `schema_version: 1
node: {id: ir-01, role: dialer}
peer: {address: "127.0.0.1:443", server_name: ex.test, allowed_identity: "urn:baft:node:ex-01"}
tls: {min_version: "1.3", ca_file: /tmp/ca.pem, cert_file: /tmp/ir.pem, key_file: /tmp/ir.key, session_tickets: false}
transport: {primary: h2, h3_enabled: false, shards: 1, profile: secure-fast}
limits: {max_flows: 32, data_memory_mib: 64, receive_initial_kib: 64, receive_max_mib: 16, replay_max_mib: 16}
recovery: {enabled: false, retention_seconds: 30}
routes:
  - {id: service-main, listen: "127.0.0.1:1443", remote_route: service-main, direction: outbound, traffic_class: interactive}
management: {unix_socket: /run/baft/admin.sock, metrics_listen: "127.0.0.1:9191"}
logging: {level: info, payload: false}
`
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil { t.Fatal(err) }
	var out, errOut bytes.Buffer
	if code := run([]string{"config","validate","--file",p}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	if out.String() != "valid\n" { t.Fatalf("out=%q", out.String()) }
}
