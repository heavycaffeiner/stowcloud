package vault

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
)

// Camellia-256 known-answer vectors. The first is the 256-bit key vector
// from RFC 3713's own appendix; the other two are from the published NESSIE
// test vectors for Camellia, the same ones the Bouncy Castle and OpenSSL
// test suites carry for this cipher.
func TestCamelliaKnownAnswerVectors(t *testing.T) {
	cases := []struct {
		name string
		key  string
		pt   string
		ct   string
	}{
		{
			name: "RFC 3713 appendix A",
			key:  "0123456789abcdeffedcba987654321000112233445566778899aabbccddeeff",
			pt:   "0123456789abcdeffedcba9876543210",
			ct:   "9acc237dff16d76c20ef7c919e3a7509",
		},
		{
			name: "NESSIE vector 7",
			key:  "4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A",
			pt:   "057764FE3A500EDBD988C5C3B56CBA9A",
			ct:   "4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A4A",
		},
		{
			name: "NESSIE vector 8",
			key:  "0303030303030303030303030303030303030303030303030303030303030303",
			pt:   "7968B08ABA92193F2295121EF8D75C8A",
			ct:   "03030303030303030303030303030303",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, err := hex.DecodeString(tc.key)
			if err != nil {
				t.Fatalf("decode key: %v", err)
			}
			pt, err := hex.DecodeString(tc.pt)
			if err != nil {
				t.Fatalf("decode plaintext: %v", err)
			}
			wantCT, err := hex.DecodeString(tc.ct)
			if err != nil {
				t.Fatalf("decode ciphertext: %v", err)
			}

			c, err := newCamelliaCipher(key)
			if err != nil {
				t.Fatalf("newCamelliaCipher: %v", err)
			}
			if c.BlockSize() != 16 {
				t.Fatalf("BlockSize() = %d, want 16", c.BlockSize())
			}

			gotCT := make([]byte, 16)
			c.Encrypt(gotCT, pt)
			if !bytes.Equal(gotCT, wantCT) {
				t.Fatalf("Encrypt(%s) = %x, want %x", tc.pt, gotCT, wantCT)
			}

			gotPT := make([]byte, 16)
			c.Decrypt(gotPT, wantCT)
			if !bytes.Equal(gotPT, pt) {
				t.Fatalf("Decrypt(%s) = %x, want %x", tc.ct, gotPT, pt)
			}
		})
	}
}

func TestCamelliaRejectsWrongKeyLength(t *testing.T) {
	for _, n := range []int{0, 1, 16, 24, 31, 33, 64} {
		_, err := newCamelliaCipher(make([]byte, n))
		if err == nil {
			t.Fatalf("key length %d: got no error, want one naming the length", n)
		}
		wantLen := strconv.Itoa(n)
		if !strings.Contains(err.Error(), wantLen) {
			t.Fatalf("key length %d: error %q does not name the length received", n, err)
		}
	}
}

func TestCamelliaRoundTripRandomBlocks(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand.Read key: %v", err)
	}
	c, err := newCamelliaCipher(key)
	if err != nil {
		t.Fatalf("newCamelliaCipher: %v", err)
	}

	for range 500 {
		pt := make([]byte, 16)
		if _, err := rand.Read(pt); err != nil {
			t.Fatalf("rand.Read plaintext: %v", err)
		}
		ct := make([]byte, 16)
		c.Encrypt(ct, pt)
		if bytes.Equal(ct, pt) {
			t.Fatalf("ciphertext equals plaintext for input %x", pt)
		}
		got := make([]byte, 16)
		c.Decrypt(got, ct)
		if !bytes.Equal(got, pt) {
			t.Fatalf("round trip mismatch: got %x, want %x", got, pt)
		}
	}
}
