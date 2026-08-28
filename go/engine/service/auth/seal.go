package auth

import (
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// Everything encrypted at rest travels as nonce(24) || ciphertext under
// XChaCha20-Poly1305. Every seal binds the record it sits in and the key
// version that sealed it as additional authenticated data, so a ciphertext
// cannot be moved between records or replayed across a rotation.

const (
	// nonceLen is the XChaCha20 nonce size.
	nonceLen = 24
	// keyLen is the AEAD key size, which is also the key ring's entry size.
	keyLen = 32
	// ntHashLen is the MD4 output the file-sharing protocol authenticates
	// against.
	ntHashLen = 16
)

// The binding prefixes. Each is short and distinct so one record's additional
// data can never be confused with another's.
const (
	bindNT           = "smb_nt"
	bindTOTP         = "totp"
	bindShareLink    = "shlink"
	bindConfig       = "config"
	bindPresentation = "pres"
)

// ErrCiphertextTooShort is a stored blob that lacks even a nonce. It is
// corruption rather than an authentication failure, and the two must stay
// distinguishable in a log.
var ErrCiphertextTooShort = errors.New("ciphertext is shorter than a nonce")

// sealWith records plain under key with the given additional data.
func sealWith(key [keyLen]byte, plain, aad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if err := fillRandom(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plain, aad), nil
}

// openWith is the inverse.
func openWith(key [keyLen]byte, blob, aad []byte) ([]byte, error) {
	if len(blob) < nonceLen {
		return nil, ErrCiphertextTooShort
	}
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, blob[:nonceLen], blob[nonceLen:], aad)
	if err != nil {
		return nil, fmt.Errorf("opening ciphertext: %w", err)
	}
	return pt, nil
}

// The four record bindings. Each appends the key version last, so a
// ciphertext sealed under one version cannot be opened as another.

// aadUser binds a row to the account it belongs to.
//
// The account id is narrowed rather than reinterpreted: the stored binding is
// the unsigned encoding of a positive row id, and a negative one is not a row
// this database can hold, so it is an error rather than a different eight
// bytes that would silently fail to open.
func aadUser(prefix string, user int64, keyVer uint32) ([]byte, error) {
	id, err := num.Narrow[uint64](user)
	if err != nil {
		return nil, fmt.Errorf("account %d cannot be bound to a sealed record: %w", user, err)
	}
	out := make([]byte, 0, len(prefix)+12)
	out = append(out, prefix...)
	out = binary.LittleEndian.AppendUint64(out, id)
	return binary.LittleEndian.AppendUint32(out, keyVer), nil
}

func aadBytes(prefix string, id []byte, keyVer uint32) []byte {
	out := make([]byte, 0, len(prefix)+len(id)+4)
	out = append(out, prefix...)
	out = append(out, id...)
	return binary.LittleEndian.AppendUint32(out, keyVer)
}

func aadName(prefix, name string, keyVer uint32) []byte {
	return aadBytes(prefix, []byte(name), keyVer)
}

func sealNT(key [keyLen]byte, nt [ntHashLen]byte, user int64, keyVer uint32) ([]byte, error) {
	aad, err := aadUser(bindNT, user, keyVer)
	if err != nil {
		return nil, err
	}
	return sealWith(key, nt[:], aad)
}

func openNT(key [keyLen]byte, blob []byte, user int64, keyVer uint32) ([ntHashLen]byte, error) {
	var out [ntHashLen]byte
	aad, err := aadUser(bindNT, user, keyVer)
	if err != nil {
		return out, err
	}
	pt, err := openWith(key, blob, aad)
	if err != nil {
		return out, err
	}
	if len(pt) != ntHashLen {
		return out, errors.New("the stored NT hash has the wrong length")
	}
	copy(out[:], pt)
	return out, nil
}

func sealTOTP(key [keyLen]byte, secretBytes []byte, user int64, keyVer uint32) ([]byte, error) {
	aad, err := aadUser(bindTOTP, user, keyVer)
	if err != nil {
		return nil, err
	}
	return sealWith(key, secretBytes, aad)
}

func openTOTP(key [keyLen]byte, blob []byte, user int64, keyVer uint32) ([]byte, error) {
	aad, err := aadUser(bindTOTP, user, keyVer)
	if err != nil {
		return nil, err
	}
	return openWith(key, blob, aad)
}

// LinkCipher is the at-rest cryptography for share-link tokens, exported for
// the store aggregate that owns those rows. The binding is the token hash and
// the key version, so a ciphertext cannot be moved between links.
type LinkCipher struct {
	key [keyLen]byte
}

// NewLinkCipher returns a cipher over one master key, which the assembly
// layer takes from the loaded ring.
func NewLinkCipher(key [keyLen]byte) LinkCipher { return LinkCipher{key: key} }

// Seal turns a token into its stored form.
func (c LinkCipher) Seal(token, tokenHash []byte, keyVer uint32) ([]byte, error) {
	return sealWith(c.key, token, aadBytes(bindShareLink, tokenHash, keyVer))
}

// Open is the inverse. A token that will not open is a degraded row rather
// than a broken one: the hash beside it still authenticates the public URL.
func (c LinkCipher) Open(blob, tokenHash []byte, keyVer uint32) ([]byte, error) {
	return openWith(c.key, blob, aadBytes(bindShareLink, tokenHash, keyVer))
}

// SealConfigSecret encrypts one configuration secret under the active key.
//
// It exists because a setting can be a credential: the single-sign-on client
// secret is one, and a settings document carrying it would put a credential
// in every read of that document and every response the settings screen
// renders.
func (s *Service) SealConfigSecret(name string, plain []byte) ([]byte, uint32, error) {
	key, ver, err := s.activeKey()
	if err != nil {
		return nil, 0, err
	}
	blob, err := sealWith(key, plain, aadName(bindConfig, name, ver))
	if err != nil {
		return nil, 0, err
	}
	return blob, ver, nil
}

// OpenConfigSecret is the inverse, under whichever ring key sealed it.
func (s *Service) OpenConfigSecret(name string, blob []byte, keyVer uint32) ([]byte, error) {
	key, err := s.keyAt(keyVer)
	if err != nil {
		return nil, err
	}
	return openWith(key, blob, aadName(bindConfig, name, keyVer))
}
