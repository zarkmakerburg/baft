//go:build !unix

package main

import "os"

func terminalWidth(f *os.File) int { return 0 }
