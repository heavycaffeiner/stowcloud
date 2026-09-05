package vault

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// The key, round keys, plaintext and ciphertext below are the worked
// example from RFC 7801, GOST R 34.12-2015's own specification of
// Kuznyechik, reproduced here as the acceptance vector.
const (
	kuznyechikTestKeyHex = "8899aabbccddeeff0011223344556677fedcba98765432100123456789abcdef"
	kuznyechikTestPtHex  = "1122334455667700ffeeddccbbaa9988"
	kuznyechikTestCtHex  = "7f679d90bebc24305a468d42b9d4edcd"
)

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

func mustDecodeBlock(t *testing.T, s string) [kuznyechikBlockSize]byte {
	t.Helper()
	var out [kuznyechikBlockSize]byte
	copy(out[:], mustDecodeHex(t, s))
	return out
}

// TestKuznyechikRoundKeys checks every derived round key against RFC 7801's
// worked example, not just the encryption result: a key schedule bug that
// happens to still round-trip a block would pass a ciphertext-only check.
func TestKuznyechikRoundKeys(t *testing.T) {
	roundKeysHex := [kuznyechikRounds]string{
		"8899aabbccddeeff0011223344556677",
		"fedcba98765432100123456789abcdef",
		"db31485315694343228d6aef8cc78c44",
		"3d4553d8e9cfec6815ebadc40a9ffd04",
		"57646468c44a5e28d3e59246f429f1ac",
		"bd079435165c6432b532e82834da581b",
		"51e640757e8745de705727265a0098b1",
		"5a7925017b9fdd3ed72a91a22286f984",
		"bb44e25378c73123a5f32f73cdb6e517",
		"72e9dd7416bcf45b755dbaa88e4a4043",
	}

	key := mustDecodeHex(t, kuznyechikTestKeyHex)
	blk, err := newKuznyechikCipher(key)
	if err != nil {
		t.Fatalf("newKuznyechikCipher: %v", err)
	}
	c, ok := blk.(*kuznyechikCipher)
	if !ok {
		t.Fatalf("newKuznyechikCipher returned %T, want *kuznyechikCipher", blk)
	}

	for i, wantHex := range roundKeysHex {
		want := mustDecodeBlock(t, wantHex)
		if c.roundKeys[i] != want {
			t.Fatalf("round key %d = %x, want %x", i+1, c.roundKeys[i], want)
		}
	}
}

// TestKuznyechikEncryptDecryptVector checks the encryption and decryption
// results from RFC 7801's own worked example.
func TestKuznyechikEncryptDecryptVector(t *testing.T) {
	key := mustDecodeHex(t, kuznyechikTestKeyHex)
	blk, err := newKuznyechikCipher(key)
	if err != nil {
		t.Fatalf("newKuznyechikCipher: %v", err)
	}

	pt := mustDecodeHex(t, kuznyechikTestPtHex)
	wantCt := mustDecodeHex(t, kuznyechikTestCtHex)

	gotCt := make([]byte, kuznyechikBlockSize)
	blk.Encrypt(gotCt, pt)
	if !bytes.Equal(gotCt, wantCt) {
		t.Fatalf("Encrypt(%x) = %x, want %x", pt, gotCt, wantCt)
	}

	gotPt := make([]byte, kuznyechikBlockSize)
	blk.Decrypt(gotPt, wantCt)
	if !bytes.Equal(gotPt, pt) {
		t.Fatalf("Decrypt(%x) = %x, want %x", wantCt, gotPt, pt)
	}
}

func TestKuznyechikBlockSize(t *testing.T) {
	key := mustDecodeHex(t, kuznyechikTestKeyHex)
	blk, err := newKuznyechikCipher(key)
	if err != nil {
		t.Fatalf("newKuznyechikCipher: %v", err)
	}
	if got := blk.BlockSize(); got != 16 {
		t.Fatalf("BlockSize() = %d, want 16", got)
	}
}

// TestKuznyechikRejectsBadKeySize checks that every key length other than
// 32 is refused, and that the error names the length actually received.
func TestKuznyechikRejectsBadKeySize(t *testing.T) {
	for _, n := range []int{0, 1, 16, 24, 31, 33, 64} {
		_, err := newKuznyechikCipher(make([]byte, n))
		if err == nil {
			t.Fatalf("key length %d: got no error, want a rejection", n)
		}
		wantRequired := "32 bytes"
		wantReceived := fmt.Sprintf("got %d", n)
		if !strings.Contains(err.Error(), wantRequired) || !strings.Contains(err.Error(), wantReceived) {
			t.Fatalf("key length %d: error %q does not name both the required size (%q) and the length received (%q)", n, err.Error(), wantRequired, wantReceived)
		}
	}
}

// TestKuznyechikRoundTripRandom is a property test: for several random
// keys and many random blocks per key, decrypting an encrypted block must
// reproduce it exactly. This catches structural bugs (a broken inverse
// table, a swapped round order) that a single fixed vector might not
// exercise. Blocks vary far more than keys because building a cipher is
// the expensive part (it fills two 64 KiB tables) while encrypting a block
// is not.
func TestKuznyechikRoundTripRandom(t *testing.T) {
	const keyTrials = 5
	const blocksPerKey = 100
	for keyTrial := range keyTrials {
		key := make([]byte, kuznyechikKeySize)
		if _, err := rand.Read(key); err != nil {
			t.Fatalf("key trial %d: rand key: %v", keyTrial, err)
		}
		blk, err := newKuznyechikCipher(key)
		if err != nil {
			t.Fatalf("key trial %d: newKuznyechikCipher: %v", keyTrial, err)
		}

		for blockTrial := range blocksPerKey {
			pt := make([]byte, kuznyechikBlockSize)
			if _, err := rand.Read(pt); err != nil {
				t.Fatalf("key trial %d block %d: rand block: %v", keyTrial, blockTrial, err)
			}

			ct := make([]byte, kuznyechikBlockSize)
			blk.Encrypt(ct, pt)
			if bytes.Equal(ct, pt) {
				t.Fatalf("key trial %d block %d: ciphertext equals plaintext for key %x", keyTrial, blockTrial, key)
			}

			got := make([]byte, kuznyechikBlockSize)
			blk.Decrypt(got, ct)
			if !bytes.Equal(got, pt) {
				t.Fatalf("key trial %d block %d: round trip failed: pt=%x ct=%x got=%x key=%x", keyTrial, blockTrial, pt, ct, got, key)
			}
		}
	}
}
