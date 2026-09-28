package metrics

import (
	"fmt"
	"net/http"
)

type Snapshot struct {
	ActiveFlows           int
	ReceiveUsedBytes      int64
	ReplayUsedBytes       int64
	TotalUsedBytes        int64
	AcceptedBacklogBytes  uint64
	CreditExposureBytes   uint64
	ReplayOutstandingBytes uint64
	InvariantViolations   int
	RecoveryAttempts      uint64
	RecoveryCommits       uint64
	RecoveryAborts        uint64
	RecoveryPostCommitFailures uint64
	RecoveryCurrentEpoch  uint64
	RecoveryReplayedBytes uint64
	RecoveryFailures      map[string]uint64
}

type Provider func() Snapshot

// Handler exposes a deliberately small Prometheus text-format surface.
// It exports conservation-oriented aggregate metrics and intentionally avoids
// peer identities, route names, target addresses, or per-stream labels.
func Handler(provider Provider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		s := Snapshot{}
		if provider != nil {
			s = provider()
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")

		fmt.Fprintln(w, "# HELP baft_active_flows Number of active BAFT application flows.")
		fmt.Fprintln(w, "# TYPE baft_active_flows gauge")
		fmt.Fprintf(w, "baft_active_flows %d\n", s.ActiveFlows)

		fmt.Fprintln(w, "# HELP baft_resource_receive_bytes Receive-pool bytes currently reserved.")
		fmt.Fprintln(w, "# TYPE baft_resource_receive_bytes gauge")
		fmt.Fprintf(w, "baft_resource_receive_bytes %d\n", s.ReceiveUsedBytes)

		fmt.Fprintln(w, "# HELP baft_resource_replay_bytes Replay-pool bytes currently reserved.")
		fmt.Fprintln(w, "# TYPE baft_resource_replay_bytes gauge")
		fmt.Fprintf(w, "baft_resource_replay_bytes %d\n", s.ReplayUsedBytes)

		fmt.Fprintln(w, "# HELP baft_resource_total_bytes Total BAFT data-memory bytes currently reserved.")
		fmt.Fprintln(w, "# TYPE baft_resource_total_bytes gauge")
		fmt.Fprintf(w, "baft_resource_total_bytes %d\n", s.TotalUsedBytes)

		fmt.Fprintln(w, "# HELP baft_conservation_accepted_backlog_bytes Bytes accepted by BAFT but not yet delivered to target sockets.")
		fmt.Fprintln(w, "# TYPE baft_conservation_accepted_backlog_bytes gauge")
		fmt.Fprintf(w, "baft_conservation_accepted_backlog_bytes %d\n", s.AcceptedBacklogBytes)

		fmt.Fprintln(w, "# HELP baft_conservation_credit_exposure_bytes Advertised receive credit ahead of delivered target bytes.")
		fmt.Fprintln(w, "# TYPE baft_conservation_credit_exposure_bytes gauge")
		fmt.Fprintf(w, "baft_conservation_credit_exposure_bytes %d\n", s.CreditExposureBytes)

		fmt.Fprintln(w, "# HELP baft_conservation_replay_outstanding_bytes Sent bytes not yet cumulatively acknowledged by peers.")
		fmt.Fprintln(w, "# TYPE baft_conservation_replay_outstanding_bytes gauge")
		fmt.Fprintf(w, "baft_conservation_replay_outstanding_bytes %d\n", s.ReplayOutstandingBytes)

		fmt.Fprintln(w, "# HELP baft_conservation_invariant_violations Number of currently observed TWRL invariant violations.")
		fmt.Fprintln(w, "# TYPE baft_conservation_invariant_violations gauge")
		fmt.Fprintf(w, "baft_conservation_invariant_violations %d\n", s.InvariantViolations)

		fmt.Fprintln(w, "# HELP baft_recovery_attempts_total Same-process carrier recovery attempts.")
		fmt.Fprintln(w, "# TYPE baft_recovery_attempts_total counter")
		fmt.Fprintf(w, "baft_recovery_attempts_total %d\n", s.RecoveryAttempts)

		fmt.Fprintln(w, "# HELP baft_recovery_commits_total Successfully committed same-process carrier recoveries.")
		fmt.Fprintln(w, "# TYPE baft_recovery_commits_total counter")
		fmt.Fprintf(w, "baft_recovery_commits_total %d\n", s.RecoveryCommits)

		fmt.Fprintln(w, "# HELP baft_recovery_aborts_total Aborted same-process carrier recoveries.")
		fmt.Fprintln(w, "# TYPE baft_recovery_aborts_total counter")
		fmt.Fprintf(w, "baft_recovery_aborts_total %d\n", s.RecoveryAborts)
		fmt.Fprintln(w, "# HELP baft_recovery_post_commit_failures_total Failures after ECRL authority commit; old epochs remain fenced.")
		fmt.Fprintln(w, "# TYPE baft_recovery_post_commit_failures_total counter")
		fmt.Fprintf(w, "baft_recovery_post_commit_failures_total %d\n", s.RecoveryPostCommitFailures)


		fmt.Fprintln(w, "# HELP baft_recovery_current_epoch Highest current ECRL session epoch in this runtime.")
		fmt.Fprintln(w, "# TYPE baft_recovery_current_epoch gauge")
		fmt.Fprintf(w, "baft_recovery_current_epoch %d\n", s.RecoveryCurrentEpoch)

		fmt.Fprintln(w, "# HELP baft_recovery_replayed_bytes_total Bytes replayed from bounded session replay memory during recovery.")
		fmt.Fprintln(w, "# TYPE baft_recovery_replayed_bytes_total counter")
		fmt.Fprintf(w, "baft_recovery_replayed_bytes_total %d\n", s.RecoveryReplayedBytes)

		fmt.Fprintln(w, "# HELP baft_recovery_failures_total Recovery failures by bounded reason.")
		fmt.Fprintln(w, "# TYPE baft_recovery_failures_total counter")
		for _,reason:=range []string{"candidate_setup","snapshot_exchange","peer_restart","state_mismatch","replay_unavailable","lease_conflict","commit","other"} {
			fmt.Fprintf(w, "baft_recovery_failures_total{reason=%q} %d\n", reason, s.RecoveryFailures[reason])
		}
	})
}
