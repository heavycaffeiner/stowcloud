// Linux only, because it serves a Linux-only engine.
//go:build linux

// The server's own TLS material.
//
// Generated on first use and reused after that. The interesting part is what
// happens when the pair on disk is not a pair: a key newer than its
// certificate is the signature of a crash between two renames, and that is
// recoverable. Anything else that does not match is a startup refusal, because
// silently generating a new identity for a deployment clients have already
// pinned is worse than not starting.
package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
)

// Certificate lifetime and the skew that keeps a freshly issued one valid on a
// client whose clock runs slow.
const (
	certLifetime  = 10 * 365 * 24 * time.Hour
	certNotBefore = time.Hour
)

// File modes. The key is readable by nobody else, and the directory holding it
// is not listable either: a mode that lets another account see the filename is
// a mode that tells them where to try.
const (
	certMode = 0o600
	keyMode  = 0o600
	dirMode  = 0o700
)

// ErrTLSMaterial is a pair on disk that cannot be used and cannot be safely
// replaced.
var ErrTLSMaterial = errors.New("the stored TLS material is not usable")

// TLSPaths names where the material lives.
type TLSPaths struct {
	Cert string
	Key  string
}

// DurableWriter publishes a set of files as one unit.
//
// A seam rather than a direct call into the persistence tier, which this tier
// may not import. What matters here is the contract: the files land in the
// order given or none of them do, and the caller binds it to the store's own
// implementation.
type DurableWriter func(paths []string, modes []uint32, write func(i int, f *os.File) error) error

// EnsureTLS returns usable material for the declared hosts, generating it if
// there is none or if what is there cannot serve them.
//
// hosts are the app and content hosts this deployment answers on. A pair that
// does not cover one of them is regenerated only on first boot, before a
// client could have pinned it; after that a missing host is a refusal, since
// replacing the identity would break every client that trusted the old one.
func EnsureTLS(
	p TLSPaths, hosts []string, clk clock.Clock, firstBoot bool, publish DurableWriter,
) (tls.Certificate, error) {
	cert, err := loadPair(p)
	switch {
	case err == nil:
		if covers(cert, hosts) {
			return cert, nil
		}
		if !firstBoot {
			return tls.Certificate{}, fmt.Errorf(
				"%w: it does not cover %s and this is not first boot",
				ErrTLSMaterial, strings.Join(missing(cert, hosts), ", "))
		}
	case errors.Is(err, os.ErrNotExist):
		// Nothing there yet, which is the ordinary first start.
	case errors.Is(err, errTornPublish):
		// A key newer than its certificate is the one crash window the durable
		// writer leaves open: it renames the key first, so a crash between the
		// two renames leaves exactly this. Regenerating is safe because the
		// certificate that key belongs to never reached disk, so nothing ever
		// served it.
	default:
		return tls.Certificate{}, fmt.Errorf("%w: %w", ErrTLSMaterial, err)
	}

	return generate(p, hosts, clk, publish)
}

// errTornPublish is a key newer than the certificate beside it.
var errTornPublish = errors.New("the key is newer than the certificate")

// loadPair reads and parses the material.
func loadPair(p TLSPaths) (tls.Certificate, error) {
	certInfo, cerr := os.Stat(p.Cert)
	keyInfo, kerr := os.Stat(p.Key)

	switch {
	case os.IsNotExist(cerr) && os.IsNotExist(kerr):
		return tls.Certificate{}, os.ErrNotExist
	case os.IsNotExist(cerr) && kerr == nil:
		// A key with no certificate at all: the same torn publish, caught one
		// step earlier.
		return tls.Certificate{}, errTornPublish
	case cerr != nil:
		return tls.Certificate{}, cerr
	case kerr != nil:
		return tls.Certificate{}, kerr
	}

	if keyInfo.ModTime().After(certInfo.ModTime()) {
		return tls.Certificate{}, errTornPublish
	}

	cert, err := tls.LoadX509KeyPair(p.Cert, p.Key)
	if err != nil {
		// A pair that does not match is not a torn publish: the writer renames
		// the key first, so a mismatch with the certificate newer means
		// something else wrote here. Refusing beats replacing an identity.
		return tls.Certificate{}, err
	}
	if len(cert.Certificate) == 0 {
		return tls.Certificate{}, errors.New("the certificate file holds no certificate")
	}
	leaf, perr := x509.ParseCertificate(cert.Certificate[0])
	if perr != nil {
		return tls.Certificate{}, perr
	}
	cert.Leaf = leaf
	return cert, nil
}

// covers reports whether the certificate serves every declared host.
func covers(cert tls.Certificate, hosts []string) bool {
	return len(missing(cert, hosts)) == 0
}

// missing lists the declared hosts the certificate does not serve.
func missing(cert tls.Certificate, hosts []string) []string {
	if cert.Leaf == nil {
		return slices.Clone(hosts)
	}
	var out []string
	for _, h := range hosts {
		name := strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
		if err := cert.Leaf.VerifyHostname(name); err != nil {
			out = append(out, h)
		}
	}
	return out
}

// generate writes a fresh pair and returns it.
func generate(p TLSPaths, hosts []string, clk clock.Clock, publish DurableWriter) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generating a key: %w", err)
	}

	// 128 bits, which is what makes a serial unguessable rather than merely
	// unique. A counter would be unique and would also say how many
	// certificates this deployment has issued.
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generating a serial: %w", err)
	}

	now := clk.Now()
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "stowcloud"},
		// An hour of skew, so a client whose clock runs slightly slow does not
		// reject a certificate issued moments ago.
		NotBefore:             now.Add(-certNotBefore),
		NotAfter:              now.Add(certLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	addNames(&tmpl, hosts)

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("creating the certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("encoding the key: %w", err)
	}

	if werr := writePair(p, der, keyDER, publish); werr != nil {
		return tls.Certificate{}, werr
	}

	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	leaf, perr := x509.ParseCertificate(der)
	if perr != nil {
		return tls.Certificate{}, perr
	}
	cert.Leaf = leaf
	return cert, nil
}

// addNames puts the hosts into the certificate, splitting names from
// addresses because a certificate carries them in different fields.
func addNames(tmpl *x509.Certificate, hosts []string) {
	// Always present, so a browser reaching the machine directly rather than
	// by its configured name still gets a certificate that matches.
	names := []string{"localhost"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}

	for _, h := range hosts {
		host := strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			ips = append(ips, ip)
			continue
		}
		names = append(names, host)
	}
	slices.Sort(names)
	tmpl.DNSNames = slices.Compact(names)
	tmpl.IPAddresses = ips
}

// writePair writes both files as one durable unit.
//
// The key goes first. A crash between the two renames then leaves a key newer
// than its certificate, which loadPair recognises and regenerates; the other
// order would leave a certificate whose key never arrived, which is
// indistinguishable from somebody else's certificate and has to be refused.
func writePair(p TLSPaths, der, keyDER []byte, publish DurableWriter) error {
	if err := os.MkdirAll(dirOf(p.Cert), dirMode); err != nil {
		return fmt.Errorf("preparing the certificate directory: %w", err)
	}

	return publish(
		[]string{p.Key, p.Cert},
		[]uint32{keyMode, certMode},
		func(i int, f *os.File) error {
			switch i {
			case 0:
				return pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
			default:
				return pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der})
			}
		})
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i > 0 {
		return path[:i]
	}
	return "."
}

// TLSConfig is the server's configuration for a certificate.
//
// 1.2 is the floor. Below it are protocol versions with known attacks and no
// client this server needs to serve requires one.
func TLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
}
