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
