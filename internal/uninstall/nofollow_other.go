//go:build !unix

package uninstall

import (
	"errors"
	"os"
)

func openNoFollow(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, errSymlink
	}
	return os.Open(path)
}

func fileOwner(fi os.FileInfo) (uid, gid int) { return -1, -1 }

func lockFile(path string) (func(), error) {
	return nil, errors.New("file locks are not supported on this platform")
}
