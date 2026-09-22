package vault

import (
	"crypto/cipher"
	"encoding/binary"
	"fmt"
)

const (
	camelliaBlockSize = 16
	camelliaKeySize   = 32
)

// The six Sigma constants seed the small Feistel network the key schedule
// runs to expand KL and KR into KA and KB. They are fixed values Camellia's
// key schedule requires, not something this driver chooses.
const (
	camelliaSigma1 = 0xA09E667F3BCC908B
	camelliaSigma2 = 0xB67AE8584CAA73B2
	camelliaSigma3 = 0xC6EF372FE94F82BE
	camelliaSigma4 = 0x54FF53A5F1D36F1C
	camelliaSigma5 = 0x10E527FADE682D1D
	camelliaSigma6 = 0xB05688C2B3E6C1FD
)

// camelliaSBoxes holds the four 256-entry substitution tables the F-function
// looks up. Building all four once per cipher, rather than deriving SBOX2..4
// from SBOX1 on every F call, keeps the per-block cost to the XOR and lookup
// chain alone.
type camelliaSBoxes struct {
	s1, s2, s3, s4 [256]byte
}

func newCamelliaSBoxes() camelliaSBoxes {
	sb := camelliaSBoxes{s1: camelliaSBox1()}
	var i byte
	for {
		sb.s2[i] = camelliaRotl8(sb.s1[i], 1)
		sb.s3[i] = camelliaRotl8(sb.s1[i], 7)
		sb.s4[i] = sb.s1[camelliaRotl8(i, 1)]
		if i == 255 {
			break
		}
		i++
	}
	return sb
}

func camelliaRotl8(x byte, n uint) byte {
	return x<<n | x>>(8-n)
}

// f is Camellia's round function: it XORs the 64-bit input with the round
// key, runs each of the eight resulting bytes through one of the four
// S-boxes, and combines the eight outputs through a fixed linear layer.
func (sb *camelliaSBoxes) f(fin, ke uint64) uint64 {
	x := fin ^ ke
	t0 := sb.s1[x>>56&0xff]
	t1 := sb.s2[x>>48&0xff]
	t2 := sb.s3[x>>40&0xff]
	t3 := sb.s4[x>>32&0xff]
	t4 := sb.s2[x>>24&0xff]
	t5 := sb.s3[x>>16&0xff]
	t6 := sb.s4[x>>8&0xff]
	t7 := sb.s1[x&0xff]
	y1 := t0 ^ t2 ^ t3 ^ t5 ^ t6 ^ t7
	y2 := t0 ^ t1 ^ t3 ^ t4 ^ t6 ^ t7
	y3 := t0 ^ t1 ^ t2 ^ t4 ^ t5 ^ t7
	y4 := t1 ^ t2 ^ t3 ^ t4 ^ t5 ^ t6
	y5 := t0 ^ t1 ^ t5 ^ t6 ^ t7
	y6 := t1 ^ t2 ^ t4 ^ t6 ^ t7
	y7 := t2 ^ t3 ^ t4 ^ t5 ^ t7
	y8 := t0 ^ t3 ^ t4 ^ t5 ^ t6
	return uint64(y1)<<56 | uint64(y2)<<48 | uint64(y3)<<40 | uint64(y4)<<32 |
		uint64(y5)<<24 | uint64(y6)<<16 | uint64(y7)<<8 | uint64(y8)
}

// camelliaFL and camelliaFLInv are Camellia's key-dependent linear layer and
// its inverse, each working on one 64-bit half split into two 32-bit words.
func camelliaFL(flIn, ke uint64) uint64 {
	x1 := uint32(flIn >> 32 & 0xffffffff)
	x2 := uint32(flIn & 0xffffffff)
	k1 := uint32(ke >> 32 & 0xffffffff)
	k2 := uint32(ke & 0xffffffff)
	v := x1 & k1
	x2 ^= (v << 1) | (v >> 31)
	x1 ^= x2 | k2
	return uint64(x1)<<32 | uint64(x2)
}

func camelliaFLInv(flInvIn, ke uint64) uint64 {
	y1 := uint32(flInvIn >> 32 & 0xffffffff)
	y2 := uint32(flInvIn & 0xffffffff)
	k1 := uint32(ke >> 32 & 0xffffffff)
	k2 := uint32(ke & 0xffffffff)
	y1 ^= y2 | k2
	v := y1 & k1
	y2 ^= (v << 1) | (v >> 31)
	return uint64(y1)<<32 | uint64(y2)
}

// camelliaU128 is a 128-bit value split into its upper and lower 64 bits,
// standing in for the 128-bit integer Go has no native type for. The key
// schedule only ever needs to XOR two of these or rotate one left.
type camelliaU128 struct {
	hi, lo uint64
}

func (v camelliaU128) xor(o camelliaU128) camelliaU128 {
	return camelliaU128{hi: v.hi ^ o.hi, lo: v.lo ^ o.lo}
}

// rotl rotates v left by n bits, treating (hi, lo) as one 128-bit word with
// hi first.
func (v camelliaU128) rotl(n uint) camelliaU128 {
	n %= 128
	if n == 0 {
		return v
	}
	if n < 64 {
		return camelliaU128{
			hi: v.hi<<n | v.lo>>(64-n),
			lo: v.lo<<n | v.hi>>(64-n),
		}
	}
	m := n - 64
	return camelliaU128{
		hi: v.lo<<m | v.hi>>(64-m),
		lo: v.hi<<m | v.lo>>(64-m),
	}
}

// camelliaSubkeys is one full set of round keys: two 64-bit whitening pairs,
// twenty-four round keys and the six keys the three FL/FLINV layers use.
type camelliaSubkeys struct {
	kw [4]uint64
	k  [24]uint64
	ke [6]uint64
}

// camelliaSchedule256 expands a 256-bit key into KL, KR, KA and KB and then
// into every subkey the 24-round data randomizing part needs, following
// Camellia's key schedule for 192- and 256-bit keys.
func camelliaSchedule256(key []byte, sb *camelliaSBoxes) camelliaSubkeys {
	kl := camelliaU128{hi: binary.BigEndian.Uint64(key[0:8]), lo: binary.BigEndian.Uint64(key[8:16])}
	kr := camelliaU128{hi: binary.BigEndian.Uint64(key[16:24]), lo: binary.BigEndian.Uint64(key[24:32])}

	klXorKr := kl.xor(kr)
	d1 := klXorKr.hi
	d2 := klXorKr.lo
	d2 ^= sb.f(d1, camelliaSigma1)
	d1 ^= sb.f(d2, camelliaSigma2)
	d1 ^= kl.hi
	d2 ^= kl.lo
	d2 ^= sb.f(d1, camelliaSigma3)
	d1 ^= sb.f(d2, camelliaSigma4)
	ka := camelliaU128{hi: d1, lo: d2}

	kaXorKr := ka.xor(kr)
	d1 = kaXorKr.hi
	d2 = kaXorKr.lo
	d2 ^= sb.f(d1, camelliaSigma5)
	d1 ^= sb.f(d2, camelliaSigma6)
	kb := camelliaU128{hi: d1, lo: d2}

	var sk camelliaSubkeys
	r := kl.rotl(0)
	sk.kw[0], sk.kw[1] = r.hi, r.lo
	r = kb.rotl(0)
	sk.k[0], sk.k[1] = r.hi, r.lo
	r = kr.rotl(15)
	sk.k[2], sk.k[3] = r.hi, r.lo
	r = ka.rotl(15)
	sk.k[4], sk.k[5] = r.hi, r.lo
	r = kr.rotl(30)
	sk.ke[0], sk.ke[1] = r.hi, r.lo
	r = kb.rotl(30)
	sk.k[6], sk.k[7] = r.hi, r.lo
	r = kl.rotl(45)
	sk.k[8], sk.k[9] = r.hi, r.lo
	r = ka.rotl(45)
	sk.k[10], sk.k[11] = r.hi, r.lo
	r = kl.rotl(60)
	sk.ke[2], sk.ke[3] = r.hi, r.lo
	r = kr.rotl(60)
	sk.k[12], sk.k[13] = r.hi, r.lo
	r = kb.rotl(60)
	sk.k[14], sk.k[15] = r.hi, r.lo
	r = kl.rotl(77)
	sk.k[16], sk.k[17] = r.hi, r.lo
	r = ka.rotl(77)
	sk.ke[4], sk.ke[5] = r.hi, r.lo
	r = kr.rotl(94)
	sk.k[18], sk.k[19] = r.hi, r.lo
	r = ka.rotl(94)
	sk.k[20], sk.k[21] = r.hi, r.lo
	r = kl.rotl(111)
	sk.k[22], sk.k[23] = r.hi, r.lo
	r = kb.rotl(111)
	sk.kw[2], sk.kw[3] = r.hi, r.lo

	return sk
}

// camelliaReverseSubkeys builds the subkey order decryption uses: the two
// whitening pairs and the six FL/FLINV keys swap end for end, and the
// twenty-four round keys run back to front.
func camelliaReverseSubkeys(sk camelliaSubkeys) camelliaSubkeys {
	var r camelliaSubkeys
	r.kw = [4]uint64{sk.kw[2], sk.kw[3], sk.kw[0], sk.kw[1]}
	for i := range 24 {
		r.k[i] = sk.k[23-i]
	}
	r.ke = [6]uint64{sk.ke[5], sk.ke[4], sk.ke[3], sk.ke[2], sk.ke[1], sk.ke[0]}
	return r
}

// camelliaCrypt runs the round network Camellia defines for 192- and
// 256-bit keys: twenty-four Feistel rounds with three FL/FLINV layers
// spaced through them. Encrypt and Decrypt share this and differ only in
// which subkey order they pass in.
func camelliaCrypt(sb *camelliaSBoxes, sk *camelliaSubkeys, dst, src []byte) {
	d1 := binary.BigEndian.Uint64(src[0:8])
	d2 := binary.BigEndian.Uint64(src[8:16])

	d1 ^= sk.kw[0]
	d2 ^= sk.kw[1]

	d2 ^= sb.f(d1, sk.k[0])
	d1 ^= sb.f(d2, sk.k[1])
	d2 ^= sb.f(d1, sk.k[2])
	d1 ^= sb.f(d2, sk.k[3])
	d2 ^= sb.f(d1, sk.k[4])
	d1 ^= sb.f(d2, sk.k[5])
	d1 = camelliaFL(d1, sk.ke[0])
	d2 = camelliaFLInv(d2, sk.ke[1])
	d2 ^= sb.f(d1, sk.k[6])
	d1 ^= sb.f(d2, sk.k[7])
	d2 ^= sb.f(d1, sk.k[8])
	d1 ^= sb.f(d2, sk.k[9])
	d2 ^= sb.f(d1, sk.k[10])
	d1 ^= sb.f(d2, sk.k[11])
	d1 = camelliaFL(d1, sk.ke[2])
	d2 = camelliaFLInv(d2, sk.ke[3])
	d2 ^= sb.f(d1, sk.k[12])
	d1 ^= sb.f(d2, sk.k[13])
	d2 ^= sb.f(d1, sk.k[14])
	d1 ^= sb.f(d2, sk.k[15])
	d2 ^= sb.f(d1, sk.k[16])
	d1 ^= sb.f(d2, sk.k[17])
	d1 = camelliaFL(d1, sk.ke[4])
	d2 = camelliaFLInv(d2, sk.ke[5])
	d2 ^= sb.f(d1, sk.k[18])
	d1 ^= sb.f(d2, sk.k[19])
	d2 ^= sb.f(d1, sk.k[20])
	d1 ^= sb.f(d2, sk.k[21])
	d2 ^= sb.f(d1, sk.k[22])
	d1 ^= sb.f(d2, sk.k[23])

	d2 ^= sk.kw[2]
	d1 ^= sk.kw[3]

	binary.BigEndian.PutUint64(dst[0:8], d2)
	binary.BigEndian.PutUint64(dst[8:16], d1)
}

// camelliaCipher is a cipher.Block over the fixed subkeys and S-boxes one
// 256-bit key produces; xts.NewCipher calls newCamelliaCipher once per
// volume and reuses the result for every block after that.
type camelliaCipher struct {
	sb  camelliaSBoxes
	enc camelliaSubkeys
	dec camelliaSubkeys
}

// newCamelliaCipher returns a cipher.Block implementing Camellia with a
// 256-bit key, the only size VeraCrypt offers for this cipher.
func newCamelliaCipher(key []byte) (cipher.Block, error) {
	if len(key) != camelliaKeySize {
		return nil, fmt.Errorf("vault: camellia: key must be %d bytes, got %d", camelliaKeySize, len(key))
	}
	sb := newCamelliaSBoxes()
	enc := camelliaSchedule256(key, &sb)
	return &camelliaCipher{sb: sb, enc: enc, dec: camelliaReverseSubkeys(enc)}, nil
}

func (c *camelliaCipher) BlockSize() int { return camelliaBlockSize }

func (c *camelliaCipher) Encrypt(dst, src []byte) {
	if len(src) < camelliaBlockSize {
		panic("vault: camellia: input not full block")
	}
	if len(dst) < camelliaBlockSize {
		panic("vault: camellia: output not full block")
	}
	camelliaCrypt(&c.sb, &c.enc, dst, src)
}

func (c *camelliaCipher) Decrypt(dst, src []byte) {
	if len(src) < camelliaBlockSize {
		panic("vault: camellia: input not full block")
	}
	if len(dst) < camelliaBlockSize {
		panic("vault: camellia: output not full block")
	}
	camelliaCrypt(&c.sb, &c.dec, dst, src)
}
