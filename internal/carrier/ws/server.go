package ws

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// CarrierPath is the request path the WebSocket carrier upgrades on. It matches
// the HTTP/2 carrier path: a deployment runs one transport or the other on it.
const CarrierPath = "/baft/v1/carrier"

// IsUpgrade reports whether r is a WebSocket upgrade request for the carrier.
// It is used to distinguish a real carrier dial from a probe so the latter can
// be served an ordinary web page.
func IsUpgrade(r *http.Request) bool {
	return r.Method == http.MethodGet &&
		headerContainsToken(r.Header.Get("Connection"), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		r.Header.Get("Sec-WebSocket-Key") != "" &&
		r.Header.Get("Sec-WebSocket-Version") == "13"
}

// Upgrade completes the server side of the WebSocket handshake by hijacking the
// connection and returns a server Conn. The caller must have already checked
// IsUpgrade. On error nothing is written and the connection is left to the
// caller's http.Handler (which should serve a cover page).
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !IsUpgrade(r) {
		return nil, errors.New("ws: not a WebSocket upgrade request")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("ws: ResponseWriter does not support hijacking (need HTTP/1.1)")
	}
	accept := acceptKey(r.Header.Get("Sec-WebSocket-Key"))
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		fmt.Sprintf("Sec-WebSocket-Accept: %s\r\n", accept) +
		"\r\n"
	if _, err := brw.WriteString(resp); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := brw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	// brw.Reader may already hold client frames pipelined after the request; it
	// must be reused so those bytes are not dropped.
	return newConn(conn, brw.Reader, false), nil
}
