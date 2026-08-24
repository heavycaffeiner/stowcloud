// Package search implements the tiered filename search: the parallel walk, the
// optional trigram index, and the estimator that chooses between them.
//
// The on-disk format is not changing, which is what makes this a translation
// with a golden-file check rather than a reimplementation.
package search

import (
	"errors"
	"math"
)

// LEB128-style unsigned varints. Posting lists are delta plus varint encoded,
// and block payloads use the same encoding for their lengths.

// ErrVarint is a truncated or overlong encoding.
var ErrVarint = errors.New("search: a malformed varint")

// PutVarint appends v to out.
func PutVarint(out []byte, v uint64) []byte {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

// Varint reads a varint at pos, returning the value and the position after it.
//
// Only the canonical encoding of a value is accepted. A continuation byte that
// would shift past 64 bits is refused, and so is a final byte contributing no
// bits: both are second spellings of a number the encoder never writes, and a
// decoder that took them could not re-encode what it just read. Refusing means
// a corrupt segment is detected here rather than turned into plausible hits.
func Varint(buf []byte, pos int) (uint64, int, error) {
	var v uint64
	var shift uint
	for {
		if pos < 0 || pos >= len(buf) {
			return 0, 0, ErrVarint
		}
		b := buf[pos]
		pos++
		if shift >= 64 {
			return 0, 0, ErrVarint
		}
		// The tenth byte can only carry the single bit that reaches bit 63.
		// Anything above it would be dropped by the shift, so a value that set
		// one is a corrupt encoding rather than a large number.
		if shift == 63 && b&0x7f > 1 {
			return 0, 0, ErrVarint
		}
		v |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			// A final byte contributing no bits is the overlong form of a
			// shorter encoding.
			if shift > 0 && b == 0 {
				return 0, 0, ErrVarint
			}
			return v, pos, nil
		}
		shift += 7
	}
}

// VarintLen is the encoded width of v in bytes, which the size estimator needs
// without encoding anything.
func VarintLen(v uint64) int {
	n := 1
	for v >>= 7; v != 0; v >>= 7 {
		n++
	}
	return n
}

// PutAscending delta-encodes a strictly ascending list of block ids.
//
// The first id is absolute and the rest are gaps, which is what makes a dense
// posting list mostly one-byte values.
func PutAscending(out []byte, ids []uint32) []byte {
	var prev uint64
	for i, id := range ids {
		v := uint64(id)
		if i == 0 {
			out = PutVarint(out, v)
		} else {
			out = PutVarint(out, v-prev)
		}
		prev = v
	}
	return out
}

// Ascending is the inverse of PutAscending.
func Ascending(buf []byte) ([]uint32, error) {
	var out []uint32
	var prev uint64
	pos := 0
	for pos < len(buf) {
		d, next, err := Varint(buf, pos)
		if err != nil {
			return nil, err
		}
		pos = next
		v := d
		if len(out) > 0 {
			v = prev + d
		}
		if v > math.MaxUint32 {
			return nil, ErrVarint
		}
		out = append(out, uint32(v))
		prev = v
	}
	return out, nil
}
