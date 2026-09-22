package vault

import (
	"encoding/binary"
	"hash"
)

const (
	whirlpoolSize      = 64 // digest size in bytes: 512 bits.
	whirlpoolBlockSize = 64 // block size in bytes: 512 bits.
	whirlpoolRounds    = 10
)

// whirlpoolDigest is the running state of one Whirlpool computation. length
// is the full 256-bit message bit count the specification defines, held as
// four big-endian words rather than truncated to 64 bits: HMAC drives Sum
// and Reset repeatedly on the same instance, so the padding path has to
// reproduce the exact length field a verifier expects even though every
// message this driver ever hashes is tiny.
type whirlpoolDigest struct {
	tables *whirlpoolTables
	hash   [8]uint64
	buf    [whirlpoolBlockSize]byte
	nx     int
	length [4]uint64
}

var _ hash.Hash = (*whirlpoolDigest)(nil)

// newWhirlpool returns a hash.Hash computing the final, corrected-S-box
// revision of Whirlpool (ISO/IEC 10118-3), the one VeraCrypt offers as a
// header and volume PRF.
func newWhirlpool() hash.Hash {
	t := newWhirlpoolTables()
	return &whirlpoolDigest{tables: &t}
}

func (d *whirlpoolDigest) Size() int      { return whirlpoolSize }
func (d *whirlpoolDigest) BlockSize() int { return whirlpoolBlockSize }

// Reset returns the digest to its just-constructed state. tables is left
// untouched: it is immutable after newWhirlpoolTables built it, and
// rebuilding it here would run the 2048-entry literal once per HMAC
// iteration instead of once per hash.Hash instance.
func (d *whirlpoolDigest) Reset() {
	d.hash = [8]uint64{}
	d.buf = [whirlpoolBlockSize]byte{}
	d.nx = 0
	d.length = [4]uint64{}
}

func (d *whirlpoolDigest) Write(p []byte) (int, error) {
	n := len(p)
	d.addLen(uint64(n))

	if d.nx > 0 {
		copied := copy(d.buf[d.nx:], p)
		d.nx += copied
		p = p[copied:]
		if d.nx == whirlpoolBlockSize {
			d.transform(d.buf[:])
			d.nx = 0
		}
	}
	for len(p) >= whirlpoolBlockSize {
		d.transform(p[:whirlpoolBlockSize])
		p = p[whirlpoolBlockSize:]
	}
	if len(p) > 0 {
		d.nx = copy(d.buf[:], p)
	}
	return n, nil
}

// Sum runs the padding and final block on a copy of the digest, so d itself
// is left exactly as Write last saw it. HMAC relies on that: it calls Sum
// once per iteration on an inner hash it goes on writing to afterward.
func (d *whirlpoolDigest) Sum(in []byte) []byte {
	tmp := *d
	digest := tmp.checkSum()
	return append(in, digest[:]...)
}

// checkSum pads the buffered tail and runs the last one or two blocks. The
// specification reserves the last 32 bytes of the padded message for a
// 256-bit bit count, twice SHA-512's reserved width, so a buffered tail
// past 32 bytes needs an extra all-but-length block before the one that
// carries it.
func (d *whirlpoolDigest) checkSum() [whirlpoolSize]byte {
	index := d.nx
	d.buf[index] = 0x80
	index++

	if index > 32 {
		for i := index; i < whirlpoolBlockSize; i++ {
			d.buf[i] = 0
		}
		d.transform(d.buf[:])
		index = 0
	}
	for i := index; i < 32; i++ {
		d.buf[i] = 0
	}

	bits := whirlpoolBitLength(d.length)
	for i, w := range bits {
		binary.BigEndian.PutUint64(d.buf[32+i*8:], w)
	}
	d.transform(d.buf[:])

	var out [whirlpoolSize]byte
	for i, w := range d.hash {
		binary.BigEndian.PutUint64(out[i*8:], w)
	}
	return out
}

// addLen adds n, a byte count from one Write call, to the 256-bit total
// byte counter, length[0] holding the most significant word. Every carry
// past the least significant word is exactly one bit, since only that word
// ever receives a full uint64 addition.
func (d *whirlpoolDigest) addLen(n uint64) {
	old := d.length[3]
	d.length[3] += n
	carry := uint64(0)
	if d.length[3] < old {
		carry = 1
	}
	for i := 2; i >= 0 && carry != 0; i-- {
		old = d.length[i]
		d.length[i] += carry
		if d.length[i] < old {
			carry = 1
		} else {
			carry = 0
		}
	}
}

// whirlpoolBitLength converts a 256-bit byte count into the 256-bit bit
// count the padding's length field carries, shifting the whole 256-bit
// value left by three bits rather than multiplying just the low word.
func whirlpoolBitLength(byteLen [4]uint64) [4]uint64 {
	return [4]uint64{
		byteLen[0]<<3 | byteLen[1]>>61,
		byteLen[1]<<3 | byteLen[2]>>61,
		byteLen[2]<<3 | byteLen[3]>>61,
		byteLen[3] << 3,
	}
}

// transform runs the compression function over one 64-byte big-endian
// block, which may be d's own buffer or a slice straight out of Write's
// argument: it only ever reads the first 64 bytes.
//
// The block cipher's round key schedule reuses the same round function as
// the data path, differing only in where the round constant is added, so
// state and the key both advance through whirlpoolRho every round. The
// Miyaguchi-Preneel feedforward folds the plaintext block and the prior
// hash value back in at the end, which is what makes this a hash rather
// than a keyed cipher.
func (d *whirlpoolDigest) transform(block []byte) {
	var x [8]uint64
	for i := range x {
		x[i] = binary.BigEndian.Uint64(block[i*8:])
	}

	k := d.hash
	var state [8]uint64
	for i := range state {
		state[i] = x[i] ^ k[i]
	}
	feedforward := state

	for round := range whirlpoolRounds {
		k = whirlpoolRho(k, d.tables)
		k[0] ^= d.tables.rc[round]
		state = whirlpoolRho(state, d.tables)
		for i := range state {
			state[i] ^= k[i]
		}
	}

	for i := range d.hash {
		d.hash[i] = feedforward[i] ^ state[i]
	}
}

// whirlpoolRho is the round function shared by the state path and the key
// schedule: SubBytes, ShiftColumns and MixRows collapse into eight
// precomputed table lookups per output word, one per input byte position,
// combined with XOR.
func whirlpoolRho(src [8]uint64, t *whirlpoolTables) [8]uint64 {
	var dst [8]uint64
	for shift := range 8 {
		dst[shift] = t.sub[0][byte((src[shift&7]>>56)&0xff)] ^
			t.sub[1][byte((src[(shift+7)&7]>>48)&0xff)] ^
			t.sub[2][byte((src[(shift+6)&7]>>40)&0xff)] ^
			t.sub[3][byte((src[(shift+5)&7]>>32)&0xff)] ^
			t.sub[4][byte((src[(shift+4)&7]>>24)&0xff)] ^
			t.sub[5][byte((src[(shift+3)&7]>>16)&0xff)] ^
			t.sub[6][byte((src[(shift+2)&7]>>8)&0xff)] ^
			t.sub[7][byte(src[(shift+1)&7]&0xff)]
	}
	return dst
}
