package main

import (
	"encoding/binary"
	"errors"
	"io"
)

// readClientHello reads a single TLS record (expected: a handshake record
// carrying the ClientHello) from r, returning the parsed SNI (empty if the
// ClientHello has no server_name extension) and the raw bytes consumed so the
// caller can replay them to an upstream when splicing.
//
// Parsing is defensive: any malformed field yields ("", raw, error) so callers
// fail closed. Only the common single-record ClientHello is handled; a hello
// fragmented across records yields an error (deny).
func readClientHello(r io.Reader) (string, []byte, error) {
	// TLS record header: type(1) version(2) length(2).
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return "", nil, err
	}
	if hdr[0] != 0x16 { // 22 = handshake
		return "", hdr, errors.New("not a TLS handshake record")
	}
	recLen := int(binary.BigEndian.Uint16(hdr[3:5]))
	if recLen == 0 || recLen > 16384 {
		return "", hdr, errors.New("bad record length")
	}
	body := make([]byte, recLen)
	n, err := io.ReadFull(r, body)
	if err != nil {
		return "", append(hdr, body[:n]...), err
	}
	raw := append(hdr, body...)
	sni, err := parseSNI(body)
	return sni, raw, err
}

// parseSNI extracts the server_name from a ClientHello handshake body.
func parseSNI(b []byte) (string, error) {
	// Handshake header: type(1) length(3).
	if len(b) < 4 || b[0] != 0x01 { // 1 = ClientHello
		return "", errors.New("not a ClientHello")
	}
	b = b[4:]
	// version(2) + random(32)
	if len(b) < 34 {
		return "", errors.New("short hello")
	}
	b = b[34:]
	// session_id
	if len(b) < 1 {
		return "", errors.New("short session_id")
	}
	sidLen := int(b[0])
	b = b[1:]
	if len(b) < sidLen {
		return "", errors.New("short session_id body")
	}
	b = b[sidLen:]
	// cipher_suites
	if len(b) < 2 {
		return "", errors.New("short cipher_suites")
	}
	csLen := int(binary.BigEndian.Uint16(b))
	b = b[2:]
	if len(b) < csLen {
		return "", errors.New("short cipher_suites body")
	}
	b = b[csLen:]
	// compression_methods
	if len(b) < 1 {
		return "", errors.New("short compression")
	}
	cmLen := int(b[0])
	b = b[1:]
	if len(b) < cmLen {
		return "", errors.New("short compression body")
	}
	b = b[cmLen:]
	// extensions
	if len(b) < 2 {
		return "", nil // no extensions => no SNI
	}
	extTotal := int(binary.BigEndian.Uint16(b))
	b = b[2:]
	if len(b) < extTotal {
		return "", errors.New("short extensions")
	}
	b = b[:extTotal]
	for len(b) >= 4 {
		extType := binary.BigEndian.Uint16(b)
		extLen := int(binary.BigEndian.Uint16(b[2:]))
		b = b[4:]
		if len(b) < extLen {
			return "", errors.New("short extension body")
		}
		ext := b[:extLen]
		b = b[extLen:]
		if extType != 0x0000 { // server_name
			continue
		}
		// server_name_list: list_len(2), then entries: type(1) len(2) name.
		if len(ext) < 2 {
			return "", errors.New("short SNI list")
		}
		listLen := int(binary.BigEndian.Uint16(ext))
		ext = ext[2:]
		if len(ext) < listLen {
			return "", errors.New("short SNI list body")
		}
		ext = ext[:listLen]
		for len(ext) >= 3 {
			nameType := ext[0]
			nameLen := int(binary.BigEndian.Uint16(ext[1:]))
			ext = ext[3:]
			if len(ext) < nameLen {
				return "", errors.New("short SNI name")
			}
			name := ext[:nameLen]
			ext = ext[nameLen:]
			if nameType == 0x00 { // host_name
				return string(name), nil
			}
		}
	}
	return "", nil
}
