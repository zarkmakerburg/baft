package securityinternal

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type keyFile struct {
	Version int    `json:"version"`
	Private string `json:"private"`
	Public  string `json:"public"`
}

func SaveKeyPair(path string, k KeyPair) error {
	if !k.Valid() {
		return errors.New("securityinternal: invalid keypair")
	}
	b, err := json.MarshalIndent(keyFile{
		Version: 1,
		Private: base64.RawURLEncoding.EncodeToString(k.Private),
		Public:  base64.RawURLEncoding.EncodeToString(k.Public),
	}, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".noise-key-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
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
	return os.Rename(name, path)
}

func LoadKeyPair(path string) (KeyPair, error) {
	st, err := os.Stat(path)
	if err != nil {
		return KeyPair{}, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return KeyPair{}, errors.New("securityinternal: key file must not be accessible by group/other")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return KeyPair{}, err
	}
	var f keyFile
	if err := json.Unmarshal(b, &f); err != nil {
		return KeyPair{}, err
	}
	if f.Version != 1 {
		return KeyPair{}, errors.New("securityinternal: unsupported key file version")
	}
	priv, err := base64.RawURLEncoding.DecodeString(f.Private)
	if err != nil {
		return KeyPair{}, err
	}
	pub, err := base64.RawURLEncoding.DecodeString(f.Public)
	if err != nil {
		return KeyPair{}, err
	}
	k := KeyPair{Private: priv, Public: pub}
	if !k.Valid() {
		return KeyPair{}, errors.New("securityinternal: invalid stored keypair")
	}
	return k, nil
}
