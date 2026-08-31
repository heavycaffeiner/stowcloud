package vfs

import "testing"

func TestKindIsDirIsStrictEquality(t *testing.T) {
	if !KindDir.IsDir() {
		t.Fatal("KindDir.IsDir() should be true")
	}
	for _, k := range []Kind{KindOther, KindFile, KindSymlink} {
		if k.IsDir() {
			t.Fatalf("%v.IsDir() should be false", k)
		}
	}
}

func TestKindString(t *testing.T) {
	cases := map[Kind]string{
		KindOther:   "other",
		KindFile:    "file",
		KindDir:     "dir",
		KindSymlink: "symlink",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", k, got, want)
		}
	}
}

func TestParseSymlinkPolicyRoundTrips(t *testing.T) {
	for _, s := range []string{"deny", "within_share", "follow"} {
		p, err := ParseSymlinkPolicy(s)
		if err != nil {
			t.Fatalf("ParseSymlinkPolicy(%q): %v", s, err)
		}
		if p.String() != s {
			t.Fatalf("ParseSymlinkPolicy(%q).String() = %q", s, p.String())
		}
	}
}

// TestParseSymlinkPolicyRefusesUnknown is the trust-boundary check: an
// unrecognized configuration value must refuse rather than silently pick
// SymlinkDeny.
func TestParseSymlinkPolicyRefusesUnknown(t *testing.T) {
	if _, err := ParseSymlinkPolicy("allow-everything"); err == nil {
		t.Fatal("an unrecognized policy string should be refused")
	}
	if _, err := ParseSymlinkPolicy(""); err == nil {
		t.Fatal("an empty policy string should be refused")
	}
}

func TestDefaultSharePolicy(t *testing.T) {
	p := DefaultSharePolicy()
	if p.Symlink != SymlinkDeny {
		t.Errorf("Symlink = %v, want SymlinkDeny", p.Symlink)
	}
	if !p.CrossMount {
		t.Error("CrossMount should default to true")
	}
	if p.ModeFile != 0o664 || p.ModeDir != 0o775 {
		t.Errorf("modes = %o/%o, want 0664/0775", p.ModeFile, p.ModeDir)
	}
	if p.Chown != nil {
		t.Error("Chown should default to nil")
	}
}

func TestFsSpaceUsed(t *testing.T) {
	s := FsSpace{Total: 100, Free: 40}
	if got := s.Used(); got != 60 {
		t.Errorf("Used() = %d, want 60", got)
	}
	// A filesystem reporting Free above Total is a fact this package does
	// not trust; Used must not underflow into a huge unsigned number.
	broken := FsSpace{Total: 10, Free: 20}
	if got := broken.Used(); got != 0 {
		t.Errorf("Used() with Free > Total = %d, want 0", got)
	}
}

func TestFsTypeString(t *testing.T) {
	cases := map[FsType]string{
		FsExt4:             "ext4",
		FsBtrfs:            "btrfs",
		FsXfs:              "xfs",
		FsZfs:              "zfs",
		FsF2fs:             "f2fs",
		FsTmpfs:            "tmpfs",
		FsOverlay:          "overlay",
		FsFuse:             "fuse",
		FsNfs:              "nfs",
		FsCifs:             "cifs",
		FsSmb2:             "cifs",
		FsSquashfs:         "squashfs",
		FsNtfs:             "ntfs",
		FsType(0xDEADBEEF): "unknown",
	}
	for fs, want := range cases {
		if got := fs.String(); got != want {
			t.Errorf("%#x.String() = %q, want %q", uint64(fs), got, want)
		}
	}
}

func TestReservedPolicyDefaultIsHide(t *testing.T) {
	var zero ReservedPolicy
	if zero != HideReserved {
		t.Fatal("the zero value of ReservedPolicy should be HideReserved")
	}
	if IncludeReserved == HideReserved {
		t.Fatal("IncludeReserved and HideReserved must be distinct")
	}
}

func TestAccessIntentDefaultIsRead(t *testing.T) {
	var zero AccessIntent
	if zero != IntentRead {
		t.Fatal("the zero value of AccessIntent should be IntentRead")
	}
	if IntentReadWrite == IntentRead {
		t.Fatal("IntentReadWrite and IntentRead must be distinct")
	}
}
