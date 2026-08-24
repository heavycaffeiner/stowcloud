// Package secret holds the type every credential, key and token in this tree is
// carried in.
//
// Never a string: a Go string is immutable, so it can never be zeroed at all.
// What this type actually buys is three things, and it is worth being exact
// about them because the fourth is not available. It cannot be printed by
// accident, it cannot be serialised by accident, and its owner can zero the one
// buffer this code holds. What it cannot do is zero a copy the garbage
// collector made before Destroy ran. That gap is accepted, not closed.
package secret

import (
	"crypto/subtle"
	"encoding/json"
)

// redacted is what every rendering of a Secret produces.
const redacted = "[redacted]"

// Secret holds bytes that must never be logged, formatted, serialised or
// compared with ==. The field is unexported and a slice, so == does not
// compile and no package outside this one can read the bytes except through
// Reveal.
type Secret struct{ b []byte }

// New takes ownership of b. The caller must not keep or reuse the slice: this
// type cannot protect a buffer someone else still holds.
func New(b []byte) Secret { return Secret{b: b} }

// String redacts. A value receiver, so a Secret and a *Secret both render this
// way under %v, %s, %q, %x and %X.
func (Secret) String() string { return redacted }

// GoString redacts too, because %#v ignores String and would otherwise print
// the struct and its bytes.
func (Secret) GoString() string { return redacted }

// MarshalJSON redacts, so a Secret embedded in a response or a config dump
// cannot serialise its bytes.
func (Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }

// Equal compares in constant time. Lengths that differ are reported as unequal
// without comparing, which is the one thing this leaks and it is the same thing
// the wire format leaks anyway.
func (s Secret) Equal(other Secret) bool {
	return subtle.ConstantTimeCompare(s.b, other.b) == 1
}

// Len is the byte length, which a caller needs to validate a key size without
// revealing anything.
func (s Secret) Len() int { return len(s.b) }

// Reveal returns the bytes for the one thing a secret is for: being used. The
// returned slice aliases the secret, so it must not be retained, copied into a
// string, or passed to anything that formats. This is the single accessor, and
// having exactly one is what makes the vetsecret analyser's job finite.
func (s Secret) Reveal() []byte { return s.b }

// Destroy zeroes the buffer this value holds. Call it when the owner is done.
// It cannot reach a copy the runtime made; see the package comment.
func (s *Secret) Destroy() {
	for i := range s.b {
		s.b[i] = 0
	}
	s.b = nil
}
