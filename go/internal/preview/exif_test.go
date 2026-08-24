package preview

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/num"
)

// Orientation is the one piece of metadata that survives a decode, and it
// survives as pixels rather than as a tag.

// exifJPEG builds a minimal JPEG carrying an APP1 Exif segment with one
// orientation tag, which is what a camera writes.
func exifJPEG(orientation uint16, bigEndian bool) []byte {
	bo := binary.ByteOrder(binary.LittleEndian)
	order := "II"
	if bigEndian {
		bo, order = binary.BigEndian, "MM"
	}
	u16 := func(b []byte, v uint16) []byte {
		var tmp [2]byte
		bo.PutUint16(tmp[:], v)
		return append(b, tmp[:]...)
	}
	u32 := func(b []byte, v uint32) []byte {
		var tmp [4]byte
		bo.PutUint32(tmp[:], v)
		return append(b, tmp[:]...)
	}

	var tif []byte
	tif = append(tif, order...)
	tif = u16(tif, 42)
	tif = u32(tif, 8) // the IFD begins right after the header
	tif = u16(tif, 1) // one entry
	tif = u16(tif, tagOrientation)
	tif = u16(tif, 3) // SHORT
	tif = u32(tif, 1) // count
	tif = u16(tif, orientation)
	tif = append(tif, 0, 0) // the rest of the value field
	tif = u32(tif, 0)       // no next IFD

	payload := append([]byte("Exif\x00\x00"), tif...)

	out := []byte{0xff, 0xd8} // SOI
	out = append(out, 0xff, 0xe1)
	var segLen [2]byte
	n, nerr := num.Narrow[uint16](len(payload) + 2)
	if nerr != nil {
		panic(nerr)
	}
	binary.BigEndian.PutUint16(segLen[:], n)
	out = append(out, segLen[:]...)
	out = append(out, payload...)
	out = append(out, 0xff, 0xda) // SOS, where the header walk stops
	return out
}

func TestOrientationIsReadFromAJPEG(t *testing.T) {
	for _, want := range []Orientation{
		OrientationNormal, OrientationFlipH, OrientationRot180, OrientationFlipV,
		OrientationTranspose, OrientationRot90, OrientationTransverse, OrientationRot270,
	} {
		for _, big := range []bool{false, true} {
			got := ReadOrientation(exifJPEG(uint16(want), big))
			if got != want {
				t.Fatalf("ReadOrientation(bigEndian=%v) = %d, want %d", big, got, want)
			}
		}
	}
}

// Anything unreadable is Normal, which is the safe answer: an image with no
// tag is already upright, and one whose tag cannot be parsed is not worth
// rotating on a guess.
func TestUnreadableMetadataIsNormal(t *testing.T) {
	for _, name := range []string{"empty", "garbage", "truncated", "no exif"} {
		var data []byte
		switch name {
		case "garbage":
			data = []byte("not an image at all")
		case "truncated":
			data = exifJPEG(6, false)
			data = data[:len(data)/2]
		case "no exif":
			data = []byte{0xff, 0xd8, 0xff, 0xda}
		}
		if got := ReadOrientation(data); got != OrientationNormal {
			t.Fatalf("%s returned orientation %d, want normal", name, got)
		}
	}
}

// An orientation value outside the eight the tag defines is not applied.
func TestAnOutOfRangeOrientationIsNormal(t *testing.T) {
	for _, bad := range []uint16{0, 9, 255, 65535} {
		if got := ReadOrientation(exifJPEG(bad, false)); got != OrientationNormal {
			t.Fatalf("orientation %d was accepted as %d", bad, got)
		}
	}
}

// The trap this exists for: a camera writes an upright sensor image plus a
// rotation tag, so dropping the tag without applying it turns every portrait
// photo sideways.
func TestAQuarterTurnSwapsTheAxes(t *testing.T) {
	// A landscape source, as the sensor recorded it.
	src := image.NewRGBA(image.Rect(0, 0, 40, 10))
	for _, o := range []Orientation{
		OrientationRot90, OrientationRot270, OrientationTranspose, OrientationTransverse,
	} {
		out := o.Apply(src)
		b := out.Bounds()
		if b.Dx() != 10 || b.Dy() != 40 {
			t.Fatalf("orientation %d produced %dx%d, want the axes swapped to 10x40",
				o, b.Dx(), b.Dy())
		}
	}
}

func TestAHalfTurnKeepsTheAxes(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 40, 10))
	for _, o := range []Orientation{OrientationRot180, OrientationFlipH, OrientationFlipV} {
		b := o.Apply(src).Bounds()
		if b.Dx() != 40 || b.Dy() != 10 {
			t.Fatalf("orientation %d produced %dx%d, want 40x10", o, b.Dx(), b.Dy())
		}
	}
}

func TestNormalOrientationIsANoOp(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	if got := OrientationNormal.Apply(src); got != image.Image(src) {
		t.Fatal("a normal orientation copied the image")
	}
}

// The pixels actually move, which is the difference between applying the tag
// and merely reshaping the bounds.
func TestTheRotationMovesThePixels(t *testing.T) {
	// A 2x1 image: red on the left, blue on the right.
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	red := color.RGBA{R: 255, A: 255}
	blue := color.RGBA{B: 255, A: 255}
	src.Set(0, 0, red)
	src.Set(1, 0, blue)

	// A 180-degree turn puts blue on the left.
	out := OrientationRot180.Apply(src)
	if r, _, _, _ := out.At(0, 0).RGBA(); r > 0x8000 {
		t.Fatal("after a half turn the left pixel is still red")
	}
	if _, _, b, _ := out.At(0, 0).RGBA(); b < 0x8000 {
		t.Fatal("after a half turn the left pixel is not blue")
	}

	// A horizontal flip does the same for this shape, and a vertical one does
	// not move anything in a one-row image.
	flipped := OrientationFlipV.Apply(src)
	if r, _, _, _ := flipped.At(0, 0).RGBA(); r < 0x8000 {
		t.Fatal("a vertical flip moved a pixel in a one-row image")
	}
}

// Rotating four times by ninety degrees returns the original, which is the
// cheapest check that the index arithmetic is not subtly transposed.
func TestFourQuarterTurnsReturnTheOriginal(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	n := 0
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			n++
			src.Set(x, y, color.RGBA{R: uint8(n * 40), A: 255})
		}
	}

	cur := image.Image(src)
	for i := 0; i < 4; i++ {
		cur = OrientationRot90.Apply(cur)
	}
	if b := cur.Bounds(); b.Dx() != 3 || b.Dy() != 2 {
		t.Fatalf("four turns produced %dx%d, want 3x2", b.Dx(), b.Dy())
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			wr, _, _, _ := src.At(x, y).RGBA()
			gr, _, _, _ := cur.At(x, y).RGBA()
			if wr != gr {
				t.Fatalf("pixel %d,%d is %d after four turns, want %d", x, y, gr, wr)
			}
		}
	}
}

// The rotated image is a fresh buffer, so nothing from the source's metadata
// can ride along into the encoder.
func TestTheRotatedImageIsAFreshBuffer(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	out := OrientationRot90.Apply(src)
	if _, ok := out.(*image.RGBA); !ok {
		t.Fatalf("the rotation produced %T, want a plain RGBA buffer", out)
	}
	var buf bytes.Buffer
	if err := EncodePNG(&buf, out); err != nil {
		t.Fatalf("encoding the rotated image: %v", err)
	}
	if bytes.Contains(buf.Bytes(), []byte("Exif")) {
		t.Fatal("the encoded rotation carries EXIF")
	}
}

// The EXIF parser reads a structure a stranger wrote, so it is fuzzed. It must
// never panic and never return a value outside the eight the tag defines.
func FuzzReadOrientation(f *testing.F) {
	f.Add(exifJPEG(6, false))
	f.Add(exifJPEG(1, true))
	f.Add([]byte{0xff, 0xd8, 0xff, 0xe1, 0x00, 0x08, 'E', 'x', 'i', 'f', 0, 0})
	f.Add([]byte("II\x2a\x00\x08\x00\x00\x00"))
	f.Add([]byte("MM\x00\x2a\x00\x00\x00\x08"))
	f.Add([]byte{0xff, 0xd8})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		got := ReadOrientation(data)
		if got < OrientationNormal || got > OrientationRot270 {
			t.Fatalf("ReadOrientation(%x) = %d, outside the defined range", data, got)
		}
		// Whatever came back has to be appliable without panicking, since that
		// is what the pipeline does with it next.
		src := image.NewRGBA(image.Rect(0, 0, 3, 2))
		out := got.Apply(src)
		if b := out.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 {
			t.Fatalf("orientation %d produced an empty image", got)
		}
	})
}
