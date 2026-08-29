// Linux only, matching the file under test.
//go:build linux

package server

import (
	"crypto/tls"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
)

// durable implements the seam's contract: stage each file, then rename them
// onto their destinations in the order given.
//
// Written here rather than bound to the store's implementation because the
// layer rule forbids this tier from importing that one, in tests as well as in
// code. What is under test is the contract the seam states, and a local writer
// honouring it is what exercises the caller against that contract.
func durable(paths []string, modes []uint32, write func(i int, f *os.File) error) error {
	return stageThenRename(paths, modes, write, len(paths))
}

// tornAfterKey renames only the first unit, which is the crash window the
// contract leaves open: a process that dies between two renames.
func tornAfterKey(paths []string, modes []uint32, write func(i int, f *os.File) error) error {
	return stageThenRename(paths, modes, write, 1)
}

// stageThenRename writes every unit to a staging name and renames the first
// renameCount of them.
func stageThenRename(
	paths []string, modes []uint32, write func(i int, f *os.File) error, renameCount int,
) error {
	staged := make([]string, len(paths))
	for i, path := range paths {
		staged[i] = path + ".staging"
		f, err := os.OpenFile(staged[i], os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(modes[i]))
		if err != nil {
			return err
		}
		werr := write(i, f)
		cerr := f.Close()
		if werr != nil {
			return werr
		}
		if cerr != nil {
			return cerr
		}
		// The mode is set explicitly, because the open call's mode is filtered
		// through the process umask.
		if merr := os.Chmod(staged[i], os.FileMode(modes[i])); merr != nil {
			return merr
		}
	}
	for i := range renameCount {
		if err := os.Rename(staged[i], paths[i]); err != nil {
			return err
		}
	}
	if renameCount < len(paths) {
		return errors.New("the write was interrupted between renames")
	}
	return nil
}

func tlsPaths(t *testing.T) TLSPaths {
	t.Helper()
	dir := t.TempDir()
	return TLSPaths{Cert: filepath.Join(dir, "tls", "cert.pem"), Key: filepath.Join(dir, "tls", "key.pem")}
}

func fixedClock() clock.Clock {
	return clock.Fixed(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
}

// A first start generates material that serves every declared host, plus the
// loopback names a browser reaching the machine directly would use.
func TestFirstStartGeneratesUsableMaterial(t *testing.T) {
	p := tlsPaths(t)
	hosts := []string{"app.example.test", "files.example.test"}

	cert, err := EnsureTLS(p, hosts, fixedClock(), true, durable)
	if err != nil {
		t.Fatalf("EnsureTLS: %v", err)
	}
	if cert.Leaf == nil {
		t.Fatal("the certificate has no parsed leaf")
	}

	for _, name := range append(hosts, "localhost") {
		if verr := cert.Leaf.VerifyHostname(name); verr != nil {
			t.Errorf("the certificate does not serve %s: %v", name, verr)
		}
	}
	for _, ip := range []string{"127.0.0.1", "::1"} {
		if verr := cert.Leaf.VerifyHostname(ip); verr != nil {
			t.Errorf("the certificate does not serve %s: %v", ip, verr)
		}
	}

	// The serial is unguessable rather than merely unique: a counter would
	// also say how many certificates this deployment has issued.
	if cert.Leaf.SerialNumber.BitLen() < 64 {
		t.Errorf("the serial is %d bits", cert.Leaf.SerialNumber.BitLen())
	}
	// An hour of skew, so a client whose clock runs slow still accepts it.
	if !cert.Leaf.NotBefore.Before(fixedClock().Now()) {
		t.Errorf("NotBefore is %v", cert.Leaf.NotBefore)
	}
}

// The files are readable by nobody else. A mode that lets another account read
// the key is the whole of the compromise.
func TestTheKeyIsPrivate(t *testing.T) {
	p := tlsPaths(t)
	if _, err := EnsureTLS(p, []string{"app.example.test"}, fixedClock(), true, durable); err != nil {
		t.Fatalf("EnsureTLS: %v", err)
	}

	for _, path := range []string{p.Key, p.Cert} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v", filepath.Base(path), info.Mode().Perm())
		}
	}
	dir, err := os.Stat(filepath.Dir(p.Key))
	if err != nil {
		t.Fatalf("stat the directory: %v", err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("the directory has mode %v", dir.Mode().Perm())
	}
}

// A usable pair is reused rather than regenerated, or every restart would hand
// clients a new identity.
func TestAUsablePairIsReused(t *testing.T) {
	p := tlsPaths(t)
	hosts := []string{"app.example.test"}

	first, err := EnsureTLS(p, hosts, fixedClock(), true, durable)
	if err != nil {
		t.Fatalf("the first start: %v", err)
	}
	second, err := EnsureTLS(p, hosts, fixedClock(), false, durable)
	if err != nil {
		t.Fatalf("the second start: %v", err)
	}

	if first.Leaf.SerialNumber.Cmp(second.Leaf.SerialNumber) != 0 {
		t.Error("the second start generated new material")
	}
}

// A crash between the two renames leaves a key newer than its certificate.
// That is recoverable: the certificate never reached disk, so nothing ever
// served it.
func TestATornPublishIsRecovered(t *testing.T) {
	p := tlsPaths(t)
	hosts := []string{"app.example.test"}

	// The torn write fails, which is the crash.
	_, err := EnsureTLS(p, hosts, fixedClock(), true, tornAfterKey)
	if err == nil {
		t.Log("the torn write reported success, which the store's hook may do")
	}

	// The key landed and the certificate did not, which is the state to
	// recover from.
	if _, kerr := os.Stat(p.Key); kerr != nil {
		t.Fatalf("the key did not land: %v", kerr)
	}
	if _, cerr := os.Stat(p.Cert); !os.IsNotExist(cerr) {
		t.Fatalf("the certificate landed after a torn write: %v", cerr)
	}

	// Recovery regenerates rather than refusing, even though this is not first
	// boot: nothing ever served the missing certificate.
	cert, rerr := EnsureTLS(p, hosts, fixedClock(), false, durable)
	if rerr != nil {
		t.Fatalf("recovering from a torn publish: %v", rerr)
	}
	if verr := cert.Leaf.VerifyHostname("app.example.test"); verr != nil {
		t.Errorf("the recovered certificate does not serve the host: %v", verr)
	}
}

// A pair that does not match is a refusal rather than a silent replacement.
// Replacing an identity clients have pinned is worse than not starting.
func TestAMismatchedPairIsARefusal(t *testing.T) {
	p := tlsPaths(t)
	if _, err := EnsureTLS(p, []string{"app.example.test"}, fixedClock(), true, durable); err != nil {
		t.Fatalf("EnsureTLS: %v", err)
	}

	// Somebody else's key, written after the certificate so this is not the
	// torn-publish signature.
	other := tlsPaths(t)
	if _, err := EnsureTLS(other, []string{"other.example.test"}, fixedClock(), true, durable); err != nil {
		t.Fatalf("the second pair: %v", err)
	}
	body, err := os.ReadFile(other.Key)
	if err != nil {
		t.Fatalf("reading the other key: %v", err)
	}
	// Written in place, which leaves it newer than the certificate beside it,
	// so the modification times are made to say the opposite.
	//nolint:gosec // G703: the path is this test's own TempDir, from tlsPaths above.
	if werr := os.WriteFile(p.Key, body, 0o600); werr != nil {
		t.Fatalf("writing the mismatched key: %v", werr)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if terr := os.Chtimes(p.Key, old, old); terr != nil {
		t.Fatalf("aging the key: %v", terr)
	}

	_, rerr := EnsureTLS(p, []string{"app.example.test"}, fixedClock(), false, durable)
	if !errors.Is(rerr, ErrTLSMaterial) {
		t.Fatalf("a mismatched pair returned %v", rerr)
	}
	// The original certificate is still there: a refusal does not delete.
	if _, serr := os.Stat(p.Cert); serr != nil {
		t.Errorf("the refusal removed the certificate: %v", serr)
	}
}

// A pair that does not cover a newly configured host is regenerated on first
// boot and refused after it, because by then a client may have pinned it.
func TestANewHostRegeneratesOnlyOnFirstBoot(t *testing.T) {
	p := tlsPaths(t)
	if _, err := EnsureTLS(p, []string{"app.example.test"}, fixedClock(), true, durable); err != nil {
		t.Fatalf("EnsureTLS: %v", err)
	}

	// Still first boot: the operator named a host before anything connected.
	grown, err := EnsureTLS(p, []string{"app.example.test", "files.example.test"}, fixedClock(), true, durable)
	if err != nil {
		t.Fatalf("regenerating on first boot: %v", err)
	}
	if verr := grown.Leaf.VerifyHostname("files.example.test"); verr != nil {
		t.Errorf("the regenerated certificate does not serve the new host: %v", verr)
	}

	// Past first boot the same situation is a refusal.
	_, rerr := EnsureTLS(p, []string{"app.example.test", "later.example.test"}, fixedClock(), false, durable)
	if !errors.Is(rerr, ErrTLSMaterial) {
		t.Fatalf("a missing host after first boot returned %v", rerr)
	}
}

// The floor is 1.2. Below it are versions with known attacks and no client
// this server needs to serve requires one.
func TestTheProtocolFloor(t *testing.T) {
	cfg := TLSConfig(tls.Certificate{})
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("the minimum version is %x", cfg.MinVersion)
	}
	if cfg.InsecureSkipVerify {
		t.Error("verification is disabled")
	}
}
