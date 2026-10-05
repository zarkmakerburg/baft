package bcc

// BCC state persistence in a versioned SQLite database (Launch-1 P1-C).
//
// The state file is one SQLite database in rollback-journal mode, so between
// transactions it is a single self-contained file: the restore transaction
// can still stage, swap and roll it back as a whole file. Every save rewrites
// the state inside one transaction, which is atomic and crash-safe. The
// connection is opened per operation, so a file swapped underneath (restore)
// is never written through a stale handle.
//
// Rows hold each record's JSON so later steps (server inventory, jobs) can
// move fields into real columns through new migrations. A database written by
// a newer schema is refused rather than downgraded.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const sqliteMagic = "SQLite format 3\x00"

// stateMigrations[i] upgrades the schema from version i to i+1.
var stateMigrations = []string{
	`CREATE TABLE nodes (id TEXT PRIMARY KEY, doc TEXT NOT NULL);
	 CREATE TABLE jobs (id TEXT PRIMARY KEY, doc TEXT NOT NULL);
	 CREATE TABLE finance (node_id TEXT PRIMARY KEY, doc TEXT NOT NULL);
	 CREATE TABLE finance_policies (node_id TEXT PRIMARY KEY, doc TEXT NOT NULL);
	 CREATE TABLE finance_rate_history (node_id TEXT NOT NULL, seq INTEGER NOT NULL, doc TEXT NOT NULL, PRIMARY KEY (node_id, seq));
	 CREATE TABLE finance_ledger (seq INTEGER PRIMARY KEY, doc TEXT NOT NULL);
	 CREATE TABLE finance_remainders (node_id TEXT PRIMARY KEY, doc TEXT NOT NULL);
	 CREATE TABLE telemetry (node_id TEXT PRIMARY KEY, doc TEXT NOT NULL);
	 CREATE TABLE history (node_id TEXT NOT NULL, seq INTEGER NOT NULL, doc TEXT NOT NULL, PRIMARY KEY (node_id, seq));
	 CREATE TABLE active_alerts (key TEXT PRIMARY KEY, doc TEXT NOT NULL);
	 CREATE TABLE retired_boot_ids (node_id TEXT NOT NULL, boot_id TEXT NOT NULL, PRIMARY KEY (node_id, boot_id));
	 CREATE TABLE counters (name TEXT PRIMARY KEY, value TEXT NOT NULL);`,
	// 2: tunnel changes (P1-E).
	`CREATE TABLE tunnels (id TEXT PRIMARY KEY, doc TEXT NOT NULL);`,
	// 3: layered node health with hysteresis (state machines and history).
	`CREATE TABLE node_health (node_id TEXT PRIMARY KEY, doc TEXT NOT NULL);`,
	// 4: report-only discovery of existing tunnels (A2).
	`CREATE TABLE node_discovery (node_id TEXT PRIMARY KEY, doc TEXT NOT NULL);`,
	// 5: certificate rotations of built tunnels (A4 Stage F).
	`CREATE TABLE cert_rotations (id TEXT PRIMARY KEY, doc TEXT NOT NULL);`,
	// 6: declarative topology for the IR pool and explicit EX routes (M-014).
	`CREATE TABLE topology (kind TEXT NOT NULL, id TEXT NOT NULL, doc TEXT NOT NULL, PRIMARY KEY (kind, id));`,
	// 7: durable IR active/standby decisions per explicit EX route (M-015).
	`CREATE TABLE ingress_selections (route_id TEXT PRIMARY KEY, doc TEXT NOT NULL);`,
	// 8: durable weighted IR distributions per explicit EX route (M-016).
	`CREATE TABLE ingress_distributions (route_id TEXT PRIMARY KEY, doc TEXT NOT NULL);`,
}

var stateTables = []string{
	"nodes", "jobs", "finance", "finance_policies", "finance_rate_history", "finance_ledger",
	"finance_remainders", "telemetry", "history", "active_alerts", "retired_boot_ids", "counters", "tunnels", "node_health", "node_discovery", "cert_rotations", "topology", "ingress_selections", "ingress_distributions",
}

func isSQLiteFile(b []byte) bool { return bytes.HasPrefix(b, []byte(sqliteMagic)) }

// openStateDB opens (creating owner-only if needed) and migrates the database.
func openStateDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600); err != nil {
		return nil, err
	} else {
		f.Close()
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(DELETE)&_pragma=synchronous(FULL)&_pragma=secure_delete(ON)&_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrateStateDB(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrateStateDB(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var version int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return err
	}
	if version > len(stateMigrations) {
		return fmt.Errorf("BCC state schema %d is newer than this build supports (%d)", version, len(stateMigrations))
	}
	for v := version; v < len(stateMigrations); v++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(stateMigrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("BCC state migration %d: %w", v+1, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v+1, time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func stateSchemaVersion(path string) (int, error) {
	db, err := openStateDB(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var v int
	err = db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	return v, err
}

// writeStateDB replaces the whole state in one transaction.
func writeStateDB(path string, st state) error {
	db, err := openStateDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := writeStateTx(tx, st); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func writeStateTx(tx *sql.Tx, st state) error {
	// The finance ledger is append-only and the largest table: when the
	// stored rows are a prefix of it, only the new rows are written.
	ledgerFrom, err := ledgerPrefix(tx, st.FinanceLedger)
	if err != nil {
		return err
	}
	for _, t := range stateTables {
		if t == "finance_ledger" && ledgerFrom > 0 {
			continue
		}
		if _, err := tx.Exec("DELETE FROM " + t); err != nil {
			return err
		}
	}
	put := func(query string, args ...any) error {
		for i, a := range args {
			if a == nil || isScalar(a) {
				continue
			}
			b, err := json.Marshal(a)
			if err != nil {
				return err
			}
			args[i] = string(b)
		}
		_, err := tx.Exec(query, args...)
		return err
	}
	for id, v := range st.Nodes {
		if err := put(`INSERT INTO nodes VALUES (?, ?)`, id, v); err != nil {
			return err
		}
	}
	for id, v := range st.Jobs {
		if err := put(`INSERT INTO jobs VALUES (?, ?)`, id, v); err != nil {
			return err
		}
	}
	for id, v := range st.Tunnels {
		if err := put(`INSERT INTO tunnels VALUES (?, ?)`, id, v); err != nil {
			return err
		}
	}
	for id, v := range st.Health {
		if err := put(`INSERT INTO node_health VALUES (?, ?)`, id, v); err != nil {
			return err
		}
	}
	for id, v := range st.Discovery {
		if err := put(`INSERT INTO node_discovery VALUES (?, ?)`, id, v); err != nil {
			return err
		}
	}
	for id, v := range st.CertRotations {
		if err := put(`INSERT INTO cert_rotations VALUES (?, ?)`, id, v); err != nil {
			return err
		}
	}
	for id, v := range st.IRPool { if err := put(`INSERT INTO topology VALUES (?, ?, ?)`, "ir", id, v); err != nil { return err } }
	for id, v := range st.EXRoutes { if err := put(`INSERT INTO topology VALUES (?, ?, ?)`, "route", id, v); err != nil { return err } }
	for id, v := range st.TopologyBindings { if err := put(`INSERT INTO topology VALUES (?, ?, ?)`, "binding", id, v); err != nil { return err } }
	for id, v := range st.IngressSelections { if err := put(`INSERT INTO ingress_selections VALUES (?, ?)`, id, v); err != nil { return err } }
	for id, v := range st.IngressDistributions { if err := put(`INSERT INTO ingress_distributions VALUES (?, ?)`, id, v); err != nil { return err } }
	for id, v := range st.Finance {
		if err := put(`INSERT INTO finance VALUES (?, ?)`, id, v); err != nil {
			return err
		}
	}
	for id, v := range st.Policies {
		if err := put(`INSERT INTO finance_policies VALUES (?, ?)`, id, v); err != nil {
			return err
		}
	}
	for id, hist := range st.RateHistory {
		for i, v := range hist {
			if err := put(`INSERT INTO finance_rate_history VALUES (?, ?, ?)`, id, i, v); err != nil {
				return err
			}
		}
	}
	for i := ledgerFrom; i < len(st.FinanceLedger); i++ {
		v := st.FinanceLedger[i]
		if err := put(`INSERT INTO finance_ledger VALUES (?, ?)`, i, v); err != nil {
			return err
		}
	}
	for id, v := range st.FinanceRemainders {
		if err := put(`INSERT INTO finance_remainders VALUES (?, ?)`, id, v); err != nil {
			return err
		}
	}
	for id, v := range st.Telemetry {
		if err := put(`INSERT INTO telemetry VALUES (?, ?)`, id, v); err != nil {
			return err
		}
	}
	for id, hist := range st.History {
		for i, v := range hist {
			if err := put(`INSERT INTO history VALUES (?, ?, ?)`, id, i, v); err != nil {
				return err
			}
		}
	}
	for k, v := range st.ActiveAlerts {
		if err := put(`INSERT INTO active_alerts VALUES (?, ?)`, k, v); err != nil {
			return err
		}
	}
	for id, boots := range st.RetiredBootIDs {
		for boot, retired := range boots {
			if !retired {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO retired_boot_ids VALUES (?, ?)`, id, boot); err != nil {
				return err
			}
		}
	}
	for name, v := range map[string]uint64{
		"next_job": st.NextJob, "next_rate_version": st.NextRateVersion, "next_telemetry_ingest_id": st.NextTelemetryIngestID,
	} {
		if _, err := tx.Exec(`INSERT INTO counters VALUES (?, ?)`, name, strconv.FormatUint(v, 10)); err != nil {
			return err
		}
	}
	return nil
}

// ledgerPrefix returns how many leading ledger entries are already stored
// unchanged, or 0 when the stored ledger is not a prefix (a full rewrite).
func ledgerPrefix(tx *sql.Tx, ledger []FinanceLedgerEntry) (int, error) {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM finance_ledger`).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 || n > len(ledger) {
		return 0, nil
	}
	var maxSeq int
	var last string
	if err := tx.QueryRow(`SELECT seq, doc FROM finance_ledger ORDER BY seq DESC LIMIT 1`).Scan(&maxSeq, &last); err != nil {
		return 0, err
	}
	want, err := json.Marshal(ledger[n-1])
	if err != nil {
		return 0, err
	}
	if maxSeq != n-1 || last != string(want) {
		return 0, nil
	}
	return n, nil
}

func isScalar(v any) bool {
	switch v.(type) {
	case string, int, int64:
		return true
	}
	return false
}

// readStateDB loads the whole state.
func readStateDB(path string) (state, error) {
	var st state
	normalizeState(&st)
	if _, err := os.Stat(path); err != nil {
		return st, err
	}
	db, err := openStateDB(path)
	if err != nil {
		return st, err
	}
	defer db.Close()
	each := func(query string, fn func(key string, seq int64, doc []byte) error) error {
		rows, err := db.Query(query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			var seq int64
			var doc []byte
			if err := rows.Scan(&key, &seq, &doc); err != nil {
				return err
			}
			if err := fn(key, seq, doc); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	decode := func(table string, doc []byte, v any) error {
		if err := json.Unmarshal(doc, v); err != nil {
			return fmt.Errorf("BCC state %s: %w", table, err)
		}
		return nil
	}
	steps := []struct {
		query string
		fn    func(string, int64, []byte) error
	}{
		{`SELECT id, 0, doc FROM nodes`, func(k string, _ int64, d []byte) error {
			var v Node
			err := decode("nodes", d, &v)
			st.Nodes[k] = v
			return err
		}},
		{`SELECT id, 0, doc FROM jobs`, func(k string, _ int64, d []byte) error {
			var v Job
			err := decode("jobs", d, &v)
			st.Jobs[k] = v
			return err
		}},
		{`SELECT id, 0, doc FROM tunnels`, func(k string, _ int64, d []byte) error {
			var v Tunnel
			err := decode("tunnels", d, &v)
			st.Tunnels[k] = v
			return err
		}},
		{`SELECT node_id, 0, doc FROM node_health`, func(k string, _ int64, d []byte) error {
			var v NodeHealthRecord
			err := decode("node_health", d, &v)
			st.Health[k] = v
			return err
		}},
		{`SELECT node_id, 0, doc FROM node_discovery`, func(k string, _ int64, d []byte) error {
			var v NodeDiscovery
			err := decode("node_discovery", d, &v)
			st.Discovery[k] = v
			return err
		}},
		{`SELECT id, 0, doc FROM cert_rotations`, func(k string, _ int64, d []byte) error {
			var v CertRotation
			err := decode("cert_rotations", d, &v)
			st.CertRotations[k] = v
			return err
		}},
		{`SELECT kind || ':' || id, 0, doc FROM topology`, func(k string, _ int64, d []byte) error {
			kind,id,ok:=strings.Cut(k,":");if !ok{return fmt.Errorf("BCC state topology key %q is invalid",k)}
			switch kind{
			case "ir": var v IRPoolMember;if err:=decode("topology",d,&v);err!=nil{return err};st.IRPool[id]=v
			case "route": var v ExplicitEXRoute;if err:=decode("topology",d,&v);err!=nil{return err};st.EXRoutes[id]=v
			case "binding": var v TopologyBinding;if err:=decode("topology",d,&v);err!=nil{return err};st.TopologyBindings[id]=v
			default:return fmt.Errorf("BCC state topology kind %q is invalid",kind)}
			return nil
		}},
		{`SELECT route_id, 0, doc FROM ingress_selections`, func(k string, _ int64, d []byte) error {
			var v IngressSelection
			err := decode("ingress_selections", d, &v)
			st.IngressSelections[k] = v
			return err
		}},
		{`SELECT route_id, 0, doc FROM ingress_distributions`, func(k string, _ int64, d []byte) error {
			var v IngressDistribution
			err := decode("ingress_distributions", d, &v)
			st.IngressDistributions[k] = v
			return err
		}},
		{`SELECT node_id, 0, doc FROM finance`, func(k string, _ int64, d []byte) error {
			var v NodeFinance
			err := decode("finance", d, &v)
			st.Finance[k] = v
			return err
		}},
		{`SELECT node_id, 0, doc FROM finance_policies`, func(k string, _ int64, d []byte) error {
			var v FinancePolicy
			err := decode("finance_policies", d, &v)
			st.Policies[k] = v
			return err
		}},
		{`SELECT node_id, seq, doc FROM finance_rate_history ORDER BY node_id, seq`, func(k string, _ int64, d []byte) error {
			var v FinancePolicy
			err := decode("finance_rate_history", d, &v)
			st.RateHistory[k] = append(st.RateHistory[k], v)
			return err
		}},
		{`SELECT '', seq, doc FROM finance_ledger ORDER BY seq`, func(_ string, _ int64, d []byte) error {
			var v FinanceLedgerEntry
			err := decode("finance_ledger", d, &v)
			st.FinanceLedger = append(st.FinanceLedger, v)
			return err
		}},
		{`SELECT node_id, 0, doc FROM finance_remainders`, func(k string, _ int64, d []byte) error {
			var v financeRemainder
			err := decode("finance_remainders", d, &v)
			if st.FinanceRemainders == nil {
				st.FinanceRemainders = map[string]financeRemainder{}
			}
			st.FinanceRemainders[k] = v
			return err
		}},
		{`SELECT node_id, 0, doc FROM telemetry`, func(k string, _ int64, d []byte) error {
			var v TelemetryCursor
			err := decode("telemetry", d, &v)
			st.Telemetry[k] = v
			return err
		}},
		{`SELECT node_id, seq, doc FROM history ORDER BY node_id, seq`, func(k string, _ int64, d []byte) error {
			var v HistoryPoint
			err := decode("history", d, &v)
			st.History[k] = append(st.History[k], v)
			return err
		}},
		{`SELECT key, 0, doc FROM active_alerts`, func(k string, _ int64, d []byte) error {
			var v Alert
			err := decode("active_alerts", d, &v)
			st.ActiveAlerts[k] = v
			return err
		}},
		{`SELECT node_id, 0, boot_id FROM retired_boot_ids`, func(k string, _ int64, d []byte) error {
			if st.RetiredBootIDs[k] == nil {
				st.RetiredBootIDs[k] = map[string]bool{}
			}
			st.RetiredBootIDs[k][string(d)] = true
			return nil
		}},
		{`SELECT name, 0, value FROM counters`, func(k string, _ int64, d []byte) error {
			n, err := strconv.ParseUint(string(d), 10, 64)
			if err != nil {
				return fmt.Errorf("BCC state counter %s: %w", k, err)
			}
			switch k {
			case "next_job":
				st.NextJob = n
			case "next_rate_version":
				st.NextRateVersion = n
			case "next_telemetry_ingest_id":
				st.NextTelemetryIngestID = n
			}
			return nil
		}},
	}
	for _, s := range steps {
		if err := each(s.query, s.fn); err != nil {
			return st, err
		}
	}
	normalizeState(&st)
	return st, nil
}

// migrateJSONState converts a JSON state file in place: the original is kept
// at <path>.json.bak, the database is built beside it and renamed over path.
func migrateJSONState(path string, raw []byte, st state) error {
	if err := writeAtomic(path+".json.bak", raw, 0o600); err != nil {
		return fmt.Errorf("keep JSON state backup: %w", err)
	}
	tmp := path + ".sqlite-migrating"
	_ = os.Remove(tmp)
	if err := writeStateDB(tmp, st); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	back, err := readStateDB(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("verify migrated state: %w", err)
	}
	if !bytes.Equal(canonicalStateJSON(st), canonicalStateJSON(back)) {
		_ = os.Remove(tmp)
		return errors.New("migrated state differs from the JSON state")
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return fsyncDir(path)
}

// canonicalStateJSON is the state's JSON without entries the database does
// not keep (empty history lists, boot IDs not marked retired).
func canonicalStateJSON(st state) []byte {
	c, err := cloneState(st)
	if err != nil {
		return nil
	}
	for k, v := range c.History {
		if len(v) == 0 {
			delete(c.History, k)
		}
	}
	for k, v := range c.RateHistory {
		if len(v) == 0 {
			delete(c.RateHistory, k)
		}
	}
	for k, boots := range c.RetiredBootIDs {
		for b, ok := range boots {
			if !ok {
				delete(boots, b)
			}
		}
		if len(boots) == 0 {
			delete(c.RetiredBootIDs, k)
		}
	}
	if len(c.FinanceRemainders) == 0 {
		c.FinanceRemainders = nil
	}
	if len(c.FinanceLedger) == 0 {
		c.FinanceLedger = nil
	}
	b, _ := json.Marshal(c)
	return b
}
