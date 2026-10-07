package bcc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zarkmakerburg/baft/internal/agentjob"
	"github.com/zarkmakerburg/baft/internal/sshboot"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	SSHMigStaging     = "STAGING"
	SSHMigAwaitVerify = "AWAIT_VERIFY"
	SSHMigCommitting  = "COMMITTING"
	SSHMigComplete    = "COMPLETE"
	SSHMigRollingBack = "ROLLING_BACK"
	SSHMigRolledBack  = "ROLLED_BACK"
	SSHMigFailed      = "FAILED"
)

type SSHMigration struct {
	ID          string    `json:"id"`
	NodeID      string    `json:"node_id"`
	Host        string    `json:"host"`
	OldPort     int       `json:"old_port"`
	NewPort     int       `json:"new_port"`
	User        string    `json:"user"`
	Fingerprint string    `json:"fingerprint"`
	PlanHash    string    `json:"plan_hash"`
	Phase       string    `json:"phase"`
	JobID       string    `json:"job_id,omitempty"`
	VerifiedAt  time.Time `json:"verified_at,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type SSHMigrationRequest struct {
	NodeID      string `json:"node_id"`
	OldPort     int    `json:"old_port"`
	NewPort     int    `json:"new_port"`
	User        string `json:"user"`
	Fingerprint string `json:"fingerprint"`
}

func sshPlanHash(r SSHMigrationRequest) string {
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (s *Store) PlanSSHMigration(r SSHMigrationRequest, now time.Time) (SSHMigration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.st.Nodes[r.NodeID]
	if !ok || n.Revoked {
		return SSHMigration{}, errors.New("node is not active/enrolled")
	}
	if r.OldPort < 1 || r.OldPort > 65535 || r.NewPort < 1 || r.NewPort > 65535 || r.OldPort == r.NewPort {
		return SSHMigration{}, errors.New("invalid SSH port transition")
	}
	if strings.TrimSpace(r.User) == "" || !strings.HasPrefix(r.Fingerprint, "SHA256:") {
		return SSHMigration{}, errors.New("ssh user and pinned SHA256 fingerprint are required")
	}
	for _, m := range s.st.SSHMigrations {
		if m.NodeID != r.NodeID {
			continue
		}
		same := m.OldPort == r.OldPort && m.NewPort == r.NewPort && m.User == r.User && m.Fingerprint == r.Fingerprint
		if same && m.Phase != SSHMigRolledBack && m.Phase != SSHMigFailed {
			return m, nil
		}
		if m.Phase != SSHMigComplete && m.Phase != SSHMigRolledBack && m.Phase != SSHMigFailed {
			return SSHMigration{}, errors.New("node already has an active SSH migration")
		}
	}
	id := "sshm-" + newTunnelID()[4:]
	m := SSHMigration{ID: id, NodeID: r.NodeID, Host: n.Address, OldPort: r.OldPort, NewPort: r.NewPort, User: r.User, Fingerprint: r.Fingerprint, PlanHash: sshPlanHash(r), Phase: "PLAN", CreatedAt: now, UpdatedAt: now}
	return m, nil
}
func (s *Store) StartSSHMigration(r SSHMigrationRequest, plan string, now time.Time) (SSHMigration, error) {
	m, err := s.PlanSSHMigration(r, now)
	if err != nil {
		return m, err
	}
	if m.PlanHash != plan {
		return m, errors.New("SSH migration plan changed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.SSHMigrations == nil {
		s.st.SSHMigrations = map[string]SSHMigration{}
	}
	j := s.newJobLocked(Job{Type: agentjob.ActionSSHMigrationStage, NodeID: m.NodeID, Params: map[string]string{"old_port": strconv.Itoa(m.OldPort), "new_port": strconv.Itoa(m.NewPort)}})
	m.JobID = j.ID
	m.Phase = SSHMigStaging
	s.st.SSHMigrations[m.ID] = m
	return m, s.saveLocked()
}
func (s *Store) AdvanceSSHMigrations(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dirty := false
	for id, m := range s.st.SSHMigrations {
		if m.JobID == "" {
			continue
		}
		j, ok := s.st.Jobs[m.JobID]
		if !ok || j.Status == "pending" || j.Status == "running" {
			continue
		}
		switch m.Phase {
		case SSHMigStaging:
			if j.Status == "succeeded" {
				m.Phase = SSHMigAwaitVerify
				m.JobID = ""
			} else {
				m.Phase = SSHMigRollingBack
				j2 := s.newJobLocked(Job{Type: agentjob.ActionSSHMigrationRollback, NodeID: m.NodeID})
				m.JobID = j2.ID
				m.Error = "staging failed: " + j.Message
			}
		case SSHMigCommitting:
			if j.Status == "succeeded" {
				m.Phase = SSHMigComplete
				m.JobID = ""
			} else {
				m.Phase = SSHMigRollingBack
				j2 := s.newJobLocked(Job{Type: agentjob.ActionSSHMigrationRollback, NodeID: m.NodeID})
				m.JobID = j2.ID
				m.Error = "commit failed: " + j.Message
			}
		case SSHMigRollingBack:
			if j.Status == "succeeded" {
				m.Phase = SSHMigRolledBack
				m.JobID = ""
			} else {
				m.Phase = SSHMigFailed
				m.Error += "; rollback failed: " + j.Message
			}
		}
		m.UpdatedAt = now
		s.st.SSHMigrations[id] = m
		dirty = true
	}
	if dirty {
		return s.saveLocked()
	}
	return nil
}
func (s *Store) GetSSHMigration(id string) (SSHMigration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.st.SSHMigrations[id]
	return m, ok
}
func (s *Store) VerifySSHMigration(ctx context.Context, id string, auth sshboot.Auth, now time.Time) (SSHMigration, error) {
	m, ok := s.GetSSHMigration(id)
	if !ok {
		return m, errors.New("migration not found")
	}
	if m.Phase != SSHMigAwaitVerify {
		return m, fmt.Errorf("migration phase is %s", m.Phase)
	}
	if err := sshboot.VerifyAuthenticated(ctx, sshboot.Target{Host: m.Host, Port: m.NewPort, User: m.User}, auth, m.Fingerprint); err != nil {
		s.mu.Lock()
		m.Phase = SSHMigRollingBack
		m.Error = "authenticated verification failed"
		j := s.newJobLocked(Job{Type: agentjob.ActionSSHMigrationRollback, NodeID: m.NodeID})
		m.JobID = j.ID
		m.UpdatedAt = now
		s.st.SSHMigrations[id] = m
		e := s.saveLocked()
		s.mu.Unlock()
		if e != nil {
			return m, e
		}
		return m, err
	}
	s.mu.Lock()
	m.VerifiedAt = now
	m.Phase = SSHMigCommitting
	j := s.newJobLocked(Job{Type: agentjob.ActionSSHMigrationCommit, NodeID: m.NodeID, Params: map[string]string{"new_port": strconv.Itoa(m.NewPort)}})
	m.JobID = j.ID
	m.UpdatedAt = now
	s.st.SSHMigrations[id] = m
	err := s.saveLocked()
	s.mu.Unlock()
	return m, err
}
func (s *Server) sshMigrationAPI(w http.ResponseWriter, r *http.Request) {
	if !s.admin(w, r) {
		return
	}
	_ = s.store.AdvanceSSHMigrations(s.now())
	switch r.Method {
	case http.MethodPost:
		var in struct {
			Action     string              `json:"action"`
			Request    SSHMigrationRequest `json:"request"`
			PlanHash   string              `json:"plan_hash"`
			ID         string              `json:"id"`
			Password   string              `json:"password"`
			PrivateKey string              `json:"private_key"`
			Passphrase string              `json:"passphrase"`
		}
		if err := decodeJSON(r, &in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		switch in.Action {
		case "plan":
			m, e := s.store.PlanSSHMigration(in.Request, s.now())
			if e != nil {
				http.Error(w, e.Error(), 400)
				return
			}
			_, _ = s.audit.Append(AuditEntry{Timestamp: s.now().UTC(), Actor: "admin", RemoteIP: s.clientIP(r), Action: "ssh.migration.plan", Outcome: "planned", Details: withRequest(r, map[string]any{"node_id": m.NodeID, "old_port": m.OldPort, "new_port": m.NewPort, "plan_hash": m.PlanHash})})
			writeJSON(w, 200, m)
		case "start":
			m, e := s.store.StartSSHMigration(in.Request, in.PlanHash, s.now())
			if e != nil {
				http.Error(w, e.Error(), 400)
				return
			}
			_, _ = s.audit.Append(AuditEntry{Timestamp: s.now().UTC(), Actor: "admin", RemoteIP: s.clientIP(r), Action: "ssh.migration.start", Outcome: m.Phase, Details: withRequest(r, map[string]any{"migration_id": m.ID, "node_id": m.NodeID, "new_port": m.NewPort})})
			writeJSON(w, 201, m)
		case "verify":
			m, e := s.store.VerifySSHMigration(r.Context(), in.ID, sshboot.Auth{Password: in.Password, PrivateKey: in.PrivateKey, Passphrase: in.Passphrase}, s.now())
			if e != nil {
				http.Error(w, e.Error(), 400)
				return
			}
			_, _ = s.audit.Append(AuditEntry{Timestamp: s.now().UTC(), Actor: "admin", RemoteIP: s.clientIP(r), Action: "ssh.migration.verify", Outcome: m.Phase, Details: withRequest(r, map[string]any{"migration_id": m.ID, "node_id": m.NodeID, "new_port": m.NewPort})})
			writeJSON(w, 200, m)
		default:
			http.Error(w, "unknown action", 400)
		}
	default:
		http.Error(w, "method not allowed", 405)
	}
}
