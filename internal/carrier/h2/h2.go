package h2

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

const CarrierPath = "/baft/v1/carrier"

type Client struct {
	endpoint string
	hc       *http.Client
}

func NewClient(endpoint string, tlsConfig *tls.Config) (*Client, error) {
	if endpoint == "" || tlsConfig == nil {
		return nil, errors.New("endpoint and TLS config are required")
	}
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:   true,
		TLSClientConfig:     tlsConfig.Clone(),
		TLSHandshakeTimeout: 10 * time.Second,
		DisableCompression:  true,
		MaxConnsPerHost:     1,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		endpoint: endpoint,
		hc: &http.Client{
			Transport: tr,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("redirects are forbidden for BAFT carrier")
			},
		},
	}, nil
}

// Open opens one long-lived bidirectional HTTP/2 request. Each Client owns one
// Transport, so callers should create one Client per BAFT Shard to guarantee an
// independent TCP/TLS connection pool.
func (c *Client) Open(ctx context.Context, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+CarrierPath, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.ProtoMajor != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("carrier requires HTTP/2, got %s", resp.Proto)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("carrier rejected: HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func (c *Client) CloseIdleConnections() {
	c.hc.CloseIdleConnections()
}

type PeerInfo struct {
	Identity   string
	RemoteAddr string
}

type StreamHandler func(context.Context, io.Reader, io.Writer, PeerInfo) error

func Handler(stream StreamHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != CarrierPath {
			http.NotFound(w, r)
			return
		}
		if r.ProtoMajor != 2 {
			http.Error(w, "HTTP/2 required", http.StatusHTTPVersionNotSupported)
			return
		}
		if r.Header.Get("Content-Type") != "application/octet-stream" {
			http.Error(w, "invalid content type", http.StatusUnsupportedMediaType)
			return
		}
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
			http.Error(w, "mTLS required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if stream == nil {
			return
		}
		leaf := r.TLS.PeerCertificates[0]
		if len(leaf.URIs) != 1 {
			return
		}
		peer := PeerInfo{Identity: leaf.URIs[0].String(), RemoteAddr: r.RemoteAddr}
		out := &flushingWriter{w: w}
		if err := stream(r.Context(), r.Body, out, peer); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
			return
		}
	})
}

type flushingWriter struct {
	w http.ResponseWriter
}

func (w *flushingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if f, ok := w.w.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}
