package auth_test

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238 fixes the algorithm; this is the reference computation the test compares against.
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// totpCode is the reference computation from RFC 6238, written here rather
// than reused from the package under test, so the test proves the
// implementation against the specification rather than against itself.
func totpCode(t *testing.T, secretB32 string, step int64) string {
	t.Helper()
	secret, derr := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	if derr != nil {
		t.Fatalf("decoding the secret: %v", derr)
	}
	counterValue, err := num.Narrow[uint64](step)
	if err != nil {
		t.Fatalf("a step of %d has no counter: %v", step, err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], counterValue)
	mac := hmac.New(sha1.New, secret)
	if _, werr := mac.Write(counter[:]); werr != nil {
		t.Fatalf("computing the reference code: %v", werr)
	}
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	val := uint32(sum[off]&0x7f)<<24 |
		uint32(sum[off+1])<<16 |
		uint32(sum[off+2])<<8 |
		uint32(sum[off+3])
	return fmt.Sprintf("%06d", val%1_000_000)
}

// The published test vector, so a change to the derivation is caught against
// the specification and not only against this tree's own arithmetic.
func TestTheReferenceVectorFromRFC6238(t *testing.T) {
	// "12345678901234567890" in Base32, which is the vector's shared secret.
	const secretB32 = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	cases := []struct {
		unix int64
		code string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
	}
	for _, c := range cases {
		if got := totpCode(t, secretB32, c.unix/30); got != c.code {
			t.Fatalf("at %d the reference gives %q, want %q", c.unix, got, c.code)
		}
	}
}
