package main

import (
	"bufio"
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

// TestTerminateAppliesAllRulesForHost guards against a regression where only
// the first masking rule for a host was ever applied (matchRule instead of
// matchRules): a request carrying the second credential's sentinel would
// reach upstream unswapped. It configures two rules for the same host — one
// for an x-api-key sentinel, one for an Authorization Bearer sentinel — and
// sends one request per sentinel, asserting each gets its OWN real secret
// injected regardless of rule order.
func TestTerminateAppliesAllRulesForHost(t *testing.T) {
	upstreamCert := selfSignedCert(t, "localhost")
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{upstreamCert.tls}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	type seenHeaders struct {
		apiKey string
		auth   string
	}
	gotHeaders := make(chan seenHeaders, 2)
	go func() {
		for i := 0; i < 2; i++ {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				req, err := http.ReadRequest(bufioReader(c))
				if err != nil {
					return
				}
				gotHeaders <- seenHeaders{apiKey: req.Header.Get("x-api-key"), auth: req.Header.Get("Authorization")}
				io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
			}(c)
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	ca, _ := loadOrCreateCA(t.TempDir())
	testUpstreamRoots = x509.NewCertPool()
	testUpstreamRoots.AddCert(upstreamCert.leaf)
	t.Cleanup(func() { testUpstreamRoots = nil })

	proxyLn, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { proxyLn.Close() })

	// Two rules for the SAME host: an api-key rule (first) and an OAuth/Bearer
	// rule (second) — mirrors BuildMaskRules' output for api.anthropic.com
	// when both ApiKey and OAuthToken are configured.
	rules := []MaskRule{
		{Sentinel: "APIKEY-SENTINEL", Secret: "APIKEY-REAL", Hosts: []string{"localhost"}, Headers: []string{"x-api-key", "authorization"}},
		{Sentinel: "OAUTH-SENTINEL", Secret: "OAUTH-REAL", Hosts: []string{"localhost"}, Headers: []string{"authorization"}},
	}
	go serveWithCA(proxyLn, Config{}, &Allowlist{exact: map[string]struct{}{"localhost": {}}}, rules, port, ca)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.certPEM())

	// send opens one connection to the proxy, writes a request carrying the
	// given raw header line, and reads (and discards) exactly one HTTP
	// response — avoiding a wait-for-EOF read that would block on the
	// connection's keep-alive loop until a deadline fires.
	send := func(t *testing.T, headerLine string) {
		t.Helper()
		conn, err := tls.Dial("tcp", proxyLn.Addr().String(), &tls.Config{ServerName: "localhost", RootCAs: pool})
		if err != nil {
			t.Fatalf("client handshake to proxy failed: %v", err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		io.WriteString(conn, "GET / HTTP/1.1\r\nHost: localhost\r\n"+headerLine+"\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("reading response: %v", err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// Request 1 carries only the api-key sentinel.
	send(t, "x-api-key: APIKEY-SENTINEL")
	// Request 2 carries only the OAuth sentinel, via Authorization: Bearer.
	send(t, "Authorization: Bearer OAUTH-SENTINEL")

	var got []seenHeaders
	for i := 0; i < 2; i++ {
		select {
		case s := <-gotHeaders:
			got = append(got, s)
		case <-time.After(2 * time.Second):
			t.Fatalf("upstream received only %d of 2 requests", i)
		}
	}

	var sawAPIKey, sawOAuth bool
	for _, s := range got {
		if s.apiKey == "APIKEY-REAL" {
			sawAPIKey = true
		}
		if s.auth == "Bearer OAUTH-REAL" {
			sawOAuth = true
		}
	}
	if !sawAPIKey {
		t.Errorf("upstream never saw the real api-key secret; got %+v", got)
	}
	if !sawOAuth {
		t.Errorf("upstream never saw the real OAuth secret; got %+v", got)
	}
}
