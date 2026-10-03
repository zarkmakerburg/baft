//go:build unix

package tunnelnode

import (
	"errors"
	"os"
	"syscall"
)

// openNoFollow opens a file for reading without following a symlink in its
// final component, and without blocking on a FIFO or device that someone swaps
// in. The caller checks what it opened with Stat on the same descriptor.
func openNoFollow(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, errSymlink
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}
