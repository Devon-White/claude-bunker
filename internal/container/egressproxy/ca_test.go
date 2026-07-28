package main

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
)

func TestCAMintsVerifiableLeaf(t *testing.T) {
	dir := t.TempDir()
	ca, err := loadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.leafFor("api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	// The leaf must chain to the CA and be valid for the host.
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca.certPEM()) {
		t.Fatal("certPEM not a valid PEM cert")
	}
	x509leaf, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x509leaf.Verify(x509.VerifyOptions{DNSName: "api.anthropic.com", Roots: roots}); err != nil {
		t.Fatalf("leaf does not verify against CA: %v", err)
	}
}

func TestCAPersistsAcrossLoad(t *testing.T) {
	dir := t.TempDir()
	ca1, err := loadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	ca2, err := loadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(ca1.certPEM()) != string(ca2.certPEM()) {
		t.Fatal("CA must persist across loads within a container")
	}
}

var _ = tls.Certificate{}
