package h2

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/zarkmakerburg/baft/internal/identity"
)

type netConnKey string

func withConnCapture(ctx context.Context, capture func(netConnKey)) context.Context {
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			capture(netConnKey(fmt.Sprintf("%p", info.Conn)))
		},
	}
	return httptrace.WithClientTrace(ctx, trace)
}

type pkiFixture struct {
	roots       *x509.CertPool
	serverCert  tls.Certificate
	clientCert  tls.Certificate
	wrongCert   tls.Certificate
	expiredCert tls.Certificate
}

func makePKI(t *testing.T) pkiFixture {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Minute)
	caT := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "BAFT Test CA"}, NotBefore: now, NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caT, caT, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)

	issue := func(serial int64, dns string, uri string, usages []x509.ExtKeyUsage, notBefore, notAfter time.Time) tls.Certificate {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		certT := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "test"}, NotBefore: notBefore, NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages}
		if dns != "" {
			certT.DNSNames = []string{dns}
		}
		if uri != "" {
			u, err := url.Parse(uri)
			if err != nil {
				t.Fatal(err)
			}
			certT.URIs = []*url.URL{u}
		}
		der, err := x509.CreateCertificate(rand.Reader, certT, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		return pair
	}

	return pkiFixture{
		roots:       roots,
		serverCert:  issue(2, "ex.test", "", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, now, now.Add(time.Hour)),
		clientCert:  issue(3, "", "urn:baft:node:ir-01", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now, now.Add(time.Hour)),
		wrongCert:   issue(4, "", "urn:baft:node:not-allowed", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now, now.Add(time.Hour)),
		expiredCert: issue(5, "", "urn:baft:node:ir-01", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, now.Add(-2*time.Hour), now.Add(-time.Hour)),
	}
}

func startServer(t *testing.T, p pkiFixture, stream StreamHandler) *httptest.Server {
	t.Helper()
	tlsCfg, err := identity.ServerTLS(p.roots, p.serverCert, map[string]struct{}{"urn:baft:node:ir-01": {}})
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewUnstartedServer(Handler(stream))
	s.EnableHTTP2 = true
	s.TLS = tlsCfg
	s.StartTLS()
	t.Cleanup(s.Close)
	return s
}

func clientFor(t *testing.T, p pkiFixture, endpoint string, cert tls.Certificate) *Client {
	t.Helper()
	tlsCfg, err := identity.ClientTLS(p.roots, cert, "ex.test")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(endpoint, tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func TestFullDuplexH2MTLS(t *testing.T) {
	p := makePKI(t)
	srv := startServer(t, p, func(ctx context.Context, r io.Reader, w io.Writer, peer PeerInfo) error {
		buf := make([]byte, 64)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return werr
				}
			}
			if err != nil {
				return err
			}
		}
	})
	c := clientFor(t, p, srv.URL, p.clientCert)
	pr, pw := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	type result struct {
		resp *http.Response
		err  error
	}
	ch := make(chan result, 1)
	go func() { resp, err := c.Open(ctx, pr); ch <- result{resp, err} }()

	var res result
	select {
	case res = <-ch:
	case <-time.After(time.Second):
		t.Fatal("response headers were not flushed before request body completed")
	}
	if res.err != nil {
		t.Fatal(res.err)
	}
	defer res.resp.Body.Close()
	if res.resp.ProtoMajor != 2 {
		t.Fatalf("expected h2, got %s", res.resp.Proto)
	}

	payload := []byte("baft-full-duplex")
	go func() { _, _ = pw.Write(payload) }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(res.resp.Body, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("got %q", got)
	}
	_ = pw.Close()
}

func TestUnauthorizedPeerIsRejected(t *testing.T) {
	p := makePKI(t)
	srv := startServer(t, p, nil)
	c := clientFor(t, p, srv.URL, p.wrongCert)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Open(ctx, http.NoBody)
	if err == nil {
		t.Fatal("expected unauthorized peer to fail")
	}
}

func TestExpiredClientCertificateRejected(t *testing.T) {
	p := makePKI(t)
	srv := startServer(t, p, nil)
	c := clientFor(t, p, srv.URL, p.expiredCert)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Open(ctx, http.NoBody); err == nil {
		t.Fatal("expected expired client certificate to fail")
	}
}

func TestServerNameMismatchRejected(t *testing.T) {
	p := makePKI(t)
	srv := startServer(t, p, nil)
	tlsCfg, err := identity.ClientTLS(p.roots, p.clientCert, "wrong.test")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(srv.URL, tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Open(ctx, http.NoBody); err == nil {
		t.Fatal("expected SAN/server_name mismatch to fail")
	}
}

func TestWrongServerCARejected(t *testing.T) {
	p := makePKI(t)
	srv := startServer(t, p, nil)
	emptyRoots := x509.NewCertPool()
	tlsCfg, err := identity.ClientTLS(emptyRoots, p.clientCert, "ex.test")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(srv.URL, tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Open(ctx, http.NoBody); err == nil {
		t.Fatal("expected untrusted server CA to fail")
	}
}

func TestCancellationUnblocksCarrier(t *testing.T) {
	p := makePKI(t)
	serverDone := make(chan struct{})
	srv := startServer(t, p, func(ctx context.Context, r io.Reader, w io.Writer, peer PeerInfo) error {
		defer close(serverDone)
		<-ctx.Done()
		return ctx.Err()
	})
	c := clientFor(t, p, srv.URL, p.clientCert)
	pr, _ := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	resp, err := c.Open(ctx, pr)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = resp.Body.Close()
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("server handler did not observe cancellation")
	}
}

func TestFourClientsUseIndependentConnections(t *testing.T) {
	p := makePKI(t)
	release := make(chan struct{})
	srv := startServer(t, p, func(ctx context.Context, r io.Reader, w io.Writer, peer PeerInfo) error {
		<-release
		return nil
	})

	var connsMu sync.Mutex
	connPtrs := map[netConnKey]struct{}{}
	type openResult struct {
		resp *http.Response
		pw   *io.PipeWriter
		err  error
	}
	results := make(chan openResult, 4)
	for i := 0; i < 4; i++ {
		tlsCfg, err := identity.ClientTLS(p.roots, p.clientCert, "ex.test")
		if err != nil {
			t.Fatal(err)
		}
		tr := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, TLSClientConfig: tlsCfg, DisableCompression: true, MaxConnsPerHost: 1, MaxIdleConnsPerHost: 1}
		hc := &http.Client{Transport: tr}
		pr, pw := io.Pipe()
		req, err := http.NewRequest(http.MethodPost, srv.URL+CarrierPath, pr)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req = req.WithContext(withConnCapture(req.Context(), func(k netConnKey) {
			connsMu.Lock()
			connPtrs[k] = struct{}{}
			connsMu.Unlock()
		}))
		go func(pw *io.PipeWriter, req *http.Request) {
			resp, err := hc.Do(req)
			results <- openResult{resp: resp, pw: pw, err: err}
		}(pw, req)
	}

	opened := make([]openResult, 0, 4)
	for i := 0; i < 4; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		opened = append(opened, r)
	}
	connsMu.Lock()
	count := len(connPtrs)
	connsMu.Unlock()
	if count != 4 {
		t.Fatalf("expected 4 independent TCP connections, observed %d", count)
	}
	close(release)
	for _, r := range opened {
		_ = r.pw.Close()
		_ = r.resp.Body.Close()
	}
}

func TestActiveIdentityRevocationTerminatesExistingCarrier(t *testing.T) {
	p := makePKI(t)
	revocations := identity.NewRevocationSet()
	serverDone := make(chan error, 1)

	tlsCfg, err := identity.ServerTLS(p.roots, p.serverCert, map[string]struct{}{"urn:baft:node:ir-01": {}})
	if err != nil { t.Fatal(err) }
	srv := httptest.NewUnstartedServer(HandlerWithRevocation(func(ctx context.Context, r io.Reader, w io.Writer, peer PeerInfo) error {
		<-ctx.Done()
		serverDone <- ctx.Err()
		return ctx.Err()
	}, revocations))
	srv.EnableHTTP2 = true
	srv.TLS = tlsCfg
	srv.StartTLS()
	defer srv.Close()

	c := clientFor(t, p, srv.URL, p.clientCert)
	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := c.Open(ctx, pr)
	if err != nil { t.Fatal(err) }
	defer resp.Body.Close()

	revocations.RevokeIdentity("urn:baft:node:ir-01")
	select {
	case err := <-serverDone:
		if err == nil { t.Fatal("expected revoked carrier context to be cancelled") }
	case <-time.After(time.Second):
		t.Fatal("active carrier did not terminate after peer revocation")
	}
}

func TestAlreadyRevokedPeerRejectedBeforeCarrierStarts(t *testing.T) {
	p := makePKI(t)
	revocations := identity.NewRevocationSet()
	revocations.RevokeSerial("3")
	started := make(chan struct{}, 1)

	tlsCfg, err := identity.ServerTLS(p.roots, p.serverCert, map[string]struct{}{"urn:baft:node:ir-01": {}})
	if err != nil { t.Fatal(err) }
	srv := httptest.NewUnstartedServer(HandlerWithRevocation(func(ctx context.Context, r io.Reader, w io.Writer, peer PeerInfo) error {
		started <- struct{}{}
		return nil
	}, revocations))
	srv.EnableHTTP2 = true
	srv.TLS = tlsCfg
	srv.StartTLS()
	defer srv.Close()

	c := clientFor(t, p, srv.URL, p.clientCert)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Open(ctx, http.NoBody); err == nil {
		t.Fatal("expected pre-revoked peer to be rejected")
	}
	select {
	case <-started:
		t.Fatal("stream handler must not start for revoked peer")
	default:
	}
}
