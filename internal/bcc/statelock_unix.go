//go:build unix

package bcc

import (
	"fmt"
	"os"
	"syscall"
)

// LockState takes the exclusive process lock on a BCC state file
// (<state-file>.lock). The kernel releases it when the process exits, however
// it exits. BCC holds it for its whole life, so a second BCC cannot start on
// the same state, and tools that must see state and audit at one instant
// (restore-preview) can tell that BCC is not running before they read.
func LockState(stateFile string) (release func(), err error) {
	path := stateFile + ".lock"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s is in use by another process (a running BCC, or another tool): %w", stateFile, err)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
