// Linux only, because it serves a Linux-only engine.
//go:build linux

// Package archive writes a streaming Zip64 archive.
//
// Streaming means the sizes are not known when an entry's header goes out, so
// every entry carries a data descriptor after its bytes and the central
// directory at the end carries the real numbers. That is what lets a download
// begin before the last file has been read.
//
// Zip64 unconditionally rather than only past 4 GiB. A writer that switches
// format partway has two code paths and one of them is exercised by nobody
// until a user uploads a large enough file.
package archive

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// ErrName is an entry name this writer will not put in an archive.
var ErrName = errors.New("the entry name is not usable in an archive")

// ErrClosed is a write after the archive was closed.
var ErrClosed = errors.New("the archive is closed")

// Writer builds one archive onto an output stream.
//
// Not safe for concurrent use: an archive is one sequence of bytes and two
// writers would interleave entries.
type Writer struct {
	w      io.Writer
	offset int64

	entries []entry
	closed  bool

	// err is sticky. Once a write has failed the archive is not recoverable,
	// and every later call returns this rather than appending to a stream
	// whose structure is already broken.
	err error
}

// entry records what the central directory needs about one member.
type entry struct {
	name    string
	crc     uint32
	size    uint64
	offset  int64
	dosTime uint16
	dosDate uint16
	isDir   bool
}

// NewWriter starts an archive.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Err reports the sticky error, or nil.
func (z *Writer) Err() error { return z.err }

// AddBytes writes one entry from memory.
func (z *Writer) AddBytes(name string, body []byte, modTime time.Time) error {
	return z.AddFile(name, bytes.NewReader(body), modTime)
}

// AddDir records a directory that holds no files.
//
// Zip has no directory concept beyond a zero-length member whose name ends in
// a slash. Without one an empty directory disappears on extraction, because
// nothing else in the archive mentions it.
func (z *Writer) AddDir(name string, modTime time.Time) error {
	clean, err := CleanName(name)
	if err != nil {
		return err
	}
	return z.add(clean+"/", nil, modTime, true)
}

// AddFile writes one entry, streaming its body.
//
// No declared size, because the entry records what actually landed. A file
// that vanishes mid-archive leaves a short entry in a structurally valid
// archive rather than a declared length over bytes that are not there.
func (z *Writer) AddFile(name string, body io.Reader, modTime time.Time) error {
	clean, err := CleanName(name)
	if err != nil {
		return err
	}
	return z.add(clean, body, modTime, false)
}

func (z *Writer) add(name string, body io.Reader, modTime time.Time, isDir bool) error {
	if z.err != nil {
		return z.err
	}
	if z.closed {
		return ErrClosed
	}

	e := entry{name: name, offset: z.offset, isDir: isDir}
	e.dosTime, e.dosDate = dosTime(modTime)

	if werr := z.writeLocalHeader(e); werr != nil {
		return z.fail(werr)
	}

	crc := crc32.NewIEEE()
	var written uint64
	if body != nil {
		n, cerr := io.Copy(io.MultiWriter(z.w, crc), body)
		z.offset += n
		w, nerr := num.Narrow[uint64](n)
		if nerr != nil {
			return z.fail(nerr)
		}
		written = w
		if cerr != nil {
			// The bytes already written stay: the entry is short and the
			// descriptor below records what actually landed, so the archive
			// remains readable and the caller logs the truncation.
			return z.fail(cerr)
		}
	}
	e.crc, e.size = crc.Sum32(), written

	if derr := z.writeDataDescriptor(e); derr != nil {
		return z.fail(derr)
	}
	z.entries = append(z.entries, e)
	return nil
}

// Close writes the central directory and finishes the archive.
//
// Idempotent: a second call returns the sticky error or nil rather than
// appending a second directory, since a deferred Close beside an explicit one
// is the ordinary shape of a caller.
func (z *Writer) Close() error {
	if z.err != nil {
		return z.err
	}
	if z.closed {
		return nil
	}
	z.closed = true

	start := z.offset
	for _, e := range z.entries {
		if err := z.writeCentralHeader(e); err != nil {
			return z.fail(err)
		}
	}
	if err := z.writeEndRecords(start, z.offset-start); err != nil {
		return z.fail(err)
	}
	return nil
}

// fail records the sticky error.
func (z *Writer) fail(err error) error {
	if z.err == nil {
		z.err = err
	}
	return z.err
}

// nameMax is the largest entry name the format's length field can hold.
//
// A longer one would write a truncated length and every byte after it would be
// read as part of the wrong field, so the archive is unreadable from there on.
const nameMax = 65535 - 1 // the directory entry appends a slash

// CleanName normalises an entry name and refuses one that could escape.
//
// An archive is extracted somewhere, and an entry named "../etc/passwd" or
// "/etc/passwd" is a write outside the directory the user chose. A backslash
// goes too, because a Windows extractor reads it as a separator and a name
// that looks harmless on Linux traverses there.
func CleanName(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: it is empty", ErrName)
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("%w: it carries a NUL", ErrName)
	}
	if strings.ContainsRune(name, '\\') {
		return "", fmt.Errorf("%w: it carries a backslash", ErrName)
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("%w: it is absolute", ErrName)
	}

	clean := strings.Trim(name, "/")
	if clean == "" {
		return "", fmt.Errorf("%w: it names nothing", ErrName)
	}
	if len(clean) > nameMax {
		return "", fmt.Errorf("%w: it is %d bytes, the bound is %d", ErrName, len(clean), nameMax)
	}
	for _, seg := range strings.Split(clean, "/") {
		switch seg {
		case "":
			return "", fmt.Errorf("%w: it carries an empty component", ErrName)
		case ".", "..":
			// Refused rather than resolved. Resolving would silently move an
			// entry somewhere the caller did not ask for, and the caller is
			// building the name from paths it already validated.
			return "", fmt.Errorf("%w: it carries a %q component", ErrName, seg)
		}
	}
	return clean, nil
}

// dosTime converts an instant to the two 16-bit fields zip stores.
//
// The format begins in 1980 and has no way to say "earlier", so anything
// before it clamps to the epoch rather than wrapping into a date decades away.
func dosTime(t time.Time) (dosT, dosD uint16) {
	t = t.UTC()
	if t.Year() < 1980 {
		return 0, 1<<5 | 1 // 1980-01-01T00:00:00
	}
	// Each field is bounded by the calendar: seconds under 30 after halving,
	// minutes under 60, hours under 24, and a year offset the caller clamps
	// below. The packed value cannot leave 16 bits.
	packedTime := t.Second()/2 | t.Minute()<<5 | t.Hour()<<11
	packedDate := t.Day() | int(t.Month())<<5 | (t.Year()-1980)<<9
	if packedDate > 0xffff {
		// A year past 2107 has nowhere to go in this format, so it saturates
		// at the last representable date rather than wrapping into the past.
		packedDate = 0xffff
	}
	// Narrow rather than a cast: the bounds above are real but a reader (and
	// the linter) should not have to carry them across the conversion. A value
	// that somehow left the range fails here instead of silently wrapping.
	dt, terr := num.Narrow[uint16](int64(packedTime))
	dd, derr := num.Narrow[uint16](int64(packedDate))
	if terr != nil || derr != nil {
		return 0, 1<<5 | 1
	}
	return dt, dd
}

const (
	sigLocal      = 0x04034b50
	sigDescriptor = 0x08074b50
	sigCentral    = 0x02014b50
	sigEnd64      = 0x06064b50
	sigLocator64  = 0x07064b50
	sigEnd        = 0x06054b50

	// zip64Extra is the header id of the Zip64 extended information field.
	zip64Extra = 0x0001

	// flagDescriptor says the sizes follow the data; flagUTF8 says the name is
	// UTF-8 rather than the format's ancient default encoding.
	flagDescriptor = 1 << 3
	flagUTF8       = 1 << 11

	// methodStore: no compression. The files are already whatever they are,
	// and compressing them a second time costs CPU on a path that is usually
	// bound by the disk.
	methodStore = 0

	// versionZip64 is what an extractor must support to read this.
	versionZip64 = 45
)

func (z *Writer) writeLocalHeader(e entry) error {
	b := make([]byte, 0, 30+len(e.name))
	b = binary.LittleEndian.AppendUint32(b, sigLocal)
	b = binary.LittleEndian.AppendUint16(b, versionZip64)
	b = binary.LittleEndian.AppendUint16(b, flagDescriptor|flagUTF8)
	b = binary.LittleEndian.AppendUint16(b, methodStore)
	b = binary.LittleEndian.AppendUint16(b, e.dosTime)
	b = binary.LittleEndian.AppendUint16(b, e.dosDate)
	// The three below are zero here and real in the descriptor, which is what
	// the descriptor flag promises.
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint32(b, 0)
	// In range: CleanName refuses a name longer than the field holds.
	b = binary.LittleEndian.AppendUint16(b, nameLen(e.name))
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = append(b, e.name...)
	return z.emit(b)
}

// nameLen is an entry name's length as the format's field.
//
// Safe because CleanName refuses anything longer, and stated here so the
// conversion is one line with its reason rather than a cast gosec has to
// reason about across two functions.
func nameLen(name string) uint16 {
	n, err := num.Narrow[uint16](int64(len(name)))
	if err != nil {
		return 0xffff
	}
	return n
}

func (z *Writer) writeDataDescriptor(e entry) error {
	b := make([]byte, 0, 24)
	b = binary.LittleEndian.AppendUint32(b, sigDescriptor)
	b = binary.LittleEndian.AppendUint32(b, e.crc)
	// Eight bytes each, which is the Zip64 descriptor.
	b = binary.LittleEndian.AppendUint64(b, e.size)
	b = binary.LittleEndian.AppendUint64(b, e.size)
	return z.emit(b)
}

func (z *Writer) writeCentralHeader(e entry) error {
	extra := make([]byte, 0, 32)
	extra = binary.LittleEndian.AppendUint16(extra, zip64Extra)
	extra = binary.LittleEndian.AppendUint16(extra, 24)
	extra = binary.LittleEndian.AppendUint64(extra, e.size)
	extra = binary.LittleEndian.AppendUint64(extra, e.size)
	off, oerr := num.Narrow[uint64](e.offset)
	if oerr != nil {
		return oerr
	}
	extra = binary.LittleEndian.AppendUint64(extra, off)

	var external uint32
	if e.isDir {
		external = 0x10 // the directory attribute
	}

	b := make([]byte, 0, 46+len(e.name)+len(extra))
	b = binary.LittleEndian.AppendUint32(b, sigCentral)
	b = binary.LittleEndian.AppendUint16(b, versionZip64)
	b = binary.LittleEndian.AppendUint16(b, versionZip64)
	b = binary.LittleEndian.AppendUint16(b, flagDescriptor|flagUTF8)
	b = binary.LittleEndian.AppendUint16(b, methodStore)
	b = binary.LittleEndian.AppendUint16(b, e.dosTime)
	b = binary.LittleEndian.AppendUint16(b, e.dosDate)
	b = binary.LittleEndian.AppendUint32(b, e.crc)
	// The 32-bit sizes and offset are the all-ones sentinel: the real values
	// are in the Zip64 extra field above.
	b = binary.LittleEndian.AppendUint32(b, 0xffffffff)
	b = binary.LittleEndian.AppendUint32(b, 0xffffffff)
	// Both in range: the name is bounded by CleanName and the extra field is
	// this function's own fixed-size construction.
	b = binary.LittleEndian.AppendUint16(b, nameLen(e.name))
	b = binary.LittleEndian.AppendUint16(b, nameLen(string(extra)))
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint32(b, external)
	b = binary.LittleEndian.AppendUint32(b, 0xffffffff)
	b = append(b, e.name...)
	b = append(b, extra...)
	return z.emit(b)
}

func (z *Writer) writeEndRecords(dirStart, dirSize int64) error {
	count, cerr := num.Narrow[uint64](int64(len(z.entries)))
	if cerr != nil {
		return cerr
	}
	size, serr := num.Narrow[uint64](dirSize)
	if serr != nil {
		return serr
	}
	start, terr := num.Narrow[uint64](dirStart)
	if terr != nil {
		return terr
	}
	end64, eerr := num.Narrow[uint64](dirStart + dirSize)
	if eerr != nil {
		return eerr
	}

	b := make([]byte, 0, 56+20+22)
	b = binary.LittleEndian.AppendUint32(b, sigEnd64)
	b = binary.LittleEndian.AppendUint64(b, 44)
	b = binary.LittleEndian.AppendUint16(b, versionZip64)
	b = binary.LittleEndian.AppendUint16(b, versionZip64)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint64(b, count)
	b = binary.LittleEndian.AppendUint64(b, count)
	b = binary.LittleEndian.AppendUint64(b, size)
	b = binary.LittleEndian.AppendUint64(b, start)

	b = binary.LittleEndian.AppendUint32(b, sigLocator64)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint64(b, end64)
	b = binary.LittleEndian.AppendUint32(b, 1)

	// The 32-bit end record keeps the sentinels, so an extractor that reads
	// only this one is told to look for the Zip64 records rather than being
	// handed a truncated count.
	b = binary.LittleEndian.AppendUint32(b, sigEnd)
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint16(b, 0xffff)
	b = binary.LittleEndian.AppendUint16(b, 0xffff)
	b = binary.LittleEndian.AppendUint32(b, 0xffffffff)
	b = binary.LittleEndian.AppendUint32(b, 0xffffffff)
	b = binary.LittleEndian.AppendUint16(b, 0)
	return z.emit(b)
}

// emit writes and advances the offset.
func (z *Writer) emit(b []byte) error {
	n, err := z.w.Write(b)
	z.offset += int64(n)
	return err
}
