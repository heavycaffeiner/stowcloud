//go:build linux

package preview

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The acceptance question for a thumbnail pipeline is whether something other
// than this program can open what it produced.
//
// The pool tests already drive real images through real worker processes, but
// the fixtures are encoded by Go's own library and the output is checked by
// Sniff, which reads magic bytes this package itself wrote. Neither can tell a
// valid PNG from a well-headed file with a corrupt body, and neither can see
// metadata that survived.
//
// These use tools that share no code with the pipeline: ImageMagick's identify
// as a decoder, exiv2 as a metadata reader, and Python's imaging library to
// build a fixture carrying real EXIF.

// lookup finds an external tool, or reports that this machine has none.
func lookup(t *testing.T, name, why string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed, so %s", name, why)
	}
	return path
}

// run invokes a tool resolved by lookup against a path this test created.
//
// stdin is closed deliberately: several of these tools read it when an argument
// names nothing readable, and a test that hangs is worse than one that fails.
//
// The gosec exception is here rather than at each call site: every caller
// passes a binary that came from LookPath and arguments that are this test's
// own temporary files or constants in this file.
func run(tool string, args ...string) *exec.Cmd {
	cmd := exec.Command(tool, args...) //nolint:gosec // the tool comes from LookPath and the arguments are this file's own constants and temp files.
	cmd.Stdin = nil
	return cmd
}

// openFixture opens a file this test wrote and closes it when the test ends.
func openFixture(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // a path this test created under its own TempDir.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Errorf("closing the fixture %s: %v", filepath.Base(path), err)
		}
	})
	return f
}

// identify asks the independent decoder what it sees, failing outright on a file
// it cannot parse.
//
// stdin is closed deliberately: identify reads it when an argument names no
// readable image, and a test that hangs is worse than one that fails.
func identify(t *testing.T, path, format string) string {
	t.Helper()
	tool := lookup(t, "identify", "no independent decoder is available")

	out, err := run(tool, "-format", format, path).Output()
	if err != nil {
		t.Fatalf("the independent decoder could not read %s: %v", filepath.Base(path), err)
	}
	return strings.TrimSpace(string(out))
}

// thumbnail runs one job through a real worker process and returns the output
// file's path.
func thumbnail(t *testing.T, src *os.File) string {
	t.Helper()
	p := newPool(t, 1, "")
	out := outputFile(t)

	resp, err := p.Generate(t.Context(), Request{
		Kind:      JobImage,
		Preset:    PresetSmall,
		Flags:     FlagStripEXIF,
		MaxPixels: maxPixelsFor(),
	}, PlainSource{F: src}, out)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if resp.Status != StatusOK {
		t.Fatalf("the worker refused the source: status %v: %s", resp.Status, resp.Err)
	}
	return out.Name()
}

// A thumbnail this pipeline produced has to be an image an unrelated decoder
// opens, at the dimensions the pipeline claims.
func TestTheThumbnailOpensInAnIndependentDecoder(t *testing.T) {
	path := thumbnail(t, sourceFile(t, 400, 200))

	got := identify(t, path, "%w %h %m")
	if got != "256 128 PNG" {
		t.Errorf("the decoder reads the thumbnail as %q, want \"256 128 PNG\"", got)
	}
}

// A source this pipeline did not encode is the more honest input: the fixture
// comes from another program's PNG writer entirely.
func TestAForeignEncodedSourceRoundTrips(t *testing.T) {
	tool := lookup(t, "magick", "no independent encoder is available")
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")

	if out, err := run(tool, "-size", "600x300", "gradient:red-blue", src).CombinedOutput(); err != nil {
		t.Skipf("the independent encoder could not produce a fixture: %v\n%s", err, out)
	}

	in := openFixture(t, src)

	if got := identify(t, thumbnail(t, in), "%w %h"); got != "256 128" {
		t.Errorf("a 600x300 foreign-encoded source produced %q, want \"256 128\"", got)
	}
}

// The pixels have to be the source's rather than a blank canvas of the right
// size. A mean at either extreme, or a deviation of zero, is an empty image.
func TestTheThumbnailHoldsTheSourcePixels(t *testing.T) {
	path := thumbnail(t, sourceFile(t, 400, 200))

	mean, merr := strconv.ParseFloat(identify(t, path, "%[fx:mean]"), 64)
	sigma, serr := strconv.ParseFloat(identify(t, path, "%[fx:standard_deviation]"), 64)
	if merr != nil || serr != nil {
		t.Fatalf("the decoder's statistics did not parse: %v %v", merr, serr)
	}

	if mean <= 0.01 || mean >= 0.99 {
		t.Errorf("the thumbnail's mean is %v, which is a blank image rather than the source", mean)
	}
	if sigma <= 0.01 {
		t.Errorf("the thumbnail's standard deviation is %v, so every pixel is identical", sigma)
	}
}

// withEXIF builds a JPEG carrying real EXIF, using a library that shares no code
// with this pipeline, and confirms the metadata is actually there before the
// test relies on it.
func withEXIF(t *testing.T) string {
	t.Helper()
	python := lookup(t, "python3", "no independent encoder can write EXIF")
	exiv2 := lookup(t, "exiv2", "no independent reader can confirm the fixture")

	dir := t.TempDir()
	src := filepath.Join(dir, "withexif.jpg")

	script := `
from PIL import Image
import sys
img = Image.new("RGB", (400, 200))
for x in range(400):
    for y in range(200):
        img.putpixel((x, y), (x % 256, y % 256, 128))
exif = img.getexif()
exif[271] = "ACME"
exif[274] = 6
img.save(sys.argv[1], exif=exif)
`
	gen := run(python, "-c", script, src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("the imaging library could not write an EXIF fixture: %v\n%s", err, out)
	}

	// The fixture is only useful if it really carries the metadata, so the
	// independent reader confirms that before the assertion below means
	// anything.
	check := run(exiv2, "-p", "a", src)
	out, err := check.Output()
	if err != nil || !strings.Contains(string(out), "ACME") {
		t.Skipf("the fixture does not carry the metadata this test strips: %v\n%s", err, out)
	}
	return src
}

// Stripping metadata is a promise about what leaves this server: a thumbnail of
// a holiday photo must not carry its location to whoever the folder is shared
// with. Only a reader that parses metadata can see whether it held.
func TestTheThumbnailCarriesNoMetadata(t *testing.T) {
	exiv2 := lookup(t, "exiv2", "no independent reader is available")
	src := withEXIF(t)

	in := openFixture(t, src)

	out := thumbnail(t, in)

	// exiv2 exits non-zero when a file carries no metadata, which is the
	// passing case here, so the output is what is read rather than the status.
	// A non-zero exit means no metadata, which is the passing case, so the
	// output is what is read rather than the status.
	body, _ := run(exiv2, "-p", "a", out).Output() //nolint:errcheck // a non-zero exit is the assertion.

	if strings.Contains(string(body), "ACME") {
		t.Errorf("the camera make survived into the thumbnail:\n%s", body)
	}
	if strings.Contains(string(body), "Exif.Image.Orientation") {
		t.Errorf("the orientation tag survived rather than being applied and dropped:\n%s", body)
	}
	if n := strings.TrimSpace(string(body)); n != "" {
		t.Errorf("the thumbnail carries metadata:\n%s", n)
	}
}

// Orientation is applied to the pixels rather than carried, which is what keeps
// a portrait photo upright once the tag is gone. The fixture is 400x200 with
// the tag that means a quarter turn, so an applied rotation makes the thumbnail
// taller than it is wide.
func TestTheOrientationIsAppliedToThePixels(t *testing.T) {
	src := withEXIF(t)

	in := openFixture(t, src)

	got := identify(t, thumbnail(t, in), "%w %h")
	fields := strings.Fields(got)
	if len(fields) != 2 {
		t.Fatalf("the decoder reported %q", got)
	}
	w, werr := strconv.Atoi(fields[0])
	h, herr := strconv.Atoi(fields[1])
	if werr != nil || herr != nil {
		// Unparsed dimensions would compare as zeros and pass silently.
		t.Fatalf("the decoder reported dimensions that did not parse: %q", got)
	}

	// A landscape source with a quarter-turn tag becomes a portrait thumbnail.
	// Without the rotation it would stay wider than it is tall.
	if w >= h {
		t.Errorf("the thumbnail is %dx%d, so the orientation tag was dropped rather than applied", w, h)
	}
}
