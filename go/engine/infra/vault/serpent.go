package vault

import (
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"math/bits"
)

// serpentBlockSize is Serpent's block size, fixed regardless of key size,
// which is what lets it sit behind XTS the same way AES does.
const serpentBlockSize = 16

// serpentKeySize is the only key length this driver accepts: VeraCrypt
// offers Serpent solely as the 256-bit variant for non-system volumes.
const serpentKeySize = 32

// serpentRounds is fixed by the algorithm, independent of key size.
const serpentRounds = 32

// serpentPhi is the fractional part of the golden ratio scaled to 32 bits.
// The key schedule's affine recurrence XORs it into every prekey word so
// an all-zero or otherwise low-entropy user key still produces a
// well-mixed expanded key.
const serpentPhi = 0x9e3779b9

// serpentCipher is a keyed Serpent-256 instance. subkeys holds the 33
// round keys the key schedule expands the user key into.
//
// This build runs Serpent in its bitslice form: the formal description's
// initial and final permutations are never applied, because they exist
// only to place data into the layout this form already computes in
// directly, so every subkey and every intermediate block state is just
// four plain 32-bit words, X0..X3.
type serpentCipher struct {
	subkeys [serpentRounds + 1][4]uint32
}

// newSerpentCipher builds a cipher.Block for Serpent-256. VeraCrypt never
// uses another key size for this cipher, so any other length is refused
// outright rather than silently padded or truncated.
func newSerpentCipher(key []byte) (cipher.Block, error) {
	if len(key) != serpentKeySize {
		return nil, fmt.Errorf("vault: serpent: key must be %d bytes, got %d", serpentKeySize, len(key))
	}
	c := &serpentCipher{}
	c.expandKey(key)
	return c, nil
}

func (c *serpentCipher) BlockSize() int { return serpentBlockSize }

// expandKey runs Serpent's key schedule. An affine recurrence over the
// user key produces 132 prekey words (w[8+i] holds prekey word i, since Go
// has no negative array indices and the recurrence reaches back to word
// -8, the first word of the key). Each group of four consecutive prekey
// words is then run through the same bitslice S-box the round function
// uses, cycling through all eight in reverse and offset by three, which is
// the algorithm's own indexing, to produce the 33 round keys.
func (c *serpentCipher) expandKey(key []byte) {
	var w [140]uint32
	for i := range 8 {
		w[i] = binary.LittleEndian.Uint32(key[4*i : 4*i+4])
	}
	for i := range uint32(132) {
		v := w[i] ^ w[i+3] ^ w[i+5] ^ w[i+7] ^ serpentPhi ^ i
		w[i+8] = bits.RotateLeft32(v, 11)
	}

	sboxes := serpentSBoxes()
	for i := range serpentRounds + 1 {
		box := (serpentRounds + 3 - i) % serpentRounds
		base := 8 + 4*i
		x0, x1, x2, x3 := serpentApplySBox(&sboxes, box, w[base], w[base+1], w[base+2], w[base+3])
		c.subkeys[i] = [4]uint32{x0, x1, x2, x3}
	}
}

// Encrypt runs the 32 bitslice rounds forward: key mix, S-box, linear
// transform, except the last round replaces the linear transform with a
// second key mix, which is what makes the round function non-invertible
// without the key even though every other round is a clean involution of
// XOR, substitution and a linear map.
func (c *serpentCipher) Encrypt(dst, src []byte) {
	if len(src) < serpentBlockSize {
		panic("vault: serpent: input not full block")
	}
	if len(dst) < serpentBlockSize {
		panic("vault: serpent: output not full block")
	}

	x0 := binary.LittleEndian.Uint32(src[0:4])
	x1 := binary.LittleEndian.Uint32(src[4:8])
	x2 := binary.LittleEndian.Uint32(src[8:12])
	x3 := binary.LittleEndian.Uint32(src[12:16])

	sboxes := serpentSBoxes()
	for i := range serpentRounds {
		k := c.subkeys[i]
		x0, x1, x2, x3 = serpentApplySBox(&sboxes, i%8, x0^k[0], x1^k[1], x2^k[2], x3^k[3])
		if i < serpentRounds-1 {
			x0, x1, x2, x3 = serpentLinear(x0, x1, x2, x3)
		} else {
			last := c.subkeys[serpentRounds]
			x0, x1, x2, x3 = x0^last[0], x1^last[1], x2^last[2], x3^last[3]
		}
	}

	binary.LittleEndian.PutUint32(dst[0:4], x0)
	binary.LittleEndian.PutUint32(dst[4:8], x1)
	binary.LittleEndian.PutUint32(dst[8:12], x2)
	binary.LittleEndian.PutUint32(dst[12:16], x3)
}

// Decrypt runs the same 32 rounds in reverse, undoing the last round's
// second key mix before the loop proper starts undoing the linear
// transform, S-box and key mix of every other round from round 31 down to
// round 0.
func (c *serpentCipher) Decrypt(dst, src []byte) {
	if len(src) < serpentBlockSize {
		panic("vault: serpent: input not full block")
	}
	if len(dst) < serpentBlockSize {
		panic("vault: serpent: output not full block")
	}

	x0 := binary.LittleEndian.Uint32(src[0:4])
	x1 := binary.LittleEndian.Uint32(src[4:8])
	x2 := binary.LittleEndian.Uint32(src[8:12])
	x3 := binary.LittleEndian.Uint32(src[12:16])

	invSBoxes := serpentInverseSBoxes()
	for i := serpentRounds - 1; i >= 0; i-- {
		if i < serpentRounds-1 {
			x0, x1, x2, x3 = serpentLinearInverse(x0, x1, x2, x3)
		} else {
			last := c.subkeys[serpentRounds]
			x0, x1, x2, x3 = x0^last[0], x1^last[1], x2^last[2], x3^last[3]
		}
		x0, x1, x2, x3 = serpentApplySBox(&invSBoxes, i%8, x0, x1, x2, x3)
		k := c.subkeys[i]
		x0, x1, x2, x3 = x0^k[0], x1^k[1], x2^k[2], x3^k[3]
	}

	binary.LittleEndian.PutUint32(dst[0:4], x0)
	binary.LittleEndian.PutUint32(dst[4:8], x1)
	binary.LittleEndian.PutUint32(dst[8:12], x2)
	binary.LittleEndian.PutUint32(dst[12:16], x3)
}

// serpentApplySBox applies S-box number box mod 8 to x0..x3 in Serpent's
// bitslice layout: bit k of the four words, taken together across the four
// registers rather than from within a single one, is one 4-bit S-box
// input, and the 4-bit output scatters back to bit k of the four results.
// This does the round's 32 parallel S-box lookups in one pass over the
// words instead of 32 lookups into one word's nibbles. box is taken mod 8
// here, rather than by every caller, because the key schedule's own box
// index cycles through a range wider than 8 before it repeats.
func serpentApplySBox(sboxes *[8][16]byte, box int, x0, x1, x2, x3 uint32) (uint32, uint32, uint32, uint32) {
	table := sboxes[box%8]
	var y0, y1, y2, y3 uint32
	for k := range uint(32) {
		b0 := (x0 >> k) & 1
		b1 := (x1 >> k) & 1
		b2 := (x2 >> k) & 1
		b3 := (x3 >> k) & 1
		out := uint32(table[b0|b1<<1|b2<<2|b3<<3])
		y0 |= (out & 1) << k
		y1 |= ((out >> 1) & 1) << k
		y2 |= ((out >> 2) & 1) << k
		y3 |= ((out >> 3) & 1) << k
	}
	return y0, y1, y2, y3
}

// serpentLinear is Serpent's linear transformation. It is expressed here
// as a fixed sequence of rotates, shifts and XORs rather than as the
// formal description's per-bit XOR table: the two compute the same
// function once the initial permutation is folded into the four-word
// bitslice layout, which is exactly what lets the round function skip that
// permutation and every lookup it would otherwise cost.
func serpentLinear(x0, x1, x2, x3 uint32) (uint32, uint32, uint32, uint32) {
	x0 = bits.RotateLeft32(x0, 13)
	x2 = bits.RotateLeft32(x2, 3)
	x1 ^= x0 ^ x2
	x3 ^= x2 ^ (x0 << 3)
	x1 = bits.RotateLeft32(x1, 1)
	x3 = bits.RotateLeft32(x3, 7)
	x0 ^= x1 ^ x3
	x2 ^= x3 ^ (x1 << 7)
	x0 = bits.RotateLeft32(x0, 5)
	x2 = bits.RotateLeft32(x2, 22)
	return x0, x1, x2, x3
}

// serpentLinearInverse undoes serpentLinear, each step reversed in the
// opposite order.
func serpentLinearInverse(x0, x1, x2, x3 uint32) (uint32, uint32, uint32, uint32) {
	x2 = bits.RotateLeft32(x2, -22)
	x0 = bits.RotateLeft32(x0, -5)
	x2 ^= x3 ^ (x1 << 7)
	x0 ^= x1 ^ x3
	x3 = bits.RotateLeft32(x3, -7)
	x1 = bits.RotateLeft32(x1, -1)
	x3 ^= x2 ^ (x0 << 3)
	x1 ^= x0 ^ x2
	x2 = bits.RotateLeft32(x2, -3)
	x0 = bits.RotateLeft32(x0, -13)
	return x0, x1, x2, x3
}
