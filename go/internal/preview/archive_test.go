//go:build linux

package preview

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

func buildZip(t *testing.T, members map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range members {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing the archive: %v", err)
	}
	return buf.Bytes()
}

func listBytes(t *testing.T, b []byte) ArchiveListing {
	t.Helper()
	got, err := ListArchive(context.Background(), bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("ListArchive: %v", err)
	}
	return got
}

func TestListingReadsTheCentralDirectory(t *testing.T) {
	raw := buildZip(t, map[string]string{
		"a.txt":       "hello",
		"dir/b.txt":   "world!",
		"dir/c/d.txt": "x",
	})
	got := listBytes(t, raw)
	if len(got.Entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(got.Entries))
	}
	if got.Truncated {
		t.Fatal("a small archive was reported truncated")
	}

	byName := map[string]ArchiveEntry{}
	for _, e := range got.Entries {
		byName[e.Name] = e
	}
	if byName["a.txt"].Size != 5 {
		t.Fatalf("a.txt is %d bytes, want 5", byName["a.txt"].Size)
	}
	if got.TotalUncompressed != 12 {
		t.Fatalf("total = %d, want 12", got.TotalUncompressed)
	}
}

// Nothing is extracted to list, which is what makes a zip bomb cost the
// directory parse and nothing else.
func TestAZipBombIsListedWithoutExtraction(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("bomb.bin")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Ten megabytes of zeroes compresses to almost nothing.
	if _, err := w.Write(make([]byte, 10<<20)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw := buf.Bytes()
	if len(raw) > 1<<20 {
		t.Fatalf("the fixture did not compress: %d bytes", len(raw))
	}
	got := listBytes(t, raw)
	if len(got.Entries) != 1 {
		t.Fatalf("got %d entries", len(got.Entries))
	}
	// The ratio is visible to a caller that wants to refuse one, which it can
	// only be because both sizes are reported.
	e := got.Entries[0]
	if e.Size != 10<<20 {
		t.Fatalf("the uncompressed size is %d", e.Size)
	}
	if e.Compressed >= e.Size {
		t.Fatalf("the compressed size %d is not smaller than %d", e.Compressed, e.Size)
	}
}

// The entry count is capped, and the truncation is reported rather than
// silent.
func TestTheEntryCapTruncatesAndSaysSo(t *testing.T) {
	members := map[string]string{}
	for i := 0; i < limits.ArchiveEntriesListed+50; i++ {
		members[fmt.Sprintf("f%06d.txt", i)] = "x"
	}
	got := listBytes(t, buildZip(t, members))
	if len(got.Entries) != limits.ArchiveEntriesListed {
		t.Fatalf("got %d entries, want the cap of %d",
			len(got.Entries), limits.ArchiveEntriesListed)
	}
	if !got.Truncated {
		t.Fatal("a truncated listing did not say so, so it looks complete")
	}
}

// A member name is attacker-chosen. It is never opened by this build, but a
// client rendering the listing or extracting from it must not be handed a name
// that escapes a directory.
func TestTraversingAndAbsoluteNamesAreNotListed(t *testing.T) {
	raw := buildZip(t, map[string]string{
		"safe.txt":              "ok",
		"../escape.txt":         "no",
		"a/../../escape.txt":    "no",
		"/absolute.txt":         "no",
		"C:/windows/system.ini": "no",
		"back\\slash.txt":       "no",
	})
	got := listBytes(t, raw)
	for _, e := range got.Entries {
		if e.Name != "safe.txt" {
			t.Fatalf("a dangerous member name was listed: %q", e.Name)
		}
	}
	if len(got.Entries) != 1 {
		t.Fatalf("got %d entries, want only the safe one", len(got.Entries))
	}
}

func TestControlCharactersInANameAreNotListed(t *testing.T) {
	raw := buildZip(t, map[string]string{
		"safe.txt":       "ok",
		"bell\x07.txt":   "no",
		"nul\x00.txt":    "no",
		"esc\x1b[0m.txt": "no",
	})
	got := listBytes(t, raw)
	if len(got.Entries) != 1 || got.Entries[0].Name != "safe.txt" {
		t.Fatalf("got %+v, want only the safe name", got.Entries)
	}
}

func TestSomethingThatIsNotAZipIsRefused(t *testing.T) {
	for _, bad := range [][]byte{
		[]byte("not a zip at all"),
		{},
		{'P', 'K'},
	} {
		_, err := ListArchive(context.Background(), bytes.NewReader(bad), int64(len(bad)))
		if !errors.Is(err, ErrNotArchive) && !errors.Is(err, context.Canceled) {
			t.Fatalf("a %d-byte non-archive returned %v, want ErrNotArchive", len(bad), err)
		}
	}
}

func TestACancelledListingStops(t *testing.T) {
	members := map[string]string{}
	for i := 0; i < 100; i++ {
		members[fmt.Sprintf("f%03d.txt", i)] = "x"
	}
	raw := buildZip(t, members)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ListArchive(ctx, bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("a cancelled listing returned a result")
	}
}

func TestADirectoryMemberIsMarkedAsOne(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if _, err := zw.Create("dir/"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := listBytes(t, buf.Bytes())
	if len(got.Entries) != 1 || !got.Entries[0].IsDir {
		t.Fatalf("got %+v, want a directory member", got.Entries)
	}
}

func TestSafeArchiveNameRefusesWhatItShould(t *testing.T) {
	for _, bad := range []string{
		"", "/abs", "../up", "a/../../up", "back\\slash",
		"C:/win", "c:/win", "ctrl\x01", "del\x7f",
		strings.Repeat("a", 4097),
	} {
		if safeArchiveName(bad) {
			t.Errorf("safeArchiveName(%q) = true", bad)
		}
	}
	for _, good := range []string{
		"a.txt", "dir/a.txt", "dir/", "a b.txt", "a..b.txt",
		"\uc0ac\uc9c4.jpg", "deep/a/b/c/d.txt",
	} {
		if !safeArchiveName(good) {
			t.Errorf("safeArchiveName(%q) = false", good)
		}
	}
}

// The archive parser reads a structure a stranger wrote, so it is fuzzed.
// Nothing may panic, no listing may exceed its cap, and no name that survives
// may be one a client could be hurt by.
func FuzzListArchive(f *testing.F) {
	f.Add(buildZipFor(f, map[string]string{"a.txt": "hello"}))
	f.Add(buildZipFor(f, map[string]string{"dir/": "", "dir/b.txt": "x"}))
	f.Add([]byte("PK\x05\x06"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, raw []byte) {
		got, err := ListArchive(context.Background(), bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			return
		}
		if len(got.Entries) > limits.ArchiveEntriesListed {
			t.Fatalf("a listing of %d entries is past the cap", len(got.Entries))
		}
		var total uint64
		for _, e := range got.Entries {
			if !safeArchiveName(e.Name) {
				t.Fatalf("a name that fails the filter was listed: %q", e.Name)
			}
			total += e.Size
		}
		if !got.Truncated && total != got.TotalUncompressed {
			t.Fatalf("the total is %d and the entries sum to %d", got.TotalUncompressed, total)
		}
	})
}

func buildZipFor(f *testing.F, members map[string]string) []byte {
	f.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range members {
		w, err := zw.Create(name)
		if err != nil {
			f.Fatalf("creating %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			f.Fatalf("writing %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		f.Fatalf("closing: %v", err)
	}
	return buf.Bytes()
}
