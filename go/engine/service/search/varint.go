package search

import (
	"errors"
	"math"
)

// Unsigned varints in the LEB128 style. Posting lists combine delta and varint
// encoding, and block payloads encode their lengths the same way.

// ErrVarint reports a truncated or overlong encoding.
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

// Varint decodes a varint at pos, returning the value and the following
// position.
//
// Only a value's canonical encoding is accepted. A continuation byte shifting
// past 64 bits is rejected, as is a final byte contributing no bits. Both are
// alternative spellings of a number the encoder never emits, and a decoder
// accepting them could not re-encode what it had just read. Rejecting them
// catches a corrupt segment here instead of converting it into plausible
// hits.
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
		// Only one bit of the tenth byte reaches bit 63. The shift would discard
		// anything above it, so a value setting one indicates a corrupt encoding
		// rather than a large number.
		if shift == 63 && b&0x7f > 1 {
			return 0, 0, ErrVarint
		}
		v |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			// A final byte adding no bits is the overlong spelling of a shorter
			// encoding.
			if shift > 0 && b == 0 {
				return 0, 0, ErrVarint
			}
			return v, pos, nil
		}
		shift += 7
	}
}

// VarintLen gives v's encoded width in bytes, which the size estimator needs
// without performing an encode.
func VarintLen(v uint64) int {
	n := 1
	for v >>= 7; v != 0; v >>= 7 {
		n++
	}
	return n
}

// PutAscending delta-encodes a strictly ascending sequence of block ids.
//
// The first id is stored absolutely and the remainder as gaps, which is what
// reduces a dense posting list to mostly single-byte values.
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

// Ascending reverses PutAscending.
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
