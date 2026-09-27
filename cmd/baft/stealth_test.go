package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

func TestStealthConfigRequiresPinnedNoiseAndPreservesRoutes(t *testing.T) {
	cfg, err := config.LoadFile("../../configs/example-ir.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	save := func() {
		b, _ := json.Marshal(cfg)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	var out, errs bytes.Buffer
	if run([]string{"config", "stealth-pro", "--file", path}, &out, &errs) == 0 {
		t.Fatal("accepted non-Noise deployment")
	}
	key, _ := securityinternal.GenerateKeyPair()
	pub, _ := securityinternal.EncodePublicKey(key.Public)
	cfg.Noise = &config.Noise{KeyFile: "/etc/baft/noise-key.json", PeerPublicKey: pub}
	save()
	before, _ := os.ReadFile(path)
	out.Reset()
	errs.Reset()
	if rc := run([]string{"config", "stealth-pro", "--file", path, "--distribution", "laplace", "--max-padding", "256"}, &out, &errs); rc != 0 {
		t.Fatal(rc, errs.String())
	}
	got, err := config.DecodeJSON(&out)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(got.Routes)
	b, _ := json.Marshal(cfg.Routes)
	if !bytes.Equal(a, b) || !got.Noise.RecordShaping.Enabled || got.Noise.RecordShaping.MaxPaddingBytes != 256 || got.Noise.PeerPublicKey != pub {
		t.Fatal("config changed unrelated fields")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("command mutated source")
	}
}
