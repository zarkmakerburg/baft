//go:build linux

package tunnelnode

import "golang.org/x/sys/unix"

// exchangeDirs swaps two paths in one atomic step (renameat2
// RENAME_EXCHANGE): at every instant each name holds one complete tree, so
// a crash can never leave the service with half of a certificate set.
func exchangeDirs(a, b string) error {
	return unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
}
