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

// The writer is checked against a reader that did not come from this tree.
//
// That is the point of the test: a hand-written container format checked only
// by its own parser proves the two agree, not that either is right. Go's zip
// reader is a second implementation of the specification.

const testMtime = 1_700_000_000_000_000_000

func readBack(t *testing.T, raw []byte) *zip.Reader {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("the archive does not open: %v", err)
	}
	return r
}

func TestAnArchiveOpensAndCarriesItsEntries(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if err := w.AddDir("photos", testMtime); err != nil {
		t.Fatalf("AddDir: %v", err)
	}
	if _, err := w.AddFile("photos/a.txt", testMtime, strings.NewReader("hello")); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if err := w.AddBytes("photos/b.txt", testMtime, []byte("second")); err != nil {
		t.Fatalf("AddBytes: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := readBack(t, buf.Bytes())
	if len(r.File) != 3 {
		t.Fatalf("got %d entries, want 3", len(r.File))
	}

	want := map[string]string{"photos/a.txt": "hello", "photos/b.txt": "second"}
	for _, f := range r.File {
		if f.Name == "photos/" {
			if !f.FileInfo().IsDir() {
				t.Error("the directory entry does not read as a directory")
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening %s: %v", f.Name, err)
		}
		got, rerr := io.ReadAll(rc)
		if cerr := rc.Close(); cerr != nil {
			t.Errorf("closing %s: %v", f.Name, cerr)
		}
		if rerr != nil {
			t.Fatalf("reading %s: %v", f.Name, rerr)
		}
		if string(got) != want[f.Name] {
			t.Errorf("%s = %q, want %q", f.Name, got, want[f.Name])
		}
	}
}

// A name outside ASCII has to survive. The flag that says the name is UTF-8
// was once unset, and without it the format defines a name as whatever the
// writer's code page was, so every mainstream extractor guesses and a name
// comes out mangled or stops round-tripping.
func TestANameOutsideASCIIRoundTrips(t *testing.T) {
	// Several scripts, none of them the one this tree's own rule reserves for
	// speaking to a person: what is being checked is that the writer says its
	// names are UTF-8, and any script outside ASCII proves it.
	names := []string{
		"Ordner/Gr\u00fc\u00dfe.txt",
		"dossier/\u00e9t\u00e9.txt",
		"\u0430\u0440\u0445\u0438\u0432/\u0444\u0430\u0439\u043b.txt",
		"emoji/\U0001F4C1.txt",
	}

	var buf bytes.Buffer
	w := NewWriter(&buf)
	for _, n := range names {
		if _, err := w.AddFile(n, testMtime, strings.NewReader("x")); err != nil {
			t.Fatalf("AddFile %q: %v", n, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := readBack(t, buf.Bytes())
	got := map[string]bool{}
	for _, f := range r.File {
		got[f.Name] = true
		// The reader reports whether the writer claimed UTF-8. Without the
		// claim it falls back to guessing, which is the defect.
		if f.NonUTF8 {
			t.Errorf("%q is not marked as UTF-8, so a reader has to guess its encoding", f.Name)
		}
	}
	for _, n := range names {
		if !got[n] {
			t.Errorf("%q did not survive: got %v", n, keys(got))
		}
	}
}

// An empty archive is still an archive. A reader has to find the trailer.
func TestAnEmptyArchiveIsValid(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if r := readBack(t, buf.Bytes()); len(r.File) != 0 {
		t.Fatalf("got %d entries, want none", len(r.File))
	}
}

// A file that stops reading partway leaves a valid archive with a short entry.
//
// That is the property the trailing-descriptor form exists for: nothing before
// the data commits to a size, so a file that vanished mid-archive truncates one
// entry rather than corrupting the container.
func TestAFileThatVanishesMidStreamLeavesAValidArchive(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	if _, err := w.AddFile("before.txt", testMtime, strings.NewReader("intact")); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	n, err := w.AddFile("vanishes.txt", testMtime, &failingReader{data: []byte("partial")})
	if err == nil {
		t.Fatal("the failing read was not reported")
	}
	if n == 0 {
		t.Fatal("nothing was written before the failure, so this proves nothing")
	}
	if _, err := w.AddFile("after.txt", testMtime, strings.NewReader("also intact")); err != nil {
		t.Fatalf("AddFile after the failure: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := readBack(t, buf.Bytes())
	if len(r.File) != 3 {
		t.Fatalf("got %d entries, want 3", len(r.File))
	}
	// The entries either side are whole, and the short one reads as what was
	// copied rather than failing its digest.
	for _, f := range r.File {
		rc, oerr := f.Open()
		if oerr != nil {
			t.Fatalf("opening %s: %v", f.Name, oerr)
		}
		body, rerr := io.ReadAll(rc)
		if cerr := rc.Close(); cerr != nil {
			t.Errorf("closing %s: %v", f.Name, cerr)
		}
		if rerr != nil {
			t.Errorf("%s does not read back: %v", f.Name, rerr)
		}
		if f.Name == "before.txt" && string(body) != "intact" {
			t.Errorf("the entry before the failure = %q", body)
		}
		if f.Name == "after.txt" && string(body) != "also intact" {
			t.Errorf("the entry after the failure = %q", body)
		}
	}
}

// A timestamp the format cannot represent is clamped rather than wrapped. A
// wrapped one is a date an extractor shows.
func TestATimestampBeforeTheFormatsEpochIsClamped(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	// 1970, which is a decade before the format's own epoch.
	if _, err := w.AddFile("old.txt", 0, strings.NewReader("x")); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := readBack(t, buf.Bytes())
	got := r.File[0].Modified
	if got.Year() != 1980 {
		t.Fatalf("the timestamp reads as %v, want it clamped to the format's epoch", got)
	}
}

// The size fields are the 64-bit ones, unconditionally, so the same path is
// taken whatever the archive holds.
func TestTheSizesAreTheWideFormEvenWhenSmall(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if _, err := w.AddFile("tiny.txt", testMtime, strings.NewReader("x")); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw := buf.Bytes()
	// The wide trailer is present, which is what a reader consults when the
	// classic one carries the sentinel values.
	if !bytes.Contains(raw, []byte{0x50, 0x4b, 0x06, 0x06}) {
		t.Error("the archive carries no wide trailer")
	}
	if !bytes.Contains(raw, []byte{0x50, 0x4b, 0x06, 0x07}) {
		t.Error("the archive carries no locator")
	}
	readBack(t, raw)
}

// A timestamp survives the round trip at the format's own resolution, which is
// two seconds.
func TestATimestampSurvivesAtTheFormatsResolution(t *testing.T) {
	when := time.Date(2026, time.August, 21, 14, 30, 45, 0, time.UTC)
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if _, err := w.AddFile("t.txt", when.UnixNano(), strings.NewReader("x")); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got := readBack(t, buf.Bytes()).File[0].Modified.UTC()
	if diff := got.Sub(when); diff > 2*time.Second || diff < -2*time.Second {
		t.Fatalf("the timestamp came back as %v, want %v within the format's resolution", got, when)
	}
}

// failingReader hands back some bytes and then an error, which is what a file
// removed mid-archive looks like.
type failingReader struct {
	data []byte
	sent bool
}

func (f *failingReader) Read(p []byte) (int, error) {
	if !f.sent {
		f.sent = true
		n := copy(p, f.data)
		return n, nil
	}
	return 0, errors.New("the file went away")
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
