//go:build linux

package vfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func names(entries []DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

func assertNoStagingResidue(t *testing.T, r *ShareRoot) {
	t.Helper()
	entries, err := r.ReadDir(RootPath(), IncludeReserved)
	if err != nil {
		t.Fatalf("list looking for staging residue: %v", err)
	}
	for _, e := range entries {
		if IsStagingName(e.Name) {
			t.Errorf("a staging file survived: %s", e.Name)
		}
	}
}

// Item 12. With the process umask set to refuse every bit, a durable write
// with Mode: 0o600 lands at exactly 0o600, proving the mode comes from
// fchmod on the open descriptor and never from O_CREAT's own umask-filtered
// mode.
func TestDurableWriteAppliesTheExactModeDespiteAHostileUmask(t *testing.T) {
	host := t.TempDir()
	old := unix.Umask(0o777)
	defer unix.Umask(old)

	r := share(t, host, denyPolicy())
	if _, err := r.WriteDurable(mustParse(t, "k"), DurableOpts{Mode: 0o600},
		func(f *File) error { _, werr := f.WriteAt([]byte("k"), 0); return werr }); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := hostStat(t, filepath.Join(host, "k")).Mode & 0o7777; got != 0o600 {
		t.Fatalf("mode = %o, want 0600", got)
	}
}

// Item 13. A writer that fails partway leaves the original content exactly
// as it was, and no staging file survives.
func TestDurableWriteLeavesThePriorContentOnFailure(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "keep.txt"), "original")
	r := share(t, host, denyPolicy())

	boom := errors.New("the caller's write failed")
	_, err := r.WriteDurable(mustParse(t, "keep.txt"), DurableOpts{Mode: 0o664},
		func(f *File) error {
			if _, werr := f.WriteAt([]byte("half"), 0); werr != nil {
				return werr
			}
			return boom
		})
	if !errors.Is(err, boom) {
		t.Fatalf("write = %v, want the caller's own error", err)
	}
	if got := readFile(t, filepath.Join(host, "keep.txt")); got != "original" {
		t.Fatalf("the destination changed to %q", got)
	}
	assertNoStagingResidue(t, r)
}

// Item 14. A successful replace changes the content atomically: no reader
// holding a descriptor opened before the replace ever observes a partial
// write, because the publish is a single rename.
func TestDurableWriteReplacesAtomically(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "f"), "old")
	r := share(t, host, denyPolicy())

	before, err := r.OpenRead(mustParse(t, "f"), IntentRead)
	if err != nil {
		t.Fatalf("open before the replace: %v", err)
	}
	defer closeAfter(before.f, "reader from before the replace")

	done, err := r.WriteDurable(mustParse(t, "f"), DurableOpts{Mode: 0o664},
		func(f *File) error { _, werr := f.WriteAt([]byte("new content"), 0); return werr })
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !done.Replaced {
		t.Fatal("the write did not report replacing an existing file")
	}

	buf := make([]byte, 32)
	n, rerr := before.ReadAt(buf, 0)
	if n == 0 && rerr != nil && !isEOF(rerr) {
		t.Fatalf("reading the pre-replace handle: %v", rerr)
	}
	if got := string(buf[:n]); got != "old" {
		t.Fatalf("the pre-replace handle now reads %q, want its own unchanged content", got)
	}

	if got := readFile(t, filepath.Join(host, "f")); got != "new content" {
		t.Fatalf("the file on disk holds %q", got)
	}
}

func TestDurableWriteRestoresTheReplacedMode(t *testing.T) {
	host := t.TempDir()
	target := filepath.Join(host, "shared.txt")
	write(t, target, "old")
	if err := os.Chmod(target, 0o600); err != nil {
		t.Fatal(err)
	}
	r := share(t, host, denyPolicy())

	if _, err := r.WriteDurable(mustParse(t, "shared.txt"), DurableOpts{Mode: 0o664},
		func(f *File) error { _, werr := f.WriteAt([]byte("new"), 0); return werr }); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := hostStat(t, target).Mode & 0o7777; got != 0o600 {
		t.Fatalf("the replacement landed with mode %o, want the 0600 it replaced", got)
	}
}

func TestDurableWriteRefusesToClobberWhenAsked(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "there.txt"), "original")
	r := share(t, host, denyPolicy())

	_, err := r.WriteDurable(mustParse(t, "there.txt"), DurableOpts{Mode: 0o664, NoClobber: true},
		func(f *File) error { _, werr := f.WriteAt([]byte("new"), 0); return werr })
	if !errors.Is(err, ErrExists) {
		t.Fatalf("write = %v, want ErrExists", err)
	}
	if got := readFile(t, filepath.Join(host, "there.txt")); got != "original" {
		t.Fatalf("the destination changed to %q", got)
	}
}

func TestTheStagingFileIsUnlistableWhileTheWriteIsInFlight(t *testing.T) {
	host := t.TempDir()
	r := share(t, host, denyPolicy())

	_, err := r.WriteDurable(mustParse(t, "x.txt"), DurableOpts{Mode: 0o664},
		func(*File) error {
			hidden, err := r.ReadDir(RootPath(), HideReserved)
			if err != nil {
				return err
			}
			if len(hidden) != 0 {
				return fmt.Errorf("a write in flight was visible: %v", names(hidden))
			}
			all, err := r.ReadDir(RootPath(), IncludeReserved)
			if err != nil {
				return err
			}
			if len(all) != 1 || !IsStagingName(all[0].Name) {
				return fmt.Errorf("the staging file was not where it should be: %v", names(all))
			}
			return nil
		})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestDurableWritePublishesOverTheSpellingOnDisk(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, nfdSpelling), "old")
	r := share(t, host, denyPolicy())

	if _, err := r.WriteDurable(mustParse(t, nfcSpelling), DurableOpts{Mode: 0o664},
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
	if got := readFile(t, filepath.Join(host, nfdSpelling)); got != "new" {
		t.Fatalf("the entry on disk holds %q", got)
	}
}

// PublishPart: a part in the same directory as its destination, publishing
// over an occupied name transplants the prior mode.
func TestPublishPartReplacesAndRestoresMode(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "dest.bin"), "old")
	if err := os.Chmod(filepath.Join(host, "dest.bin"), 0o600); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(host, ".scpart-aaaaaaaaaaaaaaaa"), "new content")
	r := share(t, host, denyPolicy())

	part := mustJoinControl(t, RootPath(), ".scpart-aaaaaaaaaaaaaaaa")
	done, err := r.PublishPart(part, mustParse(t, "dest.bin"), true)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !done.Replaced {
		t.Fatal("publish did not report replacing the prior entry")
	}
	if got := hostStat(t, filepath.Join(host, "dest.bin")).Mode & 0o7777; got != 0o600 {
		t.Fatalf("mode = %o, want the replaced entry's 0600", got)
	}
	if got := readFile(t, filepath.Join(host, "dest.bin")); got != "new content" {
		t.Fatalf("content = %q", got)
	}
}

func TestPublishPartRefusesAnOccupiedNameWithoutReplacing(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "dest.bin"), "old")
	write(t, filepath.Join(host, ".scpart-bbbbbbbbbbbbbbbb"), "new content")
	r := share(t, host, denyPolicy())

	part := mustJoinControl(t, RootPath(), ".scpart-bbbbbbbbbbbbbbbb")
	_, err := r.PublishPart(part, mustParse(t, "dest.bin"), false)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("publish = %v, want ErrExists", err)
	}
}

func TestPublishPartRefusesCrossDirectoryParts(t *testing.T) {
	host := t.TempDir()
	if err := os.Mkdir(filepath.Join(host, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(host, "sub", ".scpart-cccccccccccccccc"), "content")
	r := share(t, host, denyPolicy())

	part := mustJoinControl(t, mustParse(t, "sub"), ".scpart-cccccccccccccccc")
	_, err := r.PublishPart(part, mustParse(t, "dest.bin"), false)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("publish across directories = %v, want ErrDenied", err)
	}
}

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
		t.Fatalf("mode = %o, want the configured 0775", got)
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
	if got := readFile(t, filepath.Join(host, "b")); got != "a" {
		t.Fatalf("b holds %q", got)
	}
}

func TestRenameCrossDeviceIsAnExpectedOutcome(t *testing.T) {
	// Reproducing an actual EXDEV needs a second real filesystem, which is
	// covered by the mount-boundary machinery in escape_test.go; this
	// asserts the mapping side only, that mapErrno turns EXDEV into
	// ErrCrossDevice and not something a caller would mistake for a bug.
	if !errors.Is(mapErrno("rename", unix.EXDEV), ErrCrossDevice) {
		t.Fatal("EXDEV must map to ErrCrossDevice")
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
	if err := r.Rmdir(RootPath()); !errors.Is(err, ErrDenied) {
		t.Fatalf("removing the share root = %v, want ErrDenied", err)
	}
}

func TestCopyRangeCopiesEverythingAsked(t *testing.T) {
	host := t.TempDir()
	body := strings.Repeat("0123456789abcdef", 40_000) // over one fallback buffer
	write(t, filepath.Join(host, "src"), body)
	write(t, filepath.Join(host, "dst"), "")
	r := share(t, host, denyPolicy())

	src, err := r.OpenRead(mustParse(t, "src"), IntentRead)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAfter(src.f, "src")
	dst, err := r.OpenRead(mustParse(t, "dst"), IntentReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAfter(dst.f, "dst")

	n, err := CopyRange(src, 0, dst, 0, uint64(len(body)))
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if n != uint64(len(body)) {
		t.Fatalf("copied %d of %d", n, len(body))
	}
	if got := readFile(t, filepath.Join(host, "dst")); got != body {
		t.Fatalf("the copy differs at %d bytes", len(got))
	}
}

func TestCopyRangeStopsAtTheEndOfTheSource(t *testing.T) {
	host := t.TempDir()
	write(t, filepath.Join(host, "src"), "abc")
	write(t, filepath.Join(host, "dst"), "")
	r := share(t, host, denyPolicy())

	src, err := r.OpenRead(mustParse(t, "src"), IntentRead)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAfter(src.f, "src")
	dst, err := r.OpenRead(mustParse(t, "dst"), IntentReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAfter(dst.f, "dst")

	n, err := CopyRange(src, 0, dst, 0, 100)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if n != 3 {
		t.Fatalf("copied %d, want the 3 bytes that existed", n)
	}
}

func TestBufferedCopyRangeFallbackMatchesTheKernelPath(t *testing.T) {
	host := t.TempDir()
	body := strings.Repeat("x", copyBufBytes*2+7)
	write(t, filepath.Join(host, "src"), body)
	write(t, filepath.Join(host, "dst"), "")
	r := share(t, host, denyPolicy())

	src, err := r.OpenRead(mustParse(t, "src"), IntentRead)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAfter(src.f, "src")
	dst, err := r.OpenRead(mustParse(t, "dst"), IntentReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAfter(dst.f, "dst")

	n, err := bufferedCopyRange(src, 0, dst, 0, uint64(len(body)))
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if n != uint64(len(body)) {
		t.Fatalf("copied %d of %d", n, len(body))
	}
}
