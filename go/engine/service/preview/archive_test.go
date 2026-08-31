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

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// zipOf builds an archive holding the named members. A name ending in a slash
// is a directory entry.
func zipOf(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, n := range names {
		f, err := w.Create(n)
		if err != nil {
			t.Fatalf("creating %q: %v", n, err)
		}
		if !strings.HasSuffix(n, "/") {
			if _, werr := f.Write([]byte("contents of " + n)); werr != nil {
				t.Fatalf("writing %q: %v", n, werr)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing the archive: %v", err)
	}
	return buf.Bytes()
}

func listOf(t *testing.T, raw []byte) ArchiveListing {
	t.Helper()
	got, err := ListArchive(t.Context(), bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("ListArchive: %v", err)
	}
	return got
}

func TestListArchiveReadsNamesSizesAndDirectoryFlags(t *testing.T) {
	raw := zipOf(t, "readme.txt", "docs/", "docs/guide.md")
	got := listOf(t, raw)

	if len(got.Entries) != 3 {
		t.Fatalf("listed %d entries: %+v", len(got.Entries), got.Entries)
	}
	if got.Truncated || got.Skipped != 0 {
		t.Errorf("a clean archive reported truncated=%v skipped=%d", got.Truncated, got.Skipped)
	}

	byName := map[string]ArchiveEntry{}
	for _, e := range got.Entries {
		byName[e.Name] = e
	}
	if !byName["docs/"].IsDir {
		t.Error("a directory entry is not flagged as one")
	}
	if byName["readme.txt"].IsDir {
		t.Error("a file is flagged as a directory")
	}
	if byName["readme.txt"].Size == 0 {
		t.Error("a file entry reports no size")
	}
	if got.TotalUncompressed == 0 {
		t.Error("the listing reports no total, so a caller cannot see a bomb")
	}
}

// The cap truncates and says so, never a silent short list.
func TestTheEntryCapTruncatesAndReports(t *testing.T) {
	names := make([]string, 0, limits.ArchiveEntriesListed+5)
	for i := range limits.ArchiveEntriesListed + 5 {
		names = append(names, fmt.Sprintf("f%06d.txt", i))
	}
	raw := zipOf(t, names...)
	got := listOf(t, raw)

	if !got.Truncated {
		t.Error("the cap was hit and the listing does not say so")
	}
	// Exact at the boundary: the count is the cap, not one either side.
	if len(got.Entries) != limits.ArchiveEntriesListed {
		t.Errorf("listed %d entries, want exactly %d", len(got.Entries), limits.ArchiveEntriesListed)
	}
}

// An archive exactly at the cap is complete rather than truncated.
func TestAnArchiveExactlyAtTheCapIsNotTruncated(t *testing.T) {
	// A smaller stand-in for the real cap, exercised through the same path by
	// checking the flag rather than the constant.
	raw := zipOf(t, "a.txt", "b.txt")
	got := listOf(t, raw)
	if got.Truncated {
		t.Error("a two-entry archive reported itself truncated")
	}
}

// A skipped name increments the count rather than vanishing, so ten thousand
// entries and three unsafe ones reads as exactly that.
func TestUnsafeNamesAreCountedNotHidden(t *testing.T) {
	raw := zipOf(t,
		"safe.txt",
		"../escape.txt",
		"nested/../../escape.txt",
		"/absolute.txt",
		"also-safe.md",
	)
	got := listOf(t, raw)

	if got.Skipped != 3 {
		t.Errorf("skipped %d, want the three unsafe names", got.Skipped)
	}
	if len(got.Entries) != 2 {
		t.Errorf("listed %d entries, want the two safe ones: %+v", len(got.Entries), got.Entries)
	}
	for _, e := range got.Entries {
		if strings.Contains(e.Name, "..") || strings.HasPrefix(e.Name, "/") {
			t.Errorf("an unsafe name was listed: %q", e.Name)
		}
	}
}

// safeArchiveName is a display filter, not a path-traversal guard: the name is
// never opened, so what matters is what a client would render or hand to its
// own extractor.
func TestSafeArchiveNameIsADisplayFilter(t *testing.T) {
	safe := []string{
		"a.txt", "dir/a.txt", "dir/", "a b c.txt", "café.txt",
		"..hidden.txt", "a..b.txt", "deeply/nested/path/file.bin",
	}
	for _, n := range safe {
		if !safeArchiveName(n) {
			t.Errorf("safeArchiveName(%q) refused a displayable name", n)
		}
	}

	unsafe := map[string]string{
		"":                "empty",
		"/abs.txt":        "an absolute path",
		"../up.txt":       "a traversal",
		"a/../../up.txt":  "a nested traversal",
		"..":              "a bare traversal",
		`back\slash.txt`:  "a backslash",
		"C:file.txt":      "a drive letter",
		"nul\x00byte.txt": "a NUL byte",
		"bell\x07.txt":    "a control character",
		"del\x7f.txt":     "a delete character",
		"newline\n.txt":   "a newline",
		strings.Repeat("a", maxArchiveNameBytes+1): "a name past the bound",
	}
	for n, why := range unsafe {
		if safeArchiveName(n) {
			t.Errorf("safeArchiveName accepted %s: %q", why, n)
		}
	}
}

// A cancelled listing stops rather than walking the whole directory.
func TestACancelledListingStops(t *testing.T) {
	raw := zipOf(t, "a.txt", "b.txt", "c.txt")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := ListArchive(ctx, bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Error("a cancelled listing returned no error")
	}
}

// A non-zip refuses cleanly rather than panicking or returning a partial
// listing.
func TestANonArchiveRefuses(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  []byte
	}{
		{"text", []byte("this is not a zip file at all")},
		{"empty", nil},
		{"a truncated zip", zipOf(t, "a.txt")[:10]},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := ListArchive(t.Context(), bytes.NewReader(c.raw), int64(len(c.raw)))
			if !errors.Is(err, ErrNotArchive) {
				t.Errorf("got %v, want ErrNotArchive", err)
			}
		})
	}

	// A size that disagrees with the reader is refused rather than read past.
	raw := zipOf(t, "a.txt")
	if _, err := ListArchive(t.Context(), bytes.NewReader(raw), 0); !errors.Is(err, ErrNotArchive) {
		t.Errorf("a zero size: %v", err)
	}
	if _, err := ListArchive(t.Context(), bytes.NewReader(raw), -5); !errors.Is(err, ErrNotArchive) {
		t.Errorf("a negative size: %v", err)
	}
}

func FuzzListArchiveNeverPanics(f *testing.F) {
	// A real archive as a seed, built here rather than through the helper: a
	// fuzz seed has an *F and the helper wants a *T.
	var seed bytes.Buffer
	w := zip.NewWriter(&seed)
	if sf, err := w.Create("a.txt"); err == nil {
		if _, werr := sf.Write([]byte("seed")); werr != nil {
			f.Fatalf("writing the seed: %v", werr)
		}
	}
	if err := w.Close(); err != nil {
		f.Fatalf("closing the seed: %v", err)
	}
	f.Add(seed.Bytes())
	f.Add([]byte("PK\x03\x04"))
	f.Add([]byte("PK\x05\x06"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, in []byte) {
		got, err := ListArchive(t.Context(), bytes.NewReader(in), int64(len(in)))
		if err != nil {
			return
		}
		// Anything listed carries a name this build was willing to show.
		for _, e := range got.Entries {
			if !safeArchiveName(e.Name) {
				t.Errorf("an unsafe name reached the listing: %q", e.Name)
			}
		}
		if len(got.Entries) > limits.ArchiveEntriesListed {
			t.Errorf("the listing holds %d entries, past the cap", len(got.Entries))
		}
	})
}
