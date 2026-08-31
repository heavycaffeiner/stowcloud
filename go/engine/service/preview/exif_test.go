package preview

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// tiffWith builds a minimal little-endian TIFF header carrying one orientation
// entry, which is the structure the parser walks.
func tiffWith(orientation uint16, typ uint16) []byte {
	b := make([]byte, 8)
	copy(b[:2], "II")
	binary.LittleEndian.PutUint16(b[2:], 42)
	binary.LittleEndian.PutUint32(b[4:], 8) // the IFD begins right after

	entry := make([]byte, 2+ifdEntryLen)
	binary.LittleEndian.PutUint16(entry[0:], 1) // one entry
	binary.LittleEndian.PutUint16(entry[2:], tagOrientation)
	binary.LittleEndian.PutUint16(entry[4:], typ)
	binary.LittleEndian.PutUint32(entry[6:], 1) // count
	binary.LittleEndian.PutUint16(entry[10:], orientation)
	return append(b, entry...)
}

// jpegWith wraps a TIFF block in the APP1/Exif segment a camera writes.
func jpegWith(tif []byte) []byte {
	const header = "Exif\x00\x00"
	payload := append([]byte(header), tif...)
	seg := make([]byte, 0, 4+len(payload))
	seg = append(seg, 0xff, 0xd8) // SOI
	seg = append(seg, 0xff, 0xe1) // APP1
	// The segment length is a u16 by format, and every fixture here is far
	// inside it, so a fixture that outgrew the field is a broken fixture.
	segLen, err := num.Narrow[uint16](2 + len(payload))
	if err != nil {
		panic("the exif fixture does not fit a jpeg segment")
	}
	seg = binary.BigEndian.AppendUint16(seg, segLen)
	seg = append(seg, payload...)
	return seg
}

// Every rotation the tag defines is read back, because a camera writes an
// upright sensor image plus a rotation and dropping it turns portraits
// sideways.
func TestReadOrientationVectors(t *testing.T) {
	for _, o := range []Orientation{
		OrientationNormal, OrientationFlipH, OrientationRot180, OrientationFlipV,
		OrientationTranspose, OrientationRot90, OrientationTransverse, OrientationRot270,
	} {
		tif := tiffWith(uint16(o), tagTypeShort)
		if got := ReadOrientation(tif); got != o {
			t.Errorf("a bare TIFF carrying %d read as %d", o, got)
		}
		if got := ReadOrientation(jpegWith(tif)); got != o {
			t.Errorf("a JPEG carrying %d read as %d", o, got)
		}
	}
}

// Anything that cannot be read is upright, which is the safe answer: an image
// with no tag is already upright, and one whose tag will not parse is not
// worth rotating on a guess.
func TestReadOrientationDefaultsToNormal(t *testing.T) {
	cases := map[string][]byte{
		"empty":                   nil,
		"not an image":            []byte("hello there"),
		"a png":                   []byte("\x89PNG\r\n\x1a\n"),
		"a truncated tiff":        []byte("II\x2a\x00\x08"),
		"a tiff with bad magic":   {'I', 'I', 0x00, 0x00, 8, 0, 0, 0},
		"a tiff with a bad order": {'X', 'Y', 42, 0, 8, 0, 0, 0},
		"an out-of-range value":   tiffWith(99, tagTypeShort),
		"a zero value":            tiffWith(0, tagTypeShort),
		"the wrong tag type":      tiffWith(uint16(OrientationRot90), 4),
		"a jpeg with no exif":     {0xff, 0xd8, 0xff, 0xda},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ReadOrientation(in); got != OrientationNormal {
				t.Errorf("got %d, want OrientationNormal", got)
			}
		})
	}
}

// The IFD offset comes off the file, so an offset outside the slice must be
// refused before it is indexed.
func TestAnOffsetOutsideTheSliceIsRefused(t *testing.T) {
	b := make([]byte, 8)
	copy(b[:2], "II")
	binary.LittleEndian.PutUint16(b[2:], 42)
	binary.LittleEndian.PutUint32(b[4:], 1<<30) // far past the end
	if got := ReadOrientation(b); got != OrientationNormal {
		t.Errorf("an out-of-range IFD offset produced %d", got)
	}

	// An offset inside the header is equally invalid: the IFD cannot overlap
	// the eight bytes that name it.
	binary.LittleEndian.PutUint32(b[4:], 2)
	if got := ReadOrientation(b); got != OrientationNormal {
		t.Errorf("an IFD offset inside the header produced %d", got)
	}
}

// The entry count comes off the file too, so without a ceiling a crafted
// header would drive the scan for as long as it liked.
func TestTheEntryCountIsCapped(t *testing.T) {
	b := make([]byte, 8)
	copy(b[:2], "II")
	binary.LittleEndian.PutUint16(b[2:], 42)
	binary.LittleEndian.PutUint32(b[4:], 8)
	// A count far past what the buffer holds. The scan must stop at the buffer
	// rather than reading past it, and must not run for 65535 iterations of
	// bounds checks on a two-byte tail.
	count := make([]byte, 2)
	binary.LittleEndian.PutUint16(count, 0xffff)
	if got := ReadOrientation(append(b, count...)); got != OrientationNormal {
		t.Errorf("a huge entry count produced %d", got)
	}
}

// The scanned prefix is bounded, so a large file does not become a large scan.
func TestTheScannedPrefixIsBounded(t *testing.T) {
	tif := tiffWith(uint16(OrientationRot90), tagTypeShort)
	// The tag is inside the prefix, so it is found.
	if got := ReadOrientation(tif); got != OrientationRot90 {
		t.Fatalf("the fixture does not read back: %d", got)
	}
	// A JPEG whose declared segment runs past the scan bound stops rather than
	// walking a file-sized buffer.
	huge := make([]byte, exifMaxScan+1024)
	huge[0], huge[1] = 0xff, 0xd8
	huge[2], huge[3] = 0xff, 0xe0
	binary.BigEndian.PutUint16(huge[4:], 0xfffe)
	if got := ReadOrientation(huge); got != OrientationNormal {
		t.Errorf("a segment past the scan bound produced %d", got)
	}
}

// Apply produces a fresh pixel buffer, so nothing carries across: a thumbnail
// of a holiday photo must not take its GPS coordinates to whoever the folder
// is shared with.
func TestApplyRotatesAndSwapsAxes(t *testing.T) {
	// A 3x2 image with a marked corner, so a rotation is visible.
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	mark := color.RGBA{R: 0xff, A: 0xff}
	src.Set(0, 0, mark)

	for _, o := range []Orientation{OrientationRot90, OrientationRot270, OrientationTranspose, OrientationTransverse} {
		out := o.Apply(src)
		if b := out.Bounds(); b.Dx() != 2 || b.Dy() != 3 {
			t.Errorf("%d produced %dx%d, want the axes swapped to 2x3", o, b.Dx(), b.Dy())
		}
	}
	for _, o := range []Orientation{OrientationFlipH, OrientationFlipV, OrientationRot180} {
		out := o.Apply(src)
		if b := out.Bounds(); b.Dx() != 3 || b.Dy() != 2 {
			t.Errorf("%d produced %dx%d, want 3x2", o, b.Dx(), b.Dy())
		}
	}

	// A horizontal flip moves the marked corner to the far side.
	flipped := OrientationFlipH.Apply(src)
	if r, _, _, _ := flipped.At(2, 0).RGBA(); r>>8 != 0xff {
		t.Error("the flip did not move the marked pixel")
	}
	// A 180 turn moves it to the opposite corner.
	turned := OrientationRot180.Apply(src)
	if r, _, _, _ := turned.At(2, 1).RGBA(); r>>8 != 0xff {
		t.Error("the 180 turn did not move the marked pixel")
	}
}

// Normal and an undefined value both return the source untouched, so a file
// with no tag costs no copy.
func TestApplyIsIdentityForNormalAndInvalid(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	if got := OrientationNormal.Apply(src); got != image.Image(src) {
		t.Error("the normal orientation copied the image")
	}
	if got := Orientation(99).Apply(src); got != image.Image(src) {
		t.Error("an undefined orientation copied the image")
	}
	if Orientation(0).Valid() || Orientation(9).Valid() || !OrientationRot90.Valid() {
		t.Error("Valid does not match the eight defined values")
	}
}

func FuzzReadOrientationNeverPanics(f *testing.F) {
	f.Add(tiffWith(uint16(OrientationRot90), tagTypeShort))
	f.Add(jpegWith(tiffWith(uint16(OrientationRot180), tagTypeShort)))
	f.Add([]byte{0xff, 0xd8, 0xff, 0xe1, 0xff, 0xff})
	f.Add([]byte("II\x2a\x00\xff\xff\xff\xff"))
	f.Add([]byte("MM\x00\x2a\x00\x00\x00\x08"))
	f.Fuzz(func(t *testing.T, in []byte) {
		got := ReadOrientation(in)
		// Whatever the structure said, the answer is always one of the eight.
		if !got.Valid() {
			t.Errorf("ReadOrientation returned %d, which is not a defined orientation", got)
		}
	})
}

// A looping structure terminates: the recursion is capped and every offset is
// checked before it is indexed.
func TestALoopingStructureTerminates(t *testing.T) {
	// A JPEG whose APP1 segment carries a TIFF whose IFD points back at the
	// start of the same block.
	tif := make([]byte, 32)
	copy(tif[:2], "II")
	binary.LittleEndian.PutUint16(tif[2:], 42)
	binary.LittleEndian.PutUint32(tif[4:], 8)
	binary.LittleEndian.PutUint16(tif[8:], 1)
	binary.LittleEndian.PutUint16(tif[10:], tagOrientation)
	binary.LittleEndian.PutUint16(tif[12:], tagTypeShort)
	binary.LittleEndian.PutUint32(tif[14:], 1)
	binary.LittleEndian.PutUint16(tif[18:], uint16(OrientationRot90))

	nested := jpegWith(tif)
	if got := ReadOrientation(nested); got != OrientationRot90 {
		t.Errorf("the nested structure read as %d", got)
	}

	// The same bytes truncated mid-entry stop rather than indexing past.
	if got := ReadOrientation(bytes.Clone(nested[:len(nested)-8])); !got.Valid() {
		t.Errorf("a truncated structure produced %d", got)
	}
}
