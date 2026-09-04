// Linux only, matching the package under test.
//go:build linux

package handler

import (
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview"
)

// decodableFormats pairs each format the decoder sniffs with the extensions a
// file of that format is named with. The bytes are the magic numbers Sniff
// matches on, which is what makes this an assertion about the decoder rather
// than a copy of the list under test.
func decodableFormats() []struct {
	format preview.Format
	header []byte
	exts   []string
} {
	return []struct {
		format preview.Format
		header []byte
		exts   []string
	}{
		{preview.FormatJPEG, []byte{0xff, 0xd8, 0xff}, []string{".jpg", ".jpeg"}},
		{preview.FormatPNG, []byte("\x89PNG\r\n\x1a\n"), []string{".png"}},
		{preview.FormatGIF, []byte("GIF89a"), []string{".gif"}},
		{preview.FormatBMP, []byte("BM"), []string{".bmp"}},
		{preview.FormatTIFF, []byte("II\x2a\x00"), []string{".tif", ".tiff"}},
		{preview.FormatWebP, []byte("RIFF\x00\x00\x00\x00WEBP"), []string{".webp"}},
	}
}

// The table above covers every format the decoder defines.
//
// Without this the table is only as good as whoever last edited it: a format
// added to the decoder and not added here leaves every test below passing
// while the listing quietly advertises no thumbnail for it. The enum is walked
// rather than counted against a literal, so the check describes the decoder
// instead of restating a number that also has to be maintained.
func TestTheFormatTableCoversTheDecodersEnum(t *testing.T) {
	covered := make(map[preview.Format]bool)
	for _, f := range decodableFormats() {
		covered[f.format] = true
	}

	// FormatUnknown is the zero value and names no format, so the walk starts
	// past it and stops at the first value the decoder does not name.
	for i := 1; ; i++ {
		f := preview.Format(uint8(i))
		if f.String() == preview.FormatUnknown.String() {
			if i == 1 {
				t.Fatal("the decoder names no formats at all")
			}
			return
		}
		if !covered[f] {
			t.Errorf("the decoder defines %s and this file's table does not list it, "+
				"so nothing checks whether the listing advertises it", f)
		}
	}
}

// Every format the decoder can sniff has an extension the listing advertises.
//
// The two halves are written apart and have to agree: a format the decoder
// gained without an extension here is an image that silently never gets a
// thumbnail, which is the failure this hint exists to prevent and gives no
// symptom at the point it breaks.
func TestEveryDecodableFormatIsAdvertised(t *testing.T) {
	for _, f := range decodableFormats() {
		if got := preview.Sniff(f.header); got != f.format {
			t.Fatalf("the table is stale: %v sniffs as %v", f.format, got)
		}
		for _, ext := range f.exts {
			if !previewable("photo" + ext) {
				t.Errorf("%s decodes %s but the listing advertises no thumbnail for it",
					f.format, ext)
			}
		}
	}
}

// The advertised set claims nothing the decoder cannot sniff. A wrong entry
// costs one request that comes back refused, so this is the cheaper half of
// the pair, but a name that can never decode has no business being advertised.
func TestNothingUndecodableIsAdvertised(t *testing.T) {
	for _, name := range []string{
		"notes.txt", "archive.zip", "video.mp4", "page.pdf", "sheet.xlsx",
		"raw.heic", "vector.svg", "noext", "trailingdot.",
	} {
		if previewable(name) {
			t.Errorf("%s is advertised as previewable and no decoder handles it", name)
		}
	}
}

// The extension is matched irrespective of case, since a filesystem carries
// whatever the camera wrote and IMG_0001.JPG is the common form.
func TestTheExtensionMatchIgnoresCase(t *testing.T) {
	for _, name := range []string{"IMG_0001.JPG", "Photo.PnG", "scan.TIFF"} {
		if !previewable(name) {
			t.Errorf("%s was not recognised", name)
		}
	}
}

// A directory never carries the hint. It has no bytes to decode, and a grid
// that asked would spend a request per folder to be told so.
func TestADirectoryIsNeverPreviewable(t *testing.T) {
	v := EntryOf(core.Entry{Name: "photos.png", IsDir: true}, "", EntryRefs{})
	if v.Preview != nil {
		t.Error("a directory was projected as previewable")
	}
}

// The hint is absent rather than false on a file nothing can decode, so a
// client tests for presence instead of reading a field that is usually there
// and usually false.
func TestTheHintIsAbsentOnAnUndecodableFile(t *testing.T) {
	if v := EntryOf(core.Entry{Name: "notes.txt"}, "", EntryRefs{}); v.Preview != nil {
		t.Error("a text file carries a preview hint")
	}
	v := EntryOf(core.Entry{Name: "photo.png"}, "", EntryRefs{})
	if v.Preview == nil || !v.Preview.Available {
		t.Error("an image carries no preview hint")
	}
}
