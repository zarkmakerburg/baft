package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const version = "0.0.0-dev"

type versionInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "version" {
		if len(args) == 2 && args[1] == "--json" {
			_ = json.NewEncoder(stdout).Encode(versionInfo{Name: "baft", Version: version})
			return 0
		}
		if len(args) == 1 {
			fmt.Fprintln(stdout, "baft", version)
			return 0
		}
		fmt.Fprintln(stderr, "usage: baft version [--json]")
		return 2
	}
	fmt.Fprintln(stderr, "BAFT research software; available command: version [--json].")
	return 2
}
