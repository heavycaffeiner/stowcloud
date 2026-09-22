package vault

import (
	"encoding/binary"
	"errors"
	"hash"
)

// GOST R 34.11-2012 (Streebog) defines its transformations over a vector
// whose own worked examples write the most significant byte first (its
// hex dumps list the leftmost byte as index 63, counting down to the
// right). Real messages, HMAC keys, and every interoperable
// implementation (OpenSSL's GOST engine, pygost, and the reference C code
// VeraCrypt itself carries) instead read and write Streebog in ordinary
// stream order, the reverse of that: the first byte written is fed first
// and the digest's first byte is its most significant one in the
// conventional sense. This file keeps the standard's own vector
// convention for every internal computation, exactly as its worked
// examples present it, and reverses only at the Write and Sum boundary,
// so a message given in normal byte order produces the same digest
// OpenSSL, pygost, and VeraCrypt would compute for it.

const (
	streebogSize      = 64 // digest size in bytes: 512 bits.
	streebogBlockSize = 64 // block size in bytes: 512 bits.
	streebogRounds    = 12
)

// streebogLTable fuses substitution, byte permutation and the linear
// transform into one table indexed by input byte position: entry
// fused[i][v] is the 64-bit contribution byte v at input position i makes
// to whichever output word wordOf[i] names, after that byte has gone
// through streebogPi and landed at streebogTau[i]. LPS then costs one pass
// over the 64 input bytes, XORing each byte's contribution into its word,
// rather than a substitution pass followed by a separate matrix pass.
// The round constants and the block-length vector ride along on the same
// struct rather than sitting at package scope: both are consumed once per
// 64-byte block, and PBKDF2 drives that half a million times, so they have
// to be built once per hash and not once per block.
type streebogLTable struct {
	fused     [64][256]uint64
	wordOf    [64]byte
	c         [12][64]byte
	blockBits [64]byte
}

// newStreebogLTable builds the fused table from the substitution, the byte
// permutation and the linear transform. col[j][s] (s already substituted)
// is the plain linear transform's contribution for a byte at word-position
// j, exactly what evaluating the matrix product bit by bit for that byte
// would produce; fused[i][v] folds the substitution of v and the placement
// of position i into that same lookup.
func newStreebogLTable() *streebogLTable {
	a := streebogLinear()
	var col [8][256]uint64
	for j := range 8 {
		for v := range 256 {
			var word uint64
			for b := range 8 {
				if v&(1<<b) != 0 {
					word ^= a[7+8*j-b]
				}
			}
			col[j][v] = word
		}
	}

	var t streebogLTable
	tau := streebogPermutation()
	for i := range 64 {
		pos := tau[i]
		t.wordOf[i] = pos / 8
		j := pos % 8
		for v := range 256 {
			t.fused[i][v] = col[j][streebogPi[v]]
		}
	}
	t.c = streebogRoundConstants()
	t.blockBits = streebogUint64Vector(streebogBlockBits)
	return &t
}

// streebog512Digest is the running state of one Streebog-512 computation,
// held throughout in the standard's own big-endian vector convention: h,
// n and sigma are 512-bit values with index 0 the most significant byte.
// buf holds a message tail Write has not yet folded in, kept in ordinary
// (not reversed) byte order until a full or final block is ready.
type streebog512Digest struct {
	tbl   *streebogLTable
	h     [64]byte
	n     [64]byte
	sigma [64]byte
	buf   [64]byte
	nx    int
}

var _ hash.Hash = (*streebog512Digest)(nil)

// newStreebog512 returns a hash.Hash computing the 512-bit output variant
// of GOST R 34.11-2012 (Streebog), one of the PRFs VeraCrypt offers for
// its header and volume key derivation.
func newStreebog512() hash.Hash {
	return &streebog512Digest{tbl: newStreebogLTable()}
}

func (d *streebog512Digest) Size() int      { return streebogSize }
func (d *streebog512Digest) BlockSize() int { return streebogBlockSize }

// streebogMarshaledLen is h, n, sigma and buf at 64 bytes each, plus one
// byte for nx.
const streebogMarshaledLen = 4*64 + 1

// MarshalBinary and UnmarshalBinary let crypto/hmac cache this digest's
// state once ipad or opad has been written, instead of rewriting that
// 64-byte block through g_N on every PBKDF2 iteration: HMAC's own Reset
// checks for this pair before falling back to replaying ipad and opad
// through Write each time.
func (d *streebog512Digest) MarshalBinary() ([]byte, error) {
	if d.nx < 0 || d.nx >= streebogBlockSize {
		return nil, errors.New("vault: streebog-512 digest has an invalid buffered length")
	}
	out := make([]byte, 0, streebogMarshaledLen)
	out = append(out, d.h[:]...)
	out = append(out, d.n[:]...)
	out = append(out, d.sigma[:]...)
	out = append(out, d.buf[:]...)
	out = append(out, byte(d.nx))
	return out, nil
}

func (d *streebog512Digest) UnmarshalBinary(data []byte) error {
	if len(data) != streebogMarshaledLen {
		return errors.New("vault: invalid streebog-512 marshaled state length")
	}
	copy(d.h[:], data[0:64])
	copy(d.n[:], data[64:128])
	copy(d.sigma[:], data[128:192])
	copy(d.buf[:], data[192:256])
	d.nx = int(data[256])
	return nil
}

// Reset returns the digest to its just-constructed state. tbl is left
// alone: it is immutable once newStreebogLTable built it, and rebuilding
// it here would run the 2048-entry construction once per HMAC iteration
// instead of once per hash.Hash instance.
func (d *streebog512Digest) Reset() {
	d.h = [64]byte{}
	d.n = [64]byte{}
	d.sigma = [64]byte{}
	d.buf = [64]byte{}
	d.nx = 0
}

func (d *streebog512Digest) Write(p []byte) (int, error) {
	written := len(p)
	if d.nx > 0 {
		copied := copy(d.buf[d.nx:], p)
		d.nx += copied
		p = p[copied:]
		if d.nx == streebogBlockSize {
			d.processBlock(d.buf[:])
			d.nx = 0
		}
	}
	for len(p) >= streebogBlockSize {
		d.processBlock(p[:streebogBlockSize])
		p = p[streebogBlockSize:]
	}
	if len(p) > 0 {
		d.nx = copy(d.buf[:], p)
	}
	return written, nil
}

// processBlock folds one full 64-byte block, taken straight from the
// caller's normal byte order, into h, n and sigma. block is reversed into
// the standard's own vector convention before it touches g, streebogAdd512
// or anything else in that convention.
func (d *streebog512Digest) processBlock(block []byte) {
	var m [64]byte
	for i := range 64 {
		m[i] = block[63-i]
	}
	d.h = streebogG(d.h, d.n, m, d.tbl)
	d.n = streebogAdd512(d.n, d.tbl.blockBits)
	d.sigma = streebogAdd512(d.sigma, m)
}

// One block's length, in the two units the compression function needs it:
// bytes for the buffer, bits for the running length counter.
const (
	streebogBlockBytes = 64
	streebogBlockBits  = streebogBlockBytes * 8
)

// streebogUint64Vector places v as a big-endian integer in the low 8 bytes
// of a 512-bit vector, the rest zero: every bit count and byte count this
// driver ever hashes fits in 64 bits many times over.
func streebogUint64Vector(v uint64) [64]byte {
	var out [64]byte
	binary.BigEndian.PutUint64(out[56:], v)
	return out
}

// Sum runs the padding and final block on a copy of the digest, so d
// itself is left exactly as Write last saw it. HMAC relies on that: it
// calls Sum once per iteration on an inner hash it goes on writing to
// afterward.
func (d *streebog512Digest) Sum(in []byte) []byte {
	tmp := *d
	digest := tmp.checkSum()
	return append(in, digest[:]...)
}

// checkSum pads the buffered tail, runs the padded block and the two
// finalizing calls the standard defines over the length and checksum
// accumulators, and reverses the result into the conventional byte order
// Sum reports. d.buf holds at most 63 bytes here: Write always drains a
// full block the moment one completes.
func (d *streebog512Digest) checkSum() [64]byte {
	tail := d.nx
	var m [64]byte
	m[64-tail-1] = 0x01
	for i := range tail {
		m[64-tail+i] = d.buf[tail-1-i]
	}
	d.h = streebogG(d.h, d.n, m, d.tbl)
	// tail is a partial block, so 0..63, and its bit count cannot overflow.
	d.n = streebogAdd512(d.n, streebogUint64Vector(uint64(tail&(streebogBlockBytes-1))*8))
	d.sigma = streebogAdd512(d.sigma, m)
	d.h = streebogG(d.h, [64]byte{}, d.n, d.tbl)
	d.h = streebogG(d.h, [64]byte{}, d.sigma, d.tbl)

	var out [64]byte
	for i := range 64 {
		out[i] = d.h[63-i]
	}
	return out
}

// streebogG is the compression function g_N: it mixes the running hash h
// with n (the running bit count, doubling as the round-key derivation
// salt) before running the block cipher E over m and feeding h and m back
// in, a Miyaguchi-Preneel-style construction that makes E impossible to
// invert into a usable collision without knowing h.
func streebogG(h, n, m [64]byte, tbl *streebogLTable) [64]byte {
	key := streebogLPS(streebogXor(h, n), tbl)
	e := streebogE(key, m, tbl)
	return streebogXor(streebogXor(e, h), m)
}

// streebogE is the keyed block cipher the compression function drives:
// twelve rounds of key-xor, substitute, permute and mix, with the round
// key itself advanced by the same LPS transform against a fixed round
// constant, and a final key-xor with no further mixing.
func streebogE(key, m [64]byte, tbl *streebogLTable) [64]byte {
	state := m
	for i := range streebogRounds {
		state = streebogLPS(streebogXor(key, state), tbl)
		key = streebogLPS(streebogXor(key, tbl.c[i]), tbl)
	}
	return streebogXor(key, state)
}

// streebogLPS runs substitution, byte permutation and the linear
// transform in one pass: each input byte's precomputed contribution (see
// streebogLTable) is XORed into the output word it lands in, rather than
// substituting the whole vector, permuting it, and then walking the
// linear transform as three separate passes.
func streebogLPS(a [64]byte, tbl *streebogLTable) [64]byte {
	var words [8]uint64
	for i := range 64 {
		words[tbl.wordOf[i]] ^= tbl.fused[i][a[i]]
	}
	var out [64]byte
	for w := range 8 {
		binary.BigEndian.PutUint64(out[w*8:w*8+8], words[w])
	}
	return out
}

func streebogXor(a, b [64]byte) [64]byte {
	var out [64]byte
	for i := range out {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// streebogAdd512 adds two 512-bit big-endian vectors modulo 2^512, the
// ring the standard defines n and sigma over. It works one byte at a
// time with the same overflow-by-comparison technique
// whirlpoolDigest.addLen uses, rather than widening to a larger type and
// narrowing back down, so the carry out of the top byte is discarded
// rather than reported, exactly as modular addition requires.
func streebogAdd512(a, b [64]byte) [64]byte {
	var out [64]byte
	var carry byte
	for i := 63; i >= 0; i-- {
		sum := a[i] + b[i]
		overflowAB := byte(0)
		if sum < a[i] {
			overflowAB = 1
		}
		sum += carry
		overflowCarry := byte(0)
		if sum < carry {
			overflowCarry = 1
		}
		out[i] = sum
		carry = overflowAB | overflowCarry
	}
	return out
}
