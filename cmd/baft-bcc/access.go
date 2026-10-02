package main

import (
	"crypto/ed25519"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/zarkmakerburg/baft/internal/bcc"
	"github.com/zarkmakerburg/baft/internal/release"
)

const defaultAccessFile = "./bcc-state.json.access.json"

// runAccess is the console-only side of BCC web access: it needs write
// access to the access file on this host, and there is no web equivalent.
func runAccess(args []string) int {
	if len(args) == 0 {
		accessUsage()
		return 2
	}
	fs := flag.NewFlagSet("access "+args[0], flag.ContinueOnError)
	file := fs.String("access-file", defaultAccessFile, "web access file")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		accessUsage()
		return 2
	}
	switch args[0] {
	case "init", "regenerate":
		creds, err := bcc.RotateAccess(*file, args[0] == "init", time.Now())
		if err != nil {
			fmt.Fprintln(os.Stderr, "baft-bcc access:", err)
			return 1
		}
		fmt.Printf("BCC web access, generation %d. Shown once; store it in a password manager.\n\n", creds.Generation)
		fmt.Printf("  path:     /%s/\n  username: %s\n  password: %s\n\n", creds.SecretPath, creds.Username, creds.Password)
		if args[0] == "regenerate" {
			fmt.Println("The old path, username and password no longer work, and every session has ended.")
		}
		return 0
	case "show":
		a, err := bcc.ReadAccessFile(*file)
		if err != nil {
			fmt.Fprintln(os.Stderr, "baft-bcc access:", err)
			return 1
		}
		fmt.Printf("generation %d, rotated %s\n  path:     /%s/\n  username: %s\n  password: not stored; run `baft-bcc access regenerate` to set a new one\n",
			a.Generation, a.RotatedAt.Format(time.RFC3339), a.SecretPath, a.Username)
		return 0
	}
	accessUsage()
	return 2
}

func accessUsage() {
	fmt.Fprintln(os.Stderr, "usage: baft-bcc access <init|regenerate|show> [--access-file <state-file>.access.json]")
	fmt.Fprintln(os.Stderr, "  init        create the secret path, username and password")
	fmt.Fprintln(os.Stderr, "  regenerate  replace all three and end every session")
	fmt.Fprintln(os.Stderr, "  show        print the path and username")
}

// reloadingCert serves the certificate files and picks up a renewal (for
// example by certbot) on the next handshake after the files change.
type reloadingCert struct {
	certFile, keyFile string
	mu                sync.Mutex
	cert              *tls.Certificate
	certMod, keyMod   time.Time
}

func (c *reloadingCert) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cs, err1 := os.Stat(c.certFile)
	ks, err2 := os.Stat(c.keyFile)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err1 == nil && err2 == nil && c.cert != nil && cs.ModTime().Equal(c.certMod) && ks.ModTime().Equal(c.keyMod) {
		return c.cert, nil
	}
	cert, err := tls.LoadX509KeyPair(c.certFile, c.keyFile)
	if err != nil {
		if c.cert != nil {
			return c.cert, nil // keep serving the last good pair during a partial renewal
		}
		return nil, err
	}
	c.cert = &cert
	if err1 == nil && err2 == nil {
		c.certMod, c.keyMod = cs.ModTime(), ks.ModTime()
	}
	return c.cert, nil
}

// runJobKey prints the public half of BCC's job-signing key, which every
// agent pins at enrollment.
func runJobKey(args []string) int {
	fs := flag.NewFlagSet("jobkey", flag.ContinueOnError)
	file := fs.String("job-key-file", "./bcc-state.json.job-key", "job-signing key")
	if len(args) == 0 || args[0] != "show" || fs.Parse(args[1:]) != nil || fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: baft-bcc jobkey show [--job-key-file <state-file>.job-key]")
		return 2
	}
	key, _, err := bcc.LoadOrCreateJobKey(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "baft-bcc jobkey:", err)
		return 1
	}
	fmt.Println(release.EncodePublic(key.Public().(ed25519.PublicKey)))
	return 0
}
