package auth

import "testing"

// D16: the two parsers that read untrusted bytes must never panic. A PHC
// string arrives from a database an attacker may have read, and a recovery
// code arrives from a human typing a phone; both are fuzzed for panics.

func FuzzParsePHC(f *testing.F) {
	f.Add("$argon2id$v=19$m=49152,t=3,p=1$c2FsdHZhbHVlMTIzNDU2$Zm9vYmFyYmF6cXV4Cg")
	f.Add("not-a-hash")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = parsePHC(s)
	})
}

func FuzzCrockfordFold(f *testing.F) {
	f.Add("A1B2C3")
	f.Add("ilo-u")
	f.Add("")
	f.Fuzz(func(t *testing.T, s string) {
		crockfordFold(s) //nolint:errcheck // any input is a valid fuzz case; the fold result is irrelevant.
	})
}
