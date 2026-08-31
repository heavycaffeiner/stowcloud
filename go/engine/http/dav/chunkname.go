//go:build linux

// Chunk member names inside an upload collection.
//
// One parser, shared with the compatibility mount, because two parsers
// disagreeing about whether "00001" and "1" are the same member is how a
// client writes a chunk twice and reads one of them back.
package dav

import (
	"errors"
	"strconv"
)

// The refusals a caller distinguishes.
var (
	// ErrChunkNotDecimal reports a name with a non-digit byte.
	ErrChunkNotDecimal = errors.New("a chunk name is decimal digits only")
	// ErrChunkLeadingZero reports a name padded with zeros.
	ErrChunkLeadingZero = errors.New("a chunk name carries a leading zero")
	// ErrChunkRange reports a number outside the collection's range.
	ErrChunkRange = errors.New("a chunk number outside the collection's range")
)

// ChunkRange is the numbers one collection accepts.
type ChunkRange struct {
	// Min and Max are inclusive.
	Min, Max int64
}

// ParseChunkName returns the number a member name denotes.
//
// Canonical decimal: digits only, no leading zero unless the number is exactly
// zero and the range admits it. Every other spelling of a number is refused
// rather than accepted as an alias, so one chunk has one name.
func ParseChunkName(name string, r ChunkRange) (int64, error) {
	if name == "" {
		return 0, ErrChunkNotDecimal
	}
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return 0, ErrChunkNotDecimal
		}
	}
	if name[0] == '0' && name != "0" {
		return 0, ErrChunkLeadingZero
	}

	n, err := strconv.ParseInt(name, 10, 64)
	if err != nil {
		// Only overflow reaches here: every byte is a digit and the string is
		// not empty. An overflowing number is out of every real range.
		return 0, ErrChunkRange
	}
	if n < r.Min || n > r.Max {
		return 0, ErrChunkRange
	}
	return n, nil
}

// ChunkName renders a number in the one spelling the parser accepts.
func ChunkName(n int64) string { return strconv.FormatInt(n, 10) }
