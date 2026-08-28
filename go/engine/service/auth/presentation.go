package auth

import (
	"context"
	"crypto/sha256"
	"fmt"

	"golang.org/x/crypto/hkdf"
)

// The two pieces of key material the protocol layer needs, kept here because
// this package owns the key ring and nothing above it should hold a master
// key to derive from.

// csrfKeyLabel and presentationLabel separate the two derivations, so the
// value one of them produces can never be the value the other expects.
const (
	csrfKeyLabel         = "stowcloud/csrf-derivation/v1"
	presentationKeyLabel = "stowcloud/presentation-seal/v1"
)

// csrfKeyLen is 32 bytes, which is what an HMAC over a session identifier
// wants and what the derivation produces.
const csrfKeyLen = 32

// CSRFKey is the stable key the protocol layer derives request tokens with.
//
// It is derived from the master key rather than generated per process,
// because sessions are durable: a process-random key would invalidate every
// live session's token on every restart, and a restart is not a security
// event. A master key rotation does roll it, which costs one token refresh
// and is a deliberate, rare act.
//
// The value is never logged and never leaves the process that asked for it.
func (s *Service) CSRFKey(context.Context) ([]byte, error) {
	master, ver, err := s.activeKey()
	if err != nil {
		return nil, err
	}
	return deriveKey(master, csrfKeyLabel, ver, csrfKeyLen)
}

// SealPresentation seals a short-lived capability the protocol layer mints:
// a content claim, or a login flow's undelivered credential.
//
// purpose is bound as additional authenticated data, and callers pass fixed
// literals rather than request input, so a value minted for one purpose
// cannot be presented as another. The version comes back with the ciphertext
// because a rotation may land between minting and presenting.
func (s *Service) SealPresentation(
	_ context.Context, purpose string, plain []byte,
) (sealed []byte, version uint32, err error) {
	if purpose == "" {
		return nil, 0, fmt.Errorf("a sealed presentation value needs a purpose")
	}
	master, ver, err := s.activeKey()
	if err != nil {
		return nil, 0, err
	}
	key, err := derivePresentationKey(master, ver)
	if err != nil {
		return nil, 0, err
	}
	blob, err := sealWith(key, plain, aadName(bindPresentation, purpose, ver))
	if err != nil {
		return nil, 0, err
	}
	return blob, ver, nil
}

// OpenPresentation is the inverse, under the version the value was sealed
// with. A value whose version the ring no longer holds refuses, which is what
// bounds how long a capability outlives a rotation.
func (s *Service) OpenPresentation(
	_ context.Context, purpose string, sealed []byte, version uint32,
) ([]byte, error) {
	master, err := s.keyAt(version)
	if err != nil {
		return nil, err
	}
	key, err := derivePresentationKey(master, version)
	if err != nil {
		return nil, err
	}
	return openWith(key, sealed, aadName(bindPresentation, purpose, version))
}

func derivePresentationKey(master [keyLen]byte, ver uint32) ([keyLen]byte, error) {
	var out [keyLen]byte
	b, err := deriveKey(master, presentationKeyLabel, ver, keyLen)
	if err != nil {
		return out, err
	}
	copy(out[:], b)
	return out, nil
}

// deriveKey is one labelled derivation from the master key. The version goes
// into the label's info, so two ring versions never derive the same subkey.
func deriveKey(master [keyLen]byte, label string, ver uint32, n int) ([]byte, error) {
	info := fmt.Appendf(nil, "%s/%d", label, ver)
	out := make([]byte, n)
	if _, err := hkdf.New(sha256.New, master[:], nil, info).Read(out); err != nil {
		return nil, fmt.Errorf("deriving a subkey: %w", err)
	}
	return out, nil
}
