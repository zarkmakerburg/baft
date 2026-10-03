package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

type lifecycleActivationSnapshot struct {
	carrierID         string
	carrierEpoch      uint64
	carrierGeneration uint64
	physicalID        uint64
	sender            *outboundSender
}

func snapshotLifecycleActivation(p *Peer) lifecycleActivationSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return lifecycleActivationSnapshot{
		carrierID:         p.carrierID,
		carrierEpoch:      p.carrierEpoch,
		carrierGeneration: p.carrierGeneration,
		physicalID:        p.carrierPhysicalInstanceID,
		sender:            p.sender,
	}
}

func finalizedUnactivatedLifecycleFixture(t *testing.T, candidate string) (*Peer, context.Context, context.CancelFunc, RecoveryControl) {
	t.Helper()
	p, ctx, cancel, _, ctl := prepareCommitFixture(t, candidate)
	if _, err := p.PublishRecoveryCommit(ctl); err != nil {
		cancel()
		t.Fatal(err)
	}
	finalCtl := proveFinalizationForTest(t, p, ctl)
	return p, ctx, cancel, finalCtl
}

func waitPeerWorkers(t *testing.T, p *Peer) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Session workers did not drain")
	}
}

// This is the exact lifetime ordering that raced in the P0-C soak. The first
// half proves teardown wins without any activation-side mutation. The second
// half proves teardown cannot cross an outstanding activation/read lease.
func TestFinalizeRecoveryCommitRunTeardownInterleavings(t *testing.T) {
	t.Run("teardown-first-rejects-before-mutation", func(t *testing.T) {
		p, ctx, cancel, ctl := finalizedUnactivatedLifecycleFixture(t, "candidate-teardown-first")
		defer cancel()

		p.beginRunTeardown()
		before := snapshotLifecycleActivation(p)

		err := finalizeRecoveryAcceptedForTest(p, ctx, ctl)
		if !errors.Is(err, ErrCarrierUnavailable) {
			t.Fatalf("FinalizeRecoveryCommit after Run teardown err=%v, want ErrCarrierUnavailable", err)
		}
		after := snapshotLifecycleActivation(p)
		if after != before {
			t.Fatalf("activation mutated exited Session before=%+v after=%+v", before, after)
		}
		p.runLifecycleMu.RLock()
		active, exiting := p.runActive, p.runExiting
		p.runLifecycleMu.RUnlock()
		if active || !exiting {
			t.Fatalf("lifecycle active=%v exiting=%v, want false/true", active, exiting)
		}
		waitPeerWorkers(t, p)
	})

	t.Run("activation-lease-blocks-teardown", func(t *testing.T) {
		p, ctx, cancel, ctl := finalizedUnactivatedLifecycleFixture(t, "candidate-activation-first")

		// Hold the same read lease used by activatePreparedCarrierOwned across
		// the completed activation. A teardown request must wait until that
		// lease is released; this makes the original Add/Wait ordering
		// deterministic instead of scheduler-dependent.
		p.runLifecycleMu.RLock()
		err := finalizeRecoveryAcceptedForTest(p, ctx, ctl)
		if err != nil {
			p.runLifecycleMu.RUnlock()
			cancel()
			t.Fatal(err)
		}
		activated := snapshotLifecycleActivation(p)
		if activated.carrierEpoch != ctl.NextEpoch || activated.carrierID != ctl.CandidateID || activated.carrierGeneration <= 1 {
			p.runLifecycleMu.RUnlock()
			cancel()
			t.Fatalf("FinalizeRecoveryCommit did not activate candidate: %+v ctl=%+v", activated, ctl)
		}

		teardownDone := make(chan struct{})
		go func() {
			p.beginRunTeardown()
			close(teardownDone)
		}()
		select {
		case <-teardownDone:
			p.runLifecycleMu.RUnlock()
			cancel()
			t.Fatal("Run teardown crossed an outstanding activation lease")
		case <-time.After(20 * time.Millisecond):
		}

		p.runLifecycleMu.RUnlock()
		select {
		case <-teardownDone:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("Run teardown did not proceed after activation lease released")
		}

		after := snapshotLifecycleActivation(p)
		if after != activated {
			cancel()
			t.Fatalf("teardown changed activated carrier state before=%+v after=%+v", activated, after)
		}
		cancel()
		p.closeAll()
		waitPeerWorkers(t, p)
	})
}
