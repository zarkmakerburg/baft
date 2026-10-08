package bcc

import (
	"errors"
	"time"
)

// CanaryEvidence records independently verified rollout gates. A missing
// measurement is not a successful measurement.
type CanaryEvidence struct {
	ArtifactSignatureVerified   bool      `json:"artifact_signature_verified"`
	ArtifactDigestVerified      bool      `json:"artifact_digest_verified"`
	ConfigCompatible            bool      `json:"config_compatible"`
	AgentHealthy                bool      `json:"agent_healthy"`
	CarrierHealthy              bool      `json:"carrier_healthy"`
	TunnelsHealthy              bool      `json:"tunnels_healthy"`
	RestartErrorsAcceptable     bool      `json:"restart_errors_acceptable"`
	LatencyRegressionAcceptable bool      `json:"latency_regression_acceptable"`
	GoodputRegressionAcceptable bool      `json:"goodput_regression_acceptable"`
	NoPendingNodeTransactions   bool      `json:"no_pending_node_transactions"`
	ObservedAt                  time.Time `json:"observed_at"`
}

// ValidateCanaryEvidence is fail-closed. It only validates supplied evidence;
// it never dispatches jobs or asserts that evidence was collected remotely.
func ValidateCanaryEvidence(e CanaryEvidence, now time.Time, maxAge time.Duration) error {
	if maxAge <= 0 || e.ObservedAt.IsZero() || e.ObservedAt.After(now) || now.Sub(e.ObservedAt) > maxAge {
		return errors.New("canary evidence missing, future-dated or stale")
	}
	if !e.ArtifactSignatureVerified || !e.ArtifactDigestVerified {
		return errors.New("canary artifact verification incomplete")
	}
	if !e.ConfigCompatible {
		return errors.New("canary config compatibility unverified")
	}
	if !e.AgentHealthy || !e.CarrierHealthy || !e.TunnelsHealthy {
		return errors.New("canary service or tunnel health unverified")
	}
	if !e.RestartErrorsAcceptable || !e.LatencyRegressionAcceptable || !e.GoodputRegressionAcceptable {
		return errors.New("canary performance regression gate unverified")
	}
	if !e.NoPendingNodeTransactions {
		return errors.New("canary has pending node transactions")
	}
	return nil
}
