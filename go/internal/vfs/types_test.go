package vfs

import "testing"

// The support decision is a whitelist, and the thing worth testing is the
// default rather than the entries: a filesystem magic nobody classified must
// not become registrable by having been forgotten.
func TestFilesystemSupportFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		fs   FsType
		want bool
	}{
		{FsExt4, true},
		{FsBtrfs, true},
		{FsXfs, true},
		{FsZfs, true},
		{FsF2fs, true},
		{FsTmpfs, true},
		{FsOverlay, false},
		{FsFuse, false},
		{FsNfs, false},
		{FsCifs, false},
		{FsSmb2, false},
		{FsSquashfs, false},
		{FsNtfs, false},
		{FsType(0), false},
		{FsType(0xDEADBEEF), false},
	} {
		if got := tc.fs.Supported(); got != tc.want {
			t.Errorf("%s (%#x): Supported = %v, want %v", tc.fs, uint64(tc.fs), got, tc.want)
		}
	}
}
