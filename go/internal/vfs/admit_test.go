//go:build linux

package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Admission is a fail-closed allow-list. A magic value this version has no
// name for cannot become supported by nobody having thought about it.

func TestEverySupportedFilesystemIsAdmitted(t *testing.T) {
	for _, tc := range []struct {
		t           FsType
		wantReflink bool
		wantWarn    bool
	}{
		{FsExt4, false, false},
		{FsZfs, false, false},
		{FsF2fs, false, false},
		// These two can copy by reference, so a copy costs metadata rather
		// than every byte.
		{FsBtrfs, true, false},
		{FsXfs, true, false},
		// Admitted with the caveat the operator has to see.
		{FsTmpfs, false, true},
	} {
		adm, err := AdmitMount("/srv/x", tc.t, true)
		if err != nil {
			t.Errorf("%s was refused: %v", tc.t, err)
			continue
		}
		if adm.Reflink != tc.wantReflink {
			t.Errorf("%s reflink = %v, want %v", tc.t, adm.Reflink, tc.wantReflink)
		}
		if (adm.Warn != "") != tc.wantWarn {
			t.Errorf("%s warn = %q, want a warning: %v", tc.t, adm.Warn, tc.wantWarn)
		}
	}
}

// Every other known type is refused, and the refusal names the filesystem:
// "unsupported filesystem" is a refusal an operator cannot act on.
func TestEveryOtherKnownFilesystemIsRefusedByName(t *testing.T) {
	for _, ft := range []FsType{
		FsOverlay, FsFuse, FsNfs, FsCifs, FsSmb2, FsSquashfs, FsNtfs,
	} {
		_, err := AdmitMount("/srv/x", ft, true)
		if !errors.Is(err, ErrUnsupportedFilesystem) {
			t.Errorf("%s gave %v, want a refusal", ft, err)
			continue
		}
		if !strings.Contains(err.Error(), ft.String()) {
			t.Errorf("the refusal for %s does not name it: %v", ft, err)
		}
		if !strings.Contains(err.Error(), "/srv/x") {
			t.Errorf("the refusal for %s does not name the path: %v", ft, err)
		}
	}
}

// The fail-closed half. A future filesystem is unsupported until its identity
// and notification behaviour are known, rather than admitted because this
// version happens to have no name for it.
func TestAnUnknownMagicIsRefused(t *testing.T) {
	for _, magic := range []FsType{0, 0xDEADBEEF, 0x1, 0xFFFFFFFF, 0xEF54} {
		if _, err := AdmitMount("/srv/x", magic, true); !errors.Is(err, ErrUnsupportedFilesystem) {
			t.Errorf("the unknown magic %#x gave %v, want a refusal", uint64(magic), err)
		}
	}
	// And one just beside a supported value, so the match is exact rather than
	// approximate.
	if _, err := AdmitMount("/srv/x", FsExt4+1, true); !errors.Is(err, ErrUnsupportedFilesystem) {
		t.Fatalf("a magic beside ext4 gave %v, want a refusal", err)
	}
}

// The named type is necessary and not sufficient. A device and inode pair
// alone cannot tell a file from a different file that reused the inode after a
// deletion.
func TestASupportedTypeWithNoBirthTimeIsRefused(t *testing.T) {
	for _, ft := range []FsType{FsExt4, FsBtrfs, FsXfs, FsZfs, FsF2fs, FsTmpfs} {
		_, err := AdmitMount("/srv/x", ft, false)
		if !errors.Is(err, ErrUnsupportedFilesystem) {
			t.Errorf("%s with no birth time gave %v, want a refusal", ft, err)
			continue
		}
		if !strings.Contains(err.Error(), "birth time") {
			t.Errorf("the refusal for %s does not say why: %v", ft, err)
		}
	}
}

// Registration is where the refusal happens, rather than the first operation
// that cannot hold its contract.
func TestRegistrationAdmitsARealDirectory(t *testing.T) {
	dir := t.TempDir()
	r, adm, err := RegisterShareRoot(1, dir, DefaultSharePolicy())
	if err != nil {
		// A temporary directory on an unsupported filesystem is a real
		// possibility on a developer's machine, and it is the gate working
		// rather than the test failing.
		t.Skipf("the temporary directory is on a filesystem this build refuses: %v", err)
	}
	defer func() {
		if cerr := r.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	if !adm.OK {
		t.Fatal("registration returned a verdict that is not admitted")
	}
}

// A refused share does not stop unrelated shares: one bad configuration entry
// is not an outage of every other share.
func TestARefusedShareDoesNotStopTheOthers(t *testing.T) {
	good := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	if _, _, err := RegisterShareRoot(1, missing, DefaultSharePolicy()); err == nil {
		t.Fatal("a missing share root was admitted")
	}
	r, _, err := RegisterShareRoot(2, good, DefaultSharePolicy())
	if err != nil {
		t.Skipf("the temporary directory is on a filesystem this build refuses: %v", err)
	}
	if cerr := r.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
}

// The share root's verdict does not bless a mount below it. This is the check
// that closes the otherwise trivial route of putting a network or user-space
// filesystem under an admitted root.
//
// A real nested mount needs privileges this test does not have, so the gate is
// driven directly at the point resolution calls it: the cache is primed with
// the root's device only, and a directory on a device the cache has not seen is
// classified rather than waved through.
func TestANestedMountIsClassifiedRatherThanInherited(t *testing.T) {
	dir := t.TempDir()
	policy := DefaultSharePolicy()
	policy.CrossMount = true

	r, _, err := RegisterShareRoot(1, dir, policy)
	if err != nil {
		t.Skipf("the temporary directory is on a filesystem this build refuses: %v", err)
	}
	defer func() {
		if cerr := r.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	// The root's own device is admitted, so it is not reclassified.
	if _, seen := r.admitted[r.dev]; !seen {
		t.Fatal("the share root's device was not recorded as admitted")
	}

	// A device the cache has not seen goes through the gate. Driving it with
	// the descriptor of a directory that really exists proves the call reaches
	// the filesystem rather than answering from the cache.
	sub := filepath.Join(dir, "nested")
	if merr := os.Mkdir(sub, 0o755); merr != nil {
		t.Fatalf("mkdir: %v", merr)
	}
	f, err := os.Open(sub) //nolint:gosec // G304 reads the variable: the path is this test's own temporary directory.
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	const unseenDev = ^uint64(0)
	if aerr := r.admitDevice(f, unseenDev, sub); aerr != nil {
		// The directory is really on the same filesystem as the root, so the
		// only way this refuses is the gate refusing that filesystem, which
		// the registration above already ruled out.
		t.Fatalf("a directory on the admitted filesystem was refused: %v", aerr)
	}
	// And the verdict is now cached, so the next traversal does not pay for it
	// again.
	if _, seen := r.admitted[unseenDev]; !seen {
		t.Fatal("the verdict was not cached")
	}
}

// A share that cannot cross a mount boundary needs no per-device check,
// because the kernel already refused the crossing.
func TestAShareThatCannotCrossNeedsNoCheck(t *testing.T) {
	dir := t.TempDir()
	policy := DefaultSharePolicy()
	policy.CrossMount = false

	r, _, err := RegisterShareRoot(1, dir, policy)
	if err != nil {
		t.Skipf("the temporary directory is on a filesystem this build refuses: %v", err)
	}
	defer func() {
		if cerr := r.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	f, err := os.Open(dir) //nolint:gosec // G304 reads the variable: the path is this test's own temporary directory.
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	if aerr := r.admitResolved(f, dir); aerr != nil {
		t.Fatalf("a non-crossing share paid for a check: %v", aerr)
	}
}

// Every supported filesystem admits, every other named one refuses, and the
// two sets do not overlap. A type in both would be a gate whose answer depends
// on which branch ran.
func TestTheAllowListAndTheRefusalsDoNotOverlap(t *testing.T) {
	supported := []FsType{FsExt4, FsBtrfs, FsXfs, FsZfs, FsF2fs, FsTmpfs}
	refused := []FsType{FsOverlay, FsFuse, FsNfs, FsCifs, FsSmb2, FsSquashfs, FsNtfs}

	for _, a := range supported {
		for _, b := range refused {
			if a == b {
				t.Fatalf("%s is in both lists", a)
			}
		}
	}
	for _, ft := range supported {
		if adm, _ := AdmitFsType(ft); !adm.OK {
			t.Errorf("%s is in the supported list and does not admit", ft)
		}
	}
	for _, ft := range refused {
		if adm, reason := AdmitFsType(ft); adm.OK || reason == "" {
			t.Errorf("%s is in the refused list and admits, or refuses without a reason", ft)
		}
	}
}
