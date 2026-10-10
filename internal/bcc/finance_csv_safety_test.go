package bcc

import (
	"encoding/csv"
	"strings"
	"testing"
)

func TestFinanceCSVIdentifiersCannotBecomeSpreadsheetFormulas(t *testing.T) {
	cases := []struct{ input, want string }{
		{"=1+1", "'=1+1"},
		{" +SUM(1,2)", "' +SUM(1,2)"},
		{"\t@cmd", "'\t@cmd"},
		{"-2+3", "'-2+3"},
		{"ordinary-node", "ordinary-node"},
	}
	for _, tc := range cases {
		data, err := FinanceReportCSV([]FinanceReportRow{{
			Period: "2026-10-09", Scope: "node", NodeID: tc.input,
			Currency: "USD", CostMicros: 12,
		}})
		if err != nil { t.Fatal(err) }
		rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
		if err != nil { t.Fatal(err) }
		if len(rows) != 2 || rows[1][2] != tc.want || rows[1][5] != "12" {
			t.Fatalf("node ID %q exported as %v; want %q", tc.input, rows, tc.want)
		}
	}
}
