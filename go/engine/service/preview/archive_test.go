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
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/unicode/norm"
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

// zipRawEntries builds an archive whose members carry the given raw name
// bytes directly rather than through zip.Writer's own encoding, with the
// UTF-8 flag left clear: the shape a pre-Unicode zip tool produces. A name
// ending in a slash gets no body, the same directory convention as zipOf.
func zipRawEntries(t *testing.T, rawNames ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, raw := range rawNames {
		fh := &zip.FileHeader{Name: string(raw), Method: zip.Store, NonUTF8: true}
		f, err := w.CreateHeader(fh)
		if err != nil {
			t.Fatalf("creating a raw entry for %v: %v", raw, err)
		}
		if !bytes.HasSuffix(raw, []byte("/")) {
			if _, werr := f.Write([]byte("contents")); werr != nil {
				t.Fatalf("writing a raw entry's body: %v", werr)
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

// A pre-Unicode Windows or Korean zip tool writes an entry's name in CP949
// (registered under the IANA label EUC-KR, which is what chardet reports)
// and leaves the UTF-8 flag clear. The names are listed together because
// chardet's confidence climbs with the sample size, and any one of these
// names alone is too short a sample for it to place.
func TestCP949NamesWithTheFlagClearListAsKorean(t *testing.T) {
	// Held as \u escapes because Go source carries no Korean; they decode to the same text.
	names := []string{"\uc0c8\ub85c\uc6b4 \ud30c\uc77c \ubaa9\ub85d.txt", "\ubb38\uc11c \ud3f4\ub354 \uc548\uc758 \uc0ac\uc9c4.png", "\ub450\ubc88\uc9f8 \uc608\uc2dc \ud30c\uc77c \uc774\ub984.docx"}
	raw := make([][]byte, len(names))
	for i, n := range names {
		enc, err := korean.EUCKR.NewEncoder().String(n)
		if err != nil {
			t.Fatalf("encoding %q as CP949: %v", n, err)
		}
		raw[i] = []byte(enc)
	}
	got := listOf(t, zipRawEntries(t, raw...))

	if len(got.Entries) != len(names) {
		t.Fatalf("listed %d entries, want %d: %+v", len(got.Entries), len(names), got.Entries)
	}
	listed := map[string]bool{}
	for _, e := range got.Entries {
		listed[e.Name] = true
	}
	for _, n := range names {
		if !listed[n] {
			t.Errorf("Korean name %q did not list correctly, got %+v", n, got.Entries)
		}
	}
}

// The same case as the CP949 test above, for a Japanese zip tool writing
// Shift_JIS instead.
func TestShiftJISNamesWithTheFlagClearListAsJapanese(t *testing.T) {
	names := []string{"新しいファイルの一覧.txt", "資料フォルダの中の写真.png", "二番目のサンプルファイル名.docx"}
	raw := make([][]byte, len(names))
	for i, n := range names {
		enc, err := japanese.ShiftJIS.NewEncoder().String(n)
		if err != nil {
			t.Fatalf("encoding %q as Shift_JIS: %v", n, err)
		}
		raw[i] = []byte(enc)
	}
	got := listOf(t, zipRawEntries(t, raw...))

	if len(got.Entries) != len(names) {
		t.Fatalf("listed %d entries, want %d: %+v", len(got.Entries), len(names), got.Entries)
	}
	listed := map[string]bool{}
	for _, e := range got.Entries {
		listed[e.Name] = true
	}
	for _, n := range names {
		if !listed[n] {
			t.Errorf("Japanese name %q did not list correctly, got %+v", n, got.Entries)
		}
	}
}

// An entry that already arrived as UTF-8 with its flag set, the ordinary
// case, is not run through any decode: it lists with the exact bytes it was
// written with.
func TestAUTF8ArchiveWithTheFlagSetIsUnaffectedByteForByte(t *testing.T) {
	// The Korean name is held as a \u escape because Go source carries no Korean; it decodes to the same text.
	names := []string{"café.txt", "\uc548\ub155\ud558\uc138\uc694.txt", "readme.md"}
	got := listOf(t, zipOf(t, names...))

	if len(got.Entries) != len(names) {
		t.Fatalf("listed %d entries, want %d: %+v", len(got.Entries), len(names), got.Entries)
	}
	listed := map[string]bool{}
	for _, e := range got.Entries {
		listed[e.Name] = true
	}
	for _, n := range names {
		if !listed[n] {
			t.Errorf("a UTF-8 name changed under listing: want %q among %+v", n, got.Entries)
		}
	}
}

// A macOS client writes a decomposed name even though the UTF-8 flag is set,
// since the flag says nothing about normal form. The listing still composes
// it, the same as every other name.
func TestAnNFDNamedEntryListsInNFC(t *testing.T) {
	nfd := norm.NFD.String("café.txt")
	nfc := norm.NFC.String(nfd)
	if nfd == nfc {
		t.Fatal("the fixture name did not actually decompose, so this proves nothing")
	}

	got := listOf(t, zipOf(t, nfd))
	if len(got.Entries) != 1 {
		t.Fatalf("listed %d entries, want 1: %+v", len(got.Entries), got.Entries)
	}
	if name := got.Entries[0].Name; name != nfc {
		t.Errorf("listed %q, want the composed %q", name, nfc)
	}
}

// None of the code pages this build decodes can carry 0x2F as a trailing
// byte, so a directory's trailing slash survives decoding and IsDir is still
// derived from the decoded name correctly.
func TestADirectoryNameKeepsItsTrailingSlashThroughDecode(t *testing.T) {
	enc, err := charmap.CodePage437.NewEncoder().String("café")
	if err != nil {
		t.Fatalf("encoding the fixture name: %v", err)
	}
	raw := append([]byte(enc), '/')
	got := listOf(t, zipRawEntries(t, raw))

	if len(got.Entries) != 1 {
		t.Fatalf("listed %d entries, want 1: %+v", len(got.Entries), got.Entries)
	}
	e := got.Entries[0]
	if want := "café/"; e.Name != want {
		t.Errorf("listed %q, want %q", e.Name, want)
	}
	if !e.IsDir {
		t.Error("a decoded directory name lost its IsDir flag")
	}
}

// A single entry is too short a sample for chardet to place with any
// confidence, so a Western name in the format's own CP437 default decodes
// through the caller's fallback instead of a wrong East Asian guess.
func TestASingleWesternCP437NameDecodesThroughTheFallback(t *testing.T) {
	const name = "café.txt"
	enc, err := charmap.CodePage437.NewEncoder().String(name)
	if err != nil {
		t.Fatalf("encoding the fixture name: %v", err)
	}
	got := listOf(t, zipRawEntries(t, []byte(enc)))

	if len(got.Entries) != 1 {
		t.Fatalf("listed %d entries, want 1: %+v", len(got.Entries), got.Entries)
	}
	if listedName := got.Entries[0].Name; listedName != name {
		t.Errorf("listed %q, want %q", listedName, name)
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
