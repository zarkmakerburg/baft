//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd

package main

import "os"

// Where a terminal test is not available, the menu is never opened (fail closed).
func isTerminal(f *os.File) bool { return false }
