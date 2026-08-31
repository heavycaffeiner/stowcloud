package fsatomic

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// countingDir wraps a real controlDir and counts how many times sync is
// called on it, so the "one directory synced exactly once" claim can be
// checked without a way to observe a real fsync count.
type countingDir struct {
	controlDir
	syncs *int
}

func (c countingDir) sync() error {
	*c.syncs++
	return c.controlDir.sync()
}

func countingOpener(counts map[string]*int) dirOpener {
	return func(path string) (controlDir, error) {
		d, err := openControlDir(path)
		if err != nil {
			return nil, err
		}
		n, ok := counts[path]
		if !ok {
			n = new(int)
			counts[path] = n
		}
		return countingDir{controlDir: d, syncs: n}, nil
	}
}

func TestReplaceFilesDurableAllUnitsLandAndEachDirectorySyncsOnce(t *testing.T) {
	sameDir := t.TempDir()
	dirA := t.TempDir()
	dirB := t.TempDir()

	units := []Unit{
		{Path: filepath.Join(sameDir, "a"), Mode: 0o640},
		{Path: filepath.Join(sameDir, "b"), Mode: 0o600},
		{Path: filepath.Join(dirA, "c"), Mode: 0o644},
		{Path: filepath.Join(dirB, "d"), Mode: 0o600},
	}
	contents := []string{"content-a", "content-b", "content-c", "content-d"}

	counts := map[string]*int{}
	err := replaceUnitsDurable(units, func(i int, f *os.File) error {
		_, werr := f.Write([]byte(contents[i]))
		return werr
	}, -1, countingOpener(counts))
	if err != nil {
		t.Fatalf("replacing: %v", err)
	}

	for i, u := range units {
		got, rerr := os.ReadFile(u.Path)
		if rerr != nil {
			t.Fatalf("unit %d: %v", i, rerr)
		}
		if string(got) != contents[i] {
			t.Errorf("unit %d holds %q, want %q", i, got, contents[i])
		}
		assertMode(t, u.Path, os.FileMode(u.Mode), i)
	}

	if len(counts) != 3 {
		t.Fatalf("opened %d distinct directories, want 3 (two units share one)", len(counts))
	}
	for dir, n := range counts {
		if *n != 1 {
			t.Errorf("directory %s synced %d times, want exactly 1", dir, *n)
		}
	}
	assertNoStagingResidue(t, sameDir)
	assertNoStagingResidue(t, dirA)
	assertNoStagingResidue(t, dirB)
}

func TestReplaceFilesDurableFailingWriterLeavesEveryDestinationUntouched(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	pathA := filepath.Join(dirA, "cert.pem")
	pathB := filepath.Join(dirB, "key.pem")
	if err := os.WriteFile(pathA, []byte("old-cert"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, []byte("old-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("second unit failed")
	units := []Unit{
		{Path: pathA, Mode: 0o644},
		{Path: pathB, Mode: 0o600},
	}
	err := ReplaceFilesDurable(units, func(i int, f *os.File) error {
		if i == 1 {
			return sentinel
		}
		_, werr := f.Write([]byte("new-cert"))
		return werr
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want the second unit's sentinel", err)
	}

	gotA, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotA) != "old-cert" {
		t.Fatalf("unit 0 changed to %q despite unit 1 failing", gotA)
	}
	gotB, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotB) != "old-key" {
		t.Fatalf("unit 1 changed to %q", gotB)
	}
	assertNoStagingResidue(t, dirA)
	assertNoStagingResidue(t, dirB)
}

// This reproduces the exact crash window the TLS ordering rule depends on:
// a crash after the key's rename but before the certificate's. The key is
// new, the certificate is old, and tls.X509KeyPair has to fail to parse
// that pair, since that failure is the only signal the caller's recovery
// rule relies on.
func TestReplaceFilesDurableCrashWindowProducesADetectableTornPair(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "server.key")
	certPath := filepath.Join(dir, "server.crt")

	oldKey, oldCert := generateTestKeyPair(t, "old")
	if err := os.WriteFile(keyPath, oldKey, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, oldCert, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tls.X509KeyPair(oldCert, oldKey); err != nil {
		t.Fatalf("the fixture pair itself does not parse: %v", err)
	}

	newKey, newCert := generateTestKeyPair(t, "new")
	units := []Unit{
		{Path: keyPath, Mode: 0o600},
		{Path: certPath, Mode: 0o644},
	}
	content := [][]byte{newKey, newCert}

	// stopAfter 0: the key's rename lands, the certificate's never runs.
	err := ReplaceFilesDurableStopAfterRenameForTest(units, func(i int, f *os.File) error {
		_, werr := f.Write(content[i])
		return werr
	}, 0)
	if err != nil {
		t.Fatalf("simulating the crash window: %v", err)
	}

	gotKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotKey) != string(newKey) {
		t.Fatalf("the key was not renamed into place before the simulated crash")
	}
	gotCert, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCert) != string(oldCert) {
		t.Fatalf("the certificate changed despite the simulated crash before its own rename")
	}

	if _, err := tls.X509KeyPair(gotCert, gotKey); err == nil {
		t.Fatal("a torn pair parsed successfully; the recovery rule's detector would never trigger")
	}
}

// generateTestKeyPair returns a PEM-encoded self-signed certificate and its
// own freshly generated key. Two calls never produce keys that verify
// against each other's certificate, since each key is independently random.
func generateTestKeyPair(t *testing.T, commonName string) (keyPEM, certPEM []byte) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a test key: %v", err)
	}

	// A fixed validity window rather than one measured from the wall clock:
	// this tree reads the clock in one package, and a test fixture is not it.
	validFrom := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    validFrom,
		NotAfter:     validFrom.AddDate(100, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	derCert, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("creating a test certificate: %v", err)
	}
	derKey, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshaling a test key: %v", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derCert})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: derKey})
	return keyPEM, certPEM
}
