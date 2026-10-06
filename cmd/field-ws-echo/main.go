package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"github.com/zarkmakerburg/baft/internal/carrier/ws"
	"github.com/zarkmakerburg/baft/internal/config"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	file := flag.String("config", "/etc/baft/baft.yaml", "existing config")
	endpoint := flag.String("endpoint", "", "WSS test endpoint")
	listen := flag.String("listen", "", "temporary server address")
	peer := flag.String("peer", "", "authorized test peer IP")
	flag.Parse()
	c, err := config.LoadFile(*file)
	must(err)
	if *listen != "" {
		slots := make(chan struct{}, 4)
		srv := &http.Server{Addr: *listen, ReadHeaderTimeout: 3 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}, TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){}}
		srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			if host != *peer && host != "127.0.0.1" {
				http.NotFound(w, r)
				return
			}
			if r.URL.Path != ws.CarrierPath || !ws.IsUpgrade(r) {
				http.NotFound(w, r)
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				http.Error(w, "busy", 503)
				return
			}
			defer func() { <-slots }()
			conn, err := ws.Upgrade(w, r)
			if err != nil {
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			b := make([]byte, 98)
			n, err := io.ReadFull(conn, b)
			fmt.Printf("echo_received=%d error_type=%T\n", n, err)
			if err == nil {
				_, err = conn.Write(b)
				fmt.Printf("echo_sent=%t\n", err == nil)
			}
		})
		must(srv.ListenAndServeTLS(c.TLS.CertFile, c.TLS.KeyFile))
		return
	}
	if *endpoint == "" {
		panic("endpoint required")
	}
	data, err := os.ReadFile(c.TLS.CAFile)
	must(err)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		panic("invalid CA")
	}
	d := ws.Dialer{Endpoint: *endpoint, TLSConfig: &tls.Config{RootCAs: roots, ServerName: c.Peer.ServerName, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	conn, err := d.Dial(ctx)
	if err != nil {
		fmt.Printf("upgrade=false error_type=%T\n", err)
		os.Exit(1)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	b := bytes.Repeat([]byte{0x42}, 98)
	n, err := conn.Write(b)
	fmt.Printf("upgrade=true sent=%d error_type=%T\n", n, err)
	if err != nil {
		os.Exit(1)
	}
	got := make([]byte, len(b))
	n, err = io.ReadFull(conn, got)
	ok := err == nil && bytes.Equal(got, b)
	fmt.Printf("echo_match=%t received=%d error_type=%T\n", ok, n, err)
	if !ok {
		os.Exit(1)
	}
}
func must(err error) {
	if err != nil {
		panic(fmt.Sprintf("setup error type=%T", err))
	}
}
