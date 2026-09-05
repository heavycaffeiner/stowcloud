package vault

import (
	"bytes"
	"crypto/hmac"
	"encoding/hex"
	"testing"
)

// streebogReverse returns a new slice holding b's bytes in the opposite
// order. streebog.go documents why: GOST R 34.11-2012 defines Streebog
// over a vector whose own worked examples write the most significant byte
// first, the opposite of how a real message is read or a real digest is
// reported by OpenSSL's GOST engine, pygost, or VeraCrypt's own reference
// code. newStreebog512 follows that real-world convention, so checking it
// against the RFC's own hex dump means reversing both the message and the
// expected digest here, the same reversal Write and Sum perform one level
// down.
func streebogReverse(b []byte) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[len(b)-1-i] = v
	}
	return out
}

func streebogMustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex literal %q: %v", s, err)
	}
	return b
}

// TestStreebog512RFC6986Vectors checks both worked examples in RFC 6986's
// 512-bit variant, M1 (an incomplete final block, exercising the padding
// path alone) and M2 (one full block followed by a padded remainder,
// exercising the block loop too). Both hex strings below are copied
// verbatim from the RFC; streebogReverse is what turns them into the
// ordinary-byte-order message and digest newStreebog512 actually produces.
func TestStreebog512RFC6986Vectors(t *testing.T) {
	cases := []struct {
		name string
		m    string
		want string
	}{
		{
			name: "M1",
			m:    "323130393837363534333231303938373635343332313039383736353433323130393837363534333231303938373635343332313039383736353433323130",
			want: "486f64c1917879417fef082b3381a4e211c324f074654c38823a7b76f830ad00fa1fbae42b1285c0352f227524bc9ab16254288dd6863dccd5b9f54a1ad0541b",
		},
		{
			name: "M2",
			m:    "fbe2e5f0eee3c820fbeafaebef20fffbf0e1e0f0f520e0ed20e8ece0ebe5f0f2f120fff0eeec20f120faf2fee5e2202ce8f6f3ede220e8e6eee1e8f0f2d1202ce8f0f2e5e220e5d1",
			want: "28fbc9bada033b1460642bdcddb90c3fb3e56c497ccd0f62b8a2ad4935e85f037613966de4ee00531ae60f3b5a47f8dae06915d5f2f194996fcabf2622e6881e",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := streebogReverse(streebogMustHex(t, c.m))
			want := streebogReverse(streebogMustHex(t, c.want))
			h := newStreebog512()
			if _, err := h.Write(msg); err != nil {
				t.Fatalf("Write: %v", err)
			}
			got := h.Sum(nil)
			if !bytes.Equal(got, want) {
				t.Fatalf("%s: got %x, want %x", c.name, got, want)
			}
		})
	}
}

// TestStreebog512HMACVector checks HMAC-Streebog-512 against the second
// test example in RFC 7836's own appendix of worked examples. Unlike the
// RFC 6986 worked examples above, this key, message and MAC are already
// given in ordinary byte order, the same order any caller's key and
// message would arrive in, so nothing here needs reversing.
func TestStreebog512HMACVector(t *testing.T) {
	key := streebogMustHex(t, "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	msg := streebogMustHex(t, "0126bdb87800af214341456563780100")
	want := streebogMustHex(t, "a59bab22ecae19c65fbde6e5f4e9f5d8549d31f037f9df9b905500e171923a773d5f1530f2ed7e964cb2eedc29e9ad2f3afe93b2814f79f5000ffc0366c251e6")

	mac := hmac.New(newStreebog512, key)
	if _, err := mac.Write(msg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := mac.Sum(nil)
	if !bytes.Equal(got, want) {
		t.Fatalf("HMAC-Streebog-512: got %x, want %x", got, want)
	}
}

// TestStreebog512AwkwardWrites proves the block buffer survives chunk
// boundaries that land nowhere near a multiple of the 64-byte block size:
// the nine chunk lengths below sum to M2's 72 bytes and share no common
// factor with 64. The expected digest is RFC 6986's own H(M2), reversed
// the same way TestStreebog512RFC6986Vectors reverses it.
func TestStreebog512AwkwardWrites(t *testing.T) {
	msg := streebogReverse(streebogMustHex(t, "fbe2e5f0eee3c820fbeafaebef20fffbf0e1e0f0f520e0ed20e8ece0ebe5f0f2f120fff0eeec20f120faf2fee5e2202ce8f6f3ede220e8e6eee1e8f0f2d1202ce8f0f2e5e220e5d1"))
	want := streebogReverse(streebogMustHex(t, "28fbc9bada033b1460642bdcddb90c3fb3e56c497ccd0f62b8a2ad4935e85f037613966de4ee00531ae60f3b5a47f8dae06915d5f2f194996fcabf2622e6881e"))

	chunks := []int{1, 1, 3, 7, 50, 1, 1, 1, 7}
	total := 0
	for _, n := range chunks {
		total += n
	}
	if total != len(msg) {
		t.Fatalf("chunk lengths sum to %d, want %d", total, len(msg))
	}

	h := newStreebog512()
	pos := 0
	for _, n := range chunks {
		if _, err := h.Write(msg[pos : pos+n]); err != nil {
			t.Fatalf("Write: %v", err)
		}
		pos += n
	}
	got := h.Sum(nil)
	if !bytes.Equal(got, want) {
		t.Fatalf("chunked M2: got %x, want %x", got, want)
	}
}

// TestStreebog512SumIsIdempotent is the property HMAC depends on: Sum must
// not disturb the digest, so calling it twice in a row returns the same
// bytes, and a caller may keep writing afterward as though Sum had never
// run.
func TestStreebog512SumIsIdempotent(t *testing.T) {
	h := newStreebog512()
	if _, err := h.Write([]byte("some data, then some more")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	first := h.Sum(nil)
	second := h.Sum(nil)
	if !bytes.Equal(first, second) {
		t.Fatalf("Sum is not idempotent: %x then %x", first, second)
	}
	if _, err := h.Write([]byte(" and then some more still")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	third := h.Sum(nil)
	if bytes.Equal(first, third) {
		t.Fatalf("Sum did not change after further Write")
	}
}

// TestStreebog512Reset checks that Reset truly returns the digest to its
// just-constructed state, not to a zeroed struct that happens to look
// similar: hashing a message after writing something else and resetting
// must match hashing that message from a fresh instance.
func TestStreebog512Reset(t *testing.T) {
	h := newStreebog512()
	if _, err := h.Write([]byte("a message that will be discarded")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	h.Reset()
	if _, err := h.Write([]byte("abc")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := h.Sum(nil)

	fresh := newStreebog512()
	if _, err := fresh.Write([]byte("abc")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := fresh.Sum(nil)

	if !bytes.Equal(got, want) {
		t.Fatalf("after Reset: got %x, want %x", got, want)
	}
}

// TestStreebog512Interface checks the fixed sizes VeraCrypt's algorithm
// table and golang.org/x/crypto/xts both rely on without constructing a
// full hash.Hash.
func TestStreebog512Interface(t *testing.T) {
	h := newStreebog512()
	if got := h.Size(); got != 64 {
		t.Fatalf("Size() = %d, want 64", got)
	}
	if got := h.BlockSize(); got != 64 {
		t.Fatalf("BlockSize() = %d, want 64", got)
	}
}
