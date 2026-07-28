package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
)

// testUpstreamRoots, when non-nil (tests only), overrides upstream TLS
// verification so a self-signed upstream can be used.
var testUpstreamRoots *x509.CertPool

func bufioReader(c net.Conn) *bufio.Reader { return bufio.NewReader(c) }

// prefixConn is a net.Conn whose first read(s) replay a prefix of bytes
// already consumed from the underlying connection before falling through to
// it. handle() (main.go) reads the ClientHello record off the raw socket via
// readClientHello to make its routing decision (allowlist + mask-rule match);
// those bytes are gone from the socket for good once read. When routing to
// terminate, the caller wraps the conn in a prefixConn carrying that same
// ClientHello record so tls.Server's handshake reader sees it exactly as the
// client sent it, instead of blocking forever on bytes that will never
// arrive.
type prefixConn struct {
	net.Conn
	prefix []byte
}

func (c *prefixConn) Read(b []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(b, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(b)
}

// terminate accepts the client's TLS using a leaf minted for sni, reads each
// HTTP/1.1 request, swaps the sentinel for the real secret, and forwards over a
// genuine TLS connection to the real upstream. Responses stream back verbatim.
func terminate(client net.Conn, sni string, rule *MaskRule, dialPort string, ca *certAuthority) {
	defer client.Close()
	leaf, err := ca.leafFor(sni)
	if err != nil {
		return
	}
	tlsClient := tls.Server(client, &tls.Config{Certificates: []tls.Certificate{*leaf}})
	if err := tlsClient.Handshake(); err != nil {
		return
	}
	defer tlsClient.Close()

	upstreamCfg := &tls.Config{ServerName: sni, RootCAs: testUpstreamRoots}
	upstream, err := tls.Dial("tcp", net.JoinHostPort(sni, dialPort), upstreamCfg)
	if err != nil {
		return
	}
	defer upstream.Close()

	cr := bufio.NewReader(tlsClient)
	ur := bufio.NewReader(upstream)
	for {
		req, err := http.ReadRequest(cr)
		if err != nil {
			return
		}
		applyMask(req.Header, rule)
		req.URL.Scheme = ""
		req.URL.Host = ""
		if err := req.Write(upstream); err != nil {
			return
		}
		resp, err := http.ReadResponse(ur, req)
		if err != nil {
			return
		}
		if err := resp.Write(tlsClient); err != nil {
			return
		}
		if req.Close || resp.Close {
			return
		}
	}
}
