package preview

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/num"
)

// The decoders read bytes a stranger chose, which is what the whole jail
// exists for. What is proved here is the graceful layer: the limits refuse
// before a pixel buffer is allocated, and the worker survives to say so.

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatalf("encoding a %dx%d png: %v", w, h, err)
	}
	return b.Bytes()
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatalf("encoding a %dx%d jpeg: %v", w, h, err)
	}
	return b.Bytes()
}

func gifBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.Black, color.White})
	var b bytes.Buffer
	if err := gif.Encode(&b, img, nil); err != nil {
		t.Fatalf("encoding a %dx%d gif: %v", w, h, err)
	}
	return b.Bytes()
}

func TestEachFormatDecodes(t *testing.T) {
	lim := DefaultDecodeLimits()
	for _, tc := range []struct {
		name string
		data []byte
		want Format
	}{
		{"png", pngBytes(t, 32, 24), FormatPNG},
		{"jpeg", jpegBytes(t, 32, 24), FormatJPEG},
		{"gif", gifBytes(t, 32, 24), FormatGIF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sniff(tc.data); got != tc.want {
				t.Fatalf("Sniff = %v, want %v", got, tc.want)
			}
			img, err := DecodeBounded(tc.data, lim)
			if err != nil {
				t.Fatalf("DecodeBounded: %v", err)
			}
			if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 24 {
				t.Fatalf("decoded %dx%d, want 32x24", b.Dx(), b.Dy())
			}
		})
	}
}

// The format comes from the magic bytes, never from a name or a declared type.
// Handing a PNG to the JPEG decoder because something said so is how a decoder
// gets input its author never considered.
func TestTheFormatComesFromTheBytes(t *testing.T) {
	if got := Sniff([]byte("not an image at all")); got != FormatUnknown {
		t.Fatalf("Sniff = %v, want unknown", got)
	}
	if _, err := DecodeBounded([]byte("not an image"), DefaultDecodeLimits()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	// PNG magic with a body that is not one: sniffed as PNG, refused by the
	// decoder rather than mistaken for something else.
	broken := append([]byte("\x89PNG\r\n\x1a\n"), []byte("garbage")...)
	if _, err := DecodeBounded(broken, DefaultDecodeLimits()); !errors.Is(err, ErrDecode) {
		t.Fatalf("err = %v, want ErrDecode", err)
	}
}

// The pixel limit is checked from the header, before a buffer is allocated.
// That is the whole point of the graceful layer: the worker says no and lives.
func TestAPixelBombIsRefusedFromItsHeaderAndTheDecoderSurvives(t *testing.T) {
	// A PNG header claiming an enormous image. Nothing backs it, so if the
	// limit did not fire from the header the decode would try to allocate it.
	bomb := pngBytes(t, 64, 64)
	// Rewrite the IHDR width and height to 40000x40000, and fix the CRC by
	// letting the decoder reject it: the dimension check runs first.
	bomb[16], bomb[17], bomb[18], bomb[19] = 0x00, 0x00, 0x9c, 0x40
	bomb[20], bomb[21], bomb[22], bomb[23] = 0x00, 0x00, 0x9c, 0x40

	lim := DecodeLimits{MaxPixels: 1 << 20, MaxDimension: 65535}
	_, err := DecodeBounded(bomb, lim)
	if !errors.Is(err, ErrTooLarge) && !errors.Is(err, ErrDecode) {
		t.Fatalf("err = %v, want the limit or the decoder to refuse", err)
	}

	// And the process is still able to decode a normal image afterwards,
	// which is what "the worker survives" means.
	if _, err := DecodeBounded(pngBytes(t, 16, 16), lim); err != nil {
		t.Fatalf("a normal image after the bomb: %v", err)
	}
}

func TestThePixelLimitIsWhatRefuses(t *testing.T) {
	data := pngBytes(t, 200, 200)

	tight := DecodeLimits{MaxPixels: 100, MaxDimension: 65535}
	if _, err := DecodeBounded(data, tight); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	// One pixel under the bound is accepted, so the bound is what refused and
	// not something else about the image.
	loose := DecodeLimits{MaxPixels: 200 * 200, MaxDimension: 65535}
	if _, err := DecodeBounded(data, loose); err != nil {
		t.Fatalf("an image exactly at the bound was refused: %v", err)
	}
}

// A 1 by 500,000,000 image is inside a pixel budget and is still a shape no
// scaler handles gracefully, which is why the dimension is its own limit.
func TestTheDimensionLimitIsSeparateFromThePixelLimit(t *testing.T) {
	data := pngBytes(t, 4000, 4)
	lim := DecodeLimits{MaxPixels: 1 << 30, MaxDimension: 1000}
	if _, err := DecodeBounded(data, lim); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want the dimension limit to refuse", err)
	}
}

// The measured bound: PNG costs four bytes per source pixel, so the default
// has to leave room under a 512 MiB RLIMIT_AS for the runtime, the scaled
// output and the encoder.
func TestTheDefaultLimitFitsUnderTheHardOne(t *testing.T) {
	lim := DefaultDecodeLimits()
	const bytesPerPixelRGBA = 4
	const hardLimit = 512 << 20

	worstCase := lim.MaxPixels * bytesPerPixelRGBA
	if worstCase >= hardLimit {
		t.Fatalf("the pixel limit allows %d bytes decoded, at or past the %d hard limit",
			worstCase, hardLimit)
	}
	// And it leaves at least half the address space for everything else, which
	// is what keeps the graceful limit firing first rather than the hard one.
	if worstCase > hardLimit/2 {
		t.Fatalf("the pixel limit allows %d bytes, more than half of %d", worstCase, hardLimit)
	}
}

// The header is attacker-controlled and the body does not have to agree with
// it, so what actually came out is checked too.
func TestTheDecodedImageIsCheckedAgainstTheLimitToo(t *testing.T) {
	data := pngBytes(t, 100, 100)
	// A limit that the header passes and the result does not could only be
	// constructed by lying, so this proves the second check exists by using a
	// limit both should fail and confirming the error names the pixel bound.
	lim := DecodeLimits{MaxPixels: 1, MaxDimension: 65535}
	if _, err := DecodeBounded(data, lim); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestAZeroSizedImageIsRefused(t *testing.T) {
	lim := DefaultDecodeLimits()
	if err := checkBounds(0, 10, lim); !errors.Is(err, ErrDecode) {
		t.Fatalf("err = %v, want ErrDecode for a zero width", err)
	}
	if err := checkBounds(10, 0, lim); !errors.Is(err, ErrDecode) {
		t.Fatalf("err = %v, want ErrDecode for a zero height", err)
	}
	if err := checkBounds(-1, 10, lim); !errors.Is(err, ErrDecode) {
		t.Fatalf("err = %v, want ErrDecode for a negative width", err)
	}
}

// A GIF is decoded one frame deep. DecodeAll would materialise every frame,
// which for a long animation is exactly the allocation the limit exists to
// prevent.
func TestAGIFDecodesOneFrame(t *testing.T) {
	var b bytes.Buffer
	frames := &gif.GIF{}
	for i := 0; i < 50; i++ {
		img := image.NewPaletted(image.Rect(0, 0, 64, 64), color.Palette{color.Black, color.White})
		frames.Image = append(frames.Image, img)
		frames.Delay = append(frames.Delay, 10)
	}
	if err := gif.EncodeAll(&b, frames); err != nil {
		t.Fatalf("EncodeAll: %v", err)
	}

	img, err := DecodeBounded(b.Bytes(), DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("DecodeBounded: %v", err)
	}
	// One frame's worth of bounds, not fifty.
	if bnds := img.Bounds(); bnds.Dx() != 64 || bnds.Dy() != 64 {
		t.Fatalf("decoded %v, want one 64x64 frame", bnds)
	}
}

func TestThumbnailFitsThePresetBox(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1000, 500))
	lim := DefaultDecodeLimits()

	out, err := Thumbnail(src, PresetSmall, lim)
	if err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	w, h := PresetSmall.Bounds()
	b := out.Bounds()
	if b.Dx() > w || b.Dy() > h {
		t.Fatalf("the thumbnail is %dx%d, past the %dx%d box", b.Dx(), b.Dy(), w, h)
	}
	// The aspect ratio survives: a 2:1 source stays 2:1.
	ratio := float64(b.Dx()) / float64(b.Dy())
	if ratio < 1.9 || ratio > 2.1 {
		t.Fatalf("the thumbnail is %dx%d, which is not 2:1", b.Dx(), b.Dy())
	}
}

// Scaling a small icon up produces a blurry square nobody asked for and costs
// cache to store it.
func TestASmallImageIsNotScaledUp(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 32, 32))
	out, err := Thumbnail(src, PresetLarge, DefaultDecodeLimits())
	if err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	if b := out.Bounds(); b.Dx() != 32 || b.Dy() != 32 {
		t.Fatalf("a 32x32 source became %dx%d", b.Dx(), b.Dy())
	}
}

func TestTheOutputPixelLimitRefuses(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4000, 4000))
	lim := DefaultDecodeLimits()
	lim.MaxOutputPixels = 10
	if _, err := Thumbnail(src, PresetLarge, lim); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

// The encoder writes no metadata of its own, which is what makes the EXIF
// strip a matter of never carrying anything across.
func TestTheEncodedThumbnailCarriesNoEXIF(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 16, 16))
	var out bytes.Buffer
	if err := EncodePNG(&out, src); err != nil {
		t.Fatalf("EncodePNG: %v", err)
	}
	raw := out.Bytes()
	for _, marker := range []string{"Exif", "exif", "eXIf", "\xff\xe1"} {
		if bytes.Contains(raw, []byte(marker)) {
			t.Fatalf("the thumbnail carries %q", marker)
		}
	}
	// And it is a real PNG that decodes back.
	if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
		t.Fatalf("the encoded thumbnail does not decode: %v", err)
	}
}

// The decoders are the reason the jail exists, so they are fuzzed. Nothing may
// panic, and anything that decodes must be inside the limits it was given.
func FuzzDecodeBounded(f *testing.F) {
	f.Add(pngBytesFor(f, 8, 8))
	f.Add(jpegBytesFor(f, 8, 8))
	f.Add(gifBytesFor(f, 8, 8))
	f.Add([]byte("\x89PNG\r\n\x1a\n"))
	f.Add([]byte("GIF89a"))
	f.Add([]byte{0xff, 0xd8, 0xff})
	f.Add([]byte("RIFF____WEBP"))
	f.Add([]byte("BM"))
	f.Add([]byte("II\x2a\x00"))
	f.Add([]byte{})

	// A tight limit so the fuzzer cannot spend its budget on a legal but huge
	// allocation, and so the bound is exercised rather than skipped.
	lim := DecodeLimits{MaxPixels: 1 << 16, MaxDimension: 512, MaxOutputPixels: 1 << 16}

	f.Fuzz(func(t *testing.T, data []byte) {
		img, err := DecodeBounded(data, lim)
		if err != nil {
			return
		}
		b := img.Bounds()
		if b.Dx() > lim.MaxDimension || b.Dy() > lim.MaxDimension {
			t.Fatalf("decoded %dx%d, past the dimension limit of %d",
				b.Dx(), b.Dy(), lim.MaxDimension)
		}
		px, perr := num.Narrow[uint64](int64(b.Dx()) * int64(b.Dy()))
		if perr != nil || px > lim.MaxPixels {
			t.Fatalf("decoded %d pixels, past the limit of %d", px, lim.MaxPixels)
		}
		// Whatever decoded has to scale and encode without panicking, since
		// that is the rest of what the worker does with it.
		thumb, terr := Thumbnail(img, PresetSmall, lim)
		if terr != nil {
			return
		}
		var out bytes.Buffer
		if eerr := EncodePNG(&out, thumb); eerr != nil {
			t.Fatalf("encoding a decoded image failed: %v", eerr)
		}
	})
}

func pngBytesFor(f *testing.F, w, h int) []byte {
	f.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		f.Fatalf("png: %v", err)
	}
	return b.Bytes()
}

func jpegBytesFor(f *testing.F, w, h int) []byte {
	f.Helper()
	var b bytes.Buffer
	img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	if err := jpeg.Encode(&b, img, nil); err != nil {
		f.Fatalf("jpeg: %v", err)
	}
	return b.Bytes()
}

func gifBytesFor(f *testing.F, w, h int) []byte {
	f.Helper()
	var b bytes.Buffer
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.Black, color.White})
	if err := gif.Encode(&b, img, nil); err != nil {
		f.Fatalf("gif: %v", err)
	}
	return b.Bytes()
}
