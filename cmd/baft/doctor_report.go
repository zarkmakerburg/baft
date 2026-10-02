package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Doctor report: a verdict, the layer each finding belongs to, and for every
// problem the same four things in the same order: Problem, Evidence, Impact,
// Fix. The doctor is read-only; fix commands are shown, never run.

const (
	verdictHealthy  = "HEALTHY"
	verdictDegraded = "DEGRADED"
	verdictFailing  = "FAILING"
)

// layers is the health model. A layer nothing observed is NOT_ASSESSED, never
// PASS: absence of evidence is not health.
var layers = []struct{ id, name, unassessed string }{
	{"L0", "process alive", "the service state could not be read"},
	{"L1", "carrier path (TCP reachability only)", "this node has no peer or listener configured"},
	{"L2", "peer authenticated", "doctor does not open an authenticated session; see BCC health and telemetry"},
	{"L3", "session correctness", "needs the metrics endpoint"},
	{"L4", "route available", "this node has no outbound route"},
	{"L5", "target reachable", "this node has no inbound route target"},
	{"L6", "application traffic", "needs an end-to-end probe, which doctor does not send"},
}

type layerState struct {
	Layer string `json:"layer"`
	Name  string `json:"name"`
	State string `json:"state"` // PASS, WARN, FAIL, NOT_ASSESSED
	Note  string `json:"note,omitempty"`
}

type summary struct {
	Verdict      string       `json:"verdict"`
	LikelyDomain string       `json:"likely_failure_domain,omitempty"`
	Confidence   string       `json:"confidence,omitempty"`
	NextAction   string       `json:"next_action,omitempty"`
	Layers       []layerState `json:"layers"`
}

// annotate gives a finding that is not ok its layer, failure domain, problem
// and impact. The evidence (Detail) and the fix (Hint) are set where the
// check runs, because only that code knows them.
func annotate(c *check) {
	if c.Status == checkOK {
		return
	}
	set := func(layer, domain, problem, impact string) {
		c.Layer, c.Domain, c.Problem, c.Impact = layer, domain, problem, impact
	}
	switch {
	case c.Name == "config":
		set("", "configuration", "the configuration cannot be loaded",
			"the service cannot start with it; a running service keeps its old configuration until restarted")
	case c.Name == "revocation file":
		set("", "configuration", "the revocation file is invalid", "the listener refuses to start")
	case c.Name == "private key":
		set("", "host security", "a private key is exposed to other users or unreadable",
			"the runtime refuses an exposed key, so the carrier cannot come up")
	case c.Name == "service":
		set("L0", "service process", "the service is not running or keeps restarting",
			"no carrier and no traffic while it is down")
	case c.Name == "release":
		set("", "release provenance", "this is not a verified signed release of this version",
			"no assurance which binary runs; it cannot be matched to release evidence")
	case c.Name == "metrics":
		set("L0", "service process", "the metrics endpoint does not answer",
			"flows and recovery counters are not visible; usually the service is down")
	case c.Name == "integrity":
		set("L3", "session correctness", "a conservation invariant was violated",
			"bytes may have been duplicated, lost or reordered on a flow; do not trust data integrity")
	case c.Name == "peer":
		set("L1", "network path to the peer", "the peer does not accept a TCP connection",
			"no carrier can be established, so routes through this node carry nothing")
	case c.Name == "listener":
		set("L1", "service process", "the local listener does not accept connections",
			"the peer cannot reach this node")
	case strings.HasPrefix(c.Name, "route ") && strings.HasPrefix(c.Detail, "target "):
		set("L5", "target service", "the fixed target of this route is unreachable",
			"connections arriving on this route are accepted by BAFT but cannot be delivered")
	case strings.HasPrefix(c.Name, "route "):
		set("L4", "route", "the route's local listener is not accepting",
			"local clients cannot use this route (it listens once the carrier is up)")
	default:
		set("", "host network tuning", "TCP settings are not tuned for long or lossy paths",
			"lower throughput on such paths; no effect on correctness")
	}
}

func layerRank(id string) int {
	if id == "" {
		return -1 // host or configuration: it explains the layers above it
	}
	for i, l := range layers {
		if l.id == id {
			return i
		}
	}
	return len(layers)
}

func summarize(checks []check) summary {
	s := summary{Verdict: verdictHealthy}
	var worst []check // findings of the worst severity present
	for _, want := range []string{checkFail, checkWarn} {
		for _, c := range checks {
			if c.Status == want {
				worst = append(worst, c)
			}
		}
		if len(worst) > 0 {
			if want == checkFail {
				s.Verdict = verdictFailing
			} else {
				s.Verdict = verdictDegraded
			}
			break
		}
	}
	if len(worst) > 0 {
		sort.SliceStable(worst, func(i, j int) bool { return layerRank(worst[i].Layer) < layerRank(worst[j].Layer) })
		first := worst[0]
		s.LikelyDomain = first.Domain
		s.NextAction = first.Hint
		if s.NextAction == "" {
			s.NextAction = first.Detail
		}
		domains := map[string]bool{}
		for _, c := range worst {
			domains[c.Domain] = true
		}
		// One failing domain is a clear answer; several mean the lowest one
		// is the best guess for the root cause, not a certainty.
		s.Confidence = "high"
		if len(domains) > 1 {
			s.Confidence = "medium"
		}
	}
	for _, l := range layers {
		st := layerState{Layer: l.id, Name: l.name, State: "NOT_ASSESSED", Note: l.unassessed}
		seen, warn, fail := false, false, false
		for _, c := range checks {
			if c.Layer == l.id || (c.Status == checkOK && okLayer(c) == l.id) {
				seen = true
				warn = warn || c.Status == checkWarn
				fail = fail || c.Status == checkFail
			}
		}
		switch {
		case fail:
			st.State, st.Note = "FAIL", ""
		case warn:
			st.State, st.Note = "WARN", ""
		case seen:
			st.State, st.Note = "PASS", ""
		}
		s.Layers = append(s.Layers, st)
	}
	return s
}

// okLayer places a passing check in the layer it proves.
func okLayer(c check) string {
	switch {
	case c.Name == "service" || c.Name == "metrics":
		return "L0"
	case c.Name == "peer" || c.Name == "listener":
		return "L1"
	case c.Name == "integrity":
		return "L3"
	case strings.HasPrefix(c.Name, "route ") && strings.HasPrefix(c.Detail, "target "):
		return "L5"
	case strings.HasPrefix(c.Name, "route "):
		return "L4"
	}
	return ""
}

func printReport(w io.Writer, s summary, checks []check) {
	fmt.Fprintf(w, "BAFT doctor: %s\n", s.Verdict)
	if s.LikelyDomain != "" {
		fmt.Fprintf(w, "  likely failure domain: %s (confidence: %s)\n", s.LikelyDomain, s.Confidence)
		fmt.Fprintf(w, "  next action:           %s\n", s.NextAction)
	}
	fmt.Fprintln(w, "\nLayers")
	for _, l := range s.Layers {
		note := ""
		if l.Note != "" {
			note = "  (" + l.Note + ")"
		}
		fmt.Fprintf(w, "  %-3s %-40s %s%s\n", l.Layer, l.Name, l.State, note)
	}
	fmt.Fprintln(w, "\nFindings")
	for _, c := range checks {
		fmt.Fprintf(w, "%-5s %-22s %s\n", strings.ToUpper(c.Status), c.Name, c.Detail)
		if c.Status == checkOK {
			continue
		}
		if c.Problem != "" {
			fmt.Fprintf(w, "      problem:  %s\n", c.Problem)
			fmt.Fprintf(w, "      evidence: %s\n", c.Detail)
			fmt.Fprintf(w, "      impact:   %s\n", c.Impact)
		}
		if c.Hint != "" {
			fmt.Fprintf(w, "      fix:      %s\n", c.Hint)
		}
	}
}

// printFixPreview lists the commands that would fix findings. It runs nothing.
func printFixPreview(w io.Writer, checks []check) {
	fmt.Fprintln(w, "Preview only: doctor never runs these. Review each one before you do.")
	n := 0
	for _, c := range checks {
		if c.FixCommand == "" {
			continue
		}
		n++
		label := "REVIEW (can change behaviour for traffic or other software)"
		if c.FixSafety == fixSafe {
			label = "SAFE   (does not interrupt traffic or access; easily undone)"
		}
		fmt.Fprintf(w, "\n[%s] %s\n  %s\n  %s\n", c.Name, c.Problem, label, c.FixCommand)
	}
	if n == 0 {
		fmt.Fprintln(w, "\nNo finding has a command that fixes it; see the fix text of each finding.")
	}
}
