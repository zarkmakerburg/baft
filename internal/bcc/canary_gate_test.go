package bcc

import (
	"testing"
	"time"
)

func TestCanaryEvidenceFailsClosed(t *testing.T) {
	now := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	good := CanaryEvidence{true, true, true, true, true, true, true, true, true, true, now}
	if err := ValidateCanaryEvidence(good, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name      string
		breakGate func(*CanaryEvidence)
	}{
		{"signature", func(e *CanaryEvidence) { e.ArtifactSignatureVerified = false }},
		{"digest", func(e *CanaryEvidence) { e.ArtifactDigestVerified = false }},
		{"compatibility", func(e *CanaryEvidence) { e.ConfigCompatible = false }},
		{"agent", func(e *CanaryEvidence) { e.AgentHealthy = false }},
		{"carrier", func(e *CanaryEvidence) { e.CarrierHealthy = false }},
		{"tunnels", func(e *CanaryEvidence) { e.TunnelsHealthy = false }},
		{"restarts", func(e *CanaryEvidence) { e.RestartErrorsAcceptable = false }},
		{"latency", func(e *CanaryEvidence) { e.LatencyRegressionAcceptable = false }},
		{"goodput", func(e *CanaryEvidence) { e.GoodputRegressionAcceptable = false }},
		{"transactions", func(e *CanaryEvidence) { e.NoPendingNodeTransactions = false }},
		{"stale", func(e *CanaryEvidence) { e.ObservedAt = now.Add(-2 * time.Minute) }},
		{"future", func(e *CanaryEvidence) { e.ObservedAt = now.Add(time.Second) }},
		{"missing time", func(e *CanaryEvidence) { e.ObservedAt = time.Time{} }},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			e := good
			tc.breakGate(&e)
			if err := ValidateCanaryEvidence(e, now, time.Minute); err == nil {
				t.Fatal("unverified evidence accepted")
			}
		})
	}
	if err := ValidateCanaryEvidence(good, now, 0); err == nil {
		t.Fatal("zero freshness window accepted")
	}
}
