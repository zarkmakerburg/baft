package bcc

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCanaryPlanValidationAndDeterminism(t *testing.T) {
	s := &Store{st: state{Nodes: map[string]Node{"a": {ID: "a"}, "b": {ID: "b"}, "c": {ID: "c"}, "revoked": {ID: "revoked", Revoked: true}}}}
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	got, err := s.PlanCanary([]string{"c", "a", "b"}, "v1.2.3", "a", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.CanaryNode != "a" || got.Version != "v1.2.3" || !reflect.DeepEqual(got.RemainingNodes, []string{"b", "c"}) {
		t.Fatalf("bad plan: %+v", got)
	}
	for _, tc := range []struct {
		name            string
		ids             []string
		version, canary string
	}{
		{"one node", []string{"a"}, "v1.2.3", "a"},
		{"duplicate", []string{"a", "a"}, "v1.2.3", "a"},
		{"revoked", []string{"a", "revoked"}, "v1.2.3", "a"},
		{"unknown", []string{"a", "missing"}, "v1.2.3", "a"},
		{"unselected canary", []string{"a", "b"}, "v1.2.3", "c"},
		{"invalid version", []string{"a", "b"}, "latest", "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.PlanCanary(tc.ids, tc.version, tc.canary, now)
			if err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	if strings.Contains(strings.Join(got.RemainingNodes, ","), "a") {
		t.Fatal("canary duplicated in fleet")
	}
}
