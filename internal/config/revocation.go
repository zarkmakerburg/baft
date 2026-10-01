package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Revocation points a listener at an operator-maintained revocation list.
// Only listeners enforce revocation, because only they authenticate the
// peer of every carrier.
type Revocation struct {
	File string `json:"file"`
}

// RevocationList is the content of revocation.file. Applying it is add-only:
// a reload revokes new entries immediately (cancelling established carriers),
// while removing an entry takes effect on the next process start.
type RevocationList struct {
	Identities   []string `json:"identities,omitempty"`
	Serials      []string `json:"serials,omitempty"`
	Fingerprints []string `json:"fingerprints,omitempty"`
}

const maxRevocationEntries = 4096

func validateRevocation(c Config) error {
	if c.Revocation == nil {
		return nil
	}
	if c.Node.Role != "listener" {
		return errors.New("revocation is only enforced by listeners")
	}
	if c.Revocation.File == "" || !filepath.IsAbs(c.Revocation.File) {
		return errors.New("revocation.file must be an absolute path")
	}
	return nil
}

// LoadRevocationFile reads and strictly validates a revocation list (.yaml,
// .yml or .json). An empty file is an empty list.
func LoadRevocationFile(path string) (RevocationList, error) {
	st, err := os.Stat(path)
	if err != nil {
		return RevocationList{}, err
	}
	if !st.Mode().IsRegular() {
		return RevocationList{}, errors.New("revocation path is not a regular file")
	}
	if st.Size() > maxConfigFileBytes {
		return RevocationList{}, errors.New("revocation file exceeds 1 MiB limit")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return RevocationList{}, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return RevocationList{}, nil
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		if b, err = yamlDocumentJSON(b); err != nil {
			return RevocationList{}, err
		}
	case ".json":
	default:
		return RevocationList{}, fmt.Errorf("unsupported revocation file extension: %s", filepath.Ext(path))
	}
	var l RevocationList
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return RevocationList{}, err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return RevocationList{}, errors.New("revocation file must contain exactly one document")
	}
	if err := l.Validate(); err != nil {
		return RevocationList{}, err
	}
	return l, nil
}

func (l RevocationList) Validate() error {
	if len(l.Identities)+len(l.Serials)+len(l.Fingerprints) > maxRevocationEntries {
		return fmt.Errorf("revocation list exceeds %d entries", maxRevocationEntries)
	}
	for _, id := range l.Identities {
		node := strings.TrimPrefix(id, "urn:baft:node:")
		if node == id || node == "" || len(node) > 64 {
			return fmt.Errorf("revoked identity %q must be urn:baft:node:<1..64 bytes>", id)
		}
	}
	for _, s := range l.Serials {
		if h := revocationHex(s); h == "" || len(h) > 40 || !isHex(h) {
			return fmt.Errorf("revoked serial %q must be 1..20 bytes of hex", s)
		}
	}
	for _, f := range l.Fingerprints {
		if h := revocationHex(f); len(h) != 64 || !isHex(h) {
			return fmt.Errorf("revoked fingerprint %q must be a SHA-256 hex digest", f)
		}
	}
	return nil
}

func revocationHex(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), ":", "")
}

func isHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
