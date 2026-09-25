// Package secret holds Secret, the container every password, key and token
// in the engine passes through instead of a plain string. A string cannot be
// zeroed once created; a Secret can, and it also refuses to print or
// serialize its bytes by accident.
//
// What it does not do: it cannot reach a copy the Go runtime made of its
// buffer before Destroy runs (a stack-to-heap move, an old GC generation).
// That limit is accepted rather than solved here.
package secret

import (
	"crypto/subtle"
	"encoding/json"
)

const redactedText = "[redacted]"

// Secret wraps a byte slice that must never leak through a log line, a
// format verb, or a JSON encoder. The field is unexported so nothing outside
// this package can reach the bytes except through Reveal.
type Secret struct {
	b []byte
}

// New wraps b as a Secret. The caller gives up the slice: keeping a
// reference to b and mutating or reading it after this call defeats the
// whole point and is the caller's bug, not something this package can catch.
func New(b []byte) Secret {
	return Secret{b: b}
}

// String always returns the redacted placeholder, regardless of the
// contents. It is a value receiver so both Secret and *Secret redact under
// %v, %s, %q and %x.
func (s Secret) String() string {
	return redactedText
}

// GoString covers %#v, which bypasses String and would otherwise print the
// struct's fields, including the raw bytes.
func (s Secret) GoString() string {
	return redactedText
}

// MarshalJSON redacts so a Secret embedded anywhere in a struct that gets
// encoded does not carry its bytes onto the wire.
func (s Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal(redactedText)
}

// Equal reports whether two secrets hold the same bytes, comparing in
// constant time so the comparison itself cannot be timed to learn where the
// two values first diverge.
func (s Secret) Equal(other Secret) bool {
	return subtle.ConstantTimeCompare(s.b, other.b) == 1
}

// Len reports the byte length, enough for a caller to check a key size
// without needing Reveal.
func (s Secret) Len() int {
	return len(s.b)
}

// Reveal is the only way to get at the underlying bytes. The returned slice
// aliases the Secret's own buffer: do not retain it past the Secret's
// lifetime, copy it into a string, or hand it to something that formats or
// logs it. After Destroy, Reveal returns nil.
func (s Secret) Reveal() []byte {
	return s.b
}

// Destroy overwrites the owned buffer with zeros and drops the reference to
// it. Call it once the secret is no longer needed.
func (s *Secret) Destroy() {
	for i := range s.b {
		s.b[i] = 0
	}
	s.b = nil
}
