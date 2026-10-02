package bcc

import (
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkSaveWithLargeLedger(b *testing.B) {
	path := filepath.Join(b.TempDir(), "state.json")
	var st state
	normalizeState(&st)
	for i := 0; i < 20000; i++ {
		st.FinanceLedger = append(st.FinanceLedger, FinanceLedgerEntry{NodeID: "ex-1", Timestamp: time.Unix(int64(i), 0), IngressBytes: uint64(i), Currency: "IRR"})
	}
	if err := writeStateDB(path, st); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.FinanceLedger = append(st.FinanceLedger, FinanceLedgerEntry{NodeID: "ex-1", Timestamp: time.Unix(int64(30000+i), 0)})
		if err := writeStateDB(path, st); err != nil {
			b.Fatal(err)
		}
	}
}
