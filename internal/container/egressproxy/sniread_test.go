package main

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
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

// TestParseSNIBeyondListLen is a regression test for the server_name_list
// boundary enforcement. It crafts a malicious ClientHello body where a
// host_name entry is placed beyond the declared listLen. Without the
// `ext = ext[:listLen]` fix, the parser would skip past the declared
// boundary and find the malicious entry. With the fix, it correctly
// returns empty SNI because no valid entry exists within the boundary.
func TestParseSNIBeyondListLen(t *testing.T) {
	// Construct a minimal ClientHello body with a crafted server_name extension.
	var body bytes.Buffer
	body.WriteByte(0x01) // ClientHello type
	// Length placeholder (3 bytes), will fill later
	body.Write(make([]byte, 3))
	// Version: TLS 1.2
	body.Write([]byte{0x03, 0x03})
	// Random: 32 bytes
	body.Write(make([]byte, 32))
	// Session ID length: 0
	body.WriteByte(0x00)
	// Cipher suites length: 2
	body.Write([]byte{0x00, 0x02})
	// Cipher suite: TLS_RSA_WITH_AES_128_CBC_SHA (0x002f)
	body.Write([]byte{0x00, 0x2f})
	// Compression methods length: 1
	body.WriteByte(0x01)
	// Compression method: null
	body.WriteByte(0x00)
	// Extensions total length placeholder
	extStart := body.Len()
	body.Write([]byte{0x00, 0x00})

	// Craft the server_name extension.
	body.Write([]byte{0x00, 0x00}) // Extension type: 0x0000 (server_name)
	extBodyStart := body.Len()
	body.Write([]byte{0x00, 0x00}) // Extension length placeholder

	// server_name_list: declare listLen=5, which gives us 5 bytes to work with.
	// We'll put incomplete/partial data in those 5 bytes (e.g., just a type byte and
	// a partial length that doesn't form a complete entry). Then, AFTER those 5 bytes,
	// we'll put a complete "malicious.test" entry that should NOT be returned because
	// it's beyond the declared boundary.
	listLenBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(listLenBuf, 5) // listLen = 5
	body.Write(listLenBuf)
	// Fill the next 5 bytes with data that doesn't form a complete host_name entry
	// within those 5 bytes (just padding).
	body.Write([]byte{0x00, 0x00, 0x00, 0x00, 0x00}) // 5 bytes of incomplete/padding data
	// Now, AFTER the listLen boundary, put a complete host_name entry that should be ignored.
	body.WriteByte(0x00) // Type: host_name
	nameLenBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(nameLenBuf, 14)
	body.Write(nameLenBuf)
	body.Write([]byte("malicious.test")) // 14 bytes

	// Fill in the extension body length
	extBodyLen := body.Len() - extBodyStart - 2
	extLenBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(extLenBuf, uint16(extBodyLen))
	bodyBytes := body.Bytes()
	bodyBytes[extBodyStart] = extLenBuf[0]
	bodyBytes[extBodyStart+1] = extLenBuf[1]

	// Fill in the extensions total length
	extTotal := body.Len() - extStart - 2
	extTotalBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(extTotalBuf, uint16(extTotal))
	bodyBytes[extStart] = extTotalBuf[0]
	bodyBytes[extStart+1] = extTotalBuf[1]

	// Fill in the handshake header length
	hdrLen := len(bodyBytes) - 4
	bodyBytes[1] = byte(hdrLen >> 16)
	bodyBytes[2] = byte(hdrLen >> 8)
	bodyBytes[3] = byte(hdrLen)

	// Parse the malicious body
	sni, err := parseSNI(bodyBytes)
	if err != nil {
		t.Fatalf("parseSNI failed: %v", err)
	}
	// The key assertion: even though "malicious.test" exists in the extension body,
	// it's beyond the declared listLen, so the parser should not return it.
	// With the fix, the loop is bounded by `ext = ext[:listLen]`, so it only sees
	// the first 5 bytes of padding and returns empty SNI.
	if sni != "" {
		t.Fatalf("sni should be empty (listLen boundary enforcement), got %q", sni)
	}
}
