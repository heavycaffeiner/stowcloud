package vault

import (
	"crypto/cipher"
	"fmt"
)

// kuznyechikBlockSize is the only block size the standard defines: 128
// bits.
const kuznyechikBlockSize = 16

// kuznyechikKeySize is the only key size the standard defines: 256 bits.
const kuznyechikKeySize = 32

const kuznyechikRounds = 10

// kuznyechikGFPoly is the low byte of the field's reducing polynomial,
// used to fold a carry out of bit 7 during GF(2^8) multiplication.
const kuznyechikGFPoly = 0xC3

// kuznyechikCipher implements cipher.Block. encTable folds the
// substitution into the linear transform, one entry per byte position and
// byte value, so an encryption round costs sixteen table lookups and
// sixteen XORs instead of a substitution pass plus sixteen GF(2^8)
// multiply-accumulate passes. decTable holds the plain linear inverse
// only, because decryption needs it before, not after, the inverse
// substitution.
type kuznyechikCipher struct {
	roundKeys [kuznyechikRounds][kuznyechikBlockSize]byte
	encTable  [kuznyechikBlockSize][256][kuznyechikBlockSize]byte
	decTable  [kuznyechikBlockSize][256][kuznyechikBlockSize]byte
}

// newKuznyechikCipher returns a cipher.Block implementing Kuznyechik (GOST
// R 34.12-2015). The key must be 32 bytes, the only size the standard
// defines.
func newKuznyechikCipher(key []byte) (cipher.Block, error) {
	if len(key) != kuznyechikKeySize {
		return nil, fmt.Errorf("vault: kuznyechik key must be %d bytes, got %d", kuznyechikKeySize, len(key))
	}
	c := &kuznyechikCipher{}
	c.buildTables()
	c.expandKey(key)
	return c, nil
}

func (c *kuznyechikCipher) BlockSize() int { return kuznyechikBlockSize }

// Encrypt applies nine rounds of key-xor, substitution and linear mixing,
// then a final key-xor with no mixing, per the standard's encryption
// transform.
func (c *kuznyechikCipher) Encrypt(dst, src []byte) {
	if len(src) < kuznyechikBlockSize {
		panic("vault: kuznyechik input not full block")
	}
	if len(dst) < kuznyechikBlockSize {
		panic("vault: kuznyechik output not full block")
	}
	var state [kuznyechikBlockSize]byte
	copy(state[:], src[:kuznyechikBlockSize])
	for i := range kuznyechikRounds - 1 {
		state = kuznyechikRoundTransform(&c.encTable, c.roundKeys[i], state)
	}
	for i := range state {
		state[i] ^= c.roundKeys[kuznyechikRounds-1][i]
	}
	copy(dst[:kuznyechikBlockSize], state[:])
}

// Decrypt reverses Encrypt: a key-xor with the last round key, then nine
// rounds of inverse mixing, inverse substitution and key-xor. The standard
// runs the inverse linear step before the inverse substitution in each
// round, the opposite order from Encrypt, so this cannot reuse Encrypt's
// fused table.
func (c *kuznyechikCipher) Decrypt(dst, src []byte) {
	if len(src) < kuznyechikBlockSize {
		panic("vault: kuznyechik input not full block")
	}
	if len(dst) < kuznyechikBlockSize {
		panic("vault: kuznyechik output not full block")
	}
	var state [kuznyechikBlockSize]byte
	copy(state[:], src[:kuznyechikBlockSize])
	for i := kuznyechikRounds - 1; i >= 1; i-- {
		state = kuznyechikInverseRoundTransform(&c.decTable, c.roundKeys[i], state)
	}
	for i := range state {
		state[i] ^= c.roundKeys[0][i]
	}
	copy(dst[:kuznyechikBlockSize], state[:])
}

// buildTables fills encTable and decTable by running the slow, direct
// linear transform (and its inverse) over each of the 16*256 basis vectors
// that have a single nonzero byte at one position. Because the linear
// transform is GF(2)-linear across byte positions, the transform of any
// block is the XOR of these precomputed per-position, per-value
// contributions, which is what the round tables exist to serve up without
// recomputing.
//
// encTable folds the substitution in ahead of the linear step, matching
// Encrypt's substitute-then-mix order. decTable holds the raw linear
// inverse with no substitution folded in, because Decrypt's order is
// mix-then-substitute: the fusion trick only works when the substitution
// comes first.
func (c *kuznyechikCipher) buildTables() {
	for pos := range kuznyechikBlockSize {
		v := byte(0)
		for {
			var fwd [kuznyechikBlockSize]byte
			fwd[pos] = kuznyechikPi[v]
			c.encTable[pos][v] = kuznyechikLTransform(fwd)

			var raw [kuznyechikBlockSize]byte
			raw[pos] = v
			c.decTable[pos][v] = kuznyechikLInverseTransform(raw)

			if v == 255 {
				break
			}
			v++
		}
	}
}

// expandKey runs the Feistel-based key schedule: the 256-bit key seeds the
// first two round keys directly, and each further pair comes from eight
// Feistel steps driven by the next eight round constants.
func (c *kuznyechikCipher) expandKey(key []byte) {
	var k1, k2 [kuznyechikBlockSize]byte
	copy(k1[:], key[:kuznyechikBlockSize])
	copy(k2[:], key[kuznyechikBlockSize:kuznyechikKeySize])
	c.roundKeys[0] = k1
	c.roundKeys[1] = k2

	a1, a0 := k1, k2
	round := byte(1)
	for pair := range 4 {
		for range 8 {
			t := kuznyechikRoundTransform(&c.encTable, kuznyechikRoundConstant(round), a1)
			for i := range t {
				t[i] ^= a0[i]
			}
			a1, a0 = t, a1
			round++
		}
		c.roundKeys[2+2*pair] = a1
		c.roundKeys[2+2*pair+1] = a0
	}
}

// kuznyechikRoundTransform computes key-xor followed by the fused
// substitution and linear step, for the forward direction: table must be
// encTable, which holds each position's linear-transform contribution with
// the substitution already folded in.
func kuznyechikRoundTransform(table *[kuznyechikBlockSize][256][kuznyechikBlockSize]byte, key, a [kuznyechikBlockSize]byte) [kuznyechikBlockSize]byte {
	var t [kuznyechikBlockSize]byte
	for i := range a {
		t[i] = a[i] ^ key[i]
	}
	var out [kuznyechikBlockSize]byte
	for i := range t {
		row := &table[i][t[i]]
		for j := range out {
			out[j] ^= row[j]
		}
	}
	return out
}

// kuznyechikInverseRoundTransform computes key-xor, the linear inverse
// step, and only then the inverse substitution, for the decryption
// direction: table must be decTable, which holds each position's raw
// linear-inverse contribution with no substitution folded in, since the
// substitution here runs after the linear combination rather than before
// it.
func kuznyechikInverseRoundTransform(table *[kuznyechikBlockSize][256][kuznyechikBlockSize]byte, key, a [kuznyechikBlockSize]byte) [kuznyechikBlockSize]byte {
	var t [kuznyechikBlockSize]byte
	for i := range a {
		t[i] = a[i] ^ key[i]
	}
	var mixed [kuznyechikBlockSize]byte
	for i := range t {
		row := &table[i][t[i]]
		for j := range mixed {
			mixed[j] ^= row[j]
		}
	}
	var out [kuznyechikBlockSize]byte
	for i := range mixed {
		out[i] = kuznyechikPiInv[mixed[i]]
	}
	return out
}

// kuznyechikGFMul multiplies two elements of the cipher's GF(2^8), used
// only while building the round tables and round constants, never on the
// per-block hot path.
func kuznyechikGFMul(a, b byte) byte {
	var p byte
	for range 8 {
		if b&1 != 0 {
			p ^= a
		}
		hi := a & 0x80
		a <<= 1
		if hi != 0 {
			a ^= kuznyechikGFPoly
		}
		b >>= 1
	}
	return p
}

// kuznyechikRForward is the standard's R transform: a feedback byte,
// computed from all sixteen input bytes, enters at position zero while
// every other byte shifts up by one and the last byte drops out.
func kuznyechikRForward(a [kuznyechikBlockSize]byte) [kuznyechikBlockSize]byte {
	var feedback byte
	for i := range kuznyechikBlockSize {
		feedback ^= kuznyechikGFMul(kuznyechikLVec[i], a[i])
	}
	var out [kuznyechikBlockSize]byte
	out[0] = feedback
	copy(out[1:], a[:kuznyechikBlockSize-1])
	return out
}

// kuznyechikLTransform is the standard's L transform, sixteen applications
// of R.
func kuznyechikLTransform(a [kuznyechikBlockSize]byte) [kuznyechikBlockSize]byte {
	for range kuznyechikBlockSize {
		a = kuznyechikRForward(a)
	}
	return a
}

// kuznyechikRInverse reverses kuznyechikRForward: the byte that R dropped
// is recovered as the new feedback byte and appended at the far end, while
// every other byte shifts back down by one.
func kuznyechikRInverse(a [kuznyechikBlockSize]byte) [kuznyechikBlockSize]byte {
	var seq [kuznyechikBlockSize]byte
	copy(seq[:kuznyechikBlockSize-1], a[1:])
	seq[kuznyechikBlockSize-1] = a[0]
	var feedback byte
	for i := range kuznyechikBlockSize {
		feedback ^= kuznyechikGFMul(kuznyechikLVec[i], seq[i])
	}
	var out [kuznyechikBlockSize]byte
	copy(out[:kuznyechikBlockSize-1], a[1:])
	out[kuznyechikBlockSize-1] = feedback
	return out
}

// kuznyechikLInverseTransform is the inverse of kuznyechikLTransform,
// sixteen applications of kuznyechikRInverse.
func kuznyechikLInverseTransform(a [kuznyechikBlockSize]byte) [kuznyechikBlockSize]byte {
	for range kuznyechikBlockSize {
		a = kuznyechikRInverse(a)
	}
	return a
}

// kuznyechikRoundConstant computes the i-th key schedule constant: the
// linear transform of a block holding i in its last byte and zero
// elsewhere.
func kuznyechikRoundConstant(i byte) [kuznyechikBlockSize]byte {
	var v [kuznyechikBlockSize]byte
	v[kuznyechikBlockSize-1] = i
	return kuznyechikLTransform(v)
}
