package preview

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"
)

// Decoding, wrapped in two layers of limit.
//
// RLIMIT_AS is the hard stop, killing the worker at the cost of one thumbnail.
// DecodeLimits is the graceful one, rejecting the job with a typed error while
// the worker lives on. The graceful limit must trigger first in the common case,
// since a 40,000 by 40,000 PNG is an unremarkable find in a photo library and
// killing a worker over it is a poor trade.

// Decode failures.
var (
	// ErrUnsupported reports a format this build cannot decode.
	ErrUnsupported = errors.New("preview: unsupported image format")
	// ErrTooLarge reports a decode limit rejecting the job, with the worker
	// surviving.
	ErrTooLarge = errors.New("preview: the image is too large to decode")
	// ErrDecode reports a file that is not what it claimed.
	ErrDecode = errors.New("preview: the image could not be decoded")
)

// DecodeLimits constrains a single decode.
//
// The values are derived for Go's decoders, since allocation behaviour differs
// between implementations and a limit tuned for one is guesswork for another.
// Measured against this tree's decoders, heap bytes per source pixel:
//
//	image/png   4.00   RGBA, the worst case, and what fixes the bound
//	image/jpeg  1.50   YCbCr 4:2:0
//	image/gif   1.00   paletted, single frame
//
// At 4 bytes per pixel a 100 Mpx ceiling would demand 400 MiB of heap for the
// source by itself, ahead of the runtime, the scaled output and the encoder. A
// 64 Mpx ceiling measures 257 MiB for a PNG, which sits inside the worker's
// address-space limit with room for everything else and keeps that limit a
// backstop rather than the first thing to fire.
type DecodeLimits struct {
	// MaxPixels limits the source's width times height. It is a uint64 to avoid
	// the overflow a 32-bit multiplication reaches well before the values an
	// attacker would supply.
	MaxPixels uint64
	// MaxDimension limits each side individually. A 1 by 500,000,000 image fits
	// within a pixel budget while remaining a shape no decoder or scaler handles
	// well.
	MaxDimension int
	// MaxOutputPixels limits what gets produced, so no preset can request more
	// than the encoder ought to receive.
	MaxOutputPixels uint64
}

// DefaultDecodeLimits holds the measured bounds described above.
func DefaultDecodeLimits() DecodeLimits {
	return DecodeLimits{
		MaxPixels:       64 << 20, // 67.1 Mpx, about 257 MiB decoded as RGBA
		MaxDimension:    65535,    // what the wire's u16 can report back
		MaxOutputPixels: 4 << 20,
	}
}

// Format names a decodable image format.
type Format uint8

const (
	FormatUnknown Format = iota
	FormatJPEG
	FormatPNG
	FormatGIF
	FormatBMP
	FormatTIFF
	FormatWebP
)

func (f Format) String() string {
	switch f {
	case FormatJPEG:
		return "jpeg"
	case FormatPNG:
		return "png"
	case FormatGIF:
		return "gif"
	case FormatBMP:
		return "bmp"
	case FormatTIFF:
		return "tiff"
	case FormatWebP:
		return "webp"
	}
	return "unknown"
}

// Sniff determines a format from its magic bytes.
//
// Never from a filename or a declared content type, both of which the attacker
// selects. Handing a TIFF to the JPEG decoder because a name said so is exactly
// how a decoder receives input its author never anticipated.
func Sniff(data []byte) Format {
	switch {
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return FormatJPEG
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return FormatPNG
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return FormatGIF
	case len(data) >= 2 && data[0] == 'B' && data[1] == 'M':
		return FormatBMP
	case len(data) >= 4 && (string(data[:4]) == "II\x2a\x00" || string(data[:4]) == "MM\x00\x2a"):
		return FormatTIFF
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return FormatWebP
	}
	return FormatUnknown
}

// DecodeBounded decodes an image while rejecting a decompression bomb before any
// pixel buffer is allocated.
//
// The ordering is intentional. Format detection uses the magic bytes and never a
// name. Dimensions come from parsing the header, which for every format here
// touches only the header and not the compressed body. Only afterwards, with
// dimensions known and validated, is the body decoded.
func DecodeBounded(data []byte, lim DecodeLimits) (image.Image, error) {
	format := Sniff(data)
	if format == FormatUnknown {
		return nil, ErrUnsupported
	}

	cfg, err := decodeConfig(data, format)
	if err != nil {
		return nil, fmt.Errorf("%w: reading the %s header: %w", ErrDecode, format, err)
	}
	if berr := checkBounds(cfg.Width, cfg.Height, lim); berr != nil {
		return nil, berr
	}

	img, err := decodeBody(data, format)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrDecode, format, err)
	}

	// The header is attacker-controlled and the body need not match it.
	// Rechecking against what actually emerged is what prevents a decoder that
	// disregarded its own header from slipping past the limit.
	b := img.Bounds()
	if berr := checkBounds(b.Dx(), b.Dy(), lim); berr != nil {
		return nil, berr
	}
	return img, nil
}

// checkBounds enforces the pixel and dimension limits.
func checkBounds(w, h int, lim DecodeLimits) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("%w: a %dx%d image", ErrDecode, w, h)
	}
	if lim.MaxDimension > 0 && (w > lim.MaxDimension || h > lim.MaxDimension) {
		return fmt.Errorf("%w: %dx%d exceeds the %d-pixel dimension limit",
			ErrTooLarge, w, h, lim.MaxDimension)
	}
	// The multiplication uses uint64 so it cannot wrap before the comparison,
	// which is precisely the overflow an attacker would target.
	px, perr := num.Narrow[uint64](int64(w) * int64(h))
	if perr != nil {
		return fmt.Errorf("%w: a %dx%d image", ErrTooLarge, w, h)
	}
	if lim.MaxPixels > 0 && px > lim.MaxPixels {
		return fmt.Errorf("%w: %d pixels exceeds the limit of %d",
			ErrTooLarge, px, lim.MaxPixels)
	}
	return nil
}

// decodeConfig obtains dimensions without decoding the body.
func decodeConfig(data []byte, f Format) (image.Config, error) {
	r := bytes.NewReader(data)
	switch f {
	case FormatJPEG:
		return jpeg.DecodeConfig(r)
	case FormatPNG:
		return png.DecodeConfig(r)
	case FormatGIF:
		// DecodeConfig reports the logical screen bounds, which is what the
		// pre-decode limit requires.
		return gif.DecodeConfig(r)
	case FormatBMP:
		return bmp.DecodeConfig(r)
	case FormatTIFF:
		return tiff.DecodeConfig(r)
	case FormatWebP:
		return webp.DecodeConfig(r)
	}
	return image.Config{}, ErrUnsupported
}

func decodeBody(data []byte, f Format) (image.Image, error) {
	r := bytes.NewReader(data)
	switch f {
	case FormatJPEG:
		return jpeg.Decode(r)
	case FormatPNG:
		return png.Decode(r)
	case FormatGIF:
		// gif.Decode rather than gif.DecodeAll: the former halts after one frame
		// while the latter materialises the entire animation, which for a long
		// GIF is exactly the allocation this limit exists to stop.
		return gif.Decode(r)
	case FormatBMP:
		return bmp.Decode(r)
	case FormatTIFF:
		return tiff.Decode(r)
	case FormatWebP:
		// x/image/webp handles a still WebP. An animated one is rejected instead
		// of yielding its first frame.
		return webp.Decode(r)
	}
	return nil, ErrUnsupported
}

// Thumbnail fits an image into a preset's box while keeping the aspect ratio.
func Thumbnail(src image.Image, p Preset, lim DecodeLimits) (image.Image, error) {
	maxW, maxH := p.Bounds()
	if maxW <= 0 || maxH <= 0 {
		return nil, fmt.Errorf("%w: preset %d", ErrUnsupported, p)
	}
	return ThumbnailSized(src, maxW, maxH, lim)
}

// ThumbnailSized fits an image into an explicit box, which the compatibility
// content route requires. The caller has already clamped the dimensions.
//
// Output never exceeds the source: enlarging a 32 by 32 icon to 1024 by 1024
// yields a blurry square nobody requested and spends a megabyte of cache
// storing it.
func ThumbnailSized(src image.Image, maxW, maxH int, lim DecodeLimits) (image.Image, error) {
	if maxW <= 0 || maxH <= 0 {
		return nil, fmt.Errorf("%w: a %dx%d box", ErrUnsupported, maxW, maxH)
	}

	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("%w: a %dx%d source", ErrDecode, w, h)
	}

	scale := min(float64(maxW)/float64(w), float64(maxH)/float64(h))
	if scale >= 1 {
		// Already inside the box.
		return src, nil
	}
	outW := max(int(float64(w)*scale), 1)
	outH := max(int(float64(h)*scale), 1)

	outPx, perr := num.Narrow[uint64](int64(outW) * int64(outH))
	if perr != nil {
		return nil, fmt.Errorf("%w: an output of %dx%d", ErrTooLarge, outW, outH)
	}
	if lim.MaxOutputPixels > 0 && outPx > lim.MaxOutputPixels {
		return nil, fmt.Errorf("%w: an output of %d pixels exceeds the limit of %d",
			ErrTooLarge, outPx, lim.MaxOutputPixels)
	}

	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	scaleInto(dst, src)
	return dst, nil
}

// scaleInto resamples src into dst using area averaging, which keeps a
// downscaled photo from aliasing into noise. This produces a thumbnail, and the
// scaler is not where the interesting failures live.
func scaleInto(dst *image.RGBA, src image.Image) {
	sb := src.Bounds()
	db := dst.Bounds()
	xRatio := float64(sb.Dx()) / float64(db.Dx())
	yRatio := float64(sb.Dy()) / float64(db.Dy())

	for y := db.Min.Y; y < db.Max.Y; y++ {
		sy0 := sb.Min.Y + int(float64(y)*yRatio)
		sy1 := sb.Min.Y + int(float64(y+1)*yRatio)
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := db.Min.X; x < db.Max.X; x++ {
			sx0 := sb.Min.X + int(float64(x)*xRatio)
			sx1 := sb.Min.X + int(float64(x+1)*xRatio)
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, bl, a, n uint64
			for sy := sy0; sy < sy1 && sy < sb.Max.Y; sy++ {
				for sx := sx0; sx < sx1 && sx < sb.Max.X; sx++ {
					pr, pg, pb, pa := src.At(sx, sy).RGBA()
					r += uint64(pr)
					g += uint64(pg)
					bl += uint64(pb)
					a += uint64(pa)
					n++
				}
			}
			if n == 0 {
				continue
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = toByte(r / n)
			dst.Pix[i+1] = toByte(g / n)
			dst.Pix[i+2] = toByte(bl / n)
			dst.Pix[i+3] = toByte(a / n)
		}
	}
}

// EncodePNG writes a thumbnail.
//
// PNG because there is no WebP encoder in the standard library or in x/image.
// It is lossless, keeps alpha and writes no metadata of its own, which is what
// makes the EXIF strip a matter of never carrying anything across rather than
// of removing it.
func EncodePNG(w io.Writer, img image.Image) error {
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(w, img); err != nil {
		return fmt.Errorf("preview: encoding the thumbnail: %w", err)
	}
	return nil
}

// toByte reduces an averaged 16-bit channel to eight bits. RGBA reports 0 to
// 65535, so the shift lands within a byte, and the clamp is what turns that from
// an argument in a comment into a property of the code.
func toByte(v uint64) uint8 {
	v >>= 8
	if v > 0xff {
		return 0xff
	}
	return uint8(v)
}
