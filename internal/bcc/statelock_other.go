//go:build !unix

package bcc

import "errors"

// LockState is not available on this platform.
func LockState(stateFile string) (func(), error) {
	return nil, errors.New("the state-file lock is not supported on this platform")
}
