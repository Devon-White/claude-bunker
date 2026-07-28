package main

import (
	"io"
	"net"
)

// splice relays a raw TLS stream from client to the re-resolved SNI host and
// back, replaying the already-read ClientHello bytes first. No decryption —
// the proxy sees only ciphertext. Re-resolving by SNI (not the original dest
// IP) prevents fronting: a client claiming SNI X reaches the real X.
func splice(client net.Conn, sni, raw, dialPort string) {
	defer client.Close()
	upstream, err := net.Dial("tcp", net.JoinHostPort(sni, dialPort))
	if err != nil {
		return
	}
	defer upstream.Close()
	if _, err := upstream.Write([]byte(raw)); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, client); done <- struct{}{} }()
	go func() { io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}
