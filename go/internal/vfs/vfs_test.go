//go:build linux

package vfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"golang.org/x/sys/unix"
)

func hostStat(t *testing.T, path string) unix.Stat_t {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return st
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func mustOpen(t *testing.T, r *ShareRoot, path string, intent AccessIntent) *File {
	t.Helper()
	f, err := r.OpenRead(mustParse(t, path), intent)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { closeAfter(f.f, path) })
	return f
}

func names(entries []DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	slices.Sort(out)
	return out
}

// F6. What a directory read returns is decided at the call site, visibly, and
// not by whatever the thread was last used for.
func TestReservedPolicyIsAnArgument(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "visible.txt"), "x")
	write(t, filepath.Join(host, ".scpart-0011223344556677"), "x")
	write(t, filepath.Join(host, ".sctrash"), "x")
	r := share(t, host, denyPolicy())

	hidden, err := r.ReadDir(RootPath(), HideReserved)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := names(hidden); !slices.Equal(got, []string{"visible.txt"}) {
		t.Fatalf("hiding reserved names listed %v", got)
	}

	all, err := r.ReadDir(RootPath(), IncludeReserved)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("including reserved names listed %v", names(all))
	}
}

// F4. The primitive streams; the collecting wrapper is what carries a bound.
func TestReadDirFuncStreamsAndStopsWhenAsked(t *testing.T) {
	host := t.TempDir()
	for i := range 50 {
		write(t, filepath.Join(host, fmt.Sprintf("f%02d", i)), "x")
	}
	r := share(t, host, denyPolicy())

	seen := 0
	if err := r.ReadDirFunc(RootPath(), HideReserved, func(DirEntry) bool {
		seen++
		return seen < 3
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if seen != 3 {
		t.Fatalf("the walk saw %d entries after being told to stop at 3", seen)
	}
}

// D5. The bound is what refuses, not a large directory happening to fail. The
// stream is synthetic so the assertion is about the bound and not about how
// long it takes to create a hundred thousand files.
func TestBufferedReadRefusesAtTheBound(t *testing.T) {
	stream := func(n int) func(func(DirEntry) bool) error {
		return func(fn func(DirEntry) bool) error {
			for i := range n {
				if !fn(DirEntry{Name: fmt.Sprintf("e%d", i), Kind: KindFile}) {
					return nil
				}
			}
			return nil
		}
	}

	const bound = 8
	got, err := collectBounded(bound, stream(bound))
	if err != nil {
		t.Fatalf("exactly the bound was refused: %v", err)
	}
	if len(got) != bound {
		t.Fatalf("collected %d of %d", len(got), bound)
	}
	if _, err := collectBounded(bound, stream(bound+1)); !errors.Is(err, limits.ErrTooLarge) {
		t.Fatalf("one past the bound = %v, want ErrTooLarge", err)
	}
}

func TestReadDirReportsKindAndInode(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "file"), "x")
	if err := os.Mkdir(filepath.Join(host, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(host, "link")); err != nil {
		t.Fatal(err)
	}
	r := share(t, host, denyPolicy())

	entries, err := r.ReadDir(RootPath(), HideReserved)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := map[string]Kind{"file": KindFile, "dir": KindDir, "link": KindSymlink}
	for _, e := range entries {
		if want[e.Name] != e.Kind {
			t.Fatalf("%s reported as %s, want %s", e.Name, e.Kind, want[e.Name])
		}
		if e.Ino != hostStat(t, filepath.Join(host, e.Name)).Ino {
			t.Fatalf("%s reported inode %d", e.Name, e.Ino)
		}
	}
	if len(entries) != 3 {
		t.Fatalf("listed %v", names(entries))
	}
	// "." and ".." are skipped rather than surfaced as names a caller then has
	// to filter, which is where the filtering gets forgotten.
	if slices.Contains(names(entries), ".") {
		t.Fatal("the walk surfaced '.'")
	}
}

// F5. Privilege on a descriptor is what the caller asked for, not what the
// file's mode happens to allow.
func TestReadIntentDoesNotYieldAWritableDescriptor(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "f"), "content")
	r := share(t, host, denyPolicy())

	f, err := r.OpenRead(mustParse(t, "f"), IntentRead)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer closeAfter(f.f, "read handle")
	if _, werr := f.WriteAt([]byte("no"), 0); werr == nil {
		t.Fatal("a read handle on a writable file could write")
	}

	rw, err := r.OpenRead(mustParse(t, "f"), IntentReadWrite)
	if err != nil {
		t.Fatalf("open read-write: %v", err)
	}
	defer closeAfter(rw.f, "read-write handle")
	if _, err := rw.WriteAt([]byte("ok"), 0); err != nil {
		t.Fatalf("the one call site that asks for a writable handle could not write: %v", err)
	}
}

func TestStatCarriesWhatTheCompatLayerNeeds(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "f"), "content")
	r := share(t, host, denyPolicy())

	st, err := r.Stat(mustParse(t, "f"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size != 7 {
		t.Fatalf("size = %d", st.Size)
	}
	if st.Kind != KindFile {
		t.Fatalf("kind = %s", st.Kind)
	}
	if st.Ino != hostStat(t, filepath.Join(host, "f")).Ino {
		t.Fatalf("inode = %d", st.Ino)
	}
	if st.MtimeNs == 0 {
		t.Fatal("mtime is zero")
	}
	// btime is a per-filesystem fact and its absence is reported rather than
	// faked, so this asserts the shape and not that this filesystem has one.
	if st.BtimeNs != nil && *st.BtimeNs == 0 {
		t.Fatal("btime is present and zero, which is neither of the two answers")
	}
}

// F7. A replacement inherits the mode of what it replaced, and the result of
// putting it back is checked. Without the check the file lands with the
// configured mode and the neighbours lose access to it.
func TestDurableWriteRestoresTheReplacedMode(t *testing.T) {
	host := t.TempDir()
	target := filepath.Join(host, "shared.txt")
	write(t, target, "old")
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatal(err)
	}
	r := share(t, host, denyPolicy())

	done, err := r.WriteDurable(mustParse(t, "shared.txt"), DurableOpts{Mode: 0o664},
		func(f *File) error {
			_, werr := f.WriteAt([]byte("new"), 0)
			return werr
		})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !done.Replaced {
		t.Fatal("the write did not report replacing an existing file")
	}
	if got := hostStat(t, target).Mode & 0o7777; got != 0o600 {
		t.Fatalf("the replacement landed with mode %o, want the 0600 it replaced", got)
	}
	body := readFile(t, target)
	if body != "new" {
		t.Fatalf("content = %q", body)
	}
}

func TestDurableWriteAppliesTheConfiguredModeToANewFile(t *testing.T) {
	host := t.TempDir()
	r := share(t, host, denyPolicy())

	done, err := r.WriteDurable(mustParse(t, "fresh.txt"), DurableOpts{Mode: 0o640},
		func(f *File) error {
			_, werr := f.WriteAt([]byte("x"), 0)
			return werr
		})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if done.Replaced {
		t.Fatal("a new file reported replacing something")
	}
	// Verbatim, not filtered through umask, which the default 022 would have
	// turned into 0640 anyway; 0666 is the one a umask would visibly change.
	if got := hostStat(t, filepath.Join(host, "fresh.txt")).Mode & 0o7777; got != 0o640 {
		t.Fatalf("mode = %o, want 0640", got)
	}
}

func TestDurableWriteAppliesAModeAUmaskWouldHaveStripped(t *testing.T) {
	old := unix.Umask(0o077)
	t.Cleanup(func() { unix.Umask(old) })

	host := t.TempDir()
	r := share(t, host, denyPolicy())
	if _, err := r.WriteDurable(mustParse(t, "g.txt"), DurableOpts{Mode: 0o664},
		func(f *File) error { _, werr := f.WriteAt([]byte("x"), 0); return werr }); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := hostStat(t, filepath.Join(host, "g.txt")).Mode & 0o7777; got != 0o664 {
		t.Fatalf("mode = %o under a 0077 umask, want the configured 0664", got)
	}
}

// The staging name is unlistable while it exists, and a failed write leaves
// neither it nor a damaged destination behind.
func TestDurableWriteLeavesNothingBehindOnFailure(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "keep.txt"), "original")
	r := share(t, host, denyPolicy())

	boom := errors.New("the caller's write failed")
	_, err := r.WriteDurable(mustParse(t, "keep.txt"), DurableOpts{Mode: 0o664},
		func(*File) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("write = %v, want the caller's own error", err)
	}

	body := readFile(t, filepath.Join(host, "keep.txt"))
	if body != "original" {
		t.Fatalf("the destination changed to %q", body)
	}

	entries, err := r.ReadDir(RootPath(), IncludeReserved)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if IsStagingName(e.Name) {
			t.Fatalf("a staging file survived: %s", e.Name)
		}
	}
}

// While a write is in flight the staging file exists, and no user-facing
// listing may show it.
func TestTheStagingFileIsUnlistableWhileItExists(t *testing.T) {
	host := t.TempDir()
	r := share(t, host, denyPolicy())

	if _, err := r.WriteDurable(mustParse(t, "x.txt"), DurableOpts{Mode: 0o664},
		func(*File) error {
			entries, err := r.ReadDir(RootPath(), HideReserved)
			if err != nil {
				return err
			}
			if len(entries) != 0 {
				return fmt.Errorf("a write in flight was visible: %v", names(entries))
			}
			all, err := r.ReadDir(RootPath(), IncludeReserved)
			if err != nil {
				return err
			}
			if len(all) != 1 || !IsStagingName(all[0].Name) {
				return fmt.Errorf("the staging file was not where it should be: %v", names(all))
			}
			return nil
		}); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestDurableWriteRefusesToClobberWhenAsked(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "there.txt"), "original")
	r := share(t, host, denyPolicy())

	_, err := r.WriteDurable(mustParse(t, "there.txt"),
		DurableOpts{Mode: 0o664, NoClobber: true},
		func(f *File) error { _, werr := f.WriteAt([]byte("new"), 0); return werr })
	if !errors.Is(err, ErrExists) {
		t.Fatalf("write = %v, want ErrExists", err)
	}
	body := readFile(t, filepath.Join(host, "there.txt"))
	if body != "original" {
		t.Fatalf("the destination changed to %q", body)
	}
}

// Publishing over a name written in the other normal form has to land on that
// entry rather than beside it, or the user sees the same name twice and the
// file they meant to replace is still there.
func TestDurableWritePublishesOverTheSpellingOnDisk(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, nfdName), "old")
	r := share(t, host, denyPolicy())

	if _, err := r.WriteDurable(mustParse(t, nfcName), DurableOpts{Mode: 0o664},
		func(f *File) error { _, werr := f.WriteAt([]byte("new"), 0); return werr }); err != nil {
		t.Fatalf("write: %v", err)
	}

	entries, err := r.ReadDir(RootPath(), HideReserved)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the write created a second spelling: %v", names(entries))
	}
	body := readFile(t, filepath.Join(host, nfdName))
	if body != "new" {
		t.Fatalf("the entry on disk holds %q", body)
	}
}

// F7 again, on the directory path: the configured mode is applied verbatim and
// a failure to apply it is returned.
func TestMkdirAppliesTheConfiguredModeVerbatim(t *testing.T) {
	old := unix.Umask(0o077)
	t.Cleanup(func() { unix.Umask(old) })

	host := t.TempDir()
	policy := denyPolicy()
	policy.ModeDir = 0o775
	r := share(t, host, policy)

	if err := r.Mkdir(mustParse(t, "d")); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if got := hostStat(t, filepath.Join(host, "d")).Mode & 0o7777; got != 0o775 {
		t.Fatalf("mode = %o under a 0077 umask, want the configured 0775", got)
	}
}

func TestRenameNoReplaceRefusesAtomically(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "a"), "a")
	write(t, filepath.Join(host, "b"), "b")
	r := share(t, host, denyPolicy())

	if err := r.Rename(mustParse(t, "a"), mustParse(t, "b"), true); !errors.Is(err, ErrExists) {
		t.Fatalf("rename = %v, want ErrExists", err)
	}
	if err := r.Rename(mustParse(t, "a"), mustParse(t, "b"), false); err != nil {
		t.Fatalf("rename: %v", err)
	}
	body := readFile(t, filepath.Join(host, "b"))
	if body != "a" {
		t.Fatalf("b holds %q", body)
	}
}

func TestUnlinkAndRmdir(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "f"), "x")
	if err := os.Mkdir(filepath.Join(host, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := share(t, host, denyPolicy())

	if err := r.Unlink(mustParse(t, "f")); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if err := r.Rmdir(mustParse(t, "d")); err != nil {
		t.Fatalf("rmdir: %v", err)
	}
	if err := r.Unlink(mustParse(t, "f")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unlink again = %v, want ErrNotFound", err)
	}
	// The share root is never removable through this API.
	if err := r.Rmdir(RootPath()); !errors.Is(err, ErrDenied) {
		t.Fatalf("removing the share root = %v, want ErrDenied", err)
	}
}

func TestCopyRangeCopiesEverythingItWasAskedFor(t *testing.T) {
	host := t.TempDir()
	body := strings.Repeat("0123456789abcdef", 40_000) // over one fallback buffer
	write(t, filepath.Join(host, "src"), body)
	write(t, filepath.Join(host, "dst"), "")
	r := share(t, host, denyPolicy())

	src := mustOpen(t, r, "src", IntentRead)
	dst := mustOpen(t, r, "dst", IntentReadWrite)

	n, err := CopyRange(src, 0, dst, 0, uint64(len(body)))
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if n != uint64(len(body)) {
		t.Fatalf("copied %d of %d", n, len(body))
	}
	got := readFile(t, filepath.Join(host, "dst"))
	if got != body {
		t.Fatalf("the copy differs at %d bytes", len(got))
	}
}

// A short source reports what was actually available, the same contract the
// kernel primitive has, so a caller that fell back mid-copy does not branch on
// which path it took.
func TestCopyRangeStopsAtTheEndOfTheSource(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "src"), "abc")
	write(t, filepath.Join(host, "dst"), "")
	r := share(t, host, denyPolicy())

	src := mustOpen(t, r, "src", IntentRead)
	dst := mustOpen(t, r, "dst", IntentReadWrite)

	n, err := CopyRange(src, 0, dst, 0, 100)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if n != 3 {
		t.Fatalf("copied %d, want the 3 bytes that existed", n)
	}
}

func TestBufferedFallbackMatchesTheKernelPath(t *testing.T) {
	host := t.TempDir()
	body := strings.Repeat("x", copyBufBytes*2+7)
	write(t, filepath.Join(host, "src"), body)
	write(t, filepath.Join(host, "dst"), "")
	r := share(t, host, denyPolicy())

	src := mustOpen(t, r, "src", IntentRead)
	dst := mustOpen(t, r, "dst", IntentReadWrite)

	n, err := bufferedCopyRange(src, 0, dst, 0, uint64(len(body)))
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if n != uint64(len(body)) {
		t.Fatalf("copied %d of %d", n, len(body))
	}
}

// Space answers for the filesystem holding the path, and a path naming a file
// is answered from its parent, which is on the same one.
func TestSpaceAnswersForThePathAskedAbout(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "f"), "x")
	r := share(t, host, denyPolicy())

	root, err := r.Space(RootPath())
	if err != nil {
		t.Fatalf("space: %v", err)
	}
	if root.Total == 0 {
		t.Fatal("total is zero")
	}
	if root.Available > root.Free {
		t.Fatal("available exceeds free, so the reserved blocks were counted as writable")
	}
	file, err := r.Space(mustParse(t, "f"))
	if err != nil {
		t.Fatalf("space for a file: %v", err)
	}
	if file.Total != root.Total {
		t.Fatal("a file was answered from a different filesystem than its parent")
	}
}

func TestSyncDirNeedsAReadableDescriptor(t *testing.T) {
	// fsync on an O_PATH descriptor is EBADF, which is never a real-world
	// durability failure and always an unconditional one. This is the test that
	// catches it being reintroduced.
	host := t.TempDir()
	r := share(t, host, denyPolicy())
	if err := r.SyncDir(RootPath()); err != nil {
		t.Fatalf("sync the share root: %v", err)
	}
	if err := r.Mkdir(mustParse(t, "d")); err != nil {
		t.Fatal(err)
	}
	if err := r.SyncDir(mustParse(t, "d")); err != nil {
		t.Fatalf("sync a subdirectory: %v", err)
	}
}

func TestSetTimes(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "f"), "x")
	r := share(t, host, denyPolicy())

	const want = 1_600_000_000_123_456_789
	if err := r.SetTimes(mustParse(t, "f"), want); err != nil {
		t.Fatalf("set times: %v", err)
	}
	st, err := r.Stat(mustParse(t, "f"))
	if err != nil {
		t.Fatal(err)
	}
	// Filesystems vary in timestamp granularity, so this asserts the second
	// rather than the nanosecond.
	if st.MtimeNs/1_000_000_000 != want/1_000_000_000 {
		t.Fatalf("mtime = %d, want about %d", st.MtimeNs, want)
	}
}

func TestFsTypeIsRecordedAtRegistration(t *testing.T) {
	host := t.TempDir()
	r := share(t, host, denyPolicy())
	if r.FsType() == 0 {
		t.Fatal("no filesystem type was recorded")
	}
	if r.Dev() == 0 {
		t.Fatal("no device was recorded")
	}
}

func TestTimestampSaturatesRatherThanWrapping(t *testing.T) {
	if got := timestampNs(1<<62, 0); got <= 0 {
		t.Fatalf("a seconds field that overflows the multiply wrapped to %d", got)
	}
	if got := timestampNs(-(1 << 62), 0); got >= 0 {
		t.Fatalf("a negative one wrapped to %d", got)
	}
	if got := timestampNs(2, 500); got != 2_000_000_500 {
		t.Fatalf("an ordinary timestamp = %d", got)
	}
}
