package auth

import (
	"errors"
	"strings"
)

// ErrBadCrockford is a code that is not a Crockford Base32 string, even after
// folding the characters people mistype.
var ErrBadCrockford = errors.New("not a Crockford Base32 code")

// crockfordAlphabet is Base32 with I, L, O and U removed. A code read off a
// screen and typed into a phone must not fail on a character the reader
// guessed wrong: that is a usability property with a security consequence,
// because a code people mistype is a code people write down somewhere worse.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// crockfordEncode encodes arbitrary bytes. Recovery codes and app-password
// tokens are minted in this alphabet and stored as the SHA-256 of the string.
func crockfordEncode(b []byte) string {
	var out strings.Builder
	var acc uint32
	var bits uint
	for _, x := range b {
		acc = acc<<8 | uint32(x)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out.WriteByte(crockfordAlphabet[(acc>>bits)&31]) //nolint:errcheck // strings.Builder.WriteByte never fails.
		}
	}
	if bits > 0 {
		out.WriteByte(crockfordAlphabet[(acc<<(5-bits))&31]) //nolint:errcheck // strings.Builder.WriteByte never fails.
	}
	return out.String()
}

// crockfordFold canonicalises a typed code before it is hashed and compared.
// Folding a typed I or L to 1 and O to 0 is what lets a code that was read
// wrong still match the code that was minted.
func crockfordFold(s string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		switch c {
		case 'I', 'L':
			c = '1'
		case 'O':
			c = '0'
		case 'U':
			return "", ErrBadCrockford
		}
		if !strings.ContainsRune(crockfordAlphabet, rune(c)) {
			return "", ErrBadCrockford
		}
		b.WriteByte(c) //nolint:errcheck // strings.Builder.WriteByte never fails.
	}
	return b.String(), nil
}
