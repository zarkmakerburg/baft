package bcc

import (
	"strings"
	"testing"
	"time"
)

func doctorFixture(fail string) []DoctorStageEvidence {
	out := make([]DoctorStageEvidence, 0, len(doctorStages))
	for _, s := range doctorStages {
		st := DoctorPass
		if s == fail {
			st = DoctorFail
		}
		out = append(out, DoctorStageEvidence{Stage: s, State: st, Reference: "fixture:" + s})
		if s == fail {
			break
		}
	}
	return out
}

func TestRouteDoctorAcceptanceFixtures(t *testing.T) {
	for _, tc := range []struct{ name, fail string }{
		{"tcp fail", "tcp_path"}, {"tls fail", "tls13"}, {"carrier fail", "carrier_handshake"}, {"target fail", "target_reachability"}, {"full pass", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := DiagnoseRouteDoctor("tun", time.Unix(100, 0), doctorFixture(tc.fail))
			if tc.fail == "" {
				if r.Verdict != DoctorPass || r.FirstFailingStage != "" {
					t.Fatalf("unexpected verdict: %+v", r)
				}
			} else {
				if r.Verdict != DoctorFail || r.FirstFailingStage != tc.fail {
					t.Fatalf("unexpected first failure: %+v", r)
				}
				seen := false
				for _, s := range r.Stages {
					if seen && s.State != DoctorNotAssessed {
						t.Fatalf("downstream %s=%s want NOT_ASSESSED", s.Stage, s.State)
					}
					if s.Stage == tc.fail {
						seen = true
					}
				}
			}
		})
	}
}

func TestRouteDoctorEvidenceDigestIsReproducibleAndPayloadFree(t *testing.T) {
	at := time.Unix(200, 0)
	e := doctorFixture("")
	a := DiagnoseRouteDoctor("tun", at, e)
	b := DiagnoseRouteDoctor("tun", at, e)
	if a.EvidenceDigest != b.EvidenceDigest {
		t.Fatal("same evidence produced different digest")
	}
	raw := strings.ToLower(a.SummaryEN + a.SummaryFA + a.EvidenceDigest)
	if strings.Contains(raw, "payload") || strings.Contains(raw, "secret") {
		t.Fatal("summary leaked forbidden content")
	}
}
