package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type Observation struct {
	Time             time.Time `json:"time"`
	TunnelID         string    `json:"tunnel_id"`
	ISPID            string    `json:"isp_id"`
	TrafficProfile   string    `json:"traffic_profile"`
	Kind             string    `json:"kind"`
	Status           string    `json:"status"`
	BlockType        string    `json:"block_type,omitempty"`
	BytesTotal       uint64    `json:"bytes_total,omitempty"`
	LatencyMS        float64   `json:"latency_ms,omitempty"`
	ForeignIPToken   string    `json:"foreign_ip_token,omitempty"`
	RemoteSourceHash string    `json:"remote_source_hash,omitempty"`
	RemoteASN        string    `json:"remote_asn,omitempty"`
	Note             string    `json:"note,omitempty"`
}

type Cell struct {
	TunnelID             string  `json:"tunnel_id"`
	ISPID                string  `json:"isp_id"`
	StartedAt            string  `json:"started_at,omitempty"`
	FirstDisruptionHours *float64 `json:"time_to_first_disruption_hours,omitempty"`
	FullBlockHours       *float64 `json:"time_to_full_block_hours,omitempty"`
	BlockType            string  `json:"block_type,omitempty"`
	BytesAtDisruption    uint64  `json:"bytes_at_disruption,omitempty"`
	BytesAtBlock         uint64  `json:"bytes_at_block,omitempty"`
}

type ProbeSummary struct {
	TunnelID       string         `json:"tunnel_id"`
	InboundUnknown int            `json:"inbound_unknown"`
	ByASN          map[string]int `json:"by_asn,omitempty"`
	UniqueSources  int            `json:"unique_source_hashes"`
}

type Summary struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Cells       []Cell         `json:"cells"`
	Probes      []ProbeSummary `json:"probe_summary"`
	Notes       []string       `json:"notes,omitempty"`
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	fs := flag.NewFlagSet("baft-r003", flag.ContinueOnError)
	in := fs.String("in", "studies/r003/private/events.jsonl", "input JSONL observations")
	out := fs.String("out", "studies/r003/out/summary.json", "output summary JSON")
	if err := fs.Parse(args); err != nil { return 2 }

	obs, err := readObservations(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read observations: %v\n", err)
		return 2
	}
	s := summarize(obs)
	if err := os.MkdirAll(dirOf(*out), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create output dir: %v\n", err)
		return 2
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	b = append(b, '\n')
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write summary: %v\n", err)
		return 2
	}
	fmt.Printf("R-003 summary: %s\n", *out)
	return 0
}

func readObservations(path string) ([]Observation, error) {
	f, err := os.Open(path)
	if err != nil { return nil, err }
	defer f.Close()
	var out []Observation
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 2*1024*1024)
	line := 0
	for s.Scan() {
		line++
		raw := strings.TrimSpace(s.Text())
		if raw == "" || strings.HasPrefix(raw, "#") { continue }
		var o Observation
		if err := json.Unmarshal([]byte(raw), &o); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		if err := validate(o); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, o)
	}
	if err := s.Err(); err != nil { return nil, err }
	sort.Slice(out, func(i,j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

func validate(o Observation) error {
	if o.Time.IsZero() { return errors.New("time is required") }
	if !validTunnel(o.TunnelID) { return fmt.Errorf("invalid tunnel_id %q", o.TunnelID) }
	if o.ISPID == "" && o.Kind != "probe_inbound" && o.Kind != "fingerprint" && o.Kind != "certificate" {
		return errors.New("isp_id required for connectivity observation")
	}
	switch o.Status {
	case "ok", "degraded", "disrupted", "blocked", "observed":
	default:
		return fmt.Errorf("invalid status %q", o.Status)
	}
	return nil
}

func validTunnel(s string) bool {
	if len(s) < 2 || s[0] != 'T' { return false }
	var n int
	if _, err := fmt.Sscanf(s, "T%d", &n); err != nil { return false }
	return n >= 1 && n <= 10
}

func summarize(obs []Observation) Summary {
	type key struct{ t, i string }
	groups := map[key][]Observation{}
	probeByTunnel := map[string][]Observation{}
	for _, o := range obs {
		if o.Kind == "probe_inbound" {
			probeByTunnel[o.TunnelID] = append(probeByTunnel[o.TunnelID], o)
			continue
		}
		if o.ISPID != "" {
			k := key{o.TunnelID, o.ISPID}
			groups[k] = append(groups[k], o)
		}
	}

	var cells []Cell
	for k, rows := range groups {
		if len(rows) == 0 { continue }
		start := rows[0].Time
		c := Cell{TunnelID:k.t, ISPID:k.i, StartedAt:start.UTC().Format(time.RFC3339)}
		for _, o := range rows {
			if c.FirstDisruptionHours == nil && (o.Status == "disrupted" || o.Status == "blocked") {
				h := o.Time.Sub(start).Hours()
				c.FirstDisruptionHours = &h
				c.BytesAtDisruption = o.BytesTotal
			}
			if c.FullBlockHours == nil && o.Status == "blocked" {
				h := o.Time.Sub(start).Hours()
				c.FullBlockHours = &h
				c.BytesAtBlock = o.BytesTotal
				c.BlockType = o.BlockType
			}
		}
		cells = append(cells, c)
	}
	sort.Slice(cells, func(i,j int) bool {
		if cells[i].TunnelID == cells[j].TunnelID { return cells[i].ISPID < cells[j].ISPID }
		return cells[i].TunnelID < cells[j].TunnelID
	})

	var probes []ProbeSummary
	for tunnel, rows := range probeByTunnel {
		ps := ProbeSummary{TunnelID:tunnel, InboundUnknown:len(rows), ByASN:map[string]int{}}
		srcs := map[string]struct{}{}
		for _, o := range rows {
			if o.RemoteASN != "" { ps.ByASN[o.RemoteASN]++ }
			if o.RemoteSourceHash != "" { srcs[o.RemoteSourceHash]=struct{}{} }
		}
		ps.UniqueSources=len(srcs)
		probes=append(probes, ps)
	}
	sort.Slice(probes, func(i,j int) bool { return probes[i].TunnelID < probes[j].TunnelID })

	return Summary{
		GeneratedAt: time.Now().UTC(),
		Cells: cells,
		Probes: probes,
		Notes: []string{
			"Shared output intentionally excludes server IPs, credentials, keys, and raw probe payloads.",
			"Times are observational; causal attribution to filtering requires the diagnostic checks defined in studies/r003/README.md.",
		},
	}
}

func dirOf(path string) string {
	i := strings.LastIndexAny(path, "/\\")
	if i < 0 { return "." }
	if i == 0 { return path[:1] }
	return path[:i]
}
