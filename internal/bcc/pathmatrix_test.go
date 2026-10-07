package bcc

import (
	"testing"
	"time"
)

func matrixCell(t *testing.T, m PathMatrix, src, dst string) PathMatrixCell {
	t.Helper()
	for _, c := range m.Cells {
		if c.SourceNode == src && c.DestinationNode == dst {
			return c
		}
	}
	t.Fatalf("matrix cell %s->%s missing", src, dst)
	return PathMatrixCell{}
}

func TestPathMatrixPreservesDirectionalEvidenceStates(t *testing.T) {
	s := newGenericDiscoveryStore(t, "A", "B", "C")
	now := time.Now().UTC()
	s.mu.Lock()
	putGraphProbe(s, "ab-good", "A", "B", "4", "192.0.2.2", 443, PathFullData, 10, now)
	putGraphProbe(s, "ba-limit", "B", "A", "4", "192.0.2.1", 8443, PathByteCeiling, 3, now)
	p := s.st.PathProbes["ba-limit"]
	p.Candidates[0].ByteCeiling = 8688
	p.Candidates[0].Detail = "repeated receiver ceiling"
	s.st.PathProbes[p.ID] = p
	putGraphProbe(s, "bc-fail", "B", "C", "6", "2001:db8::3", 443, PathUnreachable, 0, now)
	s.mu.Unlock()

	m := s.PathMatrix(now.Add(time.Minute))
	if got := matrixCell(t, m, "A", "B").State; got != PathMatrixPass {
		t.Fatalf("A->B=%s want PASS", got)
	}
	ba := matrixCell(t, m, "B", "A")
	if ba.State != PathMatrixDegraded || len(ba.Candidates) != 1 || ba.Candidates[0].ByteCeiling != 8688 {
		t.Fatalf("B->A evidence lost: %+v", ba)
	}
	if got := matrixCell(t, m, "B", "C").State; got != PathMatrixFail {
		t.Fatalf("B->C=%s want FAIL", got)
	}
	if got := matrixCell(t, m, "A", "C").State; got != PathMatrixNotAssessed {
		t.Fatalf("A->C=%s want NOT_ASSESSED", got)
	}
}

func TestPathMatrixStaleNeverRemainsPass(t *testing.T) {
	s := newGenericDiscoveryStore(t, "A", "B")
	now := time.Now().UTC()
	s.mu.Lock()
	putGraphProbe(s, "ab-old", "A", "B", "4", "192.0.2.2", 443, PathFullData, 1, now.Add(-pathDiscoveryFreshFor-time.Second))
	s.mu.Unlock()

	cell := matrixCell(t, s.PathMatrix(now), "A", "B")
	if cell.State != PathMatrixStale || len(cell.Candidates) != 1 || cell.Candidates[0].State != PathMatrixStale {
		t.Fatalf("stale FULL_DATA remained usable: %+v", cell)
	}
}

func TestPathMatrixExcludesRevokedNodesAndIsDeterministic(t *testing.T) {
	s := newGenericDiscoveryStore(t, "C", "A", "B")
	now := time.Now().UTC()
	if _, err := s.RevokeNode("B", "test", now); err != nil {
		t.Fatal(err)
	}
	m := s.PathMatrix(now)
	if len(m.Nodes) != 2 || m.Nodes[0] != "A" || m.Nodes[1] != "C" {
		t.Fatalf("nodes not deterministic/revoked excluded: %+v", m.Nodes)
	}
	if len(m.Cells) != 2 || m.Cells[0].SourceNode != "A" || m.Cells[0].DestinationNode != "C" ||
		m.Cells[1].SourceNode != "C" || m.Cells[1].DestinationNode != "A" {
		t.Fatalf("cells not deterministic: %+v", m.Cells)
	}
}
