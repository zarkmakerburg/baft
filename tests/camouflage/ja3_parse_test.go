package camouflage

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"time"
)

func testDeadlineCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = cancel
	return ctx
}

func isGREASE(v uint16) bool { return (v&0x0f0f) == 0x0a0a && (v>>8) == (v&0xff) }

// ja3FromClientHello parses a TLS ClientHello handshake message and returns the
// JA3 string (SSLVersion,Ciphers,Extensions,EllipticCurves,ECPointFormats),
// the five parts, and whether any GREASE value appeared. GREASE values are
// excluded from JA3 per the spec.
func ja3FromClientHello(t *testing.T, hs []byte) (string, [5]string, bool) {
	t.Helper()
	grease := false
	u16 := func(b []byte) uint16 { return binary.BigEndian.Uint16(b) }
	if len(hs) < 4 || hs[0] != 0x01 {
		t.Fatalf("not a ClientHello: % x", hs[:min(len(hs), 4)])
	}
	p := hs[4:] // skip handshake type + length
	if len(p) < 2+32 {
		t.Fatal("short ClientHello")
	}
	ver := u16(p[0:2])
	p = p[2+32:] // legacy_version + random
	// session id
	sidLen := int(p[0])
	p = p[1+sidLen:]
	// cipher suites
	csLen := int(u16(p[0:2]))
	p = p[2:]
	var ciphers []string
	for i := 0; i+1 < csLen; i += 2 {
		c := u16(p[i : i+2])
		if isGREASE(c) {
			grease = true
			continue
		}
		ciphers = append(ciphers, fmt.Sprint(c))
	}
	p = p[csLen:]
	// compression methods
	compLen := int(p[0])
	p = p[1+compLen:]
	// extensions
	var exts, curves, fmts []string
	if len(p) >= 2 {
		extLen := int(u16(p[0:2]))
		p = p[2:]
		end := extLen
		if end > len(p) {
			end = len(p)
		}
		e := p[:end]
		for len(e) >= 4 {
			et := u16(e[0:2])
			el := int(u16(e[2:4]))
			if 4+el > len(e) {
				break
			}
			body := e[4 : 4+el]
			if isGREASE(et) {
				grease = true
			} else {
				exts = append(exts, fmt.Sprint(et))
			}
			switch et {
			case 10: // supported_groups
				if len(body) >= 2 {
					n := int(u16(body[0:2]))
					for i := 0; i+1 < n && 2+i+1 < len(body); i += 2 {
						g := u16(body[2+i : 4+i])
						if isGREASE(g) {
							grease = true
							continue
						}
						curves = append(curves, fmt.Sprint(g))
					}
				}
			case 11: // ec_point_formats
				if len(body) >= 1 {
					n := int(body[0])
					for i := 0; i < n && 1+i < len(body); i++ {
						fmts = append(fmts, fmt.Sprint(body[1+i]))
					}
				}
			}
			e = e[4+el:]
		}
	}
	parts := [5]string{
		fmt.Sprint(ver),
		strings.Join(ciphers, "-"),
		strings.Join(exts, "-"),
		strings.Join(curves, "-"),
		strings.Join(fmts, "-"),
	}
	return strings.Join(parts[:], ","), parts, grease
}
