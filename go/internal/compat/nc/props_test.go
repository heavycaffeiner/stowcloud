//go:build linux && compat_nc

package nc

import (
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// The permission string is the highest-risk value in this package: a wrong
// letter makes desktop clients refuse to sync without reporting an error.

const allPerms = ncport.Read | ncport.Write | ncport.Create | ncport.Delete |
	ncport.Rename | ncport.Move | ncport.Share | ncport.Download

func TestTheMaximalPermissionStrings(t *testing.T) {
	// The reference's maximal strings, minus M, which is never emitted.
	if got := DavPermissions(allPerms, false, true); got != "SRGDNVW" {
		t.Fatalf("a fully permitted shared file is %q, want SRGDNVW", got)
	}
	if got := DavPermissions(allPerms, true, true); got != "SRGDNVCK" {
		t.Fatalf("a fully permitted shared directory is %q, want SRGDNVCK", got)
	}
}

// The order is not alphabetical and is not free to choose, because some
// clients string-compare the value.
func TestTheLetterOrderIsTheReferenceOrder(t *testing.T) {
	got := DavPermissions(allPerms, true, true)
	want := []byte("SRGDNVCK")
	if got != string(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Each letter appears after the one before it, which is what a
	// string-comparing client depends on.
	for i := 1; i < len(want); i++ {
		a := strings.IndexByte(got, want[i-1])
		b := strings.IndexByte(got, want[i])
		if a < 0 || b < 0 || a > b {
			t.Fatalf("%c and %c are out of order in %q", want[i-1], want[i], got)
		}
	}
}

// M claims an external mount, which makes clients apply mount-specific move
// restrictions. There is no such concept here, so it is never emitted.
func TestTheMountLetterIsNeverEmitted(t *testing.T) {
	for _, isDir := range []bool{false, true} {
		for _, shared := range []bool{false, true} {
			if got := DavPermissions(allPerms, isDir, shared); strings.ContainsRune(got, 'M') {
				t.Fatalf("got %q, which claims a mount", got)
			}
		}
	}
}

// Each letter costs something specific when it is missing, so each is tied to
// the permission that produces it.
func TestEachLetterFollowsItsPermission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		perms  ncport.Perms
		isDir  bool
		letter byte
		absent bool
	}{
		{"read gives G", ncport.Read, false, 'G', false},
		{"no read, no G", ncport.Write, false, 'G', true},
		{"delete gives D", ncport.Delete, false, 'D', false},
		{"rename gives N", ncport.Rename, false, 'N', false},
		{"move gives V", ncport.Move, false, 'V', false},
		{"share gives R", ncport.Share, false, 'R', false},
		// Missing W means the client treats the file read-only and never
		// uploads a local edit.
		{"write gives W on a file", ncport.Write, false, 'W', false},
		{"a directory never carries W", ncport.Write, true, 'W', true},
		// Missing C or K means nothing can be created inside the directory.
		{"create gives C on a directory", ncport.Create, true, 'C', false},
		{"create gives K on a directory", ncport.Create, true, 'K', false},
		{"a file never carries C", ncport.Create, false, 'C', true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DavPermissions(tc.perms, tc.isDir, false)
			has := strings.ContainsRune(got, rune(tc.letter))
			if has == tc.absent {
				t.Fatalf("DavPermissions(%v, dir=%v) = %q; %c should be %v",
					tc.perms, tc.isDir, got, tc.letter,
					map[bool]string{true: "absent", false: "present"}[tc.absent])
			}
		})
	}
}

// Write and move are separate here where the reference folds them together, so
// a file with one and not the other gets one letter and not both.
func TestWriteAndMoveAreSeparateLetters(t *testing.T) {
	if got := DavPermissions(ncport.Read|ncport.Write, false, false); got != "GW" {
		t.Fatalf("write alone is %q, want GW", got)
	}
	if got := DavPermissions(ncport.Read|ncport.Move, false, false); got != "GV" {
		t.Fatalf("move alone is %q, want GV", got)
	}
}

// The S is what a shared entry carries, and it is the only letter that comes
// from something other than the permission set.
func TestSharedAddsTheLeadingLetter(t *testing.T) {
	plain := DavPermissions(ncport.Read, false, false)
	shared := DavPermissions(ncport.Read, false, true)
	if plain != "G" || shared != "SG" {
		t.Fatalf("plain = %q and shared = %q, want G and SG", plain, shared)
	}
}

// An empty string makes the client ignore the entry entirely, so a read-only
// share root must still carry G.
func TestAReadOnlyEntryIsNotEmpty(t *testing.T) {
	got := DavPermissions(ncport.Read, true, false)
	if got == "" {
		t.Fatal("a read-only directory produced an empty string, which the client ignores")
	}
	if !strings.ContainsRune(got, 'G') {
		t.Fatalf("a readable entry is %q and does not carry G", got)
	}
	// And an entry with genuinely nothing is empty, which is the honest
	// answer: the caller should not be emitting it at all.
	if got := DavPermissions(0, false, false); got != "" {
		t.Fatalf("an entry with no permissions is %q, want empty", got)
	}
}

// The id is zero-padded to at least eight digits and concatenated with the
// instance id, with no separator.
func TestTheDavIDIsPaddedAndConcatenated(t *testing.T) {
	if got := DavID(1, "abc123"); got != "00000001abc123" {
		t.Fatalf("DavID = %q, want 00000001abc123", got)
	}
	// A larger id gets longer rather than being truncated.
	if got := DavID(1234567890, "abc123"); got != "1234567890abc123" {
		t.Fatalf("DavID = %q, want the id untruncated", got)
	}
	// The sentinel is a real value, so a client sees a wrong id rather than a
	// missing entry.
	if got := DavID(0, "abc123"); got != "00000000abc123" {
		t.Fatalf("the sentinel is %q", got)
	}
}

func TestSharePermissionsMapsOntoTheBitmask(t *testing.T) {
	if got := SharePermissions(allPerms); got != SharePermAll {
		t.Fatalf("everything maps to %d, want %d", got, SharePermAll)
	}
	if got := SharePermissions(ncport.Read); got != SharePermRead {
		t.Fatalf("read maps to %d, want %d", got, SharePermRead)
	}
	if got := SharePermissions(0); got != 0 {
		t.Fatalf("nothing maps to %d, want 0", got)
	}
}

// The emitter's two fallbacks, both of which exist because a partial answer
// beats a dropped one.

func wantNames(names ...PropName) []PropName { return names }

func oc(local string) PropName { return PropName{Space: NSOwnCloud, Local: local} }

func TestAnEntryWithoutAFileIDGetsTheSentinelNotAnOmission(t *testing.T) {
	warned := 0
	src := NewPropSource(PropSourceDeps{
		InstanceID: "inst",
		FileID:     func(ncport.Entry) (FileID, bool) { return 0, false },
		Warn:       func(string, ...any) { warned++ },
	})

	got := src.Emit(ncport.Entry{Name: "a.txt", Perms: ncport.Read},
		wantNames(oc("id"), oc("fileid"), oc("permissions")))

	// The whole set is still emitted: a wrong id is visible and debuggable,
	// and a silently missing entry is not.
	if len(got) != 3 {
		t.Fatalf("got %d properties, want the whole set: %+v", len(got), got)
	}
	if warned == 0 {
		t.Fatal("an entry without a file id was not reported")
	}
	for _, p := range got {
		if p.Local == "fileid" && p.Value != "0" {
			t.Fatalf("the sentinel id is %q, want 0", p.Value)
		}
	}
}

// A share lookup that fails falls back to "not shared" rather than dropping
// the set, because a missing permissions string is worse than a missing S.
func TestAFailedShareLookupStillEmitsPermissions(t *testing.T) {
	src := NewPropSource(PropSourceDeps{
		InstanceID: "inst",
		FileID:     func(ncport.Entry) (FileID, bool) { return 7, true },
		Shared:     func(ncport.Entry) bool { return false },
	})

	got := src.Emit(ncport.Entry{Name: "a.txt", Perms: ncport.Read | ncport.Write},
		wantNames(oc("permissions")))
	if len(got) != 1 {
		t.Fatalf("got %+v, want the permissions property", got)
	}
	if got[0].Value != "GW" {
		t.Fatalf("permissions = %q, want GW without the S", got[0].Value)
	}
}

// A directory with no rollup gets no size property at all. Falling back to the
// inode's own size announced four kilobytes for a folder holding a terabyte,
// which is plausible enough that nobody reads it as an error.
func TestADirectoryWithoutARollupOmitsItsSize(t *testing.T) {
	src := NewPropSource(PropSourceDeps{
		InstanceID: "inst",
		FileID:     func(ncport.Entry) (FileID, bool) { return 7, true },
		Aggregate:  func(ncport.Entry) (uint64, bool) { return 0, false },
	})

	got := src.Emit(ncport.Entry{Name: "folder", IsDir: true, Size: 4096},
		wantNames(oc("size")))
	for _, p := range got {
		if p.Local == "size" {
			t.Fatalf("a directory with no rollup emitted a size of %q, "+
				"which is the inode's own size and not the folder's", p.Value)
		}
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want nothing", got)
	}
}

func TestADirectoryWithARollupReportsIt(t *testing.T) {
	src := NewPropSource(PropSourceDeps{
		InstanceID: "inst",
		FileID:     func(ncport.Entry) (FileID, bool) { return 7, true },
		Aggregate:  func(ncport.Entry) (uint64, bool) { return 1 << 40, true },
	})
	got := src.Emit(ncport.Entry{Name: "folder", IsDir: true, Size: 4096},
		wantNames(oc("size")))
	if len(got) != 1 || got[0].Value != "1099511627776" {
		t.Fatalf("got %+v, want the recursive rollup", got)
	}
}

func TestAFileReportsItsPlainSize(t *testing.T) {
	src := NewPropSource(PropSourceDeps{
		InstanceID: "inst",
		FileID:     func(ncport.Entry) (FileID, bool) { return 7, true },
	})
	got := src.Emit(ncport.Entry{Name: "a.txt", Size: 512}, wantNames(oc("size")))
	if len(got) != 1 || got[0].Value != "512" {
		t.Fatalf("got %+v, want 512", got)
	}
}

// A property nobody asked for is not computed, because several of them cost a
// lookup.
func TestAnUnrequestedPropertyIsNotComputed(t *testing.T) {
	lookups := 0
	src := NewPropSource(PropSourceDeps{
		InstanceID: "inst",
		FileID:     func(ncport.Entry) (FileID, bool) { return 7, true },
		Shared: func(ncport.Entry) bool {
			lookups++
			return true
		},
	})

	src.Emit(ncport.Entry{Name: "a.txt"}, wantNames(oc("fileid")))
	if lookups != 0 {
		t.Fatalf("the share lookup ran %d times for a request that did not need it", lookups)
	}

	src.Emit(ncport.Entry{Name: "a.txt"}, wantNames(oc("permissions")))
	if lookups != 1 {
		t.Fatalf("the share lookup ran %d times, want once", lookups)
	}
}

// The encryption flag is zero, which is what a reference server answers for an
// unencrypted folder too. Answering otherwise is one of the three server-side
// lies that would suppress a client-side tick.
func TestTheEncryptionFlagIsTruthful(t *testing.T) {
	src := NewPropSource(PropSourceDeps{
		InstanceID: "inst",
		FileID:     func(ncport.Entry) (FileID, bool) { return 7, true },
	})
	got := src.Emit(ncport.Entry{Name: "folder", IsDir: true},
		wantNames(PropName{Space: NSNextcloudX, Local: "is-encrypted"}))
	if len(got) != 1 || got[0].Value != "0" {
		t.Fatalf("got %+v, want the honest 0", got)
	}
}

func TestTheSourceClaimsBothVendorNamespaces(t *testing.T) {
	src := NewPropSource(PropSourceDeps{InstanceID: "inst"})
	got := src.Namespaces()
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}
