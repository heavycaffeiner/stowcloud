package vault

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// serpentDecodeHex is a package-local helper: kuznyechik_test.go already
// defines one with this shape, but this file stays self-contained rather
// than depend on a sibling test file under separate ownership.
func serpentDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

// The vectors below come from the Serpent AES submission's own known
// answer tests (floppy 4 of the submission package: ecb_vk.txt for
// variable-key, ecb_vt.txt for variable-text, both KEYSIZE=256), the same
// vectors NESSIE and every other Serpent implementation check against.
//
// Every KEY, PT and CT in those files is printed as one big-endian
// multi-word integer (most significant word first, most significant byte
// of each word first), which is the opposite of the little-endian byte
// order newSerpentCipher takes. The hex below is each published string
// with its byte order reversed, so it is what a caller actually passes to
// Encrypt or gets back from it. That reversal, and a from-specification
// reference implementation's agreement with every vector in both files
// (not just the ones reproduced here: all 256 variable-key and all 128
// variable-text vectors at the 256-bit key size), were checked
// independently before this file was written.

// TestSerpentZeroKeyZeroPlaintext checks the all-zero key against the
// all-zero plaintext, the one degenerate case the submission's own
// known-answer files never print directly: ecb_vk.txt varies one key bit
// at a time against PT=0 but never uses a fully zero key.
func TestSerpentZeroKeyZeroPlaintext(t *testing.T) {
	key := make([]byte, serpentKeySize)
	pt := make([]byte, serpentBlockSize)
	wantCT := serpentDecodeHex(t, "49672ba898d98df95019180445491089")

	blk, err := newSerpentCipher(key)
	if err != nil {
		t.Fatalf("newSerpentCipher: %v", err)
	}

	gotCT := make([]byte, serpentBlockSize)
	blk.Encrypt(gotCT, pt)
	if !bytes.Equal(gotCT, wantCT) {
		t.Fatalf("Encrypt(zero) = %x, want %x", gotCT, wantCT)
	}

	gotPT := make([]byte, serpentBlockSize)
	blk.Decrypt(gotPT, wantCT)
	if !bytes.Equal(gotPT, pt) {
		t.Fatalf("Decrypt(%x) = %x, want zero", wantCT, gotPT)
	}
}

// TestSerpentVariableKey checks ten of ecb_vk.txt's 256 variable-key
// vectors, spread across the file (I=1 through I=256): each fixes the
// plaintext at zero and sets a single key bit. Encrypt and Decrypt are
// both checked, so a key schedule bug that only shows up in one direction
// cannot hide.
func TestSerpentVariableKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		ct   string
	}{
		{name: "vk-I1", key: "0000000000000000000000000000000000000000000000000000000000000080", ct: "1908ef821ad2ebc0cb28bf66e796edab"},
		{name: "vk-I8", key: "0000000000000000000000000000000000000000000000000000000000000001", ct: "9858fd31c9c6b54ac0c99cc52324ed34"},
		{name: "vk-I16", key: "0000000000000000000000000000000000000000000000000000000000000100", ct: "4d715d9421fcb51c7b4c94def2b5c210"},
		{name: "vk-I32", key: "0000000000000000000000000000000000000000000000000000000001000000", ct: "6fc6c5718fd0b81194a198f873ede7ea"},
		{name: "vk-I64", key: "0000000000000000000000000000000000000000000000000100000000000000", ct: "a583ef976a292b406bbd5dc8256b0442"},
		{name: "vk-I100", key: "0000000000000000000000000000000000000010000000000000000000000000", ct: "14b1df955b3a20eadff35c3869b0b624"},
		{name: "vk-I150", key: "0000000000000000000000000004000000000000000000000000000000000000", ct: "49a3f4514e983616f55580ea4ea12dbf"},
		{name: "vk-I200", key: "0000000000000001000000000000000000000000000000000000000000000000", ct: "d48e45670fd978fa4db161c0e5d59fc0"},
		{name: "vk-I224", key: "0000000001000000000000000000000000000000000000000000000000000000", ct: "986a05e8447024c8468a1ebf7743f689"},
		{name: "vk-I256", key: "0100000000000000000000000000000000000000000000000000000000000000", ct: "e0885d4460373469d1fa6c36a6e1c52f"},
	}

	pt := make([]byte, serpentBlockSize)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key := serpentDecodeHex(t, tc.key)
			wantCT := serpentDecodeHex(t, tc.ct)

			blk, err := newSerpentCipher(key)
			if err != nil {
				t.Fatalf("newSerpentCipher: %v", err)
			}

			gotCT := make([]byte, serpentBlockSize)
			blk.Encrypt(gotCT, pt)
			if !bytes.Equal(gotCT, wantCT) {
				t.Fatalf("Encrypt(zero pt) = %x, want %x", gotCT, wantCT)
			}

			gotPT := make([]byte, serpentBlockSize)
			blk.Decrypt(gotPT, wantCT)
			if !bytes.Equal(gotPT, pt) {
				t.Fatalf("Decrypt(%x) = %x, want zero", wantCT, gotPT)
			}
		})
	}
}

// TestSerpentVariableText checks nine of ecb_vt.txt's 128 variable-text
// vectors, spread across the file (I=1 through I=128): each fixes the key
// at zero and sets a single plaintext bit.
func TestSerpentVariableText(t *testing.T) {
	const keyHex = "0000000000000000000000000000000000000000000000000000000000000000"
	tests := []struct {
		name string
		pt   string
		ct   string
	}{
		{name: "vt-I1", pt: "00000000000000000000000000000080", ct: "2055dea7c84b008c6faeb4b192795ada"},
		{name: "vt-I16", pt: "00000000000000000000000000000100", ct: "d2fe26fc85aa40c3c6827b0dff96ab0c"},
		{name: "vt-I32", pt: "00000000000000000000000001000000", ct: "6655b0542be057664de9b2733ca0e555"},
		{name: "vt-I48", pt: "00000000000000000000010000000000", ct: "3cf54d30e493cdd7439e1f34fbb098f3"},
		{name: "vt-I64", pt: "00000000000000000100000000000000", ct: "645f0f938a2898a3869190a1d99a3078"},
		{name: "vt-I80", pt: "00000000000001000000000000000000", ct: "b84e741ac92a42f37a77f05d6f464e10"},
		{name: "vt-I96", pt: "00000000010000000000000000000000", ct: "58d7e0d60e315eeba97f0dfa2d7307b0"},
		{name: "vt-I112", pt: "00000100000000000000000000000000", ct: "e47a19e8579807b5c44ac62619372673"},
		{name: "vt-I128", pt: "01000000000000000000000000000000", ct: "07e5e5ad7097b849badc2d5d803b7f6a"},
	}

	key := serpentDecodeHex(t, keyHex)
	blk, err := newSerpentCipher(key)
	if err != nil {
		t.Fatalf("newSerpentCipher: %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pt := serpentDecodeHex(t, tc.pt)
			wantCT := serpentDecodeHex(t, tc.ct)

			gotCT := make([]byte, serpentBlockSize)
			blk.Encrypt(gotCT, pt)
			if !bytes.Equal(gotCT, wantCT) {
				t.Fatalf("Encrypt(%x) = %x, want %x", pt, gotCT, wantCT)
			}

			gotPT := make([]byte, serpentBlockSize)
			blk.Decrypt(gotPT, wantCT)
			if !bytes.Equal(gotPT, pt) {
				t.Fatalf("Decrypt(%x) = %x, want %x", wantCT, gotPT, pt)
			}
		})
	}
}

func TestSerpentBlockSize(t *testing.T) {
	blk, err := newSerpentCipher(make([]byte, serpentKeySize))
	if err != nil {
		t.Fatalf("newSerpentCipher: %v", err)
	}
	if got := blk.BlockSize(); got != 16 {
		t.Fatalf("BlockSize() = %d, want 16", got)
	}
}

// TestSerpentRejectsBadKeySize checks that every key length other than 32
// is refused, and that the error names the length actually received:
// VeraCrypt offers Serpent only as the 256-bit variant, so any other
// length is a caller mistake worth naming precisely rather than a variant
// this driver silently tries to support.
func TestSerpentRejectsBadKeySize(t *testing.T) {
	for _, n := range []int{0, 1, 16, 24, 31, 33, 64} {
		_, err := newSerpentCipher(make([]byte, n))
		if err == nil {
			t.Fatalf("key length %d: got no error, want a rejection", n)
		}
		want := "32 bytes"
		if !bytes.Contains([]byte(err.Error()), []byte(want)) {
			t.Fatalf("key length %d: error %q does not mention %q", n, err.Error(), want)
		}
	}
}

// TestSerpentRoundTripRandom is a property test: for several random keys
// and many random blocks per key, decrypting an encrypted block must
// reproduce it exactly. The fixed vectors above prove this build matches
// the published algorithm; this proves Decrypt is genuinely the inverse of
// Encrypt across the input space those fixed vectors do not cover.
func TestSerpentRoundTripRandom(t *testing.T) {
	const keyTrials = 5
	const blocksPerKey = 200
	for keyTrial := range keyTrials {
		key := make([]byte, serpentKeySize)
		if _, err := rand.Read(key); err != nil {
			t.Fatalf("key trial %d: rand key: %v", keyTrial, err)
		}
		blk, err := newSerpentCipher(key)
		if err != nil {
			t.Fatalf("key trial %d: newSerpentCipher: %v", keyTrial, err)
		}

		for blockTrial := range blocksPerKey {
			pt := make([]byte, serpentBlockSize)
			if _, err := rand.Read(pt); err != nil {
				t.Fatalf("key trial %d block %d: rand block: %v", keyTrial, blockTrial, err)
			}

			ct := make([]byte, serpentBlockSize)
			blk.Encrypt(ct, pt)
			if bytes.Equal(ct, pt) {
				t.Fatalf("key trial %d block %d: ciphertext equals plaintext for key %x", keyTrial, blockTrial, key)
			}

			got := make([]byte, serpentBlockSize)
			blk.Decrypt(got, ct)
			if !bytes.Equal(got, pt) {
				t.Fatalf("key trial %d block %d: round trip failed: pt=%x ct=%x got=%x key=%x", keyTrial, blockTrial, pt, ct, got, key)
			}
		}
	}
}
