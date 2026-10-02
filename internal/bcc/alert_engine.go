package bcc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Alert engine aligned with the layered health model (HQ A1).
//
//	RAW OBSERVATIONS -> LAYER SAMPLING -> HYSTERESIS -> EFFECTIVE HEALTH -> alerts, webhook, API, dashboard
//
// Alerts never read a raw instantaneous signal. They follow the effective
// state of a layer, which only moves after several consistent samples:
//
//	layer state            alert
//	DEGRADED (confirmed)   opened as "warning" (L1: dashboard and history only)
//	DOWN (confirmed)       opened or escalated to "critical" (one more event)
//	RECOVERING, UNKNOWN    held as is: nothing new, nothing resolved
//	UP                     resolved
//
// A single failed probe, a lost packet or a flapping signal therefore cannot
// open an alert. UNKNOWN (no evidence) neither opens nor resolves one, and
// NOT_ASSESSED layers have no alert at all. The raw observation is kept on the
// alert only as evidence.
//
// Mapping: L0 -> telemetry_stale, L1 -> node_unreachable (confirmed DOWN only),
// L2 -> handshake_error_rate, L4 -> route_down (one alert per route whose probe
// is down while L4 is confirmed bad).
//
// Correlation: while node_unreachable is open for a node, the other alerts of
// that node belong to the same incident. They stay recorded, with their own
// layer evidence and an audit entry, but their notifications (new alerts,
// escalations, and resolutions of alerts that were never announced) are not
// sent; the root alert lists them. If the root resolves while one of them is
// still bad, that one is announced then.
//
// The health sampler (ProbeOnce) is the only writer of layer state; the alert
// engine only reads it, so evaluating alerts more often cannot count a sample
// twice.

type alertSource struct {
	kind  string // alert type
	layer string
	// openAt is the lowest confirmed state that opens the alert.
	openAt string
}

const kindUnreachable = "node_unreachable"

var alertSources = []alertSource{
	{kindUnreachable, LayerL1, HealthDown},
	{"telemetry_stale", LayerL0, HealthDegraded},
	{"handshake_error_rate", LayerL2, HealthDegraded},
	{"route_down", LayerL4, HealthDegraded},
}

func alertSeverity(state string) string {
	switch state {
	case HealthDegraded:
		return "warning"
	case HealthDown:
		return "critical"
	}
	return ""
}

func severityRank(s string) int {
	switch s {
	case "warning":
		return 1
	case "critical":
		return 2
	}
	return 0
}

// opens reports whether a confirmed layer state opens an alert of this source.
func (a alertSource) opens(state string) bool {
	if a.openAt == HealthDown {
		return state == HealthDown
	}
	return state == HealthDegraded || state == HealthDown
}

// holdsAlert: states in which an already open alert stays open unchanged.
func holdsAlert(state string) bool {
	return state == HealthDegraded || state == HealthDown || state == HealthRecovering || state == HealthUnknown
}

type alertTransition struct {
	action string // alert.firing, alert.correlated, alert.escalated, alert.resolved
	alert  Alert
	note   string
}

func (s *Server) evaluateAlertsAt(ctx context.Context, now time.Time) error {
	view := s.store.HealthSnapshot("", 1)
	routes := map[string][]string{} // node -> routes whose latest probe is down
	for _, n := range view {
		if cur, ok := s.store.TelemetrySnapshot(n.NodeID); ok {
			for _, r := range cur.Routes {
				if r.Status == "down" {
					routes[n.NodeID] = append(routes[n.NodeID], r.RouteID)
				}
			}
		}
	}
	type layerState struct{ state, evidence string }
	states := map[string]map[string]layerState{} // node -> layer -> state
	alias := map[string]string{}
	for _, n := range view {
		alias[n.NodeID] = n.Alias
		m := map[string]layerState{}
		for _, l := range n.Layers {
			m[l.Layer] = layerState{l.State, l.Evidence}
		}
		states[n.NodeID] = m
	}
	incidents := map[string]L1Incident{}
	for _, n := range view {
		if states[n.NodeID][LayerL1].state == HealthDown {
			if inc, ok := s.store.L1Incident(n.NodeID); ok {
				incidents[n.NodeID] = inc
			}
		}
	}

	s.alertMu.Lock()
	defer s.alertMu.Unlock()

	desired := map[string]Alert{}
	for _, n := range view {
		for _, src := range alertSources {
			ls := states[n.NodeID][src.layer]
			if !src.opens(ls.state) {
				continue
			}
			sev := alertSeverity(ls.state)
			switch src.kind {
			case "route_down":
				ids := routes[n.NodeID]
				sort.Strings(ids)
				for _, rid := range ids {
					desired[src.kind+":"+n.NodeID+":"+rid] = s.makeAlert(src.kind, "firing", n.NodeID, n.Alias, rid, now, sev, ls.state, ls.evidence)
				}
			case kindUnreachable:
				a := s.makeAlert(src.kind, "firing", n.NodeID, n.Alias, "", now, sev, ls.state, ls.evidence)
				a.EvidenceFields, a.Evidence = unreachableEvidence(n.NodeID, incidents[n.NodeID], now)
				desired[src.kind+":"+n.NodeID] = a
			default:
				desired[src.kind+":"+n.NodeID] = s.makeAlert(src.kind, "firing", n.NodeID, n.Alias, "", now, sev, ls.state, ls.evidence)
			}
		}
	}

	var trs []alertTransition
	// finish persists what was done so far and audits it, even when a webhook
	// failed half way: what was already delivered must not be forgotten or
	// sent twice, and the rest is retried on the next evaluation.
	finish := func(cause error) error {
		if err := s.store.SetActiveAlerts(s.activeAlerts); err != nil && cause == nil {
			cause = err
		}
		for _, t := range trs {
			outcome := "failure"
			if t.action == "alert.resolved" {
				outcome = "success"
			}
			d := map[string]any{
				"type": t.alert.Type, "route": t.alert.RouteID, "severity": t.alert.Severity,
				"health": t.alert.Health, "evidence": t.alert.Evidence,
			}
			if t.alert.CorrelatedWith != "" {
				d["correlated_with"] = t.alert.CorrelatedWith
			}
			if len(t.alert.CorrelatedAlerts) > 0 {
				d["correlated_alerts"] = t.alert.CorrelatedAlerts
			}
			if id := t.alert.EvidenceFields["event_id"]; id != "" {
				d["event_id"] = id
			}
			if t.note != "" {
				d["note"] = t.note
			}
			_, _ = s.audit.Append(AuditEntry{Timestamp: now.UTC(), Actor: "bcc", Action: t.action, Target: t.alert.NodeID, Outcome: outcome, Details: d})
		}
		return cause
	}
	notify := func(a Alert) error {
		if s.alertConfig.WebhookURL == "" {
			return nil
		}
		return s.sendWebhook(ctx, a)
	}

	keys := make([]string, 0, len(s.activeAlerts))
	for k := range s.activeAlerts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Hold what is open and not confirmed bad now, resolve what is UP again.
	var resolving []string
	for _, key := range keys {
		prior := s.activeAlerts[key]
		if _, ok := desired[key]; ok {
			continue
		}
		var src *alertSource
		for i := range alertSources {
			if alertSources[i].kind == prior.Type {
				src = &alertSources[i]
			}
		}
		if src != nil {
			if ls, ok := states[prior.NodeID][src.layer]; ok && holdsAlert(ls.state) {
				desired[key] = prior // recovering or no evidence: not yet resolved, nothing new to say
				continue
			}
		}
		resolving = append(resolving, key)
	}

	// Correlation: nodes with an open reachability incident.
	root := map[string]string{} // node -> root alert key
	for key, a := range desired {
		if a.Type == kindUnreachable {
			root[a.NodeID] = key
		}
	}
	covered := map[string][]string{}
	for key, a := range desired {
		if r, ok := root[a.NodeID]; ok && a.Type != kindUnreachable {
			covered[r] = append(covered[r], key)
		}
	}
	for r := range covered {
		sort.Strings(covered[r])
	}

	// Resolve. An alert that was never announced is closed silently.
	for _, key := range resolving {
		prior := s.activeAlerts[key]
		resolved := s.makeAlert(prior.Type, "resolved", prior.NodeID, alias[prior.NodeID], prior.RouteID, now, prior.Severity, HealthUp, "layer is UP again")
		if prior.NodeAlias != "" {
			resolved.NodeAlias = prior.NodeAlias
		}
		note := ""
		if prior.Type == kindUnreachable {
			resolved.EvidenceFields = map[string]string{"node_id": prior.NodeID, "address": prior.EvidenceFields["address"], "failure_started": prior.EvidenceFields["failure_started"], "resolved_at": now.UTC().Format(time.RFC3339)}
			if t, err := time.Parse(time.RFC3339, prior.EvidenceFields["failure_started"]); err == nil {
				resolved.EvidenceFields["failure_duration"] = now.Sub(t).Round(time.Second).String()
				resolved.Evidence = "reachable again after " + resolved.EvidenceFields["failure_duration"]
			}
		}
		if prior.Unnotified {
			note = "never announced (part of incident " + prior.CorrelatedWith + ")"
		} else if err := notify(resolved); err != nil {
			return finish(err)
		}
		delete(s.activeAlerts, key)
		trs = append(trs, alertTransition{"alert.resolved", resolved, note})
	}

	// Open, announce, escalate.
	dkeys := make([]string, 0, len(desired))
	for k := range desired {
		dkeys = append(dkeys, k)
	}
	sort.Strings(dkeys)
	for _, key := range dkeys {
		alert := desired[key]
		prior, exists := s.activeAlerts[key]
		if exists && alert.Timestamp.Equal(prior.Timestamp) {
			continue // the held copy itself
		}
		rootKey, correlated := root[alert.NodeID]
		correlated = correlated && alert.Type != kindUnreachable
		if alert.Type == kindUnreachable {
			alert.CorrelatedAlerts = covered[key]
		}
		switch {
		case !exists && correlated:
			alert.Unnotified, alert.CorrelatedWith = true, rootKey
			s.activeAlerts[key] = alert
			trs = append(trs, alertTransition{"alert.correlated", alert, "notification withheld: part of incident " + rootKey})
		case !exists:
			if err := notify(alert); err != nil {
				return finish(err)
			}
			s.activeAlerts[key] = alert
			trs = append(trs, alertTransition{"alert.firing", alert, ""})
		case prior.Unnotified && !correlated:
			// The incident that covered it is over and it is still bad: announce it now.
			alert.Unnotified, alert.CorrelatedWith = false, ""
			if err := notify(alert); err != nil {
				return finish(err)
			}
			s.activeAlerts[key] = alert
			trs = append(trs, alertTransition{"alert.firing", alert, "announced after incident " + prior.CorrelatedWith + " ended"})
		case prior.Severity == "":
			// Opened by a build that had no severity: do not re-notify on upgrade.
		case severityRank(alert.Severity) <= severityRank(prior.Severity):
			// Only an increase in severity is news; a layer that went from
			// DOWN through UNKNOWN back to DEGRADED does not lower an open alert.
		case correlated || prior.Unnotified:
			alert.Unnotified, alert.CorrelatedWith = prior.Unnotified, prior.CorrelatedWith
			if correlated && prior.Unnotified {
				alert.CorrelatedWith = rootKey
			}
			s.activeAlerts[key] = alert
			trs = append(trs, alertTransition{"alert.escalated", alert, "notification withheld: part of incident " + rootKey})
		default:
			if err := notify(alert); err != nil {
				return finish(err)
			}
			s.activeAlerts[key] = alert
			trs = append(trs, alertTransition{"alert.escalated", alert, ""})
		}
	}
	// The root alert always lists what it currently covers (no notification).
	for key, a := range s.activeAlerts {
		if a.Type != kindUnreachable {
			continue
		}
		if cur := covered[key]; !equalStrings(a.CorrelatedAlerts, cur) {
			a.CorrelatedAlerts = cur
			s.activeAlerts[key] = a
		}
	}
	return finish(nil)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// unreachableEvidence builds the structured evidence of a node_unreachable
// alert from stored layer data.
func unreachableEvidence(node string, inc L1Incident, now time.Time) (map[string]string, string) {
	f := map[string]string{"node_id": node, "address": inc.Address, "previous_state": inc.PreviousState, "current_state": inc.CurrentState,
		"transition_reason": inc.Reason, "event_id": inc.EventID}
	if !inc.FailureStarted.IsZero() {
		f["failure_started"] = inc.FailureStarted.UTC().Format(time.RFC3339)
		f["failure_duration"] = now.Sub(inc.FailureStarted).Round(time.Second).String()
	}
	if inc.LastReachable.IsZero() {
		f["last_successful_reachability"] = "never observed"
	} else {
		f["last_successful_reachability"] = inc.LastReachable.UTC().Format(time.RFC3339)
	}
	parts := []string{fmt.Sprintf("L1 %s -> %s", inc.PreviousState, inc.CurrentState)}
	if d := f["failure_duration"]; d != "" {
		parts = append(parts, "failing for "+d)
	}
	parts = append(parts, "last reachable "+f["last_successful_reachability"], inc.Reason)
	return f, strings.Join(parts, "; ")
}

func (s *Server) makeAlert(kind, status, nodeID, nodeAlias, routeID string, at time.Time, severity, health, evidence string) Alert {
	if nodeAlias == "" {
		nodeAlias = nodeID
	}
	typeFA := map[string]string{
		"telemetry_stale":      "توقف دریافت تل‌متری",
		"handshake_error_rate": "افزایش نرخ خطای Handshake",
		"route_down":           "قطع مسیر",
		kindUnreachable:        "نود در دسترس نیست",
	}[kind]
	if typeFA == "" {
		typeFA = kind
	}
	statusFA := "فعال"
	if status == "resolved" {
		statusFA = "برطرف شد"
	}
	sevFA := map[string]string{"warning": "هشدار (وضعیت تأییدشده: DEGRADED)", "critical": "بحرانی (وضعیت تأییدشده: DOWN)"}[severity]
	loc, err := time.LoadLocation("Asia/Tehran")
	if err != nil {
		loc = time.FixedZone("Asia/Tehran", 3*3600+30*60)
	}
	routeText := "—"
	if routeID != "" {
		routeText = routeID
	}
	at = at.UTC()
	msg := fmt.Sprintf("هشدار BAFT\nوضعیت: %s\nنود: %s (%s)\nمسیر: %s\nنوع هشدار: %s\nزمان UTC: %s\nزمان تهران: %s",
		statusFA, nodeAlias, nodeID, routeText, typeFA,
		at.Format(time.RFC3339), at.In(loc).Format(time.RFC3339))
	if sevFA != "" {
		msg += "\nشدت: " + sevFA
	}
	return Alert{Type: kind, Status: status, NodeID: nodeID, NodeAlias: nodeAlias, RouteID: routeID, Message: msg, Timestamp: at,
		Severity: severity, Health: health, Evidence: evidence}
}
