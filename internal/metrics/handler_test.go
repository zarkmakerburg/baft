package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerExportsConservationMetricsWithoutSensitiveLabels(t *testing.T) {
	h:=Handler(func()Snapshot{return Snapshot{
		ActiveFlows:2,ReceiveUsedBytes:65536,ReplayUsedBytes:32768,TotalUsedBytes:98304,
		AcceptedBacklogBytes:4096,CreditExposureBytes:69632,ReplayOutstandingBytes:8192,InvariantViolations:0,
	}})
	req:=httptest.NewRequest(http.MethodGet,"http://127.0.0.1/metrics",nil)
	rr:=httptest.NewRecorder()
	h.ServeHTTP(rr,req)
	if rr.Code!=http.StatusOK{t.Fatalf("status=%d",rr.Code)}
	if got:=rr.Header().Get("Content-Type");got!="text/plain; version=0.0.4; charset=utf-8"{t.Fatalf("content-type=%q",got)}
	body:=rr.Body.String()
	for _,want:=range []string{
		"baft_active_flows 2",
		"baft_resource_receive_bytes 65536",
		"baft_conservation_accepted_backlog_bytes 4096",
		"baft_conservation_credit_exposure_bytes 69632",
		"baft_conservation_replay_outstanding_bytes 8192",
		"baft_conservation_invariant_violations 0",
	}{
		if !strings.Contains(body,want){t.Fatalf("missing %q in %s",want,body)}
	}
	for _,forbidden:=range []string{"peer_id","route_id","target=","stream_id"}{
		if strings.Contains(body,forbidden){t.Fatalf("sensitive/high-cardinality label leaked: %q",forbidden)}
	}
}

func TestHandlerOnlyServesMetricsGET(t *testing.T) {
	h:=Handler(nil)
	for _,tc:=range []struct{method,path string}{{http.MethodPost,"/metrics"},{http.MethodGet,"/debug"}}{
		rr:=httptest.NewRecorder();h.ServeHTTP(rr,httptest.NewRequest(tc.method,"http://127.0.0.1"+tc.path,nil))
		if rr.Code!=http.StatusNotFound{t.Fatalf("%s %s status=%d",tc.method,tc.path,rr.Code)}
	}
}
