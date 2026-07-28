package main

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestTerminateInjectsRealSecret(t *testing.T) {
	// Upstream TLS server that records the Authorization header it receives.
	gotAuth := make(chan string, 1)
	upstreamCert := selfSignedCert(t, "localhost")
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{upstreamCert.tls}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		req, err := http.ReadRequest(bufioReader(c))
		if err != nil {
			return
		}
		gotAuth <- req.Header.Get("Authorization")
		io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	ca, _ := loadOrCreateCA(t.TempDir())
	// upstream uses a self-signed cert; make terminate trust it for the test.
	testUpstreamRoots = x509.NewCertPool()
	testUpstreamRoots.AddCert(upstreamCert.leaf)
	t.Cleanup(func() { testUpstreamRoots = nil })

	proxyLn, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { proxyLn.Close() })
	rule := &MaskRule{Sentinel: "SENTINEL", Secret: "REAL-SECRET", Hosts: []string{"localhost"}, Headers: []string{"authorization"}}
	go serveWithCA(proxyLn, Config{}, &Allowlist{exact: map[string]struct{}{"localhost": {}}}, []MaskRule{*rule}, port, ca)

	// Client trusts the bunker CA, sends the sentinel.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.certPEM())
	conn, err := tls.Dial("tcp", proxyLn.Addr().String(), &tls.Config{ServerName: "localhost", RootCAs: pool})
	if err != nil {
		t.Fatalf("client handshake to proxy failed: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	io.WriteString(conn, "GET / HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer SENTINEL\r\n\r\n")
	io.ReadAll(conn)

	select {
	case auth := <-gotAuth:
		if auth != "Bearer REAL-SECRET" {
			t.Fatalf("upstream saw %q, want the injected real secret", auth)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never received the request")
	}
}
