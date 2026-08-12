package auth

import (
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// Everything encrypted at rest travels as nonce(24) || ciphertext, sealed
// with XChaCha20-Poly1305 under a 32-byte master key. Every seal is bound to
// the record it sits in (the user id, or the token hash) and to the key
// version that sealed it, as additional authenticated data, so a value cannot
// be transplanted between records or replayed across a rotation.

// nonceLen is the XChaCha20 nonce size, and ntHashLen is the MD4 output the
// NT hash is.
const (
	nonceLen  = 24
	ntHashLen = 16
)

// Binding prefixes. Each is short and distinct so one record's AAD can never
// be confused with another's.
const (
	bindNT     = "smb_nt"
	bindTOTP   = "totp"
	bindShLink = "shlink"
)

// ErrCiphertextTooShort is a stored blob that lacks even a nonce, which is
// corruption, not an AEAD failure.
var ErrCiphertextTooShort = errors.New("ciphertext is shorter than a nonce")

// seal records a secret under key. bind is the record-bound AAD prefix, and
// keyVer is appended to it when versioned is true.
func seal(key [keyLen]byte, secret, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	mustRand(nonce)
	ct := aead.Seal(nil, nonce, secret, aad)
	return append(nonce, ct...), nil
}

// open decrypts a blob under key with the given AAD.
func open(key [keyLen]byte, blob, aad []byte) ([]byte, error) {
	if len(blob) < nonceLen {
		return nil, ErrCiphertextTooShort
	}
	nonce, ct := blob[:nonceLen], blob[nonceLen:]
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, fmt.Errorf("opening ciphertext: %w", err)
	}
	return pt, nil
}

func aadNT(user int64, keyVer uint32) []byte {
	out := make([]byte, 0, len(bindNT)+8+4)
	out = append(out, bindNT...)
	out = binary.LittleEndian.AppendUint64(out, uint64(user)) //nolint:gosec // userId is an app-generated positive id, never a negative input.
	out = binary.LittleEndian.AppendUint32(out, keyVer)
	return out
}

func aadTOTP(user int64, keyVer uint32, versioned bool) []byte {
	out := make([]byte, 0, len(bindTOTP)+8+4)
	out = append(out, bindTOTP...)
	out = binary.LittleEndian.AppendUint64(out, uint64(user)) //nolint:gosec // userId is an app-generated positive id, never a negative input.
	if versioned {
		out = binary.LittleEndian.AppendUint32(out, keyVer)
	}
	return out
}

func aadLink(tokenHash []byte, keyVer uint32, versioned bool) []byte {
	out := make([]byte, 0, len(bindShLink)+len(tokenHash)+4)
	out = append(out, bindShLink...)
	out = append(out, tokenHash...)
	if versioned {
		out = binary.LittleEndian.AppendUint32(out, keyVer)
	}
	return out
}

// summary of the on-disk residents:
//
//   - the NT hash binds key_ver into its AAD today, so an imported ciphertext
//     needs no migration; it is opened and re-sealed only by a rotation.
//   - the TOTP secret and imported share-link token did not carry a version;
//     startup re-seals them under the version-bound AAD in one transaction.

func sealNT(key [keyLen]byte, nt [16]byte, user int64, keyVer uint32) ([]byte, error) {
	return seal(key, nt[:], aadNT(user, keyVer))
}

func openNT(key [keyLen]byte, blob []byte, user int64, keyVer uint32) ([16]byte, error) {
	var out [16]byte
	pt, err := open(key, blob, aadNT(user, keyVer))
	if err != nil {
		return out, err
	}
	if len(pt) != ntHashLen {
		return out, errors.New("NT hash plaintext has the wrong length")
	}
	copy(out[:], pt)
	return out, nil
}

func sealTOTP(key [keyLen]byte, secret []byte, user int64, keyVer uint32) ([]byte, error) {
	return seal(key, secret, aadTOTP(user, keyVer, true))
}

func openTOTP(key [keyLen]byte, blob []byte, user int64, keyVer uint32) ([]byte, error) {
	return open(key, blob, aadTOTP(user, keyVer, true))
}

// openTOTPLegacy opens a TOTP secret sealed before key versions were bound
// into its AAD, which is what an import from the Rust tree produces.
func openTOTPLegacy(key [keyLen]byte, blob []byte, user int64) ([]byte, error) {
	return open(key, blob, aadTOTP(user, 0, false))
}

func sealLink(key [keyLen]byte, token, tokenHash []byte, keyVer uint32) ([]byte, error) {
	return seal(key, token, aadLink(tokenHash, keyVer, true))
}

func openLink(key [keyLen]byte, blob, tokenHash []byte, keyVer uint32) ([]byte, error) {
	return open(key, blob, aadLink(tokenHash, keyVer, true))
}

// openLinkLegacy opens a share-link token sealed without a version, which is
// what the Rust rotation path left behind.
func openLinkLegacy(key [keyLen]byte, blob, tokenHash []byte) ([]byte, error) {
	return open(key, blob, aadLink(tokenHash, 0, false))
}

// LinkCipher is the at-rest cryptography for share-link tokens, exported for
// the core's link store which owns the rows this seals. The AAD binds the
// token hash and key version, so a ciphertext cannot be transplanted between
// links or replayed across a rotation.
type LinkCipher struct {
	key [keyLen]byte
}

// NewLinkCipher returns a cipher over one master key, which the core receives
// from the server that loaded the key ring.
func NewLinkCipher(key [32]byte) LinkCipher { return LinkCipher{key: key} }

// Seal turns a token into nonce||ciphertext under this cipher's key and the
// given version.
func (c LinkCipher) Seal(token, tokenHash []byte, keyVer uint32) ([]byte, error) {
	return sealLink(c.key, token, tokenHash, keyVer)
}

// Open is the inverse. A token that will not open is a degraded row, not a
// broken one: the hash in the same row still authenticates the public URL.
func (c LinkCipher) Open(blob, tokenHash []byte, keyVer uint32) ([]byte, error) {
	return openLink(c.key, blob, tokenHash, keyVer)
}
