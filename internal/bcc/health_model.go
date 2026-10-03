package bcc

import (
	"fmt"
	"time"
)

// Layered health with hysteresis (HQ P1-1).
//
// Each layer of a node has its own state machine. A state changes only after
// several consistent samples over a minimum time, never on one reading:
//
//	UNKNOWN --(UpAfter OK)--> UP --(DegradeAfter BAD)--> DEGRADED --(DownAfter BAD, MinDown)--> DOWN
//	DEGRADED --(RecoverAfter OK)--> UP
//	DOWN --(first OK)--> RECOVERING --(UpAfter OK, MinRecover)--> UP
//	RECOVERING --(DegradeAfter consecutive BAD)--> DOWN
//
// A sample is OK, BAD or NONE. NONE means "no evidence" (nothing reported,
// nothing probed yet, no sessions): it never counts as OK or BAD, it resets the
// runs, and after UnknownAfter of continuous silence the layer becomes UNKNOWN.
// Silence is therefore never read as DOWN and never as UP. NOT_ASSESSED is a
// fixed marker for a layer BCC has no signal for; it is not a state a layer can
// enter or leave.

const (
	HealthUnknown     = "UNKNOWN"
	HealthUp          = "UP"
	HealthDegraded    = "DEGRADED"
	HealthDown        = "DOWN"
	HealthRecovering  = "RECOVERING"
	HealthNotAssessed = "NOT_ASSESSED"

	SampleOK   = "OK"
	SampleBad  = "BAD"
	SampleNone = "NONE"
)

// HealthPolicy is the hysteresis. All thresholds are samples taken at the
// health interval (10 s by default) plus a minimum time.
type HealthPolicy struct {
	DegradeAfter int           // consecutive BAD: UP/UNKNOWN -> DEGRADED
	DownAfter    int           // consecutive BAD: DEGRADED -> DOWN ...
	MinDown      time.Duration // ... and at least this long since the first BAD of the run
	RecoverAfter int           // consecutive OK: DEGRADED -> UP
	UpAfter      int           // consecutive OK: UNKNOWN/RECOVERING -> UP ...
	MinRecover   time.Duration // ... and (from RECOVERING) at least this long since the first OK
	UnknownAfter time.Duration // continuous NONE before a layer becomes UNKNOWN
	MaxGap       time.Duration // a longer gap between samples (BCC was down) resets the runs
}

func DefaultHealthPolicy() HealthPolicy {
	return HealthPolicy{
		DegradeAfter: 2, DownAfter: 5, MinDown: 30 * time.Second,
		RecoverAfter: 3, UpAfter: 5, MinRecover: 30 * time.Second,
		UnknownAfter: 3 * time.Minute, MaxGap: 2 * time.Minute,
	}
}

// LayerRecord is the persisted state of one layer of one node.
type LayerRecord struct {
	State        string    `json:"state"`
	Since        time.Time `json:"since"`
	OKRun        int       `json:"ok_run"`
	BadRun       int       `json:"bad_run"`
	RunStart     time.Time `json:"run_start,omitempty"`
	NoneSince    time.Time `json:"none_since,omitempty"`
	LastOKAt     time.Time `json:"last_ok_at,omitempty"`
	LastSample   string    `json:"last_sample,omitempty"`
	LastSampleAt time.Time `json:"last_sample_at,omitempty"`
	LastEvidence string    `json:"last_evidence,omitempty"`
}

// Sample is one reading with the evidence behind it.
type Sample struct {
	Kind     string
	Evidence string
}

// LayerTransition is a state change with the reason and the evidence.
type LayerTransition struct {
	At       time.Time `json:"at"`
	Layer    string    `json:"layer"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	NodeFrom string    `json:"node_from,omitempty"`
	NodeTo   string    `json:"node_to,omitempty"`
	Reason   string    `json:"reason"`
	Evidence string    `json:"evidence"`
	// EventID identifies this transition in history, audit and alerts.
	EventID string `json:"event_id,omitempty"`
}

// observe applies one sample. It is deterministic: the result depends only on
// the record, the sample, the time and the policy.
func observe(rec *LayerRecord, s Sample, now time.Time, p HealthPolicy) (string, string, bool) {
	if rec.State == "" {
		rec.State, rec.Since = HealthUnknown, now
	}
	if !rec.LastSampleAt.IsZero() && now.Sub(rec.LastSampleAt) > p.MaxGap {
		// Nobody was watching (BCC was down or stalled): the runs say nothing
		// about the gap, so start counting again. The state itself is kept.
		rec.OKRun, rec.BadRun, rec.RunStart, rec.NoneSince = 0, 0, time.Time{}, time.Time{}
	}
	rec.LastSample, rec.LastSampleAt, rec.LastEvidence = s.Kind, now, s.Evidence
	if s.Kind == SampleOK {
		rec.LastOKAt = now
	}
	from := rec.State
	move := func(to, reason string) (string, string, bool) {
		rec.State, rec.Since = to, now
		return from, reason, true
	}
	switch s.Kind {
	case SampleNone:
		rec.OKRun, rec.BadRun, rec.RunStart = 0, 0, time.Time{}
		if rec.NoneSince.IsZero() {
			rec.NoneSince = now
		}
		if rec.State != HealthUnknown && now.Sub(rec.NoneSince) >= p.UnknownAfter {
			return move(HealthUnknown, fmt.Sprintf("no evidence for %s", now.Sub(rec.NoneSince).Round(time.Second)))
		}
	case SampleOK:
		rec.NoneSince = time.Time{}
		rec.BadRun = 0
		rec.OKRun++
		if rec.OKRun == 1 {
			rec.RunStart = now
		}
		switch rec.State {
		case HealthUnknown:
			if rec.OKRun >= p.UpAfter {
				return move(HealthUp, fmt.Sprintf("%d consecutive OK samples", rec.OKRun))
			}
		case HealthDegraded:
			if rec.OKRun >= p.RecoverAfter {
				return move(HealthUp, fmt.Sprintf("%d consecutive OK samples", rec.OKRun))
			}
		case HealthDown:
			rec.OKRun, rec.RunStart = 1, now
			return move(HealthRecovering, "first OK sample after DOWN")
		case HealthRecovering:
			if rec.OKRun >= p.UpAfter && now.Sub(rec.RunStart) >= p.MinRecover {
				return move(HealthUp, fmt.Sprintf("%d consecutive OK samples over %s", rec.OKRun, now.Sub(rec.RunStart).Round(time.Second)))
			}
		}
	case SampleBad:
		rec.NoneSince = time.Time{}
		rec.OKRun = 0
		rec.BadRun++
		if rec.BadRun == 1 {
			rec.RunStart = now
		}
		switch rec.State {
		case HealthUp, HealthUnknown:
			if rec.BadRun >= p.DegradeAfter {
				return move(HealthDegraded, fmt.Sprintf("%d consecutive BAD samples", rec.BadRun))
			}
		case HealthDegraded:
			if rec.BadRun >= p.DownAfter && now.Sub(rec.RunStart) >= p.MinDown {
				return move(HealthDown, fmt.Sprintf("%d consecutive BAD samples over %s", rec.BadRun, now.Sub(rec.RunStart).Round(time.Second)))
			}
		case HealthRecovering:
			if rec.BadRun >= p.DegradeAfter {
				return move(HealthDown, fmt.Sprintf("%d consecutive BAD samples while recovering", rec.BadRun))
			}
		}
	}
	return "", "", false
}

// Layer ids. L3, L5 and L6 have no signal in BCC yet and are NOT_ASSESSED.
const (
	LayerAgent = "A"
	LayerL0    = "L0"
	LayerL1    = "L1"
	LayerL2    = "L2"
	LayerL3    = "L3"
	LayerL4    = "L4"
	LayerL5    = "L5"
	LayerL6    = "L6"
)

type layerInfo struct {
	ID, Name, NotAssessed string
}

var healthLayers = []layerInfo{
	{LayerL0, "BAFT process reporting (telemetry)", ""},
	{LayerL1, "node reachable (BCC TCP probe)", ""},
	{LayerL2, "peer sessions authenticated", ""},
	{LayerL3, "session correctness", "BCC receives no recovery or epoch signal yet"},
	{LayerL4, "route probes (as run by the node)", ""},
	{LayerL5, "target reachable", "telemetry does not say which probe is a target"},
	{LayerL6, "application traffic", "needs an end-to-end probe"},
	{LayerAgent, "agent polling (management plane)", ""},
}

// NodeLayerView is one layer as shown to the operator.
type NodeLayerView struct {
	Layer    string    `json:"layer"`
	Name     string    `json:"name"`
	State    string    `json:"state"`
	Since    time.Time `json:"since,omitempty"`
	Evidence string    `json:"evidence,omitempty"`
	Note     string    `json:"note,omitempty"`
}

// overallHealth derives the node's state from its layers. The node is DOWN
// only when its process or its reachability is DOWN, UP only when both are UP
// and nothing is degraded or recovering; one layer can neither make another
// look healthy nor hide that it is not. Layers with no evidence are listed as
// unproven, not counted as healthy or as failed.
func overallHealth(layers map[string]*LayerRecord) (state string, why []string) {
	get := func(id string) string {
		if r := layers[id]; r != nil && r.State != "" {
			return r.State
		}
		return HealthUnknown
	}
	core := []string{LayerL0, LayerL1}
	anyEvidence := false
	for _, id := range []string{LayerL0, LayerL1, LayerL2, LayerL4, LayerAgent} {
		if get(id) != HealthUnknown {
			anyEvidence = true
		}
	}
	if !anyEvidence {
		return HealthUnknown, []string{"no layer has evidence yet"}
	}
	for _, id := range core {
		if get(id) == HealthDown {
			return HealthDown, []string{id + " is DOWN"}
		}
	}
	for _, id := range core {
		if get(id) == HealthRecovering {
			return HealthRecovering, []string{id + " is RECOVERING"}
		}
	}
	var bad []string
	for _, id := range []string{LayerL0, LayerL1, LayerL2, LayerL4, LayerAgent} {
		switch get(id) {
		case HealthDegraded, HealthDown, HealthRecovering:
			bad = append(bad, id+" is "+get(id))
		}
	}
	if len(bad) > 0 {
		return HealthDegraded, bad
	}
	for _, id := range core {
		if get(id) != HealthUp {
			return HealthUnknown, []string{id + " has no proven state (" + get(id) + ")"}
		}
	}
	return HealthUp, nil
}
