package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zarkmakerburg/baft/internal/securityinternal"
)

func main() {
	if len(os.Args) < 2 {
		die("usage: baft-pair <keygen|ex-code|ir-apply> ...")
	}
	switch os.Args[1] {
	case "keygen":
		keygen(os.Args[2:])
	case "ex-code":
		exCode(os.Args[2:])
	case "ir-apply":
		irApply(os.Args[2:])
	default:
		die("unknown command")
	}
}

func keygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	path := fs.String("file", "", "key file")
	_ = fs.Parse(args)
	if *path == "" {
		die("--file is required")
	}
	k, err := loadOrCreate(*path)
	if err != nil {
		die(err.Error())
	}
	pub, _ := securityinternal.EncodePublicKey(k.Public)
	fmt.Println(pub)
}

func exCode(args []string) {
	fs := flag.NewFlagSet("ex-code", flag.ExitOnError)
	keyPath := fs.String("key", "", "responder Noise key file")
	address := fs.String("address", "", "dial address")
	serverName := fs.String("server-name", "", "outer TLS server name")
	identity := fs.String("identity", "", "BAFT node identity")
	caFile := fs.String("ca-file", "", "outer TLS CA PEM")
	pskOut := fs.String("psk-out", "", "one-time PSK file")
	ttl := fs.Duration("ttl", 15*time.Minute, "pairing lifetime")
	recordShaping := fs.Bool("record-shaping", false, "enable deterministic bounded record shaping")
	_ = fs.Parse(args)
	if *keyPath == "" || *address == "" || *serverName == "" || *identity == "" || *caFile == "" || *pskOut == "" {
		die("missing required flag")
	}
	k, err := loadOrCreate(*keyPath)
	if err != nil {
		die(err.Error())
	}
	ca, err := os.ReadFile(*caFile)
	if err != nil {
		die(err.Error())
	}
	d, psk, err := securityinternal.NewPairingDescriptor(*address, *serverName, *identity, k.Public, ca, *ttl)
	if err == nil {
		d.RecordShaping = *recordShaping
	}
	if err != nil {
		die(err.Error())
	}
	if err := atomicWrite(*pskOut, []byte(base64.RawURLEncoding.EncodeToString(psk)+"\n"), 0o600); err != nil {
		die(err.Error())
	}
	code, err := d.Encode()
	if err != nil {
		die(err.Error())
	}
	fmt.Println(code)
}

func irApply(args []string) {
	fs := flag.NewFlagSet("ir-apply", flag.ExitOnError)
	code := fs.String("code", "", "BAFTPAIR1 code")
	keyPath := fs.String("key", "", "initiator Noise key file")
	stateDir := fs.String("state-dir", "", "state directory")
	_ = fs.Parse(args)
	if *code == "" || *keyPath == "" || *stateDir == "" {
		die("missing required flag")
	}
	d, responder, psk, ca, err := securityinternal.DecodePairingDescriptor(*code, time.Now())
	if err != nil {
		die(err.Error())
	}
	local, err := loadOrCreate(*keyPath)
	if err != nil {
		die(err.Error())
	}
	localPub, _ := securityinternal.EncodePublicKey(local.Public)
	payload := struct {
		Version          int                                `json:"version"`
		Descriptor       securityinternal.PairingDescriptor `json:"descriptor"`
		LocalPublicKey   string                             `json:"local_public_key"`
		ResponderKey     string                             `json:"responder_key"`
		OneTimePSK       string                             `json:"one_time_psk"`
	}{
		Version:        1,
		Descriptor:     d,
		LocalPublicKey: localPub,
		ResponderKey:   base64.RawURLEncoding.EncodeToString(responder),
		OneTimePSK:     base64.RawURLEncoding.EncodeToString(psk),
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		die(err.Error())
	}
	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		die(err.Error())
	}
	if err := atomicWrite(filepath.Join(*stateDir, "pairing.pending.json"), append(b, '\n'), 0o600); err != nil {
		die(err.Error())
	}
	if err := atomicWrite(filepath.Join(*stateDir, "peer-ca.pem"), ca, 0o600); err != nil {
		die(err.Error())
	}
	fmt.Println(localPub)
}

func loadOrCreate(path string) (securityinternal.KeyPair, error) {
	k, err := securityinternal.LoadKeyPair(path)
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return securityinternal.KeyPair{}, err
	}
	k, err = securityinternal.GenerateKeyPair()
	if err != nil {
		return securityinternal.KeyPair{}, err
	}
	if err := securityinternal.SaveKeyPair(path, k); err != nil {
		return securityinternal.KeyPair{}, err
	}
	return k, nil
}

func atomicWrite(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
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

func die(msg string) {
	fmt.Fprintln(os.Stderr, "baft-pair:", msg)
	os.Exit(1)
}
