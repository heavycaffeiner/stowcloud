//go:build linux

package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every filesystem type this build supports admits with the reflink and
// warning shape the table promises, given a mount reporting a birth time.
func TestAdmitFsTypeSupportedTable(t *testing.T) {
	for _, tc := range []struct {
		t           FsType
		wantReflink bool
		wantWarn    bool
	}{
		{FsExt4, false, false},
		{FsZfs, false, false},
		{FsF2fs, false, false},
		{FsBtrfs, true, false},
		{FsXfs, true, false},
		{FsTmpfs, false, true},
	} {
		adm, err := AdmitMount("/srv/share", tc.t, true)
		if err != nil {
			t.Errorf("%s: refused, %v", tc.t, err)
			continue
		}
		if adm.Reflink != tc.wantReflink {
			t.Errorf("%s: reflink = %v, want %v", tc.t, adm.Reflink, tc.wantReflink)
		}
		if (adm.Warn != "") != tc.wantWarn {
			t.Errorf("%s: warn = %q, want present = %v", tc.t, adm.Warn, tc.wantWarn)
		}
	}
}

// Every named, unsupported type refuses, and the refusal names both the
// filesystem and the path: "unsupported filesystem" alone gives an operator
// nothing to act on.
func TestAdmitFsTypeRefusalsAreNamed(t *testing.T) {
	for _, ft := range []FsType{FsOverlay, FsFuse, FsNfs, FsCifs, FsSmb2, FsSquashfs, FsNtfs} {
		_, err := AdmitMount("/srv/nested", ft, true)
		if !errors.Is(err, ErrUnsupportedFilesystem) {
			t.Errorf("%s: %v, want ErrUnsupportedFilesystem", ft, err)
			continue
		}
		if !strings.Contains(err.Error(), ft.String()) {
			t.Errorf("%s: refusal does not name the type: %v", ft, err)
		}
		if !strings.Contains(err.Error(), "/srv/nested") {
			t.Errorf("%s: refusal does not name the path: %v", ft, err)
		}
	}
}

// The fail-closed half: a magic number this build has never classified
// refuses by falling through to the default case, not by matching a
// known-bad entry, and the message must not claim to know what it is.
func TestAdmitFsTypeUnclassifiedMagicRefuses(t *testing.T) {
	for _, magic := range []FsType{0, 1, 0xDEADBEEF, 0xFFFFFFFF, FsExt4 + 1} {
		adm, err := AdmitMount("/srv/x", magic, true)
		if !errors.Is(err, ErrUnsupportedFilesystem) {
			t.Errorf("magic %#x: %v, want a refusal", uint64(magic), err)
		}
		if adm.OK {
			t.Errorf("magic %#x: admitted", uint64(magic))
		}
		for _, known := range []FsType{FsExt4, FsBtrfs, FsXfs, FsOverlay, FsNfs} {
			if magic == known {
				continue
			}
			if strings.Contains(err.Error(), known.String()) {
				t.Errorf("magic %#x: refusal names %s, which it is not", uint64(magic), known)
			}
		}
	}
}

// A supported type with no birth time still refuses: the type alone is
// necessary and not sufficient, since without a birth time an inode reused
// after a deletion cannot be told apart from the file that had it before.
func TestAdmitMountRefusesNoBirthTimeEvenWhenSupported(t *testing.T) {
	for _, ft := range []FsType{FsExt4, FsBtrfs, FsXfs, FsZfs, FsF2fs, FsTmpfs} {
		_, err := AdmitMount("/srv/x", ft, false)
		if !errors.Is(err, ErrUnsupportedFilesystem) {
			t.Errorf("%s with no birth time: %v, want a refusal", ft, err)
			continue
		}
		if !strings.Contains(err.Error(), "birth time") {
			t.Errorf("%s: refusal does not say why: %v", ft, err)
		}
	}
}

// The supported and refused sets never overlap, and every member of each
// answers as its own list says it should. A type present in both would make
// the verdict depend on which branch of some caller's own logic ran first.
func TestAdmitFsTypeListsDoNotOverlap(t *testing.T) {
	supported := []FsType{FsExt4, FsBtrfs, FsXfs, FsZfs, FsF2fs, FsTmpfs}
	refused := []FsType{FsOverlay, FsFuse, FsNfs, FsCifs, FsSmb2, FsSquashfs, FsNtfs}
	for _, a := range supported {
		for _, b := range refused {
			if a == b {
				t.Fatalf("%s appears in both lists", a)
			}
		}
	}
	for _, ft := range supported {
		if adm, _ := AdmitFsType(ft); !adm.OK {
			t.Errorf("%s is listed supported but AdmitFsType refuses it", ft)
		}
	}
	for _, ft := range refused {
		if adm, reason := AdmitFsType(ft); adm.OK || reason == "" {
			t.Errorf("%s is listed refused but admits, or gives no reason", ft)
		}
	}
}

// Registration is where the refusal happens, on a real filesystem: a
// developer's temp directory can legitimately sit on tmpfs, which this gate
// admits with a warning rather than refusing.
func TestRegisterShareRootAdmitsARealDirectory(t *testing.T) {
	dir := t.TempDir()
	r, adm, err := RegisterShareRoot(1, dir, DefaultSharePolicy())
	if err != nil {
		t.Skipf("this host's temp directory is on a filesystem this build refuses: %v", err)
	}
	t.Cleanup(func() {
		if cerr := r.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	if !adm.OK {
		t.Fatal("registration returned an unadmitted verdict for a real directory")
	}
}

// A refused registration closes the anchor and leaves nothing half open; a
// missing host path is refused independent of admission, and does not
// prevent an unrelated share from registering successfully right after.
func TestRegisterShareRootRefusalDoesNotAffectOtherShares(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, _, err := RegisterShareRoot(1, missing, DefaultSharePolicy()); err == nil {
		t.Fatal("a nonexistent host path registered")
	}

	good := t.TempDir()
	r, _, err := RegisterShareRoot(2, good, DefaultSharePolicy())
	if err != nil {
		t.Skipf("this host's temp directory is on a filesystem this build refuses: %v", err)
	}
	if cerr := r.Close(); cerr != nil {
		t.Errorf("close: %v", cerr)
	}
}

// The share root's own verdict does not extend to a mount reached below it.
// A real nested mount needs privilege this test does not assume, so the
// gate is driven directly at admitDevice with an unseen device number,
// proving the check runs against the filesystem rather than being skipped
// because the directory happens to be real.
func TestAdmitDeviceClassifiesRatherThanInherits(t *testing.T) {
	dir := t.TempDir()
	policy := DefaultSharePolicy()
	policy.CrossMount = true

	r, _, err := RegisterShareRoot(1, dir, policy)
	if err != nil {
		t.Skipf("this host's temp directory is on a filesystem this build refuses: %v", err)
	}
	t.Cleanup(func() {
		if cerr := r.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})

	if _, seen := r.admitted[r.dev]; !seen {
		t.Fatal("the share root's own device was not recorded as admitted at registration")
	}

	sub := filepath.Join(dir, "nested")
	if mkerr := os.Mkdir(sub, 0o755); mkerr != nil {
		t.Fatalf("mkdir: %v", mkerr)
	}
	f, err := os.Open(sub)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})

	const unseenDev = ^uint64(0)
	if err := r.admitDevice(f, unseenDev, sub); err != nil {
		// sub really is on the same filesystem as the root, which
		// registration already proved admits, so a refusal here can only
		// mean admitDevice itself is broken.
		t.Fatalf("a directory on an already-admitted filesystem was refused: %v", err)
	}
	if _, seen := r.admitted[unseenDev]; !seen {
		t.Fatal("the verdict for the unseen device was not cached")
	}
}

// The cache actually saves a call: once a device is classified, admitDevice
// must not need to reach the filesystem again to answer for it. Passing a
// nil descriptor for the cached path would panic on a Statx call, so a
// clean return proves the second call short-circuited on the cache.
func TestAdmitDeviceIsCachedPerDevice(t *testing.T) {
	dir := t.TempDir()
	r, _, err := RegisterShareRoot(1, dir, DefaultSharePolicy())
	if err != nil {
		t.Skipf("this host's temp directory is on a filesystem this build refuses: %v", err)
	}
	t.Cleanup(func() {
		if cerr := r.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})

	if err := r.admitDevice(nil, r.dev, dir); err != nil {
		t.Fatalf("a cached device paid for a filesystem call and got: %v", err)
	}
}
