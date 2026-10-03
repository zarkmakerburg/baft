package uninstall

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxUnitBytes   = 64 << 10
	maxConfigBytes = 1 << 20
	maxSmallBytes  = 1 << 20
	maxHashBytes   = 1 << 30 // binaries, state databases, audit logs
)

var errSymlink = errors.New("is a symlink; not followed")

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// readRegular reads a bounded regular file. It is opened once, without
// following a symlink in its final component; the type and size checks and
// the read all use that one descriptor, so swapping the path afterwards cannot
// change what is read.
func readRegular(path string, max int64) ([]byte, string) {
	f, err := openNoFollow(path)
	if err != nil {
		if errors.Is(err, errSymlink) {
			return nil, "is a symlink (not followed)"
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, "does not exist"
		}
		return nil, "is not readable: " + clip(err.Error())
	}
	defer f.Close()
	st, err := f.Stat()
	switch {
	case err != nil:
		return nil, "is not readable: " + clip(err.Error())
	case !st.Mode().IsRegular():
		return nil, "is not a regular file"
	case st.Size() > max:
		return nil, "is larger than expected"
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, "is not readable: " + clip(err.Error())
	}
	if int64(len(b)) > max {
		return nil, "is larger than expected"
	}
	return b, ""
}

// hashRegular hashes a regular file (not followed if it is a symlink) without
// holding it in memory.
func hashRegular(path string) (sum string, size int64, problem string) {
	f, err := openNoFollow(path)
	if err != nil {
		if errors.Is(err, errSymlink) {
			return "", 0, "is a symlink (not followed)"
		}
		if errors.Is(err, os.ErrNotExist) {
			return "", 0, "does not exist"
		}
		return "", 0, "is not readable: " + clip(err.Error())
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", 0, "is not a regular file"
	}
	if st.Size() > maxHashBytes {
		return "", 0, "is larger than expected"
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxHashBytes+1))
	if err != nil {
		return "", 0, "is not readable: " + clip(err.Error())
	}
	return hex.EncodeToString(h.Sum(nil)), n, ""
}

// treeHash is a digest of a directory tree (names, types, modes, file
// contents, symlink targets), used to prove a kept or removed tree did not
// change between plan and apply.
func treeHash(root string) (string, string) {
	h := sha256.New()
	var problem string
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		io.WriteString(h, rel+"\x00"+fi.Mode().String()+"\x00")
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			io.WriteString(h, "->"+t)
		case fi.Mode().IsRegular():
			s, _, pr := hashRegular(p)
			if pr != "" {
				return errors.New(rel + " " + pr)
			}
			io.WriteString(h, s)
		}
		io.WriteString(h, "\n")
		return nil
	})
	if err != nil {
		problem = "is not readable: " + clip(err.Error())
		return "", problem
	}
	return hex.EncodeToString(h.Sum(nil)), ""
}

// BAFT's Go module. A binary is BAFT's when its embedded build information
// (read from the file, never executed) names this module and the expected
// main package. Stripped release binaries keep this record.
const baftModule = "github.com/zarkmakerburg/baft"

var defaultMainPkg = map[string]string{
	"baft":       baftModule + "/cmd/baft",
	"baft-pair":  baftModule + "/cmd/baft-pair",
	"baft-agent": baftModule + "/cmd/baft-agent",
	"baft-bcc":   baftModule + "/cmd/baft-bcc",
}

// binaryOwnership proves (or not) that path is the BAFT binary named name.
func (e *Env) binaryOwnership(path, name string) (Ownership, string) {
	want := e.MainPkg[name]
	if want == "" {
		want = defaultMainPkg[name]
	}
	f, err := openNoFollow(path)
	if err != nil {
		if errors.Is(err, errSymlink) {
			return Unknown, "is a symlink (not followed)"
		}
		return Unknown, "is not readable: " + clip(err.Error())
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return Unknown, "is not a regular file"
	}
	bi, err := buildinfo.Read(f)
	if err != nil {
		return Unmanaged, "carries no Go build information, so it is not a BAFT build"
	}
	if bi.Main.Path != baftModule && !strings.HasPrefix(bi.Path, baftModule+"/") {
		return Unmanaged, "is a Go program from " + clip(bi.Path) + ", not BAFT"
	}
	if bi.Path != want {
		return Unmanaged, "is BAFT's " + clip(filepath.Base(bi.Path)) + ", not " + name
	}
	return Managed, "BAFT build of " + clip(strings.TrimPrefix(bi.Path, baftModule+"/")) + " (embedded Go build information)"
}

func clip(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func isDir(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.IsDir()
}
