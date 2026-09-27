package identity

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

func LoadCertPool(path string) (*x509.CertPool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, errors.New("no CA certificate found")
	}
	return pool, nil
}

func LoadKeyPair(certFile, keyFile string) (tls.Certificate, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	if block, _ := pem.Decode(keyPEM); block == nil {
		return tls.Certificate{}, errors.New("invalid private key PEM")
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, err
	}
	return cert, nil
}

func ClientTLS(ca *x509.CertPool, cert tls.Certificate, serverName string) (*tls.Config, error) {
	if ca == nil || serverName == "" {
		return nil, errors.New("CA pool and server name are required")
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		RootCAs:            ca,
		Certificates:       []tls.Certificate{cert},
		ServerName:         serverName,
		InsecureSkipVerify: false,
		NextProtos:         []string{"h2"},
		ClientSessionCache: nil,
	}, nil
}

func ServerTLS(ca *x509.CertPool, cert tls.Certificate, allowedURIs map[string]struct{}) (*tls.Config, error) {
	if ca == nil || len(allowedURIs) == 0 {
		return nil, errors.New("client CA pool and peer allowlist are required")
	}
	cfg := &tls.Config{
		MinVersion:             tls.VersionTLS13,
		ClientAuth:             tls.RequireAndVerifyClientCert,
		ClientCAs:              ca,
		Certificates:           []tls.Certificate{cert},
		NextProtos:             []string{"h2"},
		SessionTicketsDisabled: true,
	}
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.VerifiedChains) == 0 || len(cs.PeerCertificates) == 0 {
			return errors.New("peer certificate was not verified")
		}
		leaf := cs.PeerCertificates[0]
		if len(leaf.URIs) != 1 {
			return errors.New("peer certificate must contain exactly one URI SAN identity")
		}
		if _, ok := allowedURIs[leaf.URIs[0].String()]; ok {
			return nil
		}
		return fmt.Errorf("peer identity is not authorized")
	}
	return cfg, nil
}
