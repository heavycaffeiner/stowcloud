package preview

import (
	"encoding/binary"
	"image"
)

// EXIF handling, which is really orientation handling.
//
// Go's encoders write no EXIF, so this is not about removal so much as about
// never carrying anything across: the pipeline decodes to a pixel buffer and
// encodes from the buffer. The only metadata that survives is the orientation,
// which is applied to the pixels and then discarded.
//
// It has to be applied rather than dropped. A camera writes an upright sensor
// image plus a rotation tag, so dropping the tag without applying it turns
// every portrait photo sideways. It has to be discarded rather than carried,
// because a thumbnail of a holiday photo would otherwise take its GPS
// coordinates to whoever the folder is shared with.

// Orientation is the EXIF tag's eight values.
type Orientation uint8

const (
	OrientationNormal Orientation = 1
	OrientationFlipH  Orientation = 2
	OrientationRot180 Orientation = 3
	OrientationFlipV  Orientation = 4
	// OrientationTranspose is a flip across the main diagonal.
	OrientationTranspose Orientation = 5
	OrientationRot90     Orientation = 6
	// OrientationTransverse is a flip across the anti-diagonal.
	OrientationTransverse Orientation = 7
	OrientationRot270     Orientation = 8
)

// exif tag and format constants.
const (
	tagOrientation = 0x0112
	// exifMaxEntries bounds an IFD. The count comes off the file, so without
	// a ceiling a crafted header would drive the scan for as long as it liked.
	exifMaxEntries = 1024
	// exifMaxScan bounds how much of a file is searched for the marker.
	exifMaxScan = 256 << 10
)

// ReadOrientation finds the EXIF orientation in a JPEG or TIFF.
//
// It returns OrientationNormal for anything it cannot read, which is the safe
// answer: an image with no tag is already upright, and one whose tag cannot be
// parsed is not worth rotating on a guess.
//
// This is a deliberately small parser over a structure a stranger wrote. It
// reads one tag and never allocates from a length in the file.
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

// jpegOrientation walks the JPEG marker segments looking for APP1/Exif.
func jpegOrientation(data []byte) Orientation {
	pos := 2 // past the SOI
	for pos+4 <= len(data) {
		if data[pos] != 0xff {
			return OrientationNormal
		}
		marker := data[pos+1]
		// Start of scan: the entropy-coded data begins and there are no more
		// headers to walk.
		if marker == 0xda {
			return OrientationNormal
		}
		segLen := int(binary.BigEndian.Uint16(data[pos+2:]))
		if segLen < 2 || pos+2+segLen > len(data) {
			return OrientationNormal
		}
		if marker == 0xe1 {
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

// tiffOrientation reads the orientation out of a TIFF header.
func tiffOrientation(tif []byte, depth int) Orientation {
	if depth > 2 || len(tif) < 8 {
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
	if bo.Uint16(tif[2:]) != 42 {
		return OrientationNormal
	}

	off := int(bo.Uint32(tif[4:]))
	if off < 8 || off+2 > len(tif) {
		return OrientationNormal
	}

	count := int(bo.Uint16(tif[off:]))
	if count > exifMaxEntries {
		count = exifMaxEntries
	}
	entries := off + 2
	for i := 0; i < count; i++ {
		at := entries + i*12
		if at+12 > len(tif) {
			return OrientationNormal
		}
		if bo.Uint16(tif[at:]) != tagOrientation {
			continue
		}
		// A SHORT, count one, whose value sits in the entry itself.
		if bo.Uint16(tif[at+2:]) != 3 {
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

func (o Orientation) valid() bool { return o >= OrientationNormal && o <= OrientationRot270 }

// Apply rotates and flips an image into its upright form.
//
// The result carries no metadata at all, because it is a fresh pixel buffer.
func (o Orientation) Apply(src image.Image) image.Image {
	if o == OrientationNormal || !o.valid() {
		return src
	}

	b := src.Bounds()
	w, h := b.Dx(), b.Dy()

	// The four quarter turns swap the axes.
	outW, outH := w, h
	switch o {
	case OrientationTranspose, OrientationRot90, OrientationTransverse, OrientationRot270:
		outW, outH = h, w
	}

	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case OrientationFlipH:
				dx, dy = w-1-x, y
			case OrientationRot180:
				dx, dy = w-1-x, h-1-y
			case OrientationFlipV:
				dx, dy = x, h-1-y
			case OrientationTranspose:
				dx, dy = y, x
			case OrientationRot90:
				dx, dy = h-1-y, x
			case OrientationTransverse:
				dx, dy = h-1-y, w-1-x
			case OrientationRot270:
				dx, dy = y, w-1-x
			default:
				dx, dy = x, y
			}
			if dx < 0 || dy < 0 || dx >= outW || dy >= outH {
				continue
			}
			dst.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
