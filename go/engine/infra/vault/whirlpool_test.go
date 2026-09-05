package vault

import (
	"bytes"
	"crypto/hmac"
	"crypto/pbkdf2"
	"encoding/hex"
	"io"
	"strings"
	"testing"
)

// whirlpoolVectors are the standard NESSIE / ISO test strings for Whirlpool:
// the empty message, the single byte "a", "abc", "message digest", the
// lowercase alphabet, the full alphanumeric alphabet, eight repetitions of
// "1234567890" and the million-'a' stress vector. The expected digests come
// from Botan's checked-in test data
// (src/tests/data/hash/whirlpool.vec in github.com/randombit/botan), and
// every one of them was independently reproduced by a from-scratch Python
// port of this same table-driven round function before this file was
// written, so the fixture is confirmed against two unrelated
// implementations rather than transcribed once and trusted.
func whirlpoolVectors() []struct {
	msg  string
	want string
} {
	return []struct {
		msg  string
		want string
	}{
		{"", "19fa61d75522a4669b44e39c1d2e1726c530232130d407f89afee0964997f7a73e83be698b288febcf88e3e03c4f0757ea8964e59b63d93708b138cc42a66eb3"},
		{"a", "8aca2602792aec6f11a67206531fb7d7f0dff59413145e6973c45001d0087b42d11bc645413aeff63a42391a39145a591a92200d560195e53b478584fdae231a"},
		{"abc", "4e2448a4c6f486bb16b6562c73b4020bf3043e3a731bce721ae1b303d97e6d4c7181eebdb6c57e277d0e34957114cbd6c797fc9d95d8b582d225292076d4eef5"},
		{"message digest", "378c84a4126e2dc6e56dcc7458377aac838d00032230f53ce1f5700c0ffb4d3b8421557659ef55c106b4b52ac5a4aaa692ed920052838f3362e86dbd37a8903e"},
		{"abcdefghijklmnopqrstuvwxyz", "f1d754662636ffe92c82ebb9212a484a8d38631ead4238f5442ee13b8054e41b08bf2a9251c30b6a0b8aae86177ab4a6f68f673e7207865d5d9819a3dba4eb3b"},
		{"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789", "dc37e008cf9ee69bf11f00ed9aba26901dd7c28cdec066cc6af42e40f82f3a1e08eba26629129d8fb7cb57211b9281a65517cc879d7b962142c65f5a7af01467"},
		{strings.Repeat("1234567890", 8), "466ef18babb0154d25b9d38a6414f5c08784372bccb204d6549c4afadb6014294d5bd8df2a6c44e538cd047b2681a51a2c60481e88c5a20b2c2a80cf3a9a083b"},
		{strings.Repeat("a", 1000000), "0c99005beb57eff50a7cf005560ddf5d29057fd86b20bfd62deca0f1ccea4af51fc15490eddc47af32bb2b66c34ff9ad8c6008ad677f77126953b226e4ed8b01"},
	}
}

func TestWhirlpoolVectors(t *testing.T) {
	for _, v := range whirlpoolVectors() {
		h := newWhirlpool()
		if _, err := h.Write([]byte(v.msg)); err != nil {
			t.Fatalf("Write(%.16q...): %v", v.msg, err)
		}
		got := hex.EncodeToString(h.Sum(nil))
		if got != v.want {
			name := v.msg
			if len(name) > 16 {
				name = name[:16] + "..."
			}
			t.Errorf("%q (len %d): got %s, want %s", name, len(v.msg), got, v.want)
		}
	}
}

// TestWhirlpoolAwkwardWrites proves the block buffer survives chunk
// boundaries that land nowhere near a multiple of the 64-byte block size:
// the five chunk lengths below sum to 104 and share no common factor with
// 64. The expected digest is the same OpenSSL-generated vector Botan
// carries for this message hashed in one piece.
func TestWhirlpoolAwkwardWrites(t *testing.T) {
	msg := strings.Repeat("abcdefghijklmnopqrstuvwxyz", 4) // 104 bytes.
	const want = "9fe4affe44e3d16d4e6109a252aeaf3fbd46f8a402a5cdc10eee48b6f64be2deee2b9ee62ea5c037236cea0b71cb1909a421672ca23662558cd7d98ccbd820ec"

	chunks := []int{1, 7, 13, 31, 52}
	sum := 0
	for _, c := range chunks {
		sum += c
	}
	if sum != len(msg) {
		t.Fatalf("test setup: chunk lengths sum to %d, message is %d bytes", sum, len(msg))
	}

	h := newWhirlpool()
	pos := 0
	for _, c := range chunks {
		n, err := h.Write([]byte(msg[pos : pos+c]))
		if err != nil || n != c {
			t.Fatalf("Write(%d bytes): n=%d err=%v", c, n, err)
		}
		pos += c
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		t.Errorf("chunked write: got %s, want %s", got, want)
	}

	whole := newWhirlpool()
	mustWrite(t, whole, []byte(msg))
	if gotWhole := hex.EncodeToString(whole.Sum(nil)); gotWhole != got {
		t.Errorf("chunked write disagrees with a single Write: %s vs %s", got, gotWhole)
	}
}

// mustWrite writes p into w and fails the test on error. hash.Hash and the
// HMAC wrapper around it both document Write as never failing, but errcheck
// still requires the return checked, so every test below goes through this
// instead of repeating the same three-line check.
func mustWrite(t *testing.T, w io.Writer, p []byte) {
	t.Helper()
	if _, err := w.Write(p); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

func TestWhirlpoolInterface(t *testing.T) {
	h := newWhirlpool()
	if h.Size() != 64 {
		t.Errorf("Size() = %d, want 64", h.Size())
	}
	if h.BlockSize() != 64 {
		t.Errorf("BlockSize() = %d, want 64", h.BlockSize())
	}
}

// TestWhirlpoolSumIsIdempotent is the property HMAC depends on: Sum must
// not disturb the digest, so calling it twice in a row returns the same
// bytes, and a caller may keep writing afterward as though Sum had never
// run.
func TestWhirlpoolSumIsIdempotent(t *testing.T) {
	h := newWhirlpool()
	mustWrite(t, h, []byte("some data"))
	first := h.Sum(nil)
	second := h.Sum(nil)
	if !bytes.Equal(first, second) {
		t.Fatalf("Sum() called twice: %x then %x", first, second)
	}

	mustWrite(t, h, []byte(" more data"))
	third := h.Sum(nil)
	if bytes.Equal(first, third) {
		t.Fatalf("digest did not change after writing more data past a Sum call")
	}

	want := newWhirlpool()
	mustWrite(t, want, []byte("some data more data"))
	wantSum := want.Sum(nil)
	if !bytes.Equal(third, wantSum) {
		t.Fatalf("continuing to write after Sum: got %x, want %x", third, wantSum)
	}
}

// TestWhirlpoolReset checks that Reset truly returns the digest to its
// just-constructed state, not to a zeroed struct that happens to look
// similar: hashing "abc" after writing and resetting must match hashing
// "abc" from a fresh instance.
func TestWhirlpoolReset(t *testing.T) {
	h := newWhirlpool()
	mustWrite(t, h, []byte("this gets discarded"))
	h.Reset()
	mustWrite(t, h, []byte("abc"))
	got := hex.EncodeToString(h.Sum(nil))

	fresh := newWhirlpool()
	mustWrite(t, fresh, []byte("abc"))
	want := hex.EncodeToString(fresh.Sum(nil))

	if got != want {
		t.Fatalf("after Reset: got %s, want %s", got, want)
	}
}

// TestWhirlpoolPBKDF2Stable exercises the padding and Reset path the way
// pbkdf2.Key actually drives it: one hash.Hash instance, Reset and
// Write and Sum called once per iteration. There is no published
// HMAC-Whirlpool test vector to check against (RFC 2104 defines the
// construction generically, and neither NESSIE nor ISO/IEC 10118-3
// publishes one for this hash), so this checks the property PBKDF2 and
// HMAC both need instead: the same inputs always derive the same key, and
// changing any one input changes it.
func TestWhirlpoolPBKDF2Stable(t *testing.T) {
	salt := []byte("a-salt-value")
	key1, err := pbkdf2.Key(newWhirlpool, "password", salt, 1, 64)
	if err != nil {
		t.Fatalf("pbkdf2.Key: %v", err)
	}
	key2, err := pbkdf2.Key(newWhirlpool, "password", salt, 1, 64)
	if err != nil {
		t.Fatalf("pbkdf2.Key: %v", err)
	}
	if !bytes.Equal(key1, key2) {
		t.Fatalf("pbkdf2.Key not stable across calls: %x vs %x", key1, key2)
	}
	if len(key1) != 64 {
		t.Fatalf("pbkdf2.Key returned %d bytes, want 64", len(key1))
	}

	if otherPassword, oerr := pbkdf2.Key(newWhirlpool, "different", salt, 1, 64); oerr != nil {
		t.Fatalf("pbkdf2.Key: %v", oerr)
	} else if bytes.Equal(key1, otherPassword) {
		t.Fatalf("pbkdf2.Key gave the same output for two different passwords")
	}

	// A higher iteration count drives many Reset/Write/Sum cycles on the
	// same instance rather than one, which is where a state leak between
	// iterations would show up.
	many1, err := pbkdf2.Key(newWhirlpool, "password", salt, 1000, 64)
	if err != nil {
		t.Fatalf("pbkdf2.Key: %v", err)
	}
	many2, err := pbkdf2.Key(newWhirlpool, "password", salt, 1000, 64)
	if err != nil {
		t.Fatalf("pbkdf2.Key: %v", err)
	}
	if !bytes.Equal(many1, many2) {
		t.Fatalf("pbkdf2.Key with 1000 iterations not stable across calls: %x vs %x", many1, many2)
	}
	if bytes.Equal(key1, many1) {
		t.Fatalf("pbkdf2.Key gave the same output for 1 and 1000 iterations")
	}
}

// TestWhirlpoolHMACReuse checks the exact pattern crypto/hmac uses
// internally: one hash.Hash instance, reused across messages via Reset
// rather than reconstructed. A single mac instance hashing A then, after
// Reset, hashing B must agree with two independent instances each hashing
// one message.
func TestWhirlpoolHMACReuse(t *testing.T) {
	key := []byte("an-hmac-key")

	shared := hmac.New(newWhirlpool, key)
	mustWrite(t, shared, []byte("message A"))
	sharedA := shared.Sum(nil)
	shared.Reset()
	mustWrite(t, shared, []byte("message B"))
	sharedB := shared.Sum(nil)

	freshA := hmac.New(newWhirlpool, key)
	mustWrite(t, freshA, []byte("message A"))
	wantA := freshA.Sum(nil)

	freshB := hmac.New(newWhirlpool, key)
	mustWrite(t, freshB, []byte("message B"))
	wantB := freshB.Sum(nil)

	if !bytes.Equal(sharedA, wantA) {
		t.Errorf("reused mac, message A: got %x, want %x", sharedA, wantA)
	}
	if !bytes.Equal(sharedB, wantB) {
		t.Errorf("reused mac after Reset, message B: got %x, want %x", sharedB, wantB)
	}
	if bytes.Equal(wantA, wantB) {
		t.Fatalf("test setup: two different messages produced the same MAC")
	}
}
