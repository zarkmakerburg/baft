package clustersync

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"
)

func GenerateWorkerKeyPair() (*ecdh.PrivateKey, *ecdh.PublicKey, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil { return nil, nil, err }
	return priv, priv.PublicKey(), nil
}

func GenerateSigningKeyPair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	return pub, priv, err
}

func EncodeWorkerPrivate(k *ecdh.PrivateKey) string { return base64.RawURLEncoding.EncodeToString(k.Bytes()) }
func EncodeWorkerPublic(k *ecdh.PublicKey) string { return base64.RawURLEncoding.EncodeToString(k.Bytes()) }
func EncodeSigningPrivate(k ed25519.PrivateKey) string { return base64.RawURLEncoding.EncodeToString(k) }
func EncodeSigningPublic(k ed25519.PublicKey) string { return base64.RawURLEncoding.EncodeToString(k) }

func DecodeWorkerPrivate(s string) (*ecdh.PrivateKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil { return nil, err }
	return ecdh.X25519().NewPrivateKey(b)
}

func DecodeWorkerPublic(s string) (*ecdh.PublicKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil { return nil, err }
	return ecdh.X25519().NewPublicKey(b)
}

func DecodeSigningPrivate(s string) (ed25519.PrivateKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PrivateKeySize { return nil, errors.New("invalid Ed25519 private key") }
	return ed25519.PrivateKey(b), nil
}

func DecodeSigningPublic(s string) (ed25519.PublicKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize { return nil, errors.New("invalid Ed25519 public key") }
	return ed25519.PublicKey(b), nil
}

func ReadWorkerPrivate(path string) (*ecdh.PrivateKey, error) {
	b, err := os.ReadFile(path); if err != nil { return nil, err }
	return DecodeWorkerPrivate(string(b))
}

func ReadWorkerPublic(path string) (*ecdh.PublicKey, error) {
	b, err := os.ReadFile(path); if err != nil { return nil, err }
	return DecodeWorkerPublic(string(b))
}

func ReadSigningPrivate(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path); if err != nil { return nil, err }
	return DecodeSigningPrivate(string(b))
}

func ReadSigningPublic(path string) (ed25519.PublicKey, error) {
	b, err := os.ReadFile(path); if err != nil { return nil, err }
	return DecodeSigningPublic(string(b))
}
