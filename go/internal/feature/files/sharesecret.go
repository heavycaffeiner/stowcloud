//go:build linux

package core

import (
	"errors"
	"fmt"
)

// ShareSecretCipher seals and opens the one credential a non-local share's
// backend needs, under the master key ring. An interface, like LinkCipher,
// so a Core can hold a zero value that refuses to seal until the server
// wires the key.
//
// The method set matches auth.Service's SealConfigSecret and
// OpenConfigSecret exactly, so the assembly layer wires the same service
// that already seals the single-sign-on client secret, rather than
// inventing a second sealing path for this one.
type ShareSecretCipher interface {
	SealConfigSecret(name string, plain []byte) ([]byte, uint32, error)
	OpenConfigSecret(name string, blob []byte, keyVer uint32) ([]byte, error)
}

// AttachShareSecretCrypto wires the cipher that seals and opens share
// credentials. Attached after construction, like the link cipher, because
// the master key loads later than the domain does.
func (c *Core) AttachShareSecretCrypto(cipher ShareSecretCipher) {
	c.secretCipher = cipher
}

// shareSecretName is the AAD binding for one share's credential. Bound to
// the share's own id, which is why the secret is written by its own
// statement after the row exists: the id this binds to does not exist
// until the insert that creates it returns one.
func shareSecretName(id ShareID) string {
	return fmt.Sprintf("share.%d.secret", id)
}

// sealShareSecret seals plain for durable storage under id's binding.
func (c *Core) sealShareSecret(id ShareID, plain []byte) ([]byte, uint32, error) {
	if c.secretCipher == nil {
		return nil, 0, errors.New("no share secret cipher is attached")
	}
	return c.secretCipher.SealConfigSecret(shareSecretName(id), plain)
}

// openShareSecret is the inverse, under whichever ring key sealed it.
func (c *Core) openShareSecret(id ShareID, blob []byte, keyVer uint32) ([]byte, error) {
	if c.secretCipher == nil {
		return nil, errors.New("no share secret cipher is attached")
	}
	return c.secretCipher.OpenConfigSecret(shareSecretName(id), blob, keyVer)
}
