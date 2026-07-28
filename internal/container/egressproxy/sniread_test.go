package main

import (
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"testing"
	"time"
)

// captureClientHello starts a throwaway TLS client handshake against a pipe and
// returns the ClientHello bytes the client sent for SNI "example.test".
func captureClientHello(t *testing.T, serverName string) []byte {
	t.Helper()
	c, s := net.Pipe()
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Read whatever the client writes (the ClientHello) then stop.
		io.Copy(&buf, s)
	}()
	go func() {
		_ = tls.Client(c, &tls.Config{ServerName: serverName, InsecureSkipVerify: true}).Handshake()
		c.Close()
	}()
	// Close server side after timeout to unblock io.Copy if handshake doesn't complete
	go func() {
		time.Sleep(500 * time.Millisecond)
		s.Close()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout capturing ClientHello")
	}
	return buf.Bytes()
}

func TestReadClientHelloSNI(t *testing.T) {
	hello := captureClientHello(t, "example.test")
	sni, raw, err := readClientHello(bytes.NewReader(hello))
	if err != nil {
		t.Fatal(err)
	}
	if sni != "example.test" {
		t.Fatalf("sni=%q want example.test", sni)
	}
	if len(raw) == 0 || !bytes.HasPrefix(hello, raw) {
		t.Fatalf("raw must be a prefix of the captured hello")
	}
}

func TestReadClientHelloNoSNI(t *testing.T) {
	// A record that is valid TLS framing but not a ClientHello with SNI:
	// handshake record, ClientHello with empty extensions.
	hello := captureClientHello(t, "") // no ServerName => no SNI extension
	sni, _, err := readClientHello(bytes.NewReader(hello))
	if err != nil {
		t.Fatal(err)
	}
	if sni != "" {
		t.Fatalf("expected empty SNI, got %q", sni)
	}
}
