package bcc

import (
	"context"
	"fmt"
	"sort"
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
//	DEGRADED (confirmed)   opened as "warning"
//	DOWN (confirmed)       opened or escalated to "critical" (one more event)
//	RECOVERING, UNKNOWN    held as is: nothing new, nothing resolved
//	UP                     resolved
//
// A single failed probe, a lost packet or a flapping signal therefore cannot
// open an alert. UNKNOWN (no evidence) neither opens nor resolves one, and
// NOT_ASSESSED layers have no alert at all. The raw observation is kept on the
// alert only as evidence.
//
// Mapping: L0 -> telemetry_stale, L2 -> handshake_error_rate, L4 -> route_down
// (one alert per route whose probe is down while L4 is confirmed bad).
//
// The health sampler (ProbeOnce) is the only writer of layer state; the alert
// engine only reads it, so evaluating alerts more often cannot count a sample
// twice.

type alertSource struct {
	kind  string // alert type
	layer string
}

var alertSources = []alertSource{
	{"telemetry_stale", LayerL0},
	{"handshake_error_rate", LayerL2},
	{"route_down", LayerL4},
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

// holdsAlert: states in which an already open alert stays open unchanged.
func holdsAlert(state string) bool {
	return state == HealthDegraded || state == HealthDown || state == HealthRecovering || state == HealthUnknown
}

type alertTransition struct {
	action string // alert.firing, alert.escalated, alert.resolved
	alert  Alert
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

	s.alertMu.Lock()
	defer s.alertMu.Unlock()

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

	desired := map[string]Alert{}
	for _, n := range view {
		for _, src := range alertSources {
			ls := states[n.NodeID][src.layer]
			sev := alertSeverity(ls.state)
			if sev == "" {
				continue
			}
			if src.kind == "route_down" {
				ids := routes[n.NodeID]
				sort.Strings(ids)
				for _, rid := range ids {
					desired[src.kind+":"+n.NodeID+":"+rid] = s.makeAlert(src.kind, "firing", n.NodeID, n.Alias, rid, now, sev, ls.state, ls.evidence)
				}
				continue
			}
			desired[src.kind+":"+n.NodeID] = s.makeAlert(src.kind, "firing", n.NodeID, n.Alias, "", now, sev, ls.state, ls.evidence)
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
			_, _ = s.audit.Append(AuditEntry{
				Timestamp: now.UTC(), Actor: "bcc", Action: t.action, Target: t.alert.NodeID, Outcome: outcome,
				Details: map[string]any{
					"type": t.alert.Type, "route": t.alert.RouteID, "severity": t.alert.Severity,
					"health": t.alert.Health, "evidence": t.alert.Evidence,
				},
			})
		}
		return cause
	}
	keys := make([]string, 0, len(s.activeAlerts))
	for k := range s.activeAlerts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Resolve, or hold, what is already open.
	for _, key := range keys {
		prior := s.activeAlerts[key]
		if _, ok := desired[key]; ok {
			continue
		}
		layer := ""
		for _, src := range alertSources {
			if src.kind == prior.Type {
				layer = src.layer
			}
		}
		if ls, ok := states[prior.NodeID][layer]; ok && holdsAlert(ls.state) {
			desired[key] = prior // recovering or no evidence: not yet resolved, nothing new to say
			continue
		}
		resolved := s.makeAlert(prior.Type, "resolved", prior.NodeID, alias[prior.NodeID], prior.RouteID, now, prior.Severity, HealthUp, "layer is UP again")
		if prior.NodeAlias != "" {
			resolved.NodeAlias = prior.NodeAlias
		}
		if s.alertConfig.WebhookURL != "" {
			if err := s.sendWebhook(ctx, resolved); err != nil {
				return finish(err)
			}
		}
		delete(s.activeAlerts, key)
		trs = append(trs, alertTransition{"alert.resolved", resolved})
	}
	// Open or escalate.
	dkeys := make([]string, 0, len(desired))
	for k := range desired {
		dkeys = append(dkeys, k)
	}
	sort.Strings(dkeys)
	for _, key := range dkeys {
		alert := desired[key]
		prior, exists := s.activeAlerts[key]
		if exists && (alert.Status == prior.Status && alert.Severity == prior.Severity || alert.Timestamp.Equal(prior.Timestamp)) {
			continue // unchanged (or the held copy itself)
		}
		action := "alert.firing"
		if exists {
			if prior.Severity == "" {
				continue // opened by a build that had no severity: do not re-notify on upgrade
			}
			// Only an increase in severity is news; a layer that went from
			// DOWN through UNKNOWN back to DEGRADED does not lower an open alert.
			if severityRank(alert.Severity) <= severityRank(prior.Severity) {
				continue
			}
			action = "alert.escalated"
		}
		if s.alertConfig.WebhookURL != "" {
			if err := s.sendWebhook(ctx, alert); err != nil {
				return finish(err)
			}
		}
		s.activeAlerts[key] = alert
		trs = append(trs, alertTransition{action, alert})
	}
	return finish(nil)
}

func (s *Server) makeAlert(kind, status, nodeID, nodeAlias, routeID string, at time.Time, severity, health, evidence string) Alert {
	if nodeAlias == "" {
		nodeAlias = nodeID
	}
	typeFA := map[string]string{
		"telemetry_stale":      "توقف دریافت تل‌متری",
		"handshake_error_rate": "افزایش نرخ خطای Handshake",
		"route_down":           "قطع مسیر",
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
