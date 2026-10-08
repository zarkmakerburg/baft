package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifestDefaultsAndSelection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	data := `{"version":1,"scenarios":[{"id":"B01","name":"baseline","kind":"micro","command":["go","version"],"informational":true}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := loadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Scenarios[0].Repetitions; got != 1 {
		t.Fatalf("repetitions=%d want 1", got)
	}
	if got := m.Scenarios[0].TimeoutSeconds; got != 300 {
		t.Fatalf("timeout=%d want 300", got)
	}
	selected, err := selectScenarios(m.Scenarios, "B01")
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].ID != "B01" {
		t.Fatalf("selected=%v", selected)
	}
}

func TestParseMetricFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.log")
	data := "noise\n    BAFT_BENCH_METRIC {\"scenario\":\"B07\",\"recovery_ms_p95\":12.5}\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	metrics, err := parseMetricFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 {
		t.Fatalf("metrics=%d want 1", len(metrics))
	}
	if got := metrics[0]["scenario"]; got != "B07" {
		t.Fatalf("scenario=%v want B07", got)
	}
}

func TestDuplicateScenarioRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	data := `{"version":1,"scenarios":[{"id":"B01","name":"a","command":["go","version"]},{"id":"B01","name":"b","command":["go","version"]}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadManifest(path); err == nil {
		t.Fatal("expected duplicate id error")
	}
}
