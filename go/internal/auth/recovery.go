package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
)

// Recovery codes are single-use, stored hashed, and consumed in the same
// transaction that accepts them, so a concurrent second use finds nothing.
// They are minted in Crockford Base32 so a code read off a screen survives
// being typed into a phone.

// GenerateRecoveryCodes mints n fresh codes for an account, replacing any it
// had, and returns them once.
func (s *Service) GenerateRecoveryCodes(ctx context.Context, userID int64, n int) ([]string, error) {
	if n <= 0 || n > 64 {
		return nil, errors.New("a recovery-code set must hold between 1 and 64 codes")
	}
	codes := make([]string, 0, n)
	hashes := make([][32]byte, 0, n)
	for i := 0; i < n; i++ {
		raw := make([]byte, recoverableCodeLen)
		if _, err := rand.Read(raw); err != nil {
			return nil, fmt.Errorf("minting a recovery code: %w", err)
		}
		code := crockfordEncode(raw)
		codes = append(codes, code)
		h := sha256.Sum256([]byte(code))
		hashes = append(hashes, h)
	}

	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlDeleteAllRecovery, userID); err != nil {
			return err
		}
		for _, h := range hashes {
			if _, err := tx.ExecContext(ctx, sqlInsertRecoveryCode, userID, h[:]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// UseRecoveryCode consumes a code, folding what was typed before hashing so a
// mistyped character still matches the minted code. The update is conditional
// on the code being unused, so a concurrent second use finds nothing.
func (s *Service) UseRecoveryCode(ctx context.Context, userID int64, code string) (bool, error) {
	folded, err := crockfordFold(code)
	if err != nil {
		return false, ErrCredentials
	}
	h := sha256.Sum256([]byte(folded))
	var used bool
	err = s.write(ctx, func(tx *sql.Tx) error {
		res, rerr := tx.ExecContext(ctx, sqlConsumeRecoveryCode, s.now(), userID, h[:])
		if rerr != nil {
			return rerr
		}
		n, raerr := res.RowsAffected()
		if raerr != nil {
			return raerr
		}
		used = n == 1
		return nil
	})
	if err != nil {
		return false, err
	}
	return used, nil
}
