package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type Manifest struct {
	Version     int        `json:"version"`
	Description string     `json:"description,omitempty"`
	Scenarios   []Scenario `json:"scenarios"`
}

type Scenario struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	Command        []string `json:"command"`
	Repetitions    int      `json:"repetitions"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Informational  bool     `json:"informational"`
}

type RunMetadata struct {
	StartedAt string `json:"started_at"`
	GitSHA    string `json:"git_sha"`
	GoVersion string `json:"go_version"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	Hostname  string `json:"hostname,omitempty"`
	Manifest  string `json:"manifest"`
}

type AttemptResult struct {
	Attempt    int    `json:"attempt"`
	DurationMS int64  `json:"duration_ms"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out"`
	StdoutLog  string `json:"stdout_log"`
	StderrLog  string `json:"stderr_log"`
}

type ScenarioResult struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Kind          string          `json:"kind"`
	Informational bool            `json:"informational"`
	Passed        bool            `json:"passed"`
	Attempts      []AttemptResult `json:"attempts"`
	Metrics       []map[string]any `json:"metrics,omitempty"`
}

type Summary struct {
	Metadata  RunMetadata      `json:"metadata"`
	Scenarios []ScenarioResult `json:"scenarios"`
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("baft-bench", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	manifestPath := fs.String("manifest", "bench/manifest.json", "benchmark manifest path")
	scenarioID := fs.String("scenario", "all", "scenario ID, or all")
	outRoot := fs.String("out", "bench/results", "result directory root")
	listOnly := fs.Bool("list", false, "list scenarios and exit")
	dryRun := fs.Bool("dry-run", false, "validate and print selected commands without executing")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	manifest, err := loadManifest(*manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifest: %v\n", err)
		return 2
	}
	selected, err := selectScenarios(manifest.Scenarios, *scenarioID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if *listOnly {
		for _, s := range manifest.Scenarios {
			fmt.Printf("%s\t%s\t%s\trepetitions=%d\tinformational=%t\n", s.ID, s.Kind, s.Name, s.Repetitions, s.Informational)
		}
		return 0
	}
	if *dryRun {
		for _, s := range selected {
			fmt.Printf("%s: %s\n", s.ID, formatCommand(s.Command))
		}
		return 0
	}

	started := time.Now().UTC()
	sha := commandOutput("git", "rev-parse", "HEAD")
	if sha == "" {
		sha = "unknown"
	}
	shortSHA := sha
	if len(shortSHA) > 12 {
		shortSHA = shortSHA[:12]
	}
	stamp := started.Format("20060102T150405Z")
	runDir := filepath.Join(*outRoot, stamp+"-"+shortSHA)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create result dir: %v\n", err)
		return 2
	}

	host, _ := os.Hostname()
	summary := Summary{Metadata: RunMetadata{
		StartedAt: started.Format(time.RFC3339),
		GitSHA:    sha,
		GoVersion: commandOutput("go", "version"),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
		Hostname:  host,
		Manifest:  *manifestPath,
	}}

	hardFailure := false
	for _, s := range selected {
		result := executeScenario(runDir, s)
		summary.Scenarios = append(summary.Scenarios, result)
		if !result.Passed && !s.Informational {
			hardFailure = true
		}
	}
	sort.Slice(summary.Scenarios, func(i, j int) bool { return summary.Scenarios[i].ID < summary.Scenarios[j].ID })
	summaryPath := filepath.Join(runDir, "summary.json")
	if err := writeJSON(summaryPath, summary); err != nil {
		fmt.Fprintf(os.Stderr, "write summary: %v\n", err)
		return 2
	}
	metrics := map[string][]map[string]any{}
	for _, s := range summary.Scenarios {
		if len(s.Metrics) > 0 {
			metrics[s.ID] = s.Metrics
		}
	}
	if err := writeJSON(filepath.Join(runDir, "metrics.json"), map[string]any{
		"metadata": summary.Metadata,
		"metrics":  metrics,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "write metrics: %v\n", err)
		return 2
	}
	fmt.Printf("BAFT benchmark results: %s\n", runDir)
	if hardFailure {
		return 1
	}
	return 0
}

func loadManifest(path string) (Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, err
	}
	if m.Version != 1 {
		return Manifest{}, fmt.Errorf("unsupported manifest version %d", m.Version)
	}
	if len(m.Scenarios) == 0 {
		return Manifest{}, errors.New("manifest has no scenarios")
	}
	seen := map[string]bool{}
	for i := range m.Scenarios {
		s := &m.Scenarios[i]
		if s.ID == "" || s.Name == "" || len(s.Command) == 0 {
			return Manifest{}, fmt.Errorf("scenario %d is missing id, name, or command", i)
		}
		if seen[s.ID] {
			return Manifest{}, fmt.Errorf("duplicate scenario id %q", s.ID)
		}
		seen[s.ID] = true
		if s.Repetitions <= 0 {
			s.Repetitions = 1
		}
		if s.TimeoutSeconds <= 0 {
			s.TimeoutSeconds = 300
		}
	}
	return m, nil
}

func selectScenarios(all []Scenario, id string) ([]Scenario, error) {
	if id == "all" {
		return append([]Scenario(nil), all...), nil
	}
	for _, s := range all {
		if s.ID == id {
			return []Scenario{s}, nil
		}
	}
	return nil, fmt.Errorf("unknown scenario %q", id)
}

func executeScenario(runDir string, s Scenario) ScenarioResult {
	result := ScenarioResult{ID: s.ID, Name: s.Name, Kind: s.Kind, Informational: s.Informational, Passed: true}
	scenarioDir := filepath.Join(runDir, s.ID)
	_ = os.MkdirAll(scenarioDir, 0o755)
	for i := 1; i <= s.Repetitions; i++ {
		stdoutPath := filepath.Join(scenarioDir, fmt.Sprintf("attempt-%02d.stdout.log", i))
		stderrPath := filepath.Join(scenarioDir, fmt.Sprintf("attempt-%02d.stderr.log", i))
		stdout, _ := os.Create(stdoutPath)
		stderr, _ := os.Create(stderrPath)

		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.TimeoutSeconds)*time.Second)
		cmd := exec.CommandContext(ctx, s.Command[0], s.Command[1:]...)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		start := time.Now()
		err := cmd.Run()
		elapsed := time.Since(start)
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		cancel()
		_ = stdout.Close()
		_ = stderr.Close()
		metrics, metricErr := parseMetricFile(stdoutPath)
		if metricErr != nil {
			fmt.Fprintf(os.Stderr, "%s attempt %d metric parse: %v\n", s.ID, i, metricErr)
			result.Passed = false
		} else {
			result.Metrics = append(result.Metrics, metrics...)
		}

		exitCode := 0
		if err != nil {
			exitCode = 1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exitCode = ee.ExitCode()
			}
			result.Passed = false
		}
		result.Attempts = append(result.Attempts, AttemptResult{
			Attempt:    i,
			DurationMS: elapsed.Milliseconds(),
			ExitCode:   exitCode,
			TimedOut:   timedOut,
			StdoutLog:  filepath.ToSlash(filepath.Join(s.ID, filepath.Base(stdoutPath))),
			StderrLog:  filepath.ToSlash(filepath.Join(s.ID, filepath.Base(stderrPath))),
		})
	}
	return result
}

const metricPrefix = "BAFT_BENCH_METRIC "

func parseMetricFile(path string) ([]map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []map[string]any
	s := bufio.NewScanner(f)
	buf := make([]byte, 64*1024)
	s.Buffer(buf, 2*1024*1024)
	for s.Scan() {
		line := s.Text()
		idx := strings.Index(line, metricPrefix)
		if idx < 0 {
			continue
		}
		payload := strings.TrimSpace(line[idx+len(metricPrefix):])
		var metric map[string]any
		if err := json.Unmarshal([]byte(payload), &metric); err != nil {
			return nil, fmt.Errorf("decode metric %q: %w", payload, err)
		}
		out = append(out, metric)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

func commandOutput(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func formatCommand(parts []string) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		if strings.ContainsAny(p, " \t|&;$()[]{}*?\\\"'") {
			out[i] = fmt.Sprintf("%q", p)
		} else {
			out[i] = p
		}
	}
	return strings.Join(out, " ")
}
