package main

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"testing"
	"time"
)

// startTLSEchoServer returns a TLS listener that echoes one line, plus a cert
// pool trusting it. The cert's CN/DNSNames is "localhost" so a client dialing
// with ServerName "localhost" (see TestSpliceAllowedHostRelays) both sends a
// real SNI extension (unlike an IP literal, see below) and passes hostname
// verification against this cert.
func startTLSEchoServer(t *testing.T) (addr string, pool *x509.CertPool) {
	t.Helper()
	cert := selfSignedCert(t, "localhost")
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert.tls}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err == nil {
					c.Write(buf) // echo
				}
			}()
		}
	}()
	pool = x509.NewCertPool()
	pool.AddCert(cert.leaf)
	return ln.Addr().String(), pool
}

func TestSpliceAllowedHostRelays(t *testing.T) {
	upstreamAddr, pool := startTLSEchoServer(t)
	_, port, _ := net.SplitHostPort(upstreamAddr)

	// Allowlist that permits "localhost"; proxy dials by SNI, so we need the
	// SNI value itself to resolve to the echo server's loopback address. Note
	// crypto/tls never sends the SNI extension when ServerName is a literal IP
	// (RFC 6066 §3; see hostnameInSNI in the stdlib) — so a plain "127.0.0.1"
	// ServerName would produce an empty SNI and get denied. Use "localhost"
	// (resolves to 127.0.0.1) so net.Dial reaches the echo server via the SNI.
	al := &Allowlist{exact: map[string]struct{}{"localhost": {}}}

	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proxyLn.Close() })
	go serve(proxyLn, Config{}, al, nil, port)

	// Client dials the proxy with SNI localhost, expecting the proxy to splice
	// to localhost:<port> (i.e. 127.0.0.1) and relay the TLS handshake + echo.
	conn, err := tls.Dial("tcp", proxyLn.Addr().String(),
		&tls.Config{ServerName: "localhost", RootCAs: pool})
	if err != nil {
		t.Fatalf("handshake through splice failed: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	conn.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo failed: %q err=%v", buf, err)
	}
}

func TestSpliceDeniedSNIClosed(t *testing.T) {
	al := &Allowlist{exact: map[string]struct{}{"allowed.test": {}}}
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proxyLn.Close() })
	go serve(proxyLn, Config{}, al, nil, "443")

	conn, err := tls.Dial("tcp", proxyLn.Addr().String(),
		&tls.Config{ServerName: "evil.test", InsecureSkipVerify: true})
	if err == nil {
		conn.Close()
		t.Fatal("expected handshake to fail for denied SNI")
	}
}
