package bcc

import (
	"testing"
	"time"
)

func sshMigStore(t *testing.T) *Store {
	s, err := OpenStore(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.st.Nodes["n1"] = Node{ID: "n1", Address: "127.0.0.1"}
	s.mu.Unlock()
	return s
}
func req() SSHMigrationRequest {
	return SSHMigrationRequest{NodeID: "n1", OldPort: 22, NewPort: 2222, User: "root", Fingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
}
func TestSSHMigrationPlanStartAndStageFailureRollsBack(t *testing.T) {
	s := sshMigStore(t)
	now := time.Unix(100, 0)
	p, err := s.PlanSSHMigration(req(), now)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.StartSSHMigration(req(), p.PlanHash, now)
	if err != nil {
		t.Fatal(err)
	}
	if m.Phase != SSHMigStaging {
		t.Fatal(m.Phase)
	}
	s.mu.Lock()
	j := s.st.Jobs[m.JobID]
	j.Status = "failed"
	j.Message = "sshd -t failed"
	s.st.Jobs[m.JobID] = j
	s.mu.Unlock()
	if err := s.AdvanceSSHMigrations(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	m, _ = s.GetSSHMigration(m.ID)
	if m.Phase != SSHMigRollingBack {
		t.Fatalf("phase=%s", m.Phase)
	}
	rb := s.st.Jobs[m.JobID]
	if rb.Type != "ssh_migrate_rollback" {
		t.Fatalf("rollback job=%s", rb.Type)
	}
}
func TestSSHMigrationStageSuccessKeepsOldPortUntilAuthVerification(t *testing.T) {
	s := sshMigStore(t)
	now := time.Unix(200, 0)
	p, _ := s.PlanSSHMigration(req(), now)
	m, _ := s.StartSSHMigration(req(), p.PlanHash, now)
	s.mu.Lock()
	j := s.st.Jobs[m.JobID]
	j.Status = "succeeded"
	s.st.Jobs[m.JobID] = j
	s.mu.Unlock()
	if err := s.AdvanceSSHMigrations(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	m, _ = s.GetSSHMigration(m.ID)
	if m.Phase != SSHMigAwaitVerify || m.JobID != "" {
		t.Fatalf("unexpected %+v", m)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.st.Jobs {
		if j.Type == "ssh_migrate_commit" {
			t.Fatal("commit queued before authenticated verification")
		}
	}
}
func TestSSHMigrationPlanHashAndSingleActiveTransaction(t *testing.T) {
	s := sshMigStore(t)
	now := time.Unix(300, 0)
	p, _ := s.PlanSSHMigration(req(), now)
	if _, err := s.StartSSHMigration(req(), "wrong", now); err == nil {
		t.Fatal("accepted stale plan")
	}
	m, err := s.StartSSHMigration(req(), p.PlanHash, now)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.PlanSSHMigration(req(), now)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != m.ID {
		t.Fatal("repeated apply was not idempotent")
	}
}
