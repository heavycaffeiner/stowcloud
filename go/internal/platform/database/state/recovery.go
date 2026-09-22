package state

import (
	"context"
	"database/sql"
	"fmt"
)

// Recovery codes: single-use, stored hashed, consumed in the transaction that
// accepts them.

// ReplaceRecoveryCodes replaces the whole set an account holds. Generation is
// all-or-nothing: a person who was shown a list of codes must hold exactly
// that list, not a mixture of it and the previous one.
func (d *DB) ReplaceRecoveryCodes(ctx context.Context, user int64, hashes [][]byte) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		if _, derr := tx.ExecContext(ctx, sqlDeleteRecoveryCodes, user); derr != nil {
			return derr
		}
		for _, h := range hashes {
			if _, ierr := tx.ExecContext(ctx, sqlInsertRecoveryCode, user, h); ierr != nil {
				return ierr
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("storing recovery codes: %w", err)
	}
	return nil
}

// ConsumeRecoveryCode accepts one code and reports whether it was still
// unused. The delete is the acceptance: a concurrent second use of one code
// finds nothing to delete and is refused, with no read-then-write window
// between the check and the consumption.
func (d *DB) ConsumeRecoveryCode(ctx context.Context, user int64, hash []byte) (bool, error) {
	var used bool
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		res, derr := tx.ExecContext(ctx, sqlConsumeRecoveryCode, user, hash)
		if derr != nil {
			return derr
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return rerr
		}
		used = n == 1
		return nil
	}); err != nil {
		return false, fmt.Errorf("consuming a recovery code: %w", err)
	}
	return used, nil
}

// CountRecoveryCodes is how many an account has left. Not a secret from its
// own owner, unlike the codes themselves, which are shown once.
func (d *DB) CountRecoveryCodes(ctx context.Context, user int64) (int, error) {
	var n int
	if err := d.f.SQL().QueryRowContext(ctx, sqlCountRecoveryCodes, user).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting recovery codes: %w", err)
	}
	return n, nil
}
