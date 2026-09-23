//go:build linux && compat_nc

package nc

import (
	"strconv"
	"strings"
)

// parseRange reads a Range header for a GET of size bytes.
//
// A single range only. No client here ever sends a Range with more than one
// spec; a multipart/byteranges body is a format this package does not write,
// so a multi-range request is answered as the whole file rather than
// synthesizing a response shape nothing exercises. ok is false only for a
// range that parses and names nothing inside the file, which the caller
// answers 416 for; an absent header or one this function does not recognise
// answers as no range at all, which is the same as ok true with a nil pair.
func parseRange(header string, size uint64) (rng *[2]uint64, ok bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, true
	}
	spec, hasPrefix := strings.CutPrefix(header, "bytes=")
	if !hasPrefix {
		return nil, true
	}
	if strings.Contains(spec, ",") {
		// More than one range. Answered as the whole file: see the doc
		// comment above.
		return nil, true
	}

	first, last, cut := strings.Cut(spec, "-")
	if !cut {
		return nil, true
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)

	switch {
	case first == "" && last == "":
		return nil, true

	case first == "":
		// A suffix range: the last n bytes.
		n, err := strconv.ParseUint(last, 10, 64)
		if err != nil || n == 0 {
			return nil, true
		}
		if size == 0 {
			return nil, false
		}
		if n > size {
			n = size
		}
		return &[2]uint64{size - n, size - 1}, true

	case last == "":
		start, err := strconv.ParseUint(first, 10, 64)
		if err != nil {
			return nil, true
		}
		if start >= size {
			return nil, false
		}
		return &[2]uint64{start, size - 1}, true

	default:
		start, serr := strconv.ParseUint(first, 10, 64)
		end, eerr := strconv.ParseUint(last, 10, 64)
		if serr != nil || eerr != nil || start > end {
			return nil, true
		}
		if start >= size {
			return nil, false
		}
		if end >= size {
			end = size - 1
		}
		return &[2]uint64{start, end}, true
	}
}
