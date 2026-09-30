package preview

import (
	"encoding/binary"
	"image"
)

// EXIF handling, which amounts to orientation handling.
//
// Go's encoders emit no EXIF, so this concerns never propagating anything rather
// than removal: the pipeline decodes into a pixel buffer and encodes back out of
// it. Orientation is the sole surviving metadata, applied to the pixels and then
// discarded.
//
// Applying it is necessary rather than optional. A camera stores an upright
// sensor image alongside a rotation tag, so discarding the tag without applying
// it leaves every portrait photo sideways. Discarding it afterwards is equally
// necessary, since a thumbnail of a holiday photo would otherwise hand its GPS
// coordinates to everyone the folder is shared with.

// Orientation enumerates the EXIF tag's eight values.
type Orientation uint8

const (
	OrientationNormal Orientation = 1
	OrientationFlipH  Orientation = 2
	OrientationRot180 Orientation = 3
	OrientationFlipV  Orientation = 4
	// OrientationTranspose flips across the main diagonal.
	OrientationTranspose Orientation = 5
	OrientationRot90     Orientation = 6
	// OrientationTransverse flips across the anti-diagonal.
	OrientationTransverse Orientation = 7
	OrientationRot270     Orientation = 8
)

// The tag and the bounds this parser works under. Every one of them exists
// because the numbers driving the scan come off a file a stranger wrote.
const (
	tagOrientation = 0x0112
	// tagTypeShort is the TIFF SHORT type, the only one the orientation tag is
	// allowed to carry.
	tagTypeShort = 3
	// tiffMagic is the 42 every TIFF header carries after its byte order.
	tiffMagic = 42
	// ifdEntryLen is the fixed width of one directory entry.
	ifdEntryLen = 12
	// exifMaxEntries limits an IFD. The count originates in the file, so absent a
	// ceiling a crafted header could drive the scan indefinitely.
	exifMaxEntries = 1024
	// exifMaxScan limits how far into a file the marker is sought.
	exifMaxScan = 256 << 10
	// exifMaxDepth caps the recursion into a nested TIFF header, so a file
	// pointing at itself terminates.
	exifMaxDepth = 2
)

// ReadOrientation locates the EXIF orientation within a JPEG or TIFF.
//
// Anything unreadable yields OrientationNormal, the safe answer: an image
// lacking a tag is already upright, and one whose tag will not parse is not
// worth rotating on a guess.
//
// This parser is intentionally minimal, operating on a structure written by a
// stranger. It reads a single tag, never sizes an allocation from a length in
// the file, and validates every offset against the slice before indexing.
func ReadOrientation(data []byte) Orientation {
	if len(data) > exifMaxScan {
		data = data[:exifMaxScan]
	}
	switch Sniff(data) {
	case FormatJPEG:
		return jpegOrientation(data)
	case FormatTIFF:
		return tiffOrientation(data, 0)
	}
	return OrientationNormal
}

// jpegOrientation scans the JPEG marker segments for APP1/Exif.
func jpegOrientation(data []byte) Orientation {
	const (
		markerPrefix      = 0xff
		markerStartOfScan = 0xda
		markerAPP1        = 0xe1
	)

	pos := 2 // past the SOI
	for pos+4 <= len(data) {
		if data[pos] != markerPrefix {
			return OrientationNormal
		}
		marker := data[pos+1]
		// Start of scan: entropy-coded data begins here and no headers remain to
		// traverse.
		if marker == markerStartOfScan {
			return OrientationNormal
		}
		segLen := int(binary.BigEndian.Uint16(data[pos+2:]))
		if segLen < 2 || pos+2+segLen > len(data) {
			return OrientationNormal
		}
		if marker == markerAPP1 {
			seg := data[pos+4 : pos+2+segLen]
			const header = "Exif\x00\x00"
			if len(seg) > len(header) && string(seg[:len(header)]) == header {
				return tiffOrientation(seg[len(header):], 0)
			}
		}
		pos += 2 + segLen
	}
	return OrientationNormal
}

// tiffOrientation extracts the orientation from a TIFF header.
func tiffOrientation(tif []byte, depth int) Orientation {
	if depth > exifMaxDepth || len(tif) < 8 {
		return OrientationNormal
	}

	var bo binary.ByteOrder
	switch string(tif[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return OrientationNormal
	}
	if bo.Uint16(tif[2:]) != tiffMagic {
		return OrientationNormal
	}

	off := int(bo.Uint32(tif[4:]))
	if off < 8 || off+2 > len(tif) {
		return OrientationNormal
	}

	count := min(int(bo.Uint16(tif[off:])), exifMaxEntries)
	entries := off + 2
	for i := range count {
		at := entries + i*ifdEntryLen
		if at+ifdEntryLen > len(tif) {
			return OrientationNormal
		}
		if bo.Uint16(tif[at:]) != tagOrientation {
			continue
		}
		// A SHORT of count one, storing its value inline in the entry.
		if bo.Uint16(tif[at+2:]) != tagTypeShort {
			return OrientationNormal
		}
		raw := bo.Uint16(tif[at+8:])
		if raw < uint16(OrientationNormal) || raw > uint16(OrientationRot270) {
			return OrientationNormal
		}
		return Orientation(raw)
	}
	return OrientationNormal
}

// Valid reports whether o is one of the eight defined values.
func (o Orientation) Valid() bool { return o >= OrientationNormal && o <= OrientationRot270 }

// Apply rotates and flips an image into upright form.
//
// The result holds no metadata whatsoever, being a newly allocated pixel
// buffer.
func (o Orientation) Apply(src image.Image) image.Image {
	if o == OrientationNormal || !o.Valid() {
		return src
	}

	b := src.Bounds()
	w, h := b.Dx(), b.Dy()

	// Quarter turns exchange the two axes.
	outW, outH := w, h
	switch o {
	case OrientationTranspose, OrientationRot90, OrientationTransverse, OrientationRot270:
		outW, outH = h, w
	}

	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	for y := range h {
		for x := range w {
			dx, dy := o.mapPixel(x, y, w, h)
			if dx < 0 || dy < 0 || dx >= outW || dy >= outH {
				continue
			}
			dst.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// mapPixel is where one source pixel lands in the upright image.
func (o Orientation) mapPixel(x, y, w, h int) (dx, dy int) {
	switch o {
	case OrientationFlipH:
		return w - 1 - x, y
	case OrientationRot180:
		return w - 1 - x, h - 1 - y
	case OrientationFlipV:
		return x, h - 1 - y
	case OrientationTranspose:
		return y, x
	case OrientationRot90:
		return h - 1 - y, x
	case OrientationTransverse:
		return h - 1 - y, w - 1 - x
	case OrientationRot270:
		return y, w - 1 - x
	}
	return x, y
}
