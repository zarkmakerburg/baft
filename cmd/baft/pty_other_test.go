//go:build !linux

package main

import (
	"os"
	"testing"
)

func openPTYForTest(t *testing.T) *os.File { return nil }
