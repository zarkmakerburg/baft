package bcc

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationCommitReconcilesLostAcknowledgment(t *testing.T) {
	db, err := openStateDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	version := len(stateMigrations) + 1
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("CREATE TABLE migration_reconcile_probe (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO schema_migrations (version, applied_at) VALUES (?, 'test')", version); err != nil {
		t.Fatal(err)
	}
	lostACK := errors.New("migration commit acknowledgment lost")
	err = commitStateMigration(db, version, func() error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return lostACK
	})
	if err != nil {
		t.Fatalf("durable migration treated as failed: %v", err)
	}
	var table string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE name = 'migration_reconcile_probe'").Scan(&table); err != nil || table != "migration_reconcile_probe" {
		t.Fatalf("migration DDL missing after marker: table=%q, err=%v", table, err)
	}
}

func TestMigrationCommitRejectsAbsentMarker(t *testing.T) {
	db, err := openStateDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	version := len(stateMigrations) + 1
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO schema_migrations (version, applied_at) VALUES (?, 'test')", version); err != nil {
		t.Fatal(err)
	}
	rejected := errors.New("migration commit rejected")
	err = commitStateMigration(db, version, func() error {
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		return rejected
	})
	if !errors.Is(err, rejected) || !strings.Contains(err.Error(), "marker") {
		t.Fatalf("uncommitted migration was accepted or lost cause: %v", err)
	}
}
