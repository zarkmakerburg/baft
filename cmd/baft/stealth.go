package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/recordshape"
)

// Emit a validated config without mutating the input. The installer performs
// atomic replacement only after this command succeeds.
func runStealthConfig(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config stealth-pro", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "existing pinned Noise config")
	c := recordshape.DefaultConfig(true)
	fs.StringVar(&c.Distribution, "distribution", c.Distribution, "normal or laplace")
	fs.Float64Var(&c.MeanBytes, "mean", c.MeanBytes, "underlying mean padding bytes")
	fs.Float64Var(&c.StdDevBytes, "stddev", c.StdDevBytes, "underlying standard deviation")
	fs.IntVar(&c.MaxPaddingBytes, "max-padding", c.MaxPaddingBytes, "hard padding byte budget")
	fs.Float64Var(&c.MaxPaddingRatio, "max-ratio", c.MaxPaddingRatio, "padding/payload cap; 0 disables ratio cap")
	fs.IntVar(&c.JitterMinUS, "jitter-min-us", c.JitterMinUS, "minimum requested jitter")
	fs.IntVar(&c.JitterMaxUS, "jitter-max-us", c.JitterMaxUS, "maximum requested jitter")
	if err := fs.Parse(args); err != nil || *file == "" || fs.NArg() != 0 {
		return 2
	}
	cfg, err := config.LoadFile(*file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if cfg.Noise == nil {
		fmt.Fprintln(stderr, "Stealth Pro requires an existing noise section with key_file and pinned peer_public_key; provision both peers first")
		return 1
	}
	cfg.Noise.RecordShaping = c
	if err := config.Validate(cfg); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
