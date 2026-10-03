//go:build unix

package main

import (
	"os"
	"syscall"
	"unsafe"
)

// terminalWidth asks the terminal for its width; 0 means unknown.
func terminalWidth(f *os.File) int {
	var ws struct{ Row, Col, X, Y uint16 }
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws))); e != 0 {
		return 0
	}
	return int(ws.Col)
}
