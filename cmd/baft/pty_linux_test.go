//go:build linux

package main

import (
	"fmt"
	"os"
	"syscall"
	"testing"
	"unsafe"
)

// openPTY returns the slave side of a new pseudo-terminal: a real TTY.
func openPTY(t *testing.T) *os.File {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), uintptr(syscall.TIOCSPTLCK), uintptr(unsafe.Pointer(&unlock))); e != 0 {
		t.Skipf("cannot unlock the pty: %v", e)
	}
	var n uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), uintptr(syscall.TIOCGPTN), uintptr(unsafe.Pointer(&n))); e != 0 {
		t.Skipf("cannot name the pty: %v", e)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR, 0)
	if err != nil {
		t.Skipf("cannot open the pty slave: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func openPTYForTest(t *testing.T) *os.File { return openPTY(t) }
