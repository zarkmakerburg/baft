// Command baft-release manages BAFT release signing.
//
// Offline, on the root key holder's machine:
//
//	baft-release keygen  -out root            # root.key (keep offline) + root.pub (commit)
//	baft-release keygen  -out release         # release.key (CI secret) + release.pub
//	baft-release certify -root-key root.key -release-pub release.pub -valid-days 180 -out release-key.cert.json
//	baft-release revoke  -root-key root.key [-key-id <id>] [-in revocations.json] [-valid-days 180] -out revocations.json
//
// revoke with no -key-id re-signs the list unchanged with a new expiry; the
// root holder must do that before the current list expires.
//
// In CI and on servers:
//
//	baft-release sign   -dir dist -version v1.0.0 -commit <sha> -cert release-key.cert.json -root-pub root.pub
//	baft-release verify -dir dist -root-pub root.pub -revocations revocations.json \
//	    [-state /var/lib/baft/release-state.json [-update-state] [-allow-downgrade]]
package main

import (
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zarkmakerburg/baft/internal/release"
)

// SigningKeyEnv lets CI pass the release key without writing it to disk.
const SigningKeyEnv = "BAFT_RELEASE_SIGNING_KEY"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	cmds := map[string]func([]string, io.Writer) error{
		"keygen": keygen, "keyid": keyid, "certify": certify, "revoke": revoke, "sign": sign, "verify": verify,
	}
	cmd, ok := cmds[args[0]]
	if !ok {
		usage(stderr)
		return 2
	}
	if err := cmd(args[1:], stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 2
		}
		fmt.Fprintln(stderr, "baft-release "+args[0]+":", err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: baft-release <keygen|keyid|certify|revoke|sign|verify> [flags]")
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

func need(fs *flag.FlagSet, names ...string) error {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	for _, n := range names {
		if !set[n] {
			return fmt.Errorf("-%s is required", n)
		}
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	return nil
}

func keygen(args []string, stdout io.Writer) error {
	fs := newFlags("keygen")
	out := fs.String("out", "", "output prefix; writes <prefix>.key (0600) and <prefix>.pub")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := need(fs, "out"); err != nil {
		return err
	}
	pub, priv, err := release.GenerateKey()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0700); err != nil {
		return err
	}
	// O_EXCL: never overwrite an existing key.
	kf, err := os.OpenFile(*out+".key", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(kf, release.EncodePrivate(priv)); err != nil {
		kf.Close()
		return err
	}
	if err := kf.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(*out+".pub", []byte(release.EncodePublic(pub)+"\n"), 0644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s.key\n%s.pub\nkey id %s\n", *out, *out, release.KeyID(pub))
	return nil
}

func keyid(args []string, stdout io.Writer) error {
	fs := newFlags("keyid")
	pubPath := fs.String("pub", "", "public key file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := need(fs, "pub"); err != nil {
		return err
	}
	pub, err := release.ReadPublic(*pubPath)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, release.KeyID(pub))
	return nil
}

func certify(args []string, stdout io.Writer) error {
	fs := newFlags("certify")
	rootKey := fs.String("root-key", "", "offline root private key file")
	relPub := fs.String("release-pub", "", "release signing public key file")
	days := fs.Int("valid-days", 180, "certificate validity in days")
	out := fs.String("out", "", "certificate output file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := need(fs, "root-key", "release-pub", "out"); err != nil {
		return err
	}
	root, err := release.ReadPrivate(*rootKey)
	if err != nil {
		return err
	}
	pub, err := release.ReadPublic(*relPub)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	env, err := release.Certify(root, pub, now, now.Add(time.Duration(*days)*24*time.Hour))
	if err != nil {
		return err
	}
	if err := release.WriteEnvelope(*out, env); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "certified release key %s until %s\n", release.KeyID(pub), now.AddDate(0, 0, *days).Format(time.RFC3339))
	return nil
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func revoke(args []string, stdout io.Writer) error {
	fs := newFlags("revoke")
	rootKey := fs.String("root-key", "", "offline root private key file")
	in := fs.String("in", "", "current revocation list to extend; its sequence is carried forward")
	out := fs.String("out", "", "revocation list output file")
	days := fs.Int("valid-days", 180, "list validity in days; re-sign before it expires")
	var ids multi
	fs.Var(&ids, "key-id", "release key id to revoke (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := need(fs, "root-key", "out"); err != nil {
		return err
	}
	root, err := release.ReadPrivate(*rootKey)
	if err != nil {
		return err
	}
	all := map[string]bool{}
	seq := uint64(1)
	if *in != "" {
		env, err := release.ReadEnvelope(*in)
		if err != nil {
			return err
		}
		prev, err := release.OpenRevocations(env, root.Public().(ed25519.PublicKey))
		if err != nil {
			return err
		}
		for _, id := range prev.RevokedKeyIDs {
			all[id] = true
		}
		seq = prev.Sequence + 1
	}
	for _, id := range ids {
		all[id] = true
	}
	list := make([]string, 0, len(all))
	for id := range all {
		list = append(list, id)
	}
	now := time.Now()
	env, err := release.SignRevocations(root, seq, list, now, now.Add(time.Duration(*days)*24*time.Hour))
	if err != nil {
		return err
	}
	if err := release.WriteEnvelope(*out, env); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "revocation list sequence %d: %d revoked key id(s), expires %s, in %s\n",
		seq, len(list), now.Add(time.Duration(*days)*24*time.Hour).UTC().Format(time.RFC3339), *out)
	if *in == "" {
		fmt.Fprintln(stdout, "note: started a new list at sequence 1; servers that saw a higher sequence will refuse it (pass -in to extend the current list)")
	}
	return nil
}

func sign(args []string, stdout io.Writer) error {
	fs := newFlags("sign")
	dir := fs.String("dir", "", "directory holding the built artifacts")
	version := fs.String("version", "", "release tag, e.g. v1.0.0")
	commit := fs.String("commit", "", "full commit SHA the artifacts were built from")
	certPath := fs.String("cert", "", "root-signed release key certificate")
	keyPath := fs.String("key", "", "release signing key file (default: $"+SigningKeyEnv+")")
	rootPub := fs.String("root-pub", "", "pinned root public key; checks the certificate before signing")
	var flags multi
	fs.Var(&flags, "build-flag", "build flag to record in provenance (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := need(fs, "dir", "version", "commit", "cert", "root-pub"); err != nil {
		return err
	}
	var key ed25519.PrivateKey
	var err error
	if *keyPath != "" {
		key, err = release.ReadPrivate(*keyPath)
	} else if v := os.Getenv(SigningKeyEnv); v != "" {
		key, err = release.DecodePrivate(v)
	} else {
		err = errors.New("no signing key: pass -key or set " + SigningKeyEnv)
	}
	if err != nil {
		return err
	}
	cert, err := release.ReadEnvelope(*certPath)
	if err != nil {
		return err
	}
	root, err := release.ReadPublic(*rootPub)
	if err != nil {
		return err
	}
	m, err := release.SignDir(release.SignInput{
		Dir: *dir, Version: *version, Commit: *commit, CreatedAt: time.Now(),
		Provenance: provenance(flags), Key: key, Cert: cert, Root: root,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "signed %s (%d artifacts) with release key %s\n", m.Version, len(m.Artifacts), m.SigningKeyID)
	return nil
}

// provenance records the GitHub Actions run when there is one.
func provenance(flags []string) release.Provenance {
	p := release.Provenance{Builder: "local", GoVersion: runtime.Version(), BuildFlags: flags}
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		server := os.Getenv("GITHUB_SERVER_URL")
		p.Builder = "github-actions"
		p.Repository = server + "/" + os.Getenv("GITHUB_REPOSITORY")
		p.Ref = os.Getenv("GITHUB_REF")
		p.Workflow = os.Getenv("GITHUB_WORKFLOW_REF")
		p.RunID = os.Getenv("GITHUB_RUN_ID")
		p.RunAttempt = os.Getenv("GITHUB_RUN_ATTEMPT")
	}
	return p
}

func verify(args []string, stdout io.Writer) error {
	fs := newFlags("verify")
	dir := fs.String("dir", "", "release directory")
	rootPub := fs.String("root-pub", "", "pinned root public key")
	revPath := fs.String("revocations", "", "current root-signed revocation list")
	statePath := fs.String("state", "", "trust state file; refuses downgrades and replayed revocation lists (missing file: first install)")
	update := fs.Bool("update-state", false, "record this release in -state after it verifies")
	downgrade := fs.Bool("allow-downgrade", false, "accept a release older than the one in -state")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := need(fs, "dir", "root-pub", "revocations"); err != nil {
		return err
	}
	if (*update || *downgrade) && *statePath == "" {
		return errors.New("-update-state and -allow-downgrade need -state")
	}
	root, err := release.ReadPublic(*rootPub)
	if err != nil {
		return err
	}
	rev, err := release.ReadEnvelope(*revPath)
	if err != nil {
		return err
	}
	in := release.VerifyInput{Dir: *dir, Root: root, Revocations: &rev, AllowDowngrade: *downgrade}
	var state release.TrustState
	if *statePath != "" {
		if state, err = release.ReadState(*statePath); err != nil {
			return err
		}
		in.State = &state
	}
	v, err := release.VerifyDir(in)
	if err != nil {
		return err
	}
	if *update {
		if err := release.WriteState(*statePath, state.Advance(v, time.Now())); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "OK %s %s: %d artifacts, release key %s, root %s, revocation list %d (expires %s)\n",
		v.Version, v.Commit[:12], len(v.Artifacts), v.SigningKeyID, release.KeyID(root),
		v.Revocations.Sequence, v.Revocations.ExpiresAt.Format(time.RFC3339))
	return nil
}
