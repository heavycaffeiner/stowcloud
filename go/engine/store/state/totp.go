package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// The second factor's durable half: the sealed secret and the replay guard.
//
// The guard is a table rather than memory because the window it covers
// outlives a restart by design: a code captured in transit is replayable for
// up to ninety seconds, and a process that forgot which steps it accepted
// would accept them again.

// ErrNoTOTPSecret is an account with no second factor enrolled.
var ErrNoTOTPSecret = errors.New("no second factor is enrolled")

// TOTPSecret is the sealed secret and the key version that sealed it.
type TOTPSecret struct {
	Ciphertext []byte
	KeyVer     uint32
}

// EnrollTOTP stores the sealed secret and drops the account's SMB credential
// in the same transaction.
//
// The drop is the point rather than tidiness: leaving the NT hash means the
// account password keeps working over SMB, which is exactly the factor the
// person just added being bypassed by the older protocol.
func (d *DB) EnrollTOTP(ctx context.Context, user int64, s TOTPSecret, nowNs int64) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		if _, uerr := tx.ExecContext(ctx, sqlUpsertTOTP,
			user, s.Ciphertext, int64(s.KeyVer), nowNs); uerr != nil {
			return uerr
		}
		_, derr := tx.ExecContext(ctx, sqlDeleteSMBSecret, user)
		return derr
	}); err != nil {
		return fmt.Errorf("enrolling a second factor: %w", err)
	}
	return nil
}

// TOTPSecretOf reads the sealed secret.
func (d *DB) TOTPSecretOf(ctx context.Context, user int64) (TOTPSecret, error) {
	var (
		s   TOTPSecret
		ver int64
	)
	err := d.f.SQL().QueryRowContext(ctx, sqlSelectTOTP, user).Scan(&s.Ciphertext, &ver)
	if errors.Is(err, sql.ErrNoRows) {
		return TOTPSecret{}, ErrNoTOTPSecret
	}
	if err != nil {
		return TOTPSecret{}, fmt.Errorf("reading a second factor: %w", err)
	}
	v, nerr := num.Narrow[uint32](ver)
	if nerr != nil {
		return TOTPSecret{}, fmt.Errorf(
			"the second factor of account %d carries key version %d: %w", user, ver, nerr)
	}
	s.KeyVer = v
	return s, nil
}

// DisableTOTP removes the secret and the replay window together. Leaving the
// window behind would refuse the steps it holds after a re-enrolment with a
// different secret, for no benefit.
func (d *DB) DisableTOTP(ctx context.Context, user int64) error {
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		if _, derr := tx.ExecContext(ctx, sqlDeleteTOTP, user); derr != nil {
			return derr
		}
		_, rerr := tx.ExecContext(ctx, sqlDeleteTOTPUsedAll, user)
		return rerr
	}); err != nil {
		return fmt.Errorf("disabling a second factor: %w", err)
	}
	return nil
}

// ClaimTOTPStep records an accepted step and prunes what the window has left
// behind, in one transaction. It reports whether this caller was the one that
// claimed the step: a code presented twice inside its window finds the row
// already there and is refused, including when the two presentations race.
func (d *DB) ClaimTOTPStep(ctx context.Context, user, step, oldestKept, nowNs int64) (bool, error) {
	if err := d.f.EnsureWritable(); err != nil {
		return false, err
	}
	var claimed bool
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertTOTPUsed, user, step, nowNs)
		if ierr != nil {
			return ierr
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return rerr
		}
		claimed = n == 1
		_, perr := tx.ExecContext(ctx, sqlDeleteTOTPUsedBefore, user, oldestKept)
		return perr
	}); err != nil {
		return false, fmt.Errorf("recording a second-factor step: %w", err)
	}
	return claimed, nil
}
