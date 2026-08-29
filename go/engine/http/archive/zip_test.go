// Linux only, matching the package under test.
//go:build linux

package archive

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func when() time.Time { return time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC) }

// The archive reads back through the standard library's own zip reader.
//
// Checked against an independent implementation rather than against this
// package's idea of the format: a writer verified only by its own reader
// agrees with itself about a file nothing else can open.
func TestTheArchiveReadsBackThroughTheStandardLibrary(t *testing.T) {
	var buf bytes.Buffer
	z := NewWriter(&buf)

	if err := z.AddBytes("readme.txt", []byte("hello"), when()); err != nil {
		t.Fatalf("AddBytes: %v", err)
	}
	if err := z.AddDir("empty", when()); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	if err := z.AddFile("docs/report.txt", strings.NewReader("a longer body"), when()); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if err := z.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("the standard library refused the archive: %v", err)
	}
	if len(r.File) != 3 {
		t.Fatalf("the archive holds %d entries", len(r.File))
	}

	want := map[string]string{
		"readme.txt":      "hello",
		"empty/":          "",
		"docs/report.txt": "a longer body",
	}
	for _, f := range r.File {
		body, ok := want[f.Name]
		if !ok {
			t.Errorf("the archive holds an unexpected entry %q", f.Name)
			continue
		}
		delete(want, f.Name)

		rc, oerr := f.Open()
		if oerr != nil {
			t.Errorf("opening %q: %v", f.Name, oerr)
			continue
		}
		got, rerr := io.ReadAll(rc)
		cerr := rc.Close()
		if rerr != nil || cerr != nil {
			t.Errorf("reading %q: %v %v", f.Name, rerr, cerr)
			continue
		}
		if string(got) != body {
			t.Errorf("%q holds %q, want %q", f.Name, got, body)
		}
		// The size the directory records is the size that came out, which is
		// what a data descriptor is for.
		if f.UncompressedSize64 != uint64(len(body)) {
			t.Errorf("%q records %d bytes and holds %d", f.Name, f.UncompressedSize64, len(body))
		}
	}
	for name := range want {
		t.Errorf("the archive is missing %q", name)
	}
}

// The directory entry is a directory to the reader, which is what makes an
// empty directory survive extraction at all.
func TestAnEmptyDirectorySurvives(t *testing.T) {
	var buf bytes.Buffer
	z := NewWriter(&buf)
	if err := z.AddDir("photos/2026", when()); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	if err := z.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if len(r.File) != 1 {
		t.Fatalf("the archive holds %d entries", len(r.File))
	}
	if !r.File[0].FileInfo().IsDir() {
		t.Errorf("%q is not a directory to the reader", r.File[0].Name)
	}
}

// The timestamp round-trips, and anything before 1980 clamps rather than
// wrapping into a date decades away.
func TestTimestampsRoundTripAndClamp(t *testing.T) {
	var buf bytes.Buffer
	z := NewWriter(&buf)
	if err := z.AddBytes("now.txt", []byte("x"), when()); err != nil {
		t.Fatalf("AddBytes: %v", err)
	}
	// The zip format begins in 1980 and cannot say "earlier".
	if err := z.AddBytes("old.txt", []byte("x"), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("AddBytes: %v", err)
	}
	if err := z.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	for _, f := range r.File {
		got := f.Modified.UTC()
		switch f.Name {
		case "now.txt":
			// Zip stores two-second resolution, so the seconds may differ by
			// one; the minute must not.
			if got.Year() != 2026 || got.Month() != time.March || got.Day() != 14 ||
				got.Hour() != 15 || got.Minute() != 9 {
				t.Errorf("the timestamp round-tripped to %v", got)
			}
		case "old.txt":
			if got.Year() != 1980 {
				t.Errorf("a pre-1980 time became %v", got)
			}
		}
	}
}

// A name that could escape the extraction directory is refused rather than
// resolved, because resolving would put the file somewhere the user did not
// choose.
func TestTraversingNamesAreRefused(t *testing.T) {
	for _, c := range []struct{ what, name string }{
		{"a parent component", "../etc/passwd"},
		{"a nested parent", "docs/../../etc/passwd"},
		{"an absolute path", "/etc/passwd"},
		{"a current-directory component", "./secret"},
		{"a backslash", `docs\..\..\etc\passwd`},
		{"a NUL", "docs/report\x00.txt"},
		{"an empty name", ""},
		{"only slashes", "///"},
		{"an empty component", "docs//report.txt"},
	} {
		if _, err := CleanName(c.name); !errors.Is(err, ErrName) {
			t.Errorf("%s (%q) was accepted", c.what, c.name)
		}
		var buf bytes.Buffer
		z := NewWriter(&buf)
		if err := z.AddBytes(c.name, []byte("x"), when()); !errors.Is(err, ErrName) {
			t.Errorf("%s (%q) was written: %v", c.what, c.name, err)
		}
	}

	// The ordinary names pass, including surrounding slashes that are trimmed
	// rather than refused.
	for _, c := range []struct{ in, want string }{
		{"readme.txt", "readme.txt"},
		{"docs/report.txt", "docs/report.txt"},
		{"docs/report.txt/", "docs/report.txt"},
		{"a file with spaces.txt", "a file with spaces.txt"},
		{"файл.txt", "файл.txt"},
	} {
		got, err := CleanName(c.in)
		if err != nil {
			t.Errorf("%q was refused: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q cleaned to %q, want %q", c.in, got, c.want)
		}
	}
}

// A non-ASCII name survives, which is what the UTF-8 flag is for.
func TestANonASCIINameSurvives(t *testing.T) {
	var buf bytes.Buffer
	z := NewWriter(&buf)
	if err := z.AddBytes("사진/여름.txt", []byte("x"), when()); err != nil {
		t.Fatalf("AddBytes: %v", err)
	}
	if err := z.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if r.File[0].Name != "사진/여름.txt" {
		t.Errorf("the name came back as %q", r.File[0].Name)
	}
	if !r.File[0].NonUTF8 == false {
		t.Log("the reader reports the name as UTF-8")
	}
}

// failingWriter fails once, at a set number of bytes, and accepts everything
// after that.
//
// Recovering rather than staying broken on purpose. A writer that keeps
// failing would make the sticky-error rule untestable: every later call would
// return the same error whether the rule existed or not, and the mutation
// removing it would pass.
type failingWriter struct {
	limit   int
	written int
	failed  bool
}

var errBroken = errors.New("the connection went away")

func (w *failingWriter) Write(p []byte) (int, error) {
	if !w.failed && w.written+len(p) > w.limit {
		w.failed = true
		n := max(w.limit-w.written, 0)
		w.written += n
		return n, errBroken
	}
	w.written += len(p)
	return len(p), nil
}

// The first error sticks. Every later call returns it rather than appending to
// a stream whose structure is already broken.
func TestTheFirstErrorSticks(t *testing.T) {
	w := &failingWriter{limit: 10}
	z := NewWriter(w)

	first := z.AddBytes("a.txt", []byte("some content here"), when())
	if first == nil {
		t.Fatal("a write past the failure point succeeded")
	}
	if !errors.Is(first, errBroken) {
		t.Errorf("the first failure is %v", first)
	}

	// Later calls report the same error without writing.
	before := w.written
	if got := z.AddBytes("b.txt", []byte("more"), when()); !errors.Is(got, errBroken) {
		t.Errorf("a later add returned %v", got)
	}
	if got := z.Close(); !errors.Is(got, errBroken) {
		t.Errorf("Close returned %v", got)
	}
	if w.written != before {
		t.Errorf("the writer took %d more bytes after failing", w.written-before)
	}
	if !errors.Is(z.Err(), errBroken) {
		t.Errorf("Err reports %v", z.Err())
	}
}

// Closing twice does not append a second directory, since a deferred Close
// beside an explicit one is the ordinary shape of a caller.
func TestClosingTwiceIsHarmless(t *testing.T) {
	var buf bytes.Buffer
	z := NewWriter(&buf)
	if err := z.AddBytes("a.txt", []byte("x"), when()); err != nil {
		t.Fatalf("AddBytes: %v", err)
	}
	if err := z.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	size := buf.Len()
	if err := z.Close(); err != nil {
		t.Fatalf("the second Close returned %v", err)
	}
	if buf.Len() != size {
		t.Errorf("the second Close wrote %d bytes", buf.Len()-size)
	}

	// Adding after the close is refused rather than corrupting the archive.
	if err := z.AddBytes("b.txt", []byte("x"), when()); !errors.Is(err, ErrClosed) {
		t.Errorf("an add after the close returned %v", err)
	}
}

// An empty archive is still a readable archive, so a selection that resolved
// to nothing produces an empty download rather than a broken one.
func TestAnEmptyArchiveIsValid(t *testing.T) {
	var buf bytes.Buffer
	z := NewWriter(&buf)
	if err := z.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("the standard library refused an empty archive: %v", err)
	}
	if len(r.File) != 0 {
		t.Errorf("an empty archive holds %d entries", len(r.File))
	}
}

// A file that ends early leaves a short entry in a structurally valid archive,
// which is what happens when a file is deleted mid-download.
func TestAFileThatVanishesLeavesAValidArchive(t *testing.T) {
	var buf bytes.Buffer
	z := NewWriter(&buf)

	if err := z.AddFile("short.txt", io.LimitReader(strings.NewReader("only this much"), 4), when()); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if err := z.AddBytes("after.txt", []byte("still written"), when()); err != nil {
		t.Fatalf("AddBytes: %v", err)
	}
	if err := z.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if len(r.File) != 2 {
		t.Fatalf("the archive holds %d entries", len(r.File))
	}
	rc, oerr := r.File[0].Open()
	if oerr != nil {
		t.Fatalf("opening: %v", oerr)
	}
	got, rerr := io.ReadAll(rc)
	if cerr := rc.Close(); cerr != nil {
		t.Errorf("closing: %v", cerr)
	}
	if rerr != nil {
		t.Fatalf("reading the short entry: %v", rerr)
	}
	// Four bytes, and the recorded size agrees, so the CRC checks out.
	if string(got) != "only" {
		t.Errorf("the short entry holds %q", got)
	}
}

// A name longer than the format's length field is refused.
//
// Writing one would truncate the recorded length, and every byte after it
// would be read as part of the wrong field: the archive is unreadable from
// that entry on. gosec found this, and it was a real defect rather than a
// false positive about a conversion.
func TestAnOversizedNameIsRefused(t *testing.T) {
	long := strings.Repeat("a", 70000)
	if _, err := CleanName(long); !errors.Is(err, ErrName) {
		t.Fatalf("a 70000-byte name returned %v", err)
	}

	var buf bytes.Buffer
	z := NewWriter(&buf)
	if err := z.AddBytes(long, []byte("x"), when()); !errors.Is(err, ErrName) {
		t.Errorf("an oversized name was written: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("the refusal still wrote %d bytes", buf.Len())
	}

	// A name at the bound is written, and reads back whole.
	atBound := strings.Repeat("b", 65534)
	z2 := NewWriter(&buf)
	if err := z2.AddBytes(atBound, []byte("x"), when()); err != nil {
		t.Fatalf("a name at the bound returned %v", err)
	}
	if err := z2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if len(r.File) != 1 || r.File[0].Name != atBound {
		t.Errorf("the name at the bound came back as %d bytes", len(r.File[0].Name))
	}
}
