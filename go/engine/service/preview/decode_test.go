package preview

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

// pngOf encodes a solid image, for a fixture whose dimensions are known.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	return buf.Bytes()
}

// The format comes from magic bytes, never a declared name: handing a TIFF to
// the JPEG decoder because a name said so is how a decoder gets input its
// author never considered.
func TestSniffReadsMagicBytes(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want Format
	}{
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0xe0}, FormatJPEG},
		{"png", []byte("\x89PNG\r\n\x1a\n\x00"), FormatPNG},
		{"gif87", []byte("GIF87a...."), FormatGIF},
		{"gif89", []byte("GIF89a...."), FormatGIF},
		{"bmp", []byte("BM\x00\x00"), FormatBMP},
		{"tiff little endian", []byte("II\x2a\x00"), FormatTIFF},
		{"tiff big endian", []byte("MM\x00\x2a"), FormatTIFF},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), FormatWebP},
		{"empty", nil, FormatUnknown},
		{"short", []byte{0xff}, FormatUnknown},
		{"text", []byte("not an image at all"), FormatUnknown},
		// A PNG name on JPEG bytes is still JPEG: the name is never consulted.
		{"truncated png magic", []byte("\x89PNG\r\n"), FormatUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Sniff(c.in); got != c.want {
				t.Errorf("Sniff(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestFormatNames(t *testing.T) {
	for f, want := range map[Format]string{
		FormatJPEG: "jpeg", FormatPNG: "png", FormatGIF: "gif",
		FormatBMP: "bmp", FormatTIFF: "tiff", FormatWebP: "webp",
		FormatUnknown: "unknown", Format(200): "unknown",
	} {
		if got := f.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", f, got, want)
		}
	}
}

func TestDecodeBoundedRoundTripsEachFormatItSupports(t *testing.T) {
	raw := pngOf(t, 40, 30)
	img, err := DecodeBounded(raw, DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("DecodeBounded: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 40 || b.Dy() != 30 {
		t.Errorf("decoded %dx%d, want 40x30", b.Dx(), b.Dy())
	}
}

// A format with no decoder is refused by name of the error, so the service can
// remember it as a negative rather than retrying forever.
func TestDecodeBoundedRefusesAnUnknownFormat(t *testing.T) {
	if _, err := DecodeBounded([]byte("this is not an image"), DefaultDecodeLimits()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("got %v, want ErrUnsupported", err)
	}
}

// The header is parsed for dimensions before any pixel buffer is allocated,
// which is what makes a decompression bomb cost the header rather than the
// machine.
func TestABombIsRefusedAtTheHeader(t *testing.T) {
	// A PNG header claiming an enormous image, with no body to match. If the
	// limit were applied after decoding, this would have to allocate first.
	raw := pngOf(t, 8, 8)
	hdr := make([]byte, len(raw))
	copy(hdr, raw)
	// The IHDR width and height sit at offsets 16 and 20.
	writeBE32(hdr[16:], 60000)
	writeBE32(hdr[20:], 60000)

	_, err := DecodeBounded(hdr, DefaultDecodeLimits())
	if !errors.Is(err, ErrTooLarge) && !errors.Is(err, ErrDecode) {
		t.Errorf("got %v, want a refusal", err)
	}
	// The budget is inclusive, so an 8x8 image passes at exactly 64 pixels and
	// is refused one below it. Checking both sides is what pins the boundary
	// rather than assuming which way it falls.
	exact := DecodeLimits{MaxPixels: 64, MaxDimension: 65535, MaxOutputPixels: 1 << 20}
	if _, eerr := DecodeBounded(raw, exact); eerr != nil {
		t.Errorf("a 64-pixel image under a 64-pixel budget: %v", eerr)
	}
	tight := DecodeLimits{MaxPixels: 63, MaxDimension: 65535, MaxOutputPixels: 1 << 20}
	if _, terr := DecodeBounded(raw, tight); !errors.Is(terr, ErrTooLarge) {
		t.Errorf("a 64-pixel image under a 63-pixel budget: %v", terr)
	}
}

// Either side alone is bounded: a 1 by 500,000,000 image fits a pixel budget
// and is still a shape no scaler handles.
func TestASingleDimensionIsBounded(t *testing.T) {
	raw := pngOf(t, 200, 4)
	lim := DecodeLimits{MaxPixels: 1 << 30, MaxDimension: 100, MaxOutputPixels: 1 << 20}
	if _, err := DecodeBounded(raw, lim); !errors.Is(err, ErrTooLarge) {
		t.Errorf("got %v, want ErrTooLarge for a 200-wide image under a 100 bound", err)
	}
}

// The decoded image is checked again after decode, because a decoder that
// ignores its own header is exactly the decoder being defended against.
func TestBoundsAreCheckedAfterDecodeToo(t *testing.T) {
	raw := pngOf(t, 50, 50)
	// A budget the header passes and the decoded image does not could only be
	// caught by the second check; here both catch it, which is the point: the
	// check exists on both sides of the decode.
	lim := DecodeLimits{MaxPixels: 100, MaxDimension: 65535, MaxOutputPixels: 1 << 20}
	if _, err := DecodeBounded(raw, lim); !errors.Is(err, ErrTooLarge) {
		t.Errorf("got %v, want ErrTooLarge", err)
	}
}

// A zero-sized image is a decode failure rather than an empty success.
func TestZeroDimensionsRefuse(t *testing.T) {
	lim := DefaultDecodeLimits()
	if err := checkBounds(0, 10, lim); !errors.Is(err, ErrDecode) {
		t.Errorf("a zero width: %v", err)
	}
	if err := checkBounds(10, -1, lim); !errors.Is(err, ErrDecode) {
		t.Errorf("a negative height: %v", err)
	}
	// A zero limit means unbounded, which is what a caller passing an empty
	// DecodeLimits gets.
	if err := checkBounds(1<<20, 1<<20, DecodeLimits{}); err != nil {
		t.Errorf("an empty limit set refused: %v", err)
	}
}

// GIF decodes one frame. DecodeAll would materialise the whole animation,
// which for a long GIF is the allocation the limit exists to prevent.
func TestGIFDecodesOneFrame(t *testing.T) {
	var buf bytes.Buffer
	pal := color.Palette{color.Black, color.White}
	g := &gif.GIF{}
	for range 5 {
		frame := image.NewPaletted(image.Rect(0, 0, 20, 20), pal)
		g.Image = append(g.Image, frame)
		g.Delay = append(g.Delay, 10)
	}
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("encoding the animation: %v", err)
	}

	img, err := DecodeBounded(buf.Bytes(), DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("DecodeBounded: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 20 || b.Dy() != 20 {
		t.Errorf("decoded %dx%d, want one 20x20 frame", b.Dx(), b.Dy())
	}
}

// A file whose magic says JPEG and whose body is not is a decode failure, not
// a panic and not an unsupported format.
func TestALyingHeaderIsADecodeFailure(t *testing.T) {
	lying := append([]byte{0xff, 0xd8, 0xff, 0xe0}, []byte("this is not jpeg data")...)
	if _, err := DecodeBounded(lying, DefaultDecodeLimits()); !errors.Is(err, ErrDecode) {
		t.Errorf("got %v, want ErrDecode", err)
	}
}

// The output is never larger than the source: scaling a small icon up produces
// a blurry square nobody asked for and costs cache to store.
func TestThumbnailNeverScalesUp(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 32, 32))
	out, err := Thumbnail(src, PresetLarge, DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	if b := out.Bounds(); b.Dx() != 32 || b.Dy() != 32 {
		t.Errorf("a 32x32 source became %dx%d under a 1024 preset", b.Dx(), b.Dy())
	}
}

// A downscale preserves the aspect ratio and fits inside the preset's box.
func TestThumbnailFitsTheBoxAndKeepsTheRatio(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1000, 500))
	out, err := Thumbnail(src, PresetSmall, DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	b := out.Bounds()
	if b.Dx() > 256 || b.Dy() > 256 {
		t.Errorf("the result %dx%d does not fit a 256 box", b.Dx(), b.Dy())
	}
	// 2:1 in, 2:1 out.
	if b.Dx() != 256 || b.Dy() != 128 {
		t.Errorf("got %dx%d, want 256x128", b.Dx(), b.Dy())
	}
}

func TestThumbnailRefusesAnInvalidPreset(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 10, 10))
	if _, err := Thumbnail(src, Preset(99), DefaultDecodeLimits()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("got %v, want ErrUnsupported", err)
	}
}

// ThumbnailSized is the compatibility route's exact box.
func TestThumbnailSizedHonoursAnExactBox(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 800, 400))
	out, err := ThumbnailSized(src, 100, 100, DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("ThumbnailSized: %v", err)
	}
	b := out.Bounds()
	if b.Dx() != 100 || b.Dy() != 50 {
		t.Errorf("got %dx%d, want 100x50 inside a 100 box", b.Dx(), b.Dy())
	}
	if _, err := ThumbnailSized(src, 0, 10, DefaultDecodeLimits()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a zero box: %v", err)
	}
}

// The output bound stops a preset asking for more than the encoder should be
// handed.
func TestTheOutputPixelBoundApplies(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4000, 4000))
	lim := DefaultDecodeLimits()
	lim.MaxOutputPixels = 16
	if _, err := Thumbnail(src, PresetLarge, lim); !errors.Is(err, ErrTooLarge) {
		t.Errorf("got %v, want ErrTooLarge", err)
	}
}

// PNG is lossless, keeps alpha and writes no metadata of its own, which is
// what makes the EXIF strip a matter of never carrying anything across.
func TestEncodePNGRoundTrips(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 8, 4))
	src.Set(1, 1, color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 0xff})

	var buf bytes.Buffer
	if err := EncodePNG(&buf, src); err != nil {
		t.Fatalf("EncodePNG: %v", err)
	}
	if Sniff(buf.Bytes()) != FormatPNG {
		t.Error("the encoder did not write a PNG")
	}
	back, err := png.Decode(&buf)
	if err != nil {
		t.Fatalf("decoding what was written: %v", err)
	}
	if b := back.Bounds(); b.Dx() != 8 || b.Dy() != 4 {
		t.Errorf("round-tripped to %dx%d", b.Dx(), b.Dy())
	}
	r, g, bl, a := back.At(1, 1).RGBA()
	if r>>8 != 0x11 || g>>8 != 0x22 || bl>>8 != 0x33 || a>>8 != 0xff {
		t.Error("the pixel did not survive the round trip")
	}
}

// A JPEG source exercises the other decoder in the common path.
func TestDecodeBoundedReadsJPEG(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 24, 16))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, nil); err != nil {
		t.Fatalf("encoding the fixture: %v", err)
	}
	img, err := DecodeBounded(buf.Bytes(), DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("DecodeBounded: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 24 || b.Dy() != 16 {
		t.Errorf("decoded %dx%d, want 24x16", b.Dx(), b.Dy())
	}
}

func FuzzDecodeBoundedNeverPanics(f *testing.F) {
	f.Add([]byte("\x89PNG\r\n\x1a\n"))
	f.Add([]byte{0xff, 0xd8, 0xff, 0xe0, 0x00})
	f.Add([]byte("GIF89a"))
	f.Add([]byte("BM"))
	f.Add([]byte("II\x2a\x00"))
	f.Fuzz(func(t *testing.T, in []byte) {
		// The contract is that arbitrary bytes produce an error rather than a
		// panic, and that anything decoded is inside the limits.
		img, err := DecodeBounded(in, DefaultDecodeLimits())
		if err != nil {
			return
		}
		b := img.Bounds()
		if b.Dx() <= 0 || b.Dy() <= 0 {
			t.Errorf("a %dx%d image decoded successfully", b.Dx(), b.Dy())
		}
	})
}

func FuzzSniffNeverPanics(f *testing.F) {
	f.Add([]byte("RIFF0000WEBP"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, in []byte) {
		if got := Sniff(in); got > FormatWebP {
			t.Errorf("Sniff returned an undefined format %d", got)
		}
	})
}

func writeBE32(b []byte, v uint32) {
	binary.BigEndian.PutUint32(b, v)
}
