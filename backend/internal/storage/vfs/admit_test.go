//go:build linux

package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Every filesystem type this build supports admits with the reflink and
// warning shape the table promises.
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
		adm, reason := AdmitFsType(tc.t)
		if !adm.OK {
			t.Errorf("%s: refused, %s", tc.t, reason)
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

// The fail-closed half: a magic number this build has never classified
// refuses.
func TestAdmitFsTypeUnclassifiedMagicRefuses(t *testing.T) {
	for _, magic := range []FsType{0, 1, 0xDEADBEEF, 0xFFFFFFFF, FsExt4 + 1} {
		adm, reason := AdmitFsType(magic)
		if adm.OK || reason == "" {
			t.Errorf("magic %#x: admitted, or refused without a reason", uint64(magic))
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

// Scratch space is not a share, and the constructor that opens it says so
// rather than borrowing an id. What it does not skip is admission: a
// filesystem this build cannot hold its contracts on is the same problem
// under the spool as under a share.
func TestScratchRootIsAdmittedAndMarked(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenScratchRoot(dir, DefaultSharePolicy())
	if err != nil {
		t.Skipf("this host's temp directory is on a filesystem this build refuses: %v", err)
	}
	t.Cleanup(func() {
		if cerr := r.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	if !r.IsScratch() {
		t.Fatal("a scratch root does not report itself as one")
	}

	share, _, err := RegisterShareRoot(1, t.TempDir(), DefaultSharePolicy())
	if err != nil {
		t.Skipf("this host's temp directory is on a filesystem this build refuses: %v", err)
	}
	t.Cleanup(func() {
		if cerr := share.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	if share.IsScratch() {
		t.Fatal("a registered share reports itself as scratch space")
	}
}

// The two constructors refuse the same things, so scratch space is not a way
// around the gate.
func TestScratchRootRefusesWhatAShareRefuses(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := OpenScratchRoot(missing, DefaultSharePolicy()); err == nil {
		t.Fatal("a nonexistent directory opened as scratch space")
	}
	if _, _, err := RegisterShareRoot(1, missing, DefaultSharePolicy()); err == nil {
		t.Fatal("a nonexistent directory registered as a share")
	}
}

// A directory whose mode denies reading stands in for a Landlock domain that
// permits resolving a path but not reading it. Registration reports it as a
// sandbox refusal that still unwraps to ErrDenied.
func TestRegisterShareRootNamesTheOpenProveAsymmetry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the mode bit this test depends on")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "denied")
	if err := os.Mkdir(dir, 0o000); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() {
		if cerr := os.Chmod(dir, 0o755); cerr != nil {
			t.Errorf("restoring the directory mode: %v", cerr)
		}
	})

	_, _, err := RegisterShareRoot(1, dir, DefaultSharePolicy())
	if err == nil {
		t.Fatal("a directory with mode 0000 registered")
	}
	if !errors.Is(err, ErrSandboxDenied) {
		t.Fatalf("RegisterShareRoot(%q) = %v, want ErrSandboxDenied", dir, err)
	}
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("RegisterShareRoot(%q) = %v, want it to still unwrap to ErrDenied", dir, err)
	}
}
