// Linux only: it depends on packages that are Linux only.
//go:build linux

package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
)

// TLS material is self-signed, generated into data/tls on first start, with
// 127.0.0.1 always in the SAN list because the healthcheck dials it and
// verifies properly rather than skipping verification.
type tlsMaterial struct {
	Cert *tls.Certificate
}

// loadOrCreateTLS reads the certificate pair from data/tls, or generates and
// persists a fresh pair. A stale or corrupt pair is a startup refusal rather
// than a silent regeneration: silently minting a new certificate invalidates
// every client's pinned fingerprint with no record of the change.
func loadOrCreateTLS(dir string, appHost string) (*tlsMaterial, error) {
	certPath := filepath.Join(dir, "tls", "cert.pem")
	keyPath := filepath.Join(dir, "tls", "key.pem")
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return &tlsMaterial{Cert: &cert}, nil
	}
	if err := os.MkdirAll(filepath.Join(dir, "tls"), 0o700); err != nil {
		return nil, fmt.Errorf("creating the TLS directory: %w", err)
	}
	certPEM, keyPEM, err := generateSelfSigned(appHost)
	if err != nil {
		return nil, err
	}
	if werr := os.WriteFile(certPath, certPEM, 0o600); werr != nil {
		return nil, fmt.Errorf("persisting the certificate: %w", werr)
	}
	if werr := os.WriteFile(keyPath, keyPEM, 0o600); werr != nil {
		return nil, fmt.Errorf("persisting the key: %w", werr)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &tlsMaterial{Cert: &cert}, nil
}

// generateSelfSigned mints a certificate whose SANs cover the app host,
// localhost and the loopback addresses, and returns the PEM pair.
func generateSelfSigned(appHost string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating the server key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	now := clock.System().Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: appHost},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{appHost, "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}
