// Package archive writes a zip stream.
//
// Stored entries only, always the 64-bit form, always the trailing-descriptor
// form. It exists rather than using a library because a library writer needs to
// seek: it goes back and patches the local header once an entry's size is
// known. An HTTP response body cannot be seeked, and this server deliberately
// never promises a length for an archive, so every entry is written with a
// zeroed size in its header and the real values after the bytes.
//
// That is also what makes the useful failure possible: an entry cut short
// because the file vanished mid-stream still produces a valid archive, because
// nothing before the entry's data committed to a size.
//
// Always the 64-bit form, rather than deciding per archive whether it is over
// four gigabytes or sixty-five thousand entries. One path, always correct, and
// the archives this produces are already-compressed media that is exactly the
// case the format exists for.
package archive

import (
	"encoding/binary"
	"hash/crc32"
	"io"
	"strings"
	"time"
)

// The signatures and the fixed fields.
const (
	sigLocalHeader   uint32 = 0x04034b50
	sigDataDesc      uint32 = 0x08074b50
	sigCentralHeader uint32 = 0x02014b50
	sigZip64EOCD     uint32 = 0x06064b50
	sigZip64Locator  uint32 = 0x07064b50
	sigEOCD          uint32 = 0x06054b50

	extraTagZip64 uint16 = 0x0001
	versionZip64  uint16 = 45

	modeFile uint32 = 0o100644 << 16
	// The directory bit as well as the mode, because some extractors read the
	// legacy attribute rather than the unix one.
	modeDir uint32 = (0o040755 << 16) | 0x10

	// Bit three says the sizes follow the data. Bit eleven says the name is
	// UTF-8, and leaving it unset was a real defect rather than a theoretical
	// one: without it the format defines a name as whatever the writer's local
	// code page happened to be, so every mainstream extractor guesses, and a
	// name outside ASCII comes out mangled or stops round-tripping at all.
	// This writer only ever produces UTF-8, so the flag is simply true.
	flagBits uint16 = 0x0008 | 0x0800
)

// copyChunk is the read size while streaming one entry. Bounded so an archive
// of any size costs one buffer.
const copyChunk = 256 << 10

// record is what the central directory needs about a finished entry.
type record struct {
	name    []byte
	crc     uint32
	size    uint64
	offset  uint64
	isDir   bool
	dosTime uint16
	dosDate uint16
}

// Writer streams a zip archive.
//
// Every write goes straight to the underlying writer. Nothing is buffered
// beyond one copy buffer, so the archive's size is not bounded by memory.
type Writer struct {
	out     io.Writer
	offset  uint64
	records []record
	// err is the first write failure. Once set, every later call is a no-op
	// returning it: a caller streaming a thousand entries should not have to
	// check after each one, and continuing after a broken pipe writes an
	// archive nobody is reading.
	err error
}

// NewWriter starts an archive.
func NewWriter(out io.Writer) *Writer { return &Writer{out: out} }

func (w *Writer) write(b []byte) error {
	if w.err != nil {
		return w.err
	}
	n, err := w.out.Write(b)
	w.offset += uint64(n) //nolint:gosec // a byte count from a successful write is never negative.
	if err != nil {
		w.err = err
	}
	return err
}

// AddDir writes a directory entry.
//
// Not needed for a directory that has files under it, since extracting those
// recreates the tree. It is what makes an empty directory appear at all.
func (w *Writer) AddDir(name string, mtimeNs int64) error {
	full := strings.TrimSuffix(name, "/") + "/"
	dosTime, dosDate := dosDateTime(mtimeNs)
	offset := w.offset
	if err := w.localHeader([]byte(full), dosTime, dosDate); err != nil {
		return err
	}
	if err := w.dataDescriptor(0, 0); err != nil {
		return err
	}
	w.records = append(w.records, record{
		name: []byte(full), offset: offset, isDir: true,
		dosTime: dosTime, dosDate: dosDate,
	})
	return nil
}

// AddFile streams one entry from r, and reports how many bytes it copied.
//
// A read that fails partway still produces a valid entry: the size and digest
// written after the data are the ones that were actually copied, which is what
// makes a file that vanished mid-archive a short entry rather than a corrupt
// archive.
func (w *Writer) AddFile(name string, mtimeNs int64, r io.Reader) (uint64, error) {
	dosTime, dosDate := dosDateTime(mtimeNs)
	offset := w.offset
	if err := w.localHeader([]byte(name), dosTime, dosDate); err != nil {
		return 0, err
	}

	digest := crc32.NewIEEE()
	buf := make([]byte, copyChunk)
	var total uint64
	var readErr error
	for {
		n, err := r.Read(buf)
		if n > 0 {
			digest.Write(buf[:n]) //nolint:errcheck // hash.Write never fails.
			if werr := w.write(buf[:n]); werr != nil {
				return total, werr
			}
			total += uint64(n) //nolint:gosec // a read count is never negative.
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			// The entry is closed out at the length that was read, so the
			// archive stays valid and the truncation is visible as a short
			// file rather than as a broken container.
			readErr = err
			break
		}
	}

	crc := digest.Sum32()
	if err := w.dataDescriptor(crc, total); err != nil {
		return total, err
	}
	w.records = append(w.records, record{
		name: []byte(name), crc: crc, size: total, offset: offset,
		dosTime: dosTime, dosDate: dosDate,
	})
	return total, readErr
}

// Err is the first write failure, or nil.
//
// It exists so a caller can tell the two failures AddFile reports apart. A
// read that failed leaves a valid archive and the next entry can still be
// written; a write that failed means the response body is gone and everything
// after it is wasted work.
func (w *Writer) Err() error { return w.err }

// AddBytes writes a small entry that is already in memory. It goes through the
// same header path as a real file rather than a second one.
func (w *Writer) AddBytes(name string, mtimeNs int64, data []byte) error {
	_, err := w.AddFile(name, mtimeNs, strings.NewReader(string(data)))
	return err
}

func (w *Writer) localHeader(name []byte, dosTime, dosDate uint16) error {
	h := make([]byte, 0, 30+len(name)+20)
	h = le32(h, sigLocalHeader)
	h = le16(h, versionZip64)
	h = le16(h, flagBits)
	h = le16(h, 0) // stored
	h = le16(h, dosTime)
	h = le16(h, dosDate)
	h = le32(h, 0)          // the digest follows the data
	h = le32(h, 0xFFFFFFFF) // the sizes are in the extra field
	h = le32(h, 0xFFFFFFFF)
	h = le16(h, uint16(len(name))) //nolint:gosec // the caller's names are path segments, far inside this bound.
	h = le16(h, 20)                // the extra field: tag, size, and two eight-byte sizes
	h = append(h, name...)
	h = le16(h, extraTagZip64)
	h = le16(h, 16)
	h = le64(h, 0) // uncompressed, deferred
	h = le64(h, 0) // compressed, deferred
	return w.write(h)
}

func (w *Writer) dataDescriptor(crc uint32, size uint64) error {
	d := make([]byte, 0, 24)
	d = le32(d, sigDataDesc)
	d = le32(d, crc)
	d = le64(d, size) // compressed, which for a stored entry is the same
	d = le64(d, size)
	return w.write(d)
}

// Close writes the central directory and the trailers.
func (w *Writer) Close() error {
	if w.err != nil {
		return w.err
	}
	entries := uint64(len(w.records))
	cdStart := w.offset

	for _, rec := range w.records {
		h := make([]byte, 0, 46+len(rec.name)+28)
		h = le32(h, sigCentralHeader)
		h = le16(h, versionZip64) // made by
		h = le16(h, versionZip64) // needed
		// This has to match the local header's flags exactly, or a reader that
		// compares them refuses the entry.
		h = le16(h, flagBits)
		h = le16(h, 0) // stored
		h = le16(h, rec.dosTime)
		h = le16(h, rec.dosDate)
		h = le32(h, rec.crc)
		h = le32(h, 0xFFFFFFFF) // sizes in the extra field
		h = le32(h, 0xFFFFFFFF)
		h = le16(h, uint16(len(rec.name))) //nolint:gosec // a path segment, far inside this bound.
		h = le16(h, 28)                    // tag, size, two sizes, and the offset
		h = le16(h, 0)                     // no comment
		h = le16(h, 0)                     // one volume
		h = le16(h, 0)                     // no internal attributes
		if rec.isDir {
			h = le32(h, modeDir)
		} else {
			h = le32(h, modeFile)
		}
		h = le32(h, 0xFFFFFFFF) // the offset is in the extra field
		h = append(h, rec.name...)
		h = le16(h, extraTagZip64)
		h = le16(h, 24)
		h = le64(h, rec.size)
		h = le64(h, rec.size)
		h = le64(h, rec.offset)
		if err := w.write(h); err != nil {
			return err
		}
	}

	cdSize := w.offset - cdStart
	zip64Offset := w.offset

	z := make([]byte, 0, 56)
	z = le32(z, sigZip64EOCD)
	z = le64(z, 44) // the size of this record after this field
	z = le16(z, versionZip64)
	z = le16(z, versionZip64)
	z = le32(z, 0) // this volume
	z = le32(z, 0) // the volume the directory starts on
	z = le64(z, entries)
	z = le64(z, entries)
	z = le64(z, cdSize)
	z = le64(z, cdStart)
	if err := w.write(z); err != nil {
		return err
	}

	loc := make([]byte, 0, 20)
	loc = le32(loc, sigZip64Locator)
	loc = le32(loc, 0)
	loc = le64(loc, zip64Offset)
	loc = le32(loc, 1)
	if err := w.write(loc); err != nil {
		return err
	}

	// The classic trailer, with every field set to the value that means "read
	// the record above instead". A reader that does not know the 64-bit form
	// still finds a trailer where it expects one.
	e := make([]byte, 0, 22)
	e = le32(e, sigEOCD)
	e = le16(e, 0)
	e = le16(e, 0)
	e = le16(e, 0xFFFF)
	e = le16(e, 0xFFFF)
	e = le32(e, 0xFFFFFFFF)
	e = le32(e, 0xFFFFFFFF)
	e = le16(e, 0) // no comment
	return w.write(e)
}

func le16(b []byte, v uint16) []byte { return binary.LittleEndian.AppendUint16(b, v) }
func le32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }
func le64(b []byte, v uint64) []byte { return binary.LittleEndian.AppendUint64(b, v) }

// dosDateTime converts a timestamp into the format's own, which is a packed
// pair with two-second resolution and an epoch of 1980.
//
// A time before that epoch is clamped to it rather than wrapped: the field
// cannot represent it, and a wrapped value is a date an extractor will show.
func dosDateTime(ns int64) (dosTime, dosDate uint16) {
	t := time.Unix(0, ns).UTC()
	if t.Year() < 1980 {
		t = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	dosTime = uint16(t.Hour()<<11 | t.Minute()<<5 | t.Second()/2)      //nolint:gosec // every field is masked by its width above.
	dosDate = uint16((t.Year()-1980)<<9 | int(t.Month())<<5 | t.Day()) //nolint:gosec // the year is clamped above and the rest are calendar fields.
	return dosTime, dosDate
}
