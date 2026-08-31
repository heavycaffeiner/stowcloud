package auth

import (
	"errors"
	"strings"
)

// ErrBadCrockford is a code that is not this alphabet, even after folding the
// characters people mistype.
var ErrBadCrockford = errors.New("not a Crockford Base32 code")

// crockfordAlphabet is Base32 with I, L, O and U removed.
//
// A code read off a screen and typed into a phone must not fail on a
// character the reader guessed wrong. That is a usability property with a
// security consequence: a code people mistype is a code people write down
// somewhere worse.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// crockfordEncode encodes arbitrary bytes. App-password tokens and recovery
// codes are minted in this alphabet and stored as the digest of the string.
func crockfordEncode(b []byte) string {
	out := make([]byte, 0, (len(b)*8+4)/5)
	var acc uint32
	var bits uint
	for _, x := range b {
		acc = acc<<8 | uint32(x)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out = append(out, crockfordAlphabet[(acc>>bits)&31])
		}
	}
	if bits > 0 {
		out = append(out, crockfordAlphabet[(acc<<(5-bits))&31])
	}
	return string(out)
}

// crockfordFold canonicalizes a typed code before it is hashed and compared.
// Folding a typed I or L to 1 and O to 0 is what lets a code read wrong still
// match the code that was minted.
func crockfordFold(s string) (string, error) {
	out := make([]byte, 0, len(s))
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
		out = append(out, c)
	}
	return string(out), nil
}
