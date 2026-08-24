package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// The checks a self-service screen needs before it changes a credential.
//
// They live here rather than in the HTTP layer because they read a password
// hash, and a hash that leaves this package is one every other package can be
// careless with.

// VerifyAccountPassword reports whether pw is the account's current password.
//
// It is the check every credential-changing screen runs first. A live session
// is not enough on its own: a session is what somebody who walked past an
// unlocked screen already has, and the credentials these screens create
// outlive the session that created them.
//
// An account that does not exist is a failed check rather than an error, so a
// caller cannot tell the two apart, and the cost is paid either way.
func (s *Service) VerifyAccountPassword(ctx context.Context, userID int64, pw secret.Secret) (bool, error) {
	u, err := s.userByID(ctx, userID)
	if errors.Is(err, errUserMissing) {
		decoy, derr := s.decoyPHC(ctx)
		if derr != nil {
			return false, derr
		}
		// Verified and discarded, so a missing account costs what a real one
		// costs and cannot be found by timing the answer.
		s.Verify(ctx, decoy, pw) //nolint:errcheck // the answer is discarded by design; only the cost is wanted.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ok, _, err := s.Verify(ctx, u.pwHash, pw)
	return ok, err
}

// NameOf is the account's login name, which the second-factor screen needs to
// label the entry an authenticator app stores.
func (s *Service) NameOf(ctx context.Context, userID int64) (string, error) {
	u, err := s.userByID(ctx, userID)
	if err != nil {
		return "", err
	}
	return u.name, nil
}

// RecoveryCodesRemaining counts the codes still unused.
//
// Not a secret from the account's own owner, unlike the codes themselves,
// which are returned once at the moment they are minted and never again.
func (s *Service) RecoveryCodesRemaining(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.st.SQL().QueryRowContext(ctx, sqlCountUnusedRecoveryCodes, userID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// RequestWipe asks the device holding an app password to erase its local copy.
//
// Distinct from revoking, which stops the credential here. This is a message
// the device only receives when it next connects, so it is a request rather
// than a guarantee, and the credential is revoked as well: a device that never
// comes back must not keep working.
func (s *Service) RequestWipe(ctx context.Context, userID, id int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlRequestWipe, userID, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			// Somebody else's credential and one that never existed answer
			// identically, so an id cannot be probed for.
			return ErrCredentials
		}
		return nil
	})
}

// SetSMBPassword stores a credential for the file-sharing protocol that is not
// the account password.
//
// The point of a separate one is that the account password stops being usable
// over a protocol whose authentication cannot be made as strong, without the
// account losing access to it.
func (s *Service) SetSMBPassword(ctx context.Context, userID int64, pw secret.Secret) error {
	active, activeVer := s.mk.Active()
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, uerr := tx.ExecContext(ctx, sqlClearSMBOptOut, userID); uerr != nil {
			return uerr
		}
		return s.sealAndStoreNT(ctx, tx, userID, pw, active, activeVer)
	})
	if err != nil {
		return err
	}
	s.bumpGeneration()
	return s.republishPassdb(ctx)
}

// ClearSMBPassword removes a separate SMB password, and reports whether the
// account password takes over.
//
// It does not for an account that is enrolled in a second factor, linked to a
// provider, or opted out: for those, the separate password was the only thing
// making SMB work, and clearing it means losing SMB access altogether. The
// caller says so rather than reporting a success that reads as "nothing
// changed".
func (s *Service) ClearSMBPassword(ctx context.Context, userID int64) (revertible bool, err error) {
	var optOut, has2fa, linked bool
	row := s.st.SQL().QueryRowContext(ctx, sqlSMBRevertible, userID)
	if serr := row.Scan(&optOut, &has2fa, &linked); serr != nil {
		return false, fmt.Errorf("reading the account's SMB state: %w", serr)
	}

	if werr := s.write(ctx, func(tx *sql.Tx) error {
		_, derr := tx.ExecContext(ctx, sqlDeleteSMBSecret, userID)
		return derr
	}); werr != nil {
		return false, fmt.Errorf("clearing the SMB password: %w", werr)
	}

	s.bumpGeneration()
	// Republished either way: the credential is gone from the database, and a
	// rendered file still carrying it is a revoked password that still works.
	if perr := s.republishPassdb(ctx); perr != nil {
		return false, perr
	}

	blocked := has2fa && s.smbTOTPPolicy == TOTPBlock
	return !optOut && !linked && !blocked, nil
}

// UserIDByName resolves a login name to an account id.
//
// Exported for the sign-in flow's second step, which has to name the account
// whose password was just accepted: the call that accepted it deliberately
// returns nothing about who it was refusing.
func (s *Service) UserIDByName(ctx context.Context, name string) (int64, error) {
	u, err := s.userByName(ctx, name)
	if err != nil {
		return 0, err
	}
	return u.id, nil
}
