// Linux only, for the same reason as the rest of this package.
//go:build linux

// Byte ranges, for the surfaces that stream a file.
//
// One range or none. A multi-range request is refused rather than quietly
// served as its first range: a client that asked for three pieces and received
// one, with a 206 saying nothing went wrong, assembles a file out of what it
// got and finds the damage later.
package handler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrRangeUnsatisfiable is a syntactically valid range that names nothing
// inside the file. The caller answers 416 and reports the real size.
var ErrRangeUnsatisfiable = errors.New("the requested range is not satisfiable")

// ErrRangeUnsupported is a range this server will not serve, including a
// multi-range request and any unit other than bytes.
var ErrRangeUnsupported = errors.New("the requested range is not supported")

// ByteRange is a resolved half-open interval: Start is inclusive, End is
// exclusive, both already checked against the file's size.
type ByteRange struct {
	Start int64
	End   int64
}

// Length is how many bytes the range covers.
func (r ByteRange) Length() int64 { return r.End - r.Start }

// ContentRange is the response header for this range of a file of size total.
func (r ByteRange) ContentRange(total int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", r.Start, r.End-1, total)
}

// ParseRange reads a Range header against a known file size.
//
// Returns ok false with a nil error when the header is absent, which is the
// ordinary whole-file request rather than a problem.
func ParseRange(header string, size int64) (rng ByteRange, ok bool, err error) {
	h := strings.TrimSpace(header)
	if h == "" {
		return ByteRange{}, false, nil
	}

	if size <= 0 {
		return ByteRange{}, false, fmt.Errorf("%w: the file is empty", ErrRangeUnsatisfiable)
	}

	spec, found := strings.CutPrefix(h, "bytes=")
	if !found {
		// Another unit, which this server does not serve. Refusing beats
		// ignoring the header and sending the whole file with a 200, because
		// the client asked for a part and would treat the whole as that part.
		return ByteRange{}, false, fmt.Errorf("%w: only byte ranges", ErrRangeUnsupported)
	}
	if strings.Contains(spec, ",") {
		return ByteRange{}, false, fmt.Errorf("%w: one range at a time", ErrRangeUnsupported)
	}

	first, last, hasDash := strings.Cut(spec, "-")
	if !hasDash {
		return ByteRange{}, false, fmt.Errorf("%w: %q is not a range", ErrRangeUnsupported, spec)
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)

	switch {
	case first == "" && last == "":
		return ByteRange{}, false, fmt.Errorf("%w: the range names no bytes", ErrRangeUnsupported)

	case first == "":
		// A suffix range: the last N bytes. N larger than the file is the
		// whole file, which is what the specification asks for and is not an
		// error: the client asked for at most that many from the end.
		n, perr := strconv.ParseInt(last, 10, 64)
		if perr != nil || n < 0 {
			return ByteRange{}, false, fmt.Errorf("%w: %q is not a length", ErrRangeUnsupported, last)
		}
		if n == 0 {
			return ByteRange{}, false, fmt.Errorf("%w: a zero-length suffix", ErrRangeUnsatisfiable)
		}
		if n > size {
			n = size
		}
		return ByteRange{Start: size - n, End: size}, true, nil

	default:
		start, perr := strconv.ParseInt(first, 10, 64)
		if perr != nil || start < 0 {
			return ByteRange{}, false, fmt.Errorf("%w: %q is not an offset", ErrRangeUnsupported, first)
		}
		if start >= size {
			// Past the end. Distinct from a malformed header: the syntax was
			// fine and the file is simply shorter, which is what 416 with the
			// real size tells the client.
			return ByteRange{}, false, fmt.Errorf("%w: the file holds %d bytes", ErrRangeUnsatisfiable, size)
		}

		end := size
		if last != "" {
			// Inclusive on the wire, exclusive here. The conversion happens
			// once, at the boundary, so nothing downstream has to remember
			// which convention it is holding.
			lastByte, lerr := strconv.ParseInt(last, 10, 64)
			if lerr != nil || lastByte < 0 {
				return ByteRange{}, false, fmt.Errorf("%w: %q is not an offset", ErrRangeUnsupported, last)
			}
			if lastByte < start {
				return ByteRange{}, false, fmt.Errorf("%w: the range ends before it starts", ErrRangeUnsupported)
			}
			end = lastByte + 1
			if end > size {
				// A client asking past the end of a file it has a stale size
				// for gets what exists rather than a refusal.
				end = size
			}
		}
		return ByteRange{Start: start, End: end}, true, nil
	}
}

// UnsatisfiedRange is the Content-Range header for a 416.
//
// It reports the real size, which is what lets a client that guessed wrong
// correct itself rather than retrying the same bad range.
func UnsatisfiedRange(size int64) string {
	return "bytes */" + strconv.FormatInt(size, 10)
}
