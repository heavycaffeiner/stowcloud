package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// Recovery codes: single-use, stored hashed, shown once at the moment they
// are minted. They are minted in the same alphabet as app passwords, so a
// code read off a screen survives being typed into a phone.

// recoveryCodeLen is the entropy of one code.
const recoveryCodeLen = 16

// RecoveryCodesMax bounds one set. A set nobody can print is not a recovery
// mechanism.
const RecoveryCodesMax = 64

// ErrRecoverySetSize is a set size outside what a set may hold.
var ErrRecoverySetSize = fmt.Errorf("a recovery-code set holds between 1 and %d codes", RecoveryCodesMax)

// GenerateRecoveryCodes mints n codes for an account, replacing whatever it
// had, and returns them once.
func (s *Service) GenerateRecoveryCodes(ctx context.Context, userID int64, n int) ([]string, error) {
	if n <= 0 || n > RecoveryCodesMax {
		return nil, ErrRecoverySetSize
	}
	codes := make([]string, 0, n)
	hashes := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		raw := make([]byte, recoveryCodeLen)
		if _, err := rand.Read(raw); err != nil {
			return nil, fmt.Errorf("minting a recovery code: %w", err)
		}
		code := crockfordEncode(raw)
		h := sha256.Sum256([]byte(code))
		codes = append(codes, code)
		hashes = append(hashes, h[:])
	}
	if err := s.store.ReplaceRecoveryCodes(ctx, userID, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

// UseRecoveryCode consumes a code, folding what was typed before hashing it,
// so a mistyped character still matches the minted code.
func (s *Service) UseRecoveryCode(ctx context.Context, userID int64, code string) (bool, error) {
	folded, err := crockfordFold(code)
	if err != nil {
		if errors.Is(err, ErrBadCrockford) {
			return false, nil
		}
		return false, err
	}
	h := sha256.Sum256([]byte(folded))
	return s.store.ConsumeRecoveryCode(ctx, userID, h[:])
}

// RecoveryCodesRemaining counts the codes still unused. Not a secret from the
// account's own owner, unlike the codes themselves.
func (s *Service) RecoveryCodesRemaining(ctx context.Context, userID int64) (int, error) {
	return s.store.CountRecoveryCodes(ctx, userID)
}
