package securityinternal

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"
)

func TestIKPSK0PairThenPinnedIK(t *testing.T) {
	initKey, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	respKey, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	desc, psk, err := NewPairingDescriptor(
		"127.0.0.1:8443",
		"127.0.0.1",
		"urn:baft:node:ex-test",
		respKey.Public,
		[]byte("test-ca"),
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	code, err := desc.Encode()
	if err != nil {
		t.Fatal(err)
	}
	_, gotPub, gotPSK, _, err := DecodePairingDescriptor(code, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPub, respKey.Public) || !bytes.Equal(gotPSK, psk) {
		t.Fatal("pairing descriptor changed key material")
	}

	learned := runDuplex(t,
		HandshakeConfig{Static: initKey, PeerStatic: respKey.Public, OneTimePairingPSK: psk},
		HandshakeConfig{Static: respKey, OneTimePairingPSK: psk},
	)
	if !bytes.Equal(learned, initKey.Public) {
		t.Fatal("responder learned wrong initiator static key")
	}

	runDuplex(t,
		HandshakeConfig{Static: initKey, PeerStatic: respKey.Public},
		HandshakeConfig{Static: respKey, PeerStatic: initKey.Public},
	)
}

func runDuplex(t *testing.T, initCfg, respCfg HandshakeConfig) []byte {
	t.Helper()

	reqR, reqW := io.Pipe()
	respR, respW := io.Pipe()

	var wg sync.WaitGroup
	var learned []byte
	var serverErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		server, peer, err := Responder(reqR, respW, respCfg)
		if err != nil {
			serverErr = err
			_ = reqR.CloseWithError(err)
			_ = respW.CloseWithError(err)
			return
		}
		learned = append([]byte(nil), peer...)
		buf := make([]byte, 64)
		n, err := server.Read(buf)
		if err != nil {
			serverErr = err
			return
		}
		_, serverErr = server.Write(append([]byte("ack:"), buf[:n]...))
	}()

	client, err := Initiator(respR, reqW, initCfg)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("first-byte")
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 64)
	n, err := client.Read(out)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out[:n]), "ack:first-byte"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	_ = reqW.Close()
	_ = respR.Close()
	wg.Wait()
	if serverErr != nil {
		t.Fatal(serverErr)
	}
	return learned
}
