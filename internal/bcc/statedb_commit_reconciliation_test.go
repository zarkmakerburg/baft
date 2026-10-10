package bcc

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitStateReconcilesLostAcknowledgment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeStateDB(path, store.st); err != nil {
		t.Fatal(err)
	}

	desired, err := cloneState(store.st)
	if err != nil {
		t.Fatal(err)
	}
	desired.Nodes["ex-1"] = Node{ID: "ex-1", Alias: "EX", Address: "127.0.0.1", Role: "foreign"}
	db, err := openStateDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeStateTx(tx, desired); err != nil {
		t.Fatal(err)
	}
	lostACK := errors.New("commit acknowledgment lost")
	err = commitStateWithReconciliation(path, desired, func() error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return lostACK
	})
	if err != nil {
		t.Fatalf("durable commit reported as failure: %v", err)
	}
	persisted, err := readStateDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonicalStateJSON(desired), canonicalStateJSON(persisted)) {
		t.Fatal("reconciled state differs from the committed state")
	}
}

func TestCommitStateDoesNotAcceptUncommittedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeStateDB(path, store.st); err != nil {
		t.Fatal(err)
	}
	desired, err := cloneState(store.st)
	if err != nil {
		t.Fatal(err)
	}
	desired.NextJob++
	db, err := openStateDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeStateTx(tx, desired); err != nil {
		t.Fatal(err)
	}
	rejected := errors.New("commit rejected")
	err = commitStateWithReconciliation(path, desired, func() error {
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		return rejected
	})
	if !errors.Is(err, rejected) || !strings.Contains(err.Error(), "did not persist") {
		t.Fatalf("uncommitted state was accepted or lost original cause: %v", err)
	}
	persisted, err := readStateDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonicalStateJSON(store.st), canonicalStateJSON(persisted)) {
		t.Fatal("failed commit changed durable state")
	}
}
