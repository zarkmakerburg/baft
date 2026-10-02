// Package release signs and verifies BAFT release artifacts.
//
// Trust is two-tier. An offline Ed25519 root key certifies a release signing
// key for a bounded validity window; the release signing key (held by CI)
// signs the release manifest. Servers pin only the root public key, so the
// release key can be rotated or revoked without re-pinning anything.
//
// Every signed document is a DSSE envelope: the signature covers the exact
// payload bytes, so verifiers never re-serialise JSON before checking it.
package release

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	PayloadTypeKeyCert    = "application/vnd.baft.release-key-cert+json"
	PayloadTypeManifest   = "application/vnd.baft.release-manifest+json"
	PayloadTypeRevocation = "application/vnd.baft.release-revocations+json"

	PurposeRelease = "baft-release"
	Product        = "baft"

	ManifestFile = "manifest.json"
	CertFile     = "release-key.cert.json"
	SumsFile     = "SHA256SUMS"

	// MaxCertValidity bounds how long one release key may be trusted.
	MaxCertValidity = 400 * 24 * time.Hour
)

// Artifact names are "<binary>-<os>-<arch>", nothing else.
var artifactName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*-(linux)-(amd64|arm64)$`)

// ---- keys ----

func GenerateKey() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

// KeyID is the first 16 bytes of SHA-256 over the raw public key, hex encoded.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:16])
}

func EncodePublic(k ed25519.PublicKey) string { return base64.RawURLEncoding.EncodeToString(k) }

// EncodePrivate stores only the 32-byte seed.
func EncodePrivate(k ed25519.PrivateKey) string {
	return base64.RawURLEncoding.EncodeToString(k.Seed())
}

func DecodePublic(s string) (ed25519.PublicKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("invalid Ed25519 public key")
	}
	return ed25519.PublicKey(b), nil
}

func DecodePrivate(s string) (ed25519.PrivateKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.SeedSize {
		return nil, errors.New("invalid Ed25519 private key")
	}
	return ed25519.NewKeyFromSeed(b), nil
}

func ReadPublic(path string) (ed25519.PublicKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return DecodePublic(string(b))
}

func ReadPrivate(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return DecodePrivate(string(b))
}

// ---- DSSE envelope ----

type Signature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

type Envelope struct {
	PayloadType string      `json:"payloadType"`
	Payload     string      `json:"payload"`
	Signatures  []Signature `json:"signatures"`
}

// pae is DSSE's pre-authentication encoding.
func pae(payloadType string, payload []byte) []byte {
	return []byte(fmt.Sprintf("DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload))
}

func Sign(payloadType string, payload []byte, key ed25519.PrivateKey) Envelope {
	pub := key.Public().(ed25519.PublicKey)
	return Envelope{
		PayloadType: payloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures: []Signature{{
			KeyID: KeyID(pub),
			Sig:   base64.StdEncoding.EncodeToString(ed25519.Sign(key, pae(payloadType, payload))),
		}},
	}
}

// Open checks the envelope's type and its single signature against pub and
// returns the signed payload bytes.
func (e Envelope) Open(payloadType string, pub ed25519.PublicKey) ([]byte, error) {
	if e.PayloadType != payloadType {
		return nil, fmt.Errorf("payload type %q, want %q", e.PayloadType, payloadType)
	}
	if len(e.Signatures) != 1 {
		return nil, fmt.Errorf("want exactly one signature, got %d", len(e.Signatures))
	}
	s := e.Signatures[0]
	if s.KeyID != KeyID(pub) {
		return nil, fmt.Errorf("signed by key %s, want %s", s.KeyID, KeyID(pub))
	}
	payload, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return nil, fmt.Errorf("payload encoding: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(s.Sig)
	if err != nil {
		return nil, fmt.Errorf("signature encoding: %w", err)
	}
	if !ed25519.Verify(pub, pae(payloadType, payload), sig) {
		return nil, errors.New("signature does not verify")
	}
	return payload, nil
}

func WriteEnvelope(path string, e Envelope) error {
	b, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}

func ReadEnvelope(path string) (Envelope, error) {
	var e Envelope
	b, err := os.ReadFile(path)
	if err != nil {
		return e, err
	}
	return e, strictUnmarshal(b, &e)
}

func strictUnmarshal(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after JSON document")
	}
	return nil
}

// ---- release key certificate ----

type KeyCert struct {
	SchemaVersion int       `json:"schema_version"`
	Purpose       string    `json:"purpose"`
	KeyID         string    `json:"key_id"`
	PublicKey     string    `json:"public_key"`
	NotBefore     time.Time `json:"not_before"`
	NotAfter      time.Time `json:"not_after"`
}

// Certify has the root key vouch for a release key over [notBefore, notAfter].
func Certify(root ed25519.PrivateKey, releasePub ed25519.PublicKey, notBefore, notAfter time.Time) (Envelope, error) {
	if !notAfter.After(notBefore) {
		return Envelope{}, errors.New("not_after must be after not_before")
	}
	if notAfter.Sub(notBefore) > MaxCertValidity {
		return Envelope{}, fmt.Errorf("validity exceeds %s", MaxCertValidity)
	}
	payload, err := json.Marshal(KeyCert{
		SchemaVersion: 1,
		Purpose:       PurposeRelease,
		KeyID:         KeyID(releasePub),
		PublicKey:     EncodePublic(releasePub),
		NotBefore:     notBefore.UTC().Truncate(time.Second),
		NotAfter:      notAfter.UTC().Truncate(time.Second),
	})
	if err != nil {
		return Envelope{}, err
	}
	return Sign(PayloadTypeKeyCert, payload, root), nil
}

// OpenCert verifies a key certificate against the pinned root.
func OpenCert(e Envelope, root ed25519.PublicKey) (KeyCert, ed25519.PublicKey, error) {
	var c KeyCert
	payload, err := e.Open(PayloadTypeKeyCert, root)
	if err != nil {
		return c, nil, fmt.Errorf("release key certificate: %w", err)
	}
	if err := strictUnmarshal(payload, &c); err != nil {
		return c, nil, fmt.Errorf("release key certificate: %w", err)
	}
	if c.SchemaVersion != 1 || c.Purpose != PurposeRelease {
		return c, nil, fmt.Errorf("release key certificate: unsupported schema %d / purpose %q", c.SchemaVersion, c.Purpose)
	}
	pub, err := DecodePublic(c.PublicKey)
	if err != nil {
		return c, nil, fmt.Errorf("release key certificate: %w", err)
	}
	if KeyID(pub) != c.KeyID {
		return c, nil, errors.New("release key certificate: key_id does not match public_key")
	}
	if !c.NotAfter.After(c.NotBefore) || c.NotAfter.Sub(c.NotBefore) > MaxCertValidity {
		return c, nil, errors.New("release key certificate: invalid validity window")
	}
	return c, pub, nil
}

// ---- revocation ----

// MaxRevocationValidity bounds how long one revocation list may be trusted.
// The root re-signs the list (even an unchanged one) before it expires, so a
// stale list cannot be replayed forever to hide a revocation.
const MaxRevocationValidity = 400 * 24 * time.Hour

var keyIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Revocations is the root-signed list of revoked release keys. Sequence grows
// by one with every list the root signs; verifiers remember the highest
// sequence they accepted and refuse older lists.
type Revocations struct {
	SchemaVersion int       `json:"schema_version"`
	Sequence      uint64    `json:"sequence"`
	IssuedAt      time.Time `json:"issued_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	RevokedKeyIDs []string  `json:"revoked_key_ids"`
}

func (r Revocations) validate() error {
	if r.SchemaVersion != 2 {
		return fmt.Errorf("unsupported schema %d", r.SchemaVersion)
	}
	if r.Sequence == 0 {
		return errors.New("sequence must be at least 1")
	}
	if !r.ExpiresAt.After(r.IssuedAt) || r.ExpiresAt.Sub(r.IssuedAt) > MaxRevocationValidity {
		return errors.New("invalid validity window")
	}
	for _, id := range r.RevokedKeyIDs {
		if !keyIDRe.MatchString(id) {
			return fmt.Errorf("malformed key id %q", id)
		}
	}
	return nil
}

func SignRevocations(root ed25519.PrivateKey, sequence uint64, keyIDs []string, issuedAt, expiresAt time.Time) (Envelope, error) {
	ids := append([]string{}, keyIDs...)
	sort.Strings(ids)
	r := Revocations{
		SchemaVersion: 2,
		Sequence:      sequence,
		IssuedAt:      issuedAt.UTC().Truncate(time.Second),
		ExpiresAt:     expiresAt.UTC().Truncate(time.Second),
		RevokedKeyIDs: ids,
	}
	if err := r.validate(); err != nil {
		return Envelope{}, fmt.Errorf("revocation list: %w", err)
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return Envelope{}, err
	}
	return Sign(PayloadTypeRevocation, payload, root), nil
}

// OpenRevocations checks the root signature and the list's shape. It does
// not check expiry; VerifyDir does that against the verification time.
func OpenRevocations(e Envelope, root ed25519.PublicKey) (Revocations, error) {
	var r Revocations
	payload, err := e.Open(PayloadTypeRevocation, root)
	if err != nil {
		return r, fmt.Errorf("revocation list: %w", err)
	}
	if err := strictUnmarshal(payload, &r); err != nil {
		return r, fmt.Errorf("revocation list: %w", err)
	}
	if err := r.validate(); err != nil {
		return r, fmt.Errorf("revocation list: %w", err)
	}
	return r, nil
}

// ---- version ordering ----

type semver struct {
	core [3]uint64
	pre  []string
}

func parseVersion(v string) (semver, error) {
	var s semver
	if !versionRe.MatchString(v) {
		return s, fmt.Errorf("version %q is not vMAJOR.MINOR.PATCH[-pre]", v)
	}
	core, pre, hasPre := strings.Cut(v[1:], "-")
	for i, part := range strings.Split(core, ".") {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return s, fmt.Errorf("version %q: %w", v, err)
		}
		s.core[i] = n
	}
	if hasPre {
		s.pre = strings.Split(pre, ".")
		for _, id := range s.pre {
			if id == "" {
				return s, fmt.Errorf("version %q: empty pre-release identifier", v)
			}
		}
	}
	return s, nil
}

func isNumeric(id string) bool {
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func comparePre(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

// CompareVersions orders two release versions by SemVer 2.0 precedence:
// -1 if a < b, 0 if equal, 1 if a > b.
func CompareVersions(a, b string) (int, error) {
	va, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	vb, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := range va.core {
		if va.core[i] != vb.core[i] {
			if va.core[i] < vb.core[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	// A release without a pre-release part outranks any pre-release of it.
	switch {
	case len(va.pre) == 0 && len(vb.pre) == 0:
		return 0, nil
	case len(va.pre) == 0:
		return 1, nil
	case len(vb.pre) == 0:
		return -1, nil
	}
	for i := 0; i < len(va.pre) && i < len(vb.pre); i++ {
		if c := comparePre(va.pre[i], vb.pre[i]); c != 0 {
			return c, nil
		}
	}
	switch {
	case len(va.pre) < len(vb.pre):
		return -1, nil
	case len(va.pre) > len(vb.pre):
		return 1, nil
	}
	return 0, nil
}

// ---- local trust state ----

// TrustState is what a server remembers between installs: the release it
// last accepted and the highest revocation list sequence it has seen. It
// lets VerifyDir refuse downgrades and replayed revocation lists.
type TrustState struct {
	SchemaVersion      int       `json:"schema_version"`
	Version            string    `json:"version,omitempty"`
	Commit             string    `json:"commit,omitempty"`
	RevocationSequence uint64    `json:"revocation_sequence"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// ReadState loads the trust state; a missing file is a fresh install.
func ReadState(path string) (TrustState, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return TrustState{SchemaVersion: 1}, nil
	}
	if err != nil {
		return TrustState{}, err
	}
	var s TrustState
	if err := strictUnmarshal(b, &s); err != nil {
		return s, fmt.Errorf("trust state %s: %w", path, err)
	}
	if s.SchemaVersion != 1 {
		return s, fmt.Errorf("trust state %s: unsupported schema %d", path, s.SchemaVersion)
	}
	if s.Version != "" {
		if _, err := parseVersion(s.Version); err != nil {
			return s, fmt.Errorf("trust state %s: %w", path, err)
		}
	}
	return s, nil
}

// WriteState replaces the trust state atomically.
func WriteState(path string, s TrustState) error {
	s.SchemaVersion = 1
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".trust-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Advance records an accepted release. The revocation sequence never goes
// back, even when an operator explicitly allowed a downgrade.
func (s TrustState) Advance(v Verified, now time.Time) TrustState {
	s.SchemaVersion = 1
	s.Version, s.Commit = v.Version, v.Commit
	if v.Revocations.Sequence > s.RevocationSequence {
		s.RevocationSequence = v.Revocations.Sequence
	}
	s.UpdatedAt = now.UTC().Truncate(time.Second)
	return s
}

// ---- manifest ----

type Artifact struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Provenance records where and how the artifacts were built. It is part of
// the signed manifest, so it carries the release key's signature.
type Provenance struct {
	Builder    string   `json:"builder"`
	Repository string   `json:"repository,omitempty"`
	Ref        string   `json:"ref,omitempty"`
	Workflow   string   `json:"workflow,omitempty"`
	RunID      string   `json:"run_id,omitempty"`
	RunAttempt string   `json:"run_attempt,omitempty"`
	GoVersion  string   `json:"go_version"`
	BuildFlags []string `json:"build_flags,omitempty"`
}

type Manifest struct {
	SchemaVersion int        `json:"schema_version"`
	Product       string     `json:"product"`
	Version       string     `json:"version"`
	Commit        string     `json:"commit"`
	CreatedAt     time.Time  `json:"created_at"`
	SigningKeyID  string     `json:"signing_key_id"`
	SHA256SUMS    string     `json:"sha256sums_sha256"`
	Artifacts     []Artifact `json:"artifacts"`
	Provenance    Provenance `json:"provenance"`
}

var (
	versionRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
	commitRe  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type SignInput struct {
	Dir        string
	Version    string
	Commit     string
	CreatedAt  time.Time
	Provenance Provenance
	Key        ed25519.PrivateKey
	Cert       Envelope
	// Root, when set, is checked against Cert before anything is written.
	Root ed25519.PublicKey
}

// SignDir hashes every artifact in Dir, writes SHA256SUMS, the signed
// manifest and a copy of the release key certificate.
func SignDir(in SignInput) (Manifest, error) {
	var m Manifest
	if !versionRe.MatchString(in.Version) {
		return m, fmt.Errorf("version %q is not vMAJOR.MINOR.PATCH[-pre]", in.Version)
	}
	if !commitRe.MatchString(in.Commit) {
		return m, fmt.Errorf("commit %q is not a full SHA-1", in.Commit)
	}
	pub := in.Key.Public().(ed25519.PublicKey)
	certID, err := certKeyID(in.Cert, in.Root)
	if err != nil {
		return m, err
	}
	if certID != KeyID(pub) {
		return m, fmt.Errorf("certificate is for key %s, signing key is %s", certID, KeyID(pub))
	}
	arts, err := scanArtifacts(in.Dir)
	if err != nil {
		return m, err
	}
	sums := formatSums(arts)
	if err := os.WriteFile(filepath.Join(in.Dir, SumsFile), sums, 0644); err != nil {
		return m, err
	}
	sumsHash := sha256.Sum256(sums)
	m = Manifest{
		SchemaVersion: 1,
		Product:       Product,
		Version:       in.Version,
		Commit:        in.Commit,
		CreatedAt:     in.CreatedAt.UTC().Truncate(time.Second),
		SigningKeyID:  KeyID(pub),
		SHA256SUMS:    hex.EncodeToString(sumsHash[:]),
		Artifacts:     arts,
		Provenance:    in.Provenance,
	}
	payload, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, err
	}
	if err := WriteEnvelope(filepath.Join(in.Dir, ManifestFile), Sign(PayloadTypeManifest, payload, in.Key)); err != nil {
		return m, err
	}
	return m, WriteEnvelope(filepath.Join(in.Dir, CertFile), in.Cert)
}

func certKeyID(e Envelope, root ed25519.PublicKey) (string, error) {
	if root != nil {
		c, _, err := OpenCert(e, root)
		return c.KeyID, err
	}
	payload, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		return "", fmt.Errorf("release key certificate: %w", err)
	}
	var c KeyCert
	if err := strictUnmarshal(payload, &c); err != nil {
		return "", fmt.Errorf("release key certificate: %w", err)
	}
	return c.KeyID, nil
}

func isMetaFile(name string) bool {
	return name == ManifestFile || name == CertFile || name == SumsFile
}

func scanArtifacts(dir string) ([]Artifact, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var arts []Artifact
	for _, e := range entries {
		if isMetaFile(e.Name()) {
			continue
		}
		if !e.Type().IsRegular() {
			return nil, fmt.Errorf("%s: not a regular file", e.Name())
		}
		parts := artifactName.FindStringSubmatch(e.Name())
		if parts == nil {
			return nil, fmt.Errorf("%s: artifact names must be <binary>-linux-<amd64|arm64>", e.Name())
		}
		size, sum, err := hashFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		arts = append(arts, Artifact{Name: e.Name(), OS: parts[1], Arch: parts[2], Size: size, SHA256: sum})
	}
	if len(arts) == 0 {
		return nil, errors.New("no artifacts found")
	}
	sort.Slice(arts, func(i, j int) bool { return arts[i].Name < arts[j].Name })
	return arts, nil
}

func hashFile(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// formatSums writes the coreutils `sha256sum` format so operators can also
// run `sha256sum -c SHA256SUMS`.
func formatSums(arts []Artifact) []byte {
	var b bytes.Buffer
	for _, a := range arts {
		fmt.Fprintf(&b, "%s  %s\n", a.SHA256, a.Name)
	}
	return b.Bytes()
}

func parseSums(b []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		hash, name, ok := strings.Cut(sc.Text(), "  ")
		if !ok || len(hash) != 64 || name == "" {
			return nil, fmt.Errorf("malformed %s line %q", SumsFile, sc.Text())
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("%s lists %s twice", SumsFile, name)
		}
		out[name] = hash
	}
	return out, sc.Err()
}

// ---- verification ----

type VerifyInput struct {
	Dir  string
	Root ed25519.PublicKey
	// Revocations is the root-signed revocation list. It is required and
	// must not have expired at Now.
	Revocations *Envelope
	// Now is the verification time; zero means time.Now().
	Now time.Time
	// State, when set, is the server's trust state: older revocation lists
	// are refused, and so are older releases unless AllowDowngrade is set.
	State          *TrustState
	AllowDowngrade bool
	// Only, when set, verifies a partial download: exactly these artifacts
	// must be present (with signed hashes); other files are still refused.
	Only []string
}

// Verified is a release that passed VerifyDir, with the revocation list it
// was checked against.
type Verified struct {
	Manifest
	Revocations Revocations
}

// VerifyDir checks the whole release in Dir against the pinned root key:
// certificate, revocation list, manifest signature, signing time,
// SHA256SUMS and every artifact's size and hash, and, with a trust state,
// downgrade and revocation replay. Extra or missing files fail verification.
func VerifyDir(in VerifyInput) (Verified, error) {
	var v Verified
	m := &v.Manifest
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	if in.Revocations == nil {
		return v, errors.New("revocation list: required")
	}
	rev, err := OpenRevocations(*in.Revocations, in.Root)
	if err != nil {
		return v, err
	}
	if !now.Before(rev.ExpiresAt) {
		return v, fmt.Errorf("revocation list: expired at %s; the root holder must re-sign it", rev.ExpiresAt.Format(time.RFC3339))
	}
	if in.State != nil && rev.Sequence < in.State.RevocationSequence {
		return v, fmt.Errorf("revocation list: sequence %d is older than %d already accepted", rev.Sequence, in.State.RevocationSequence)
	}
	v.Revocations = rev
	certEnv, err := ReadEnvelope(filepath.Join(in.Dir, CertFile))
	if err != nil {
		return v, fmt.Errorf("release key certificate: %w", err)
	}
	cert, releasePub, err := OpenCert(certEnv, in.Root)
	if err != nil {
		return v, err
	}
	for _, id := range rev.RevokedKeyIDs {
		if id == cert.KeyID {
			return v, fmt.Errorf("release key %s is revoked", id)
		}
	}
	manEnv, err := ReadEnvelope(filepath.Join(in.Dir, ManifestFile))
	if err != nil {
		return v, fmt.Errorf("manifest: %w", err)
	}
	payload, err := manEnv.Open(PayloadTypeManifest, releasePub)
	if err != nil {
		return v, fmt.Errorf("manifest: %w", err)
	}
	if err := strictUnmarshal(payload, m); err != nil {
		return v, fmt.Errorf("manifest: %w", err)
	}
	if m.SchemaVersion != 1 || m.Product != Product {
		return v, fmt.Errorf("manifest: unsupported schema %d / product %q", m.SchemaVersion, m.Product)
	}
	if !versionRe.MatchString(m.Version) || !commitRe.MatchString(m.Commit) {
		return v, errors.New("manifest: malformed version or commit")
	}
	if m.SigningKeyID != cert.KeyID {
		return v, errors.New("manifest: signing_key_id does not match certificate")
	}
	if m.CreatedAt.Before(cert.NotBefore) || m.CreatedAt.After(cert.NotAfter) {
		return v, fmt.Errorf("manifest: created_at %s is outside the release key's validity %s..%s",
			m.CreatedAt.Format(time.RFC3339), cert.NotBefore.Format(time.RFC3339), cert.NotAfter.Format(time.RFC3339))
	}

	sums, err := os.ReadFile(filepath.Join(in.Dir, SumsFile))
	if err != nil {
		return v, err
	}
	sumsHash := sha256.Sum256(sums)
	if hex.EncodeToString(sumsHash[:]) != m.SHA256SUMS {
		return v, fmt.Errorf("%s does not match the manifest", SumsFile)
	}
	listed, err := parseSums(sums)
	if err != nil {
		return v, err
	}
	if len(listed) != len(m.Artifacts) {
		return v, fmt.Errorf("%s and manifest list different artifacts", SumsFile)
	}
	want := map[string]Artifact{}
	for _, a := range m.Artifacts {
		if listed[a.Name] != a.SHA256 {
			return v, fmt.Errorf("%s: %s and manifest disagree", a.Name, SumsFile)
		}
		want[a.Name] = a
	}
	entries, err := os.ReadDir(in.Dir)
	if err != nil {
		return v, err
	}
	seen := 0
	for _, e := range entries {
		if isMetaFile(e.Name()) {
			continue
		}
		a, ok := want[e.Name()]
		if !ok {
			return v, fmt.Errorf("%s: not part of the signed release", e.Name())
		}
		if !e.Type().IsRegular() {
			return v, fmt.Errorf("%s: not a regular file", e.Name())
		}
		size, sum, err := hashFile(filepath.Join(in.Dir, e.Name()))
		if err != nil {
			return v, err
		}
		if size != a.Size || sum != a.SHA256 {
			return v, fmt.Errorf("%s: hash or size does not match the signed manifest", e.Name())
		}
		seen++
	}
	if len(in.Only) > 0 {
		for _, name := range in.Only {
			if _, ok := want[name]; !ok {
				return v, fmt.Errorf("%s: not in the signed release", name)
			}
			if _, err := os.Stat(filepath.Join(in.Dir, name)); err != nil {
				return v, fmt.Errorf("%s: missing", name)
			}
		}
		if seen != len(in.Only) {
			return v, errors.New("unexpected artifacts in a partial release download")
		}
	} else if seen != len(want) {
		return v, fmt.Errorf("%d signed artifact(s) missing", len(want)-seen)
	}
	if in.State != nil && in.State.Version != "" {
		c, err := CompareVersions(m.Version, in.State.Version)
		if err != nil {
			return v, err
		}
		switch {
		case c == 0 && m.Commit != in.State.Commit:
			return v, fmt.Errorf("release %s is commit %s, but %s was already accepted as commit %s", m.Version, m.Commit, in.State.Version, in.State.Commit)
		case c < 0 && !in.AllowDowngrade:
			return v, fmt.Errorf("release %s is older than the accepted %s (downgrade refused)", m.Version, in.State.Version)
		}
	}
	return v, nil
}
