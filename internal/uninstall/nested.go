package uninstall

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Nested ownership: a directory BAFT created does not make everything in it
// BAFT's. Each entry is classified on its own; only entries proven BAFT's are
// eligible, everything else (including every symlink, which is never
// followed) is UNKNOWN and stays, and so does its parent. Directories are
// only ever removed when they end up empty (rmdir), never recursively.

const maxNestedDepth = 8

// nestedRule decides one regular file inside a walked tree, by its path
// relative to the tree root. ok=false means UNKNOWN.
type nestedRule func(rel string, path string, fi os.FileInfo) (class Class, evidence string, ok bool)

type nestedDir struct {
	path  string
	owner *Unit
	area  string
}

// walkNested classifies every entry under root (root itself included as a
// directory to remove if it ends up empty).
func (e *Env) walkNested(inv *Inventory, l location, root string, rule nestedRule) {
	fi, err := os.Lstat(root)
	if err != nil {
		return
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		a := newArtifact(l, root, "")
		a.Ownership, a.Evidence = Unknown, "is not a directory (a symlink is never followed)"
		inv.add(a)
		return
	}
	e.walkDir(inv, l, root, root, rule, 0)
}

func (e *Env) walkDir(inv *Inventory, l location, root, dir string, rule nestedRule, depth int) {
	inv.nested = append(inv.nested, nestedDir{path: dir, owner: l.owner, area: l.area})
	ents, err := os.ReadDir(dir)
	if err != nil {
		inv.Errors = append(inv.Errors, dir+": "+clip(err.Error()))
		return
	}
	for _, ent := range ents {
		p := filepath.Join(dir, ent.Name())
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			a := newArtifact(l, p, "")
			a.Ownership, a.Evidence = Unknown, "a symlink inside a BAFT directory: never followed, never removed"
			inv.add(a)
		case fi.IsDir():
			if depth+1 >= maxNestedDepth {
				a := newArtifact(l, p, "")
				a.Ownership, a.Evidence, a.Dir = Unknown, "nested deeper than BAFT writes", true
				inv.add(a)
				continue
			}
			e.walkDir(inv, l, root, p, rule, depth+1)
		case fi.Mode().IsRegular():
			a := newArtifact(l, p, "")
			sum, size, problem := hashRegular(p)
			a.SHA256, a.Size = sum, size
			if problem != "" {
				a.Ownership, a.Evidence = Unknown, problem
				inv.add(a)
				continue
			}
			if class, ev, ok := rule(rel, p, fi); ok {
				a.Class, a.Ownership, a.Evidence = class, Managed, ev
			} else {
				a.Ownership, a.Evidence = Unknown, "not a file BAFT writes here"
			}
			inv.add(a)
		default:
			a := newArtifact(l, p, "")
			a.Ownership, a.Evidence = Unknown, "not a regular file"
			inv.add(a)
		}
	}
}

var tunnelIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// tunnelsRule: the tunnel builder's records (internal/tunnelnode): the
// generation counter, the active-change pointer, and per change
// <id>/{txn.json, psk, pending.json, pairing.pending.json, ex-params.json,
// baft.new.yaml, peer-ca.pem, backup/{baft.yaml,unit,marker.json}}.
func tunnelsRule(rel, path string, fi os.FileInfo) (Class, string, bool) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	read := func(check func([]byte) bool) bool {
		b, problem := readRegular(path, maxSmallBytes)
		return problem == "" && check(b)
	}
	small := func(b []byte) bool { return len(b) > 0 && len(b) < 4096 }
	const ev = "the tunnel builder's change record"
	switch {
	case len(parts) == 1 && parts[0] == "generation":
		return ClassTunnelConfigs, ev, read(func(b []byte) bool { return regexp.MustCompile(`^[0-9]+\n?$`).Match(b) })
	case len(parts) == 1 && parts[0] == "active":
		return ClassTunnelConfigs, ev, read(func(b []byte) bool { return tunnelIDRe.Match(bytes.TrimSpace(b)) })
	case len(parts) == 2 && tunnelIDRe.MatchString(parts[0]):
		switch parts[1] {
		case "txn.json":
			return ClassTunnelConfigs, ev, read(func(b []byte) bool {
				return isJSONObject(b) && bytes.Contains(b, []byte(`"phase"`))
			})
		case "pending.json", "pairing.pending.json", "ex-params.json":
			return ClassTunnelConfigs, ev, read(isJSONObject)
		case "psk":
			return ClassTunnelConfigs, ev, read(small)
		case "baft.new.yaml":
			return ClassTunnelConfigs, ev, read(func(b []byte) bool { _, err := decodeConfig(path, b); return err == nil })
		case "peer-ca.pem":
			return ClassTunnelConfigs, ev, read(isPEM("CERTIFICATE"))
		}
	case len(parts) == 3 && tunnelIDRe.MatchString(parts[0]) && parts[1] == "backup":
		switch parts[2] {
		case "baft.yaml":
			return ClassTunnelConfigs, ev, read(func(b []byte) bool { _, err := decodeConfig("x.yaml", b); return err == nil })
		case "unit":
			return ClassTunnelConfigs, ev, read(func(b []byte) bool { return bytes.Contains(b, []byte("ExecStart=")) })
		case "marker.json":
			return ClassTunnelConfigs, ev, read(isMarker)
		}
	}
	return "", "", false
}

// pkiBackupRule: pki.before-<id> holds the four files `baft-pair pki` writes.
func pkiBackupRule(rel, path string, fi os.FileInfo) (Class, string, bool) {
	var check func([]byte) bool
	switch filepath.ToSlash(rel) {
	case "ca.pem", "server.pem":
		check = isPEM("CERTIFICATE")
	case "ca.key", "server.key":
		check = isPEMKey
	default:
		return "", "", false
	}
	b, problem := readRegular(path, maxSmallBytes)
	return ClassBackups, "the tunnel builder's copy of the previous certificates", problem == "" && check(b)
}

var hostMarkerRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.:-]{0,251}[A-Za-z0-9])?\n?$`)

// isHostMarker: pki/host, the certificate name the tunnel builder writes.
func isHostMarker(b []byte) bool { return hostMarkerRe.Match(b) }

// pkiRotationRule: pki.next-<rotation> / pki.prev-<rotation> hold a staged or
// previous certificate set of a certificate rotation (internal/tunnelnode):
// the four files `baft-pair pki` writes plus the host marker.
func pkiRotationRule(rel, path string, fi os.FileInfo) (Class, string, bool) {
	var check func([]byte) bool
	switch filepath.ToSlash(rel) {
	case "ca.pem", "server.pem":
		check = isPEM("CERTIFICATE")
	case "ca.key", "server.key":
		check = isPEMKey
	case "host":
		check = isHostMarker
	default:
		return "", "", false
	}
	b, problem := readRegular(path, maxSmallBytes)
	return ClassCertificates, "a certificate set of a BAFT certificate rotation", problem == "" && check(b)
}

// rotationsRule: certificate rotation records (internal/tunnelnode): the
// active pointer, the certificate epoch, and per rotation
// <id>/{rot.json, new-ca.pem, new-cert.pem, bundle.pem, backup/ca.pem}.
func rotationsRule(rel, path string, fi os.FileInfo) (Class, string, bool) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	read := func(check func([]byte) bool) bool {
		b, problem := readRegular(path, maxSmallBytes)
		return problem == "" && check(b)
	}
	const ev = "a BAFT certificate rotation record"
	switch {
	case len(parts) == 1 && parts[0] == "epoch":
		return ClassCertificates, ev, read(func(b []byte) bool { return regexp.MustCompile(`^[0-9]+\n?$`).Match(b) })
	case len(parts) == 1 && parts[0] == "active":
		return ClassCertificates, ev, read(func(b []byte) bool { return tunnelIDRe.Match(bytes.TrimSpace(b)) })
	case len(parts) == 2 && tunnelIDRe.MatchString(parts[0]):
		switch parts[1] {
		case "rot.json":
			return ClassCertificates, ev, read(func(b []byte) bool {
				return isJSONObject(b) && bytes.Contains(b, []byte(`"phase"`)) && bytes.Contains(b, []byte(`"epoch"`))
			})
		case "new-ca.pem", "new-cert.pem", "bundle.pem":
			return ClassCertificates, ev, read(isPEM("CERTIFICATE"))
		}
	case len(parts) == 3 && tunnelIDRe.MatchString(parts[0]) && parts[1] == "backup" && parts[2] == "ca.pem":
		return ClassCertificates, ev, read(isPEM("CERTIFICATE"))
	}
	return "", "", false
}

// rerunRule proves rerun-* entries by the manifest the installer writes as it
// copies (MANIFEST.sha256): a file it lists with the same digest is the
// installer's copy. A rerun directory without a manifest proves nothing.
func rerunRule(root string) nestedRule {
	listed := map[string]string{}
	manifest := filepath.Join(root, "MANIFEST.sha256")
	b, problem := readRegular(manifest, maxSmallBytes)
	ok := problem == ""
	if ok {
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			sum, name, found := strings.Cut(sc.Text(), "  ")
			if _, err := hex.DecodeString(sum); !found || err != nil || len(sum) != 64 || name == "" || strings.ContainsAny(name, "/\\") {
				ok = false
				break
			}
			listed[name] = sum // the last copy of a name is the one on disk
		}
	}
	return func(rel, path string, fi os.FileInfo) (Class, string, bool) {
		if !ok || strings.Contains(rel, string(filepath.Separator)) {
			return "", "", false
		}
		if rel == "MANIFEST.sha256" {
			return ClassBackups, "the installer's manifest of this rerun backup", true
		}
		want, isListed := listed[rel]
		if !isListed {
			return "", "", false
		}
		sum, _, problem := hashRegular(path)
		return ClassBackups, "the installer's rollback copy (listed in its manifest, digest matches)", problem == "" && sum == want
	}
}

// skelRule: entries identical to their counterpart in the user skeleton
// (content and modes) are the copy useradd made into the service user's home.
func (e *Env) skelRule(skelRoot string) nestedRule {
	return func(rel, path string, fi os.FileInfo) (Class, string, bool) {
		sp := filepath.Join(skelRoot, rel)
		sfi, err := os.Lstat(sp)
		if err != nil || !sfi.Mode().IsRegular() || sfi.Mode().Perm() != fi.Mode().Perm() {
			return "", "", false
		}
		a, _, pa := hashRegular(path)
		b, _, pb := hashRegular(sp)
		return ClassRuntime, "copied from " + e.SkelDir + " by useradd for the service user (identical to " + sp + ")", pa == "" && pb == "" && a == b
	}
}

// sourceCheckout is fail-closed: the installer's source tree is eligible
// only when it is BAFT's module and git proves the checkout clean: no
// modified or staged tracked file, no untracked or ignored file, and it is
// its own repository. Anything else, or no git, keeps it.
func sourceCheckout(path string) (bool, string) {
	b, problem := readRegular(filepath.Join(path, "go.mod"), maxSmallBytes)
	if problem != "" || !bytes.HasPrefix(b, []byte("module "+baftModule+"\n")) {
		return false, "not a BAFT source checkout"
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return false, "git is not available to prove the checkout clean; kept"
	}
	run := func(args ...string) (string, error) {
		// No hooks, no fsmonitor: a repository's own settings never run code here.
		full := append([]string{"-C", path, "-c", "safe.directory=*", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}, args...)
		cmd := exec.CommandContext(context.Background(), git, full...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "HOME=/nonexistent", "GIT_OPTIONAL_LOCKS=0"}
		out, err := cmd.Output()
		return string(out), err
	}
	top, err := run("rev-parse", "--show-toplevel")
	if err != nil {
		return false, "not a git checkout; kept"
	}
	if real, _ := filepath.EvalSymlinks(path); filepath.Clean(strings.TrimSpace(top)) != filepath.Clean(real) {
		return false, "part of another repository; kept"
	}
	st, err := run("status", "--porcelain=v1", "--ignored", "--untracked-files=all")
	if err != nil {
		return false, "git status failed; kept"
	}
	if s := strings.TrimSpace(st); s != "" {
		n := len(strings.Split(s, "\n"))
		return false, "the checkout has local changes or extra files (" + clip(strings.Split(s, "\n")[0]) + "; " + strconv.Itoa(n) + " entr(ies)); kept"
	}
	return true, "the installer's BAFT source checkout, proven clean by git (no modified, untracked or ignored file)"
}
