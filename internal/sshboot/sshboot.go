// Package sshboot installs baft-agent on a fresh server over SSH.
//
// Rules it enforces:
//   - the server's host key must match a SHA256 fingerprint the operator
//     already confirmed (no trust-on-first-use inside Bootstrap);
//   - SSH credentials live only in the Request value for the duration of the
//     call: they are never written to disk, logged or put in an error;
//   - nothing secret is on the remote command line; the agent token and the
//     BCC job key travel on the script's standard input.
package sshboot

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	dialTimeout    = 15 * time.Second
	maxOutputBytes = 64 << 10
	// RunTimeout bounds one whole install (download, verify, systemd start).
	RunTimeout = 10 * time.Minute
)

var (
	hostRE   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.:-]{0,251}[A-Za-z0-9])?$`)
	userRE   = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)
	nodeIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// Target is the SSH endpoint.
type Target struct {
	Host string
	Port int
	User string
}

// Auth is one set of credentials. Set Password or PrivateKey (PEM text).
type Auth struct {
	Password   string
	PrivateKey string
	Passphrase string
}

// Request is everything Bootstrap needs.
type Request struct {
	Target
	Auth
	// HostKeyFingerprint is the pinned "SHA256:..." fingerprint.
	HostKeyFingerprint string
	// InstallScript is install.sh as shipped with this BCC.
	InstallScript []byte
	BCCURL        string
	NodeID        string
	AgentToken    string
	BCCJobKey     string
	// AllowHTTP passes BAFT_AGENT_ALLOW_HTTP=1 (lab setups only).
	AllowHTTP bool
}

// Result is what the operator sees after a run.
type Result struct {
	Output string
}

func (t Target) addr() string { return net.JoinHostPort(t.Host, strconv.Itoa(t.Port)) }

// ValidateTarget checks the user-supplied SSH endpoint.
func ValidateTarget(t Target) error {
	if !hostRE.MatchString(t.Host) {
		return errors.New("invalid host")
	}
	if t.Port < 1 || t.Port > 65535 {
		return errors.New("invalid port")
	}
	if t.User != "" && !userRE.MatchString(t.User) {
		return errors.New("invalid ssh user")
	}
	return nil
}

// Fingerprint renders a host key the way `ssh-keygen -l` does.
func Fingerprint(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }

// ScanHostKey connects only to read the server's host key and returns its
// fingerprint and type. The operator confirms it out of band before Bootstrap.
func ScanHostKey(ctx context.Context, t Target) (fingerprint, keyType string, err error) {
	if err := ValidateTarget(t); err != nil {
		return "", "", err
	}
	var got ssh.PublicKey
	stop := errors.New("host key captured")
	cfg := &ssh.ClientConfig{
		User: "baft-hostkey-scan",
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			got = k
			return stop
		},
		Timeout: dialTimeout,
	}
	_, derr := dial(ctx, t.addr(), cfg)
	if got == nil {
		if derr != nil {
			return "", "", fmt.Errorf("could not read the host key: %w", derr)
		}
		return "", "", errors.New("server sent no host key")
	}
	return Fingerprint(got), got.Type(), nil
}

// Bootstrap installs baft-agent on the target. The returned output has the
// agent token and job key removed.
func Bootstrap(ctx context.Context, req Request) (Result, error) {
	if err := validate(req); err != nil {
		return Result{}, err
	}
	methods, err := authMethods(req.Auth)
	if err != nil {
		return Result{}, err
	}
	pinned := req.HostKeyFingerprint
	cfg := &ssh.ClientConfig{
		User: req.User,
		Auth: methods,
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			got := Fingerprint(k)
			if subtle.ConstantTimeCompare([]byte(got), []byte(pinned)) != 1 {
				return fmt.Errorf("host key mismatch: server presented %s, expected %s", got, pinned)
			}
			return nil
		},
		HostKeyAlgorithms: nil,
		Timeout:           dialTimeout,
	}
	ctx, cancel := context.WithTimeout(ctx, RunTimeout)
	defer cancel()
	client, err := dial(ctx, req.addr(), cfg)
	if err != nil {
		return Result{}, scrub(err, req)
	}
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return Result{}, scrub(err, req)
	}
	defer sess.Close()

	var out limitedBuffer
	sess.Stdout, sess.Stderr = &out, &out
	sess.Stdin = bytes.NewReader(buildScript(req))
	done := make(chan error, 1)
	go func() { done <- sess.Run(remoteCommand(req.User)) }()
	select {
	case err = <-done:
	case <-ctx.Done():
		client.Close()
		err = ctx.Err()
	}
	res := Result{Output: redact(out.String(), req)}
	if err != nil {
		return res, fmt.Errorf("install failed: %w", scrub(err, req))
	}
	return res, nil
}

func validate(req Request) error {
	if err := ValidateTarget(req.Target); err != nil {
		return err
	}
	if req.User == "" {
		return errors.New("ssh user is required")
	}
	if !strings.HasPrefix(req.HostKeyFingerprint, "SHA256:") {
		return errors.New("a confirmed SHA256 host key fingerprint is required")
	}
	if len(req.InstallScript) == 0 {
		return errors.New("install script is not configured")
	}
	if !nodeIDRE.MatchString(req.NodeID) {
		return errors.New("invalid node id")
	}
	if !strings.HasPrefix(req.BCCURL, "https://") && !(req.AllowHTTP && strings.HasPrefix(req.BCCURL, "http://")) {
		return errors.New("bcc url must be https://")
	}
	if strings.ContainsAny(req.BCCURL, "'\n\r\x00") {
		return errors.New("invalid bcc url")
	}
	if req.AgentToken == "" || req.BCCJobKey == "" {
		return errors.New("agent token and bcc job key are required")
	}
	if strings.ContainsAny(req.AgentToken+req.BCCJobKey, "'\n\r\x00") {
		return errors.New("agent token or job key has forbidden characters")
	}
	return nil
}

func authMethods(a Auth) ([]ssh.AuthMethod, error) {
	switch {
	case a.PrivateKey != "" && a.Password != "":
		return nil, errors.New("give a password or a private key, not both")
	case a.PrivateKey != "":
		var signer ssh.Signer
		var err error
		if a.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(a.PrivateKey), []byte(a.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(a.PrivateKey))
		}
		if err != nil {
			// The library's message never contains key material, but keep ours generic.
			return nil, errors.New("private key could not be read (wrong passphrase or unsupported format)")
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	case a.Password != "":
		pw := a.Password
		return []ssh.AuthMethod{
			ssh.Password(pw),
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				ans := make([]string, len(qs))
				for i := range ans {
					ans[i] = pw
				}
				return ans, nil
			}),
		}, nil
	}
	return nil, errors.New("a password or a private key is required")
}

func dial(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	d := net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(dialTimeout * 2))
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(c, chans, reqs), nil
}

// remoteCommand runs the script as root, via passwordless sudo otherwise.
// It contains no secret.
func remoteCommand(user string) string {
	if user == "root" {
		return "bash -s"
	}
	return `if [ "$(id -u)" = 0 ]; then exec bash -s; else exec sudo -n bash -s; fi`
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// buildScript puts the secrets in exported variables ahead of install.sh so
// they reach it on stdin, not through the process list.
func buildScript(req Request) []byte {
	var b bytes.Buffer
	b.WriteString("set -eu\n")
	fmt.Fprintf(&b, "export BAFT_AGENT_TOKEN=%s\n", shQuote(req.AgentToken))
	fmt.Fprintf(&b, "export BAFT_BCC_JOB_KEY=%s\n", shQuote(req.BCCJobKey))
	if req.AllowHTTP {
		b.WriteString("export BAFT_AGENT_ALLOW_HTTP=1\n")
	}
	// install.sh reads its own arguments; hand it over as a function so the
	// script text stays exactly what the operator shipped.
	fmt.Fprintf(&b, "set -- --agent-only --bcc-url %s --node-id %s\n", shQuote(req.BCCURL), shQuote(req.NodeID))
	b.WriteString(`bash -s -- "$@" <<'BAFT_INSTALL_SCRIPT_EOF'` + "\n")
	b.Write(req.InstallScript)
	if !bytes.HasSuffix(req.InstallScript, []byte("\n")) {
		b.WriteByte('\n')
	}
	b.WriteString("BAFT_INSTALL_SCRIPT_EOF\n")
	return b.Bytes()
}

func redact(s string, req Request) string {
	for _, secret := range []string{req.AgentToken, req.BCCJobKey, req.Password, req.PrivateKey, req.Passphrase} {
		if len(secret) >= 4 {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}

func scrub(err error, req Request) error {
	return errors.New(redact(err.Error(), req))
}

type limitedBuffer struct{ b bytes.Buffer }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxOutputBytes - l.b.Len(); room > 0 {
		if len(p) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return l.b.String() }
