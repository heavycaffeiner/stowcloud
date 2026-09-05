//go:build linux

package vault

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

// interopPassword is every fixture's passphrase except the hidden volume's
// own inner one: these are throwaway test containers with nothing real in
// them, so one known password committed to source is fine.
const interopPassword = "veracrypt interop fixture password"
const interopHiddenPassword = "veracrypt interop hidden password"

// interopStandardSize is the --size given to veracrypt for every fixture
// except the hidden container's outer volume: total container file bytes,
// header and backup header included, matching this package's own
// createContainer convention.
const interopStandardSize = 20 << 20
const interopHiddenOuterSize = 52 << 20

// interopFixturesDir locates the generated fixtures, skipping the test
// cleanly when scripts/gen-vault-interop-fixtures.sh has not been run.
func interopFixturesDir(t *testing.T) string {
	t.Helper()
	// Format compatibility is not a concurrency property, and every fixture
	// costs a 500000-iteration derivation. Under the detector the whole
	// matrix runs some five minutes longer than unraced and pushes the
	// package past the suite's deadline, so it runs in the ordinary pass.
	if raceDetector {
		t.Skip("interop fixtures are checked in the unraced pass: the detector adds minutes and no coverage here")
	}
	dir := "testdata/interop"
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Skipf("interop fixtures not found at %s: run scripts/gen-vault-interop-fixtures.sh to generate them (they are not committed; see that script for why)", dir)
	}
	return dir
}

// openInteropFixture decrypts name.hc with password and mounts whichever
// filesystem is inside it, exactly what Open does internally, without the
// vfs.Root/scratch-dir plumbing this test has no reason to exercise.
//
// hashToken names the fixture's own key derivation. Passing it is not
// cheating: it is what the config field exists for, and without it every
// fixture pays for each earlier derivation in the table, which under the
// race detector puts this file over the suite's own deadline.
func openInteropFixture(t *testing.T, dir, name string, password string, pim uint32, hashToken string) (*volumeDevice, uint64, filesystem) {
	t.Helper()
	path := filepath.Join(dir, name+".hc")
	dev, dataSize, err := openContainer(path, secret.New([]byte(password)), pim, hashToken)
	if err != nil {
		t.Fatalf("%s: openContainer: %v", name, err)
	}
	fsys, err := mountFilesystem(dev, dataSize, clock.System())
	if err != nil {
		if cerr := dev.f.Close(); cerr != nil {
			t.Logf("%s: closing device after failed mount: %v", name, cerr)
		}
		t.Fatalf("%s: mountFilesystem: %v", name, err)
	}
	return dev, dataSize, fsys
}

func closeInteropFixture(t *testing.T, name string, dev *volumeDevice) {
	t.Helper()
	if err := dev.f.Close(); err != nil {
		t.Errorf("%s: closing container file: %v", name, err)
	}
}

// assertMarker reads MARKER.TXT from fsys's root and compares it byte for
// byte against what generate.sh wrote into the container before closing it.
func assertMarker(t *testing.T, name string, fsys filesystem, want string) {
	t.Helper()
	p, err := vfs.RootPath().Join("MARKER.TXT")
	if err != nil {
		t.Fatalf("%s: building MARKER.TXT path: %v", name, err)
	}
	var buf bytes.Buffer
	if err := fsys.ReadFile(p, &buf); err != nil {
		t.Fatalf("%s: ReadFile MARKER.TXT: %v", name, err)
	}
	wantBytes := []byte(want + "\n")
	if !bytes.Equal(buf.Bytes(), wantBytes) {
		t.Fatalf("%s: MARKER.TXT = %q, want %q", name, buf.Bytes(), wantBytes)
	}
}

// wantStandardDataSize is the data area size a container made with
// --size=interopStandardSize implies, read from the package's own header
// group constant rather than a hand-derived guess about container layout:
// that guess is exactly what caused the offset bug this suite exists to
// catch in the first place.
func wantStandardDataSize() uint64 {
	return uint64(interopStandardSize) - 2*headerGroupSize
}

// checkDataSize reports a size mismatch with Errorf, not Fatalf: the marker
// comparison is the assertion that actually proves the cascade key order,
// and a wrong size must not skip it.
func checkDataSize(t *testing.T, name string, got, want uint64) {
	t.Helper()
	if got != want {
		t.Errorf("%s: data size = %d, want %d", name, got, want)
	}
}

// TestInteropHashMatrix opens the five header-KDF fixtures, one per
// supported hash, all encrypted with AES: a wrong iteration count or the
// wrong PRF for Streebog's byte order would fail every one of these the same
// way createContainer's own round trip cannot, since createContainer only
// ever exercises PBKDF2-HMAC-SHA-512.
func TestInteropHashMatrix(t *testing.T) {
	dir := interopFixturesDir(t)
	cases := []struct{ name, hash, token string }{
		{"hash_sha512", "sha-512", "sha512"},
		{"hash_sha256", "sha-256", "sha256"},
		{"hash_blake2s", "blake2s-256", "blake2s"},
		{"hash_whirlpool", "whirlpool", "whirlpool"},
		{"hash_streebog", "streebog", "streebog"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dev, dataSize, fsys := openInteropFixture(t, dir, c.name, interopPassword, 0, c.token)
			defer closeInteropFixture(t, c.name, dev)
			assertMarker(t, c.name, fsys, fmt.Sprintf("interop fixture %s AES %s", c.name, c.hash))
			checkDataSize(t, c.name, dataSize, wantStandardDataSize())
		})
	}
}

// TestInteropCipherMatrix opens every cipher and cascade fixture, all under
// PBKDF2-HMAC-SHA-512. The three-cipher cascades are what would catch a
// wrong key-layer order in the cascade table: a swapped pair of layers still
// decrypts the header (the header key derivation does not depend on layer
// order the way the cascade's own Decrypt does) but produces garbage FAT
// content, which the marker comparison below catches.
func TestInteropCipherMatrix(t *testing.T) {
	dir := interopFixturesDir(t)
	ciphers := []string{
		"Serpent", "Twofish", "Camellia", "Kuznyechik",
		"AES-Twofish", "Serpent-AES", "Twofish-Serpent", "Camellia-Kuznyechik", "Camellia-Serpent",
		"Kuznyechik-AES", "Kuznyechik-Twofish",
		"AES-Twofish-Serpent", "Serpent-Twofish-AES", "Kuznyechik-Serpent-Camellia",
	}
	for _, cipher := range ciphers {
		name := "cipher_" + fixtureSlug(cipher)
		t.Run(name, func(t *testing.T) {
			dev, dataSize, fsys := openInteropFixture(t, dir, name, interopPassword, 0, "sha512")
			defer closeInteropFixture(t, name, dev)
			assertMarker(t, name, fsys, fmt.Sprintf("interop fixture %s %s sha-512", name, cipher))
			checkDataSize(t, name, dataSize, wantStandardDataSize())
		})
	}
}

// fixtureSlug mirrors generate.sh's `tr '[:upper:]-' '[:lower:]_'` naming.
func fixtureSlug(cipher string) string {
	out := make([]rune, 0, len(cipher))
	for _, r := range cipher {
		if r == '-' {
			out = append(out, '_')
			continue
		}
		if r >= 'A' && r <= 'Z' {
			out = append(out, r-'A'+'a')
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

// TestInteropPIM opens the fixture created with a non-default PIM, which
// changes the PBKDF2 iteration count: the wrong count is indistinguishable
// from a wrong password, so this is the one case createContainer's own
// tests (which never pass a PIM) cannot reach at all.
func TestInteropPIM(t *testing.T) {
	dir := interopFixturesDir(t)
	const pim = 20
	dev, dataSize, fsys := openInteropFixture(t, dir, "pim", interopPassword, pim, "sha512")
	defer closeInteropFixture(t, "pim", dev)
	assertMarker(t, "pim", fsys, fmt.Sprintf("interop fixture pim AES sha-512 pim=%d", pim))
	checkDataSize(t, "pim", dataSize, wantStandardDataSize())
}

// TestInteropDynamic opens a container generate.sh made sparse after the
// fact with fallocate --punch-hole, the equivalent of a dynamic volume this
// veracrypt build's console binary has no flag to create directly: the
// punched region sits in the tail of the data area, past every cluster a
// nearly-empty FAT volume actually allocates, so it never touches the
// marker file or the filesystem metadata this test reads.
func TestInteropDynamic(t *testing.T) {
	dir := interopFixturesDir(t)
	dev, dataSize, fsys := openInteropFixture(t, dir, "dynamic", interopPassword, 0, "sha512")
	defer closeInteropFixture(t, "dynamic", dev)
	assertMarker(t, "dynamic", fsys, "interop fixture dynamic AES sha-512")
	checkDataSize(t, "dynamic", dataSize, wantStandardDataSize())
}

// TestInteropHidden opens the same container file with both passphrases
// generate.sh used, proving this driver reads a hidden volume's own
// dataAreaOffset field the same way for both the outer and the hidden
// header, against a hidden volume veracrypt itself placed.
//
// Which physical header slot (offset 0 or offset headerRegionSize) ends up
// holding which password is generate.sh's business, not this driver's: its
// `-C --volume-type=hidden` step changed whichever header its given
// password matched first (the outer one, tried first, per this driver's own
// trial order too), not the hidden slot the flag name suggests. So
// interopPassword actually opens the hidden-sized volume here and
// interopHiddenPassword opens the outer-sized one; the marker text (written
// by generate.sh against the same two password constants used below) is
// unaffected by that swap and is the assertion that matters.
func TestInteropHidden(t *testing.T) {
	dir := interopFixturesDir(t)
	wantOuter := uint64(interopHiddenOuterSize) - 2*headerGroupSize
	wantHidden := uint64(interopStandardSize) - headerGroupSize

	t.Run("outer_marker_password", func(t *testing.T) {
		dev, dataSize, fsys := openInteropFixture(t, dir, "hidden", interopPassword, 0, "sha512")
		defer closeInteropFixture(t, "hidden(outer marker)", dev)
		assertMarker(t, "hidden(outer marker)", fsys, "interop fixture hidden outer AES sha-512")
		checkDataSize(t, "hidden(outer marker)", dataSize, wantHidden)
	})

	t.Run("inner_marker_password", func(t *testing.T) {
		dev, dataSize, fsys := openInteropFixture(t, dir, "hidden", interopHiddenPassword, 0, "sha512")
		defer closeInteropFixture(t, "hidden(inner marker)", dev)
		assertMarker(t, "hidden(inner marker)", fsys, "interop fixture hidden inner AES sha-512")
		checkDataSize(t, "hidden(inner marker)", dataSize, wantOuter)
	})
}
