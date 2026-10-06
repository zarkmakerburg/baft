package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/zarkmakerburg/baft/internal/carrier/ws"
	"github.com/zarkmakerburg/baft/internal/config"
	"github.com/zarkmakerburg/baft/internal/securityinternal"
	"golang.org/x/sys/unix"
)

func main() {
	file := flag.String("config", "/etc/baft/baft.yaml", "existing dialer config")
	endpoint := flag.String("endpoint", "", "direct WSS endpoint")
	flag.Parse()
	if *endpoint == "" {
		panic("endpoint required")
	}
	c, err := config.LoadFile(*file)
	must(err)
	key, err := securityinternal.LoadKeyPair(c.Noise.KeyFile)
	must(err)
	peer, err := securityinternal.DecodePublicKey(c.Noise.PeerPublicKey)
	must(err)
	ca, err := os.ReadFile(c.TLS.CAFile)
	must(err)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		panic("invalid CA")
	}
	for _, mode := range []string{"rbuf16k", "rbuf64k", "rbuf1m"} {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		dialer := &net.Dialer{Timeout: 8 * time.Second}
		if mode != "baseline" && mode != "nagle" {
			dialer.Control = func(network, address string, raw syscall.RawConn) error {
				var optionErr error
				err := raw.Control(func(fd uintptr) {

					size := 0
					switch mode {
					case "rbuf16k":
						size = 16 << 10
					case "rbuf64k":
						size = 64 << 10
					case "rbuf1m":
						size = 1 << 20
					}
					if size != 0 {
						optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF, size)
					}

					if mode == "cubic" || mode == "cubic-mss-lowat" {
						optionErr = unix.SetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION, "cubic")
					}
					if optionErr == nil && (mode == "mss1200" || mode == "cubic-mss-lowat") {
						optionErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_MAXSEG, 1200)
					}
					if optionErr == nil && mode == "cubic-mss-lowat" {
						optionErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT, 1)
					}
				})
				if err != nil {
					return err
				}
				return optionErr
			}
		}
		d := &ws.Dialer{Endpoint: *endpoint, TLSConfig: &tls.Config{RootCAs: roots, ServerName: c.Peer.ServerName, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}}
		d.NetDial = func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, address)
			if err == nil && mode == "nagle" {
				err = conn.(*net.TCPConn).SetNoDelay(false)
				if err != nil {
					conn.Close()
					return nil, err
				}
			}
			return conn, err
		}
		start := time.Now()
		conn, _, err := d.OpenNoise(ctx, securityinternal.HandshakeConfig{Static: key, PeerStatic: peer, RecordShaping: c.Noise.RecordShaping})
		if conn != nil {
			conn.Close()
		}
		cancel()
		fmt.Printf("mode=%s authenticated=%t duration=%s error_type=%T\n", mode, err == nil, time.Since(start).Round(time.Millisecond), err)
	}
}
func must(err error) {
	if err != nil {
		panic(fmt.Sprintf("probe setup error type=%T", err))
	}
}
