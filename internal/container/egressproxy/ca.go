package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type certAuthority struct {
	cert         *x509.Certificate
	key          *ecdsa.PrivateKey
	certPEMbytes []byte

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
}

func (c *certAuthority) certPEM() []byte { return c.certPEMbytes }

// loadOrCreateCA loads ca.pem/ca.key from dir, generating them if absent.
func loadOrCreateCA(dir string) (*certAuthority, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, "ca.pem")
	keyPath := filepath.Join(dir, "ca.key")

	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	if errors.Is(certErr, fs.ErrNotExist) || errors.Is(keyErr, fs.ErrNotExist) {
		return generateCA(dir, certPath, keyPath)
	}
	if certErr != nil {
		return nil, certErr
	}
	if keyErr != nil {
		return nil, keyErr
	}
	cblock, _ := pem.Decode(certPEM)
	kblock, _ := pem.Decode(keyPEM)
	if cblock == nil || kblock == nil {
		return nil, errors.New("corrupt CA files")
	}
	cert, err := x509.ParseCertificate(cblock.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParseECPrivateKey(kblock.Bytes)
	if err != nil {
		return nil, err
	}
	return &certAuthority{cert: cert, key: key, certPEMbytes: certPEM, leaves: map[string]*tls.Certificate{}}, nil
}

func generateCA(dir, certPath, keyPath string) (*certAuthority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "claude-bunker egress CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0444); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0400); err != nil {
		return nil, err
	}
	return &certAuthority{cert: cert, key: key, certPEMbytes: certPEM, leaves: map[string]*tls.Certificate{}}, nil
}

// leafFor mints (and caches) a leaf certificate for host, signed by the CA.
func (c *certAuthority) leafFor(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if lc, ok := c.leaves[host]; ok {
		return lc, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	leaf := &tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: key}
	c.leaves[host] = leaf
	return leaf, nil
}
