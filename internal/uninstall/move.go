package uninstall

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// moveAside moves src to dst: a rename on the same filesystem (atomic, all
// metadata kept), otherwise a verified copy that keeps modes and owners,
// followed by removing the original.
func moveAside(src, dst string, dir bool) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if fi, err := os.Lstat(src); err == nil && !dir && !fi.Mode().IsRegular() && fi.Mode()&os.ModeSymlink == 0 {
		// A socket across filesystems: nothing to carry, the service makes a
		// new one when it starts.
		return os.Remove(src)
	}
	if err := copyTree(src, dst); err != nil {
		os.RemoveAll(dst)
		return err
	}
	var a, b string
	var pa, pb string
	if dir {
		a, pa = treeHash(src)
		b, pb = treeHash(dst)
	} else {
		a, _, pa = hashRegular(src)
		b, _, pb = hashRegular(dst)
	}
	if pa != "" || pb != "" || a != b {
		os.RemoveAll(dst)
		return fmt.Errorf("copy of %s did not verify", src)
	}
	return os.RemoveAll(src)
}

func copyTree(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	uid, gid := fileOwner(fi)
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		t, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.Symlink(t, dst); err != nil {
			return err
		}
		if uid >= 0 {
			_ = os.Lchown(dst, uid, gid)
		}
		return nil
	case fi.IsDir():
		if err := os.Mkdir(dst, 0o700); err != nil {
			return err
		}
		ents, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range ents {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
	case fi.Mode().IsRegular():
		in, err := openNoFollow(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Sync(); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
	default:
		// Sockets and other special files are not data; nothing to carry.
		return nil
	}
	if uid >= 0 {
		if err := os.Lchown(dst, uid, gid); err != nil && os.Geteuid() == 0 {
			return err
		}
	}
	if err := os.Chmod(dst, fi.Mode().Perm()|fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky)); err != nil {
		return err
	}
	return os.Chtimes(dst, fi.ModTime(), fi.ModTime())
}
