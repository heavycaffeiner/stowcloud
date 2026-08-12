package auth

import (
	"context"
	"database/sql"
	"errors"

	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// userRow is one account row as this package reads it.
type userRow struct {
	id         int64
	name       string
	display    string
	pwHash     string
	disabled   bool
	smbEnabled bool
}

// errUserMissing is an account that does not exist. Login maps it to the
// decoy-verify path; a caller that must not reveal existence treats it as a
// credential failure.
var errUserMissing = errors.New("no such account")

// userByName reads an account by its login name.
func (s *Service) userByName(ctx context.Context, name string) (userRow, error) {
	var u userRow
	err := s.st.SQL().QueryRowContext(ctx, sqlReadUserByName, name).
		Scan(&u.id, &u.name, &u.display, &u.pwHash, &u.disabled, &u.smbEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return userRow{}, errUserMissing
	}
	if err != nil {
		return userRow{}, err
	}
	return u, nil
}

// userByID reads an account by its id.
func (s *Service) userByID(ctx context.Context, id int64) (userRow, error) {
	var u userRow
	err := s.st.SQL().QueryRowContext(ctx, sqlReadUserByID, id).
		Scan(&u.id, &u.name, &u.display, &u.pwHash, &u.disabled, &u.smbEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return userRow{}, errUserMissing
	}
	if err != nil {
		return userRow{}, err
	}
	return u, nil
}

// CreateUser makes an account with a hashed password and SMB enabled.
func (s *Service) CreateUser(ctx context.Context, name, display string, pw secret.Secret) (int64, error) {
	hash, err := s.Hash(ctx, pw)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertUser, name, display, hash, 1, s.now())
		if ierr != nil {
			return ierr
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// SetPassword hashes a new password under the gate, persists it, re-seals the
// SMB NT hash it derives, invalidates every cached credential and republishes
// the SMB passdb. It is one of the six paths a credential change has to reach
// on every surface.
func (s *Service) SetPassword(ctx context.Context, userID int64, newPW secret.Secret) error {
	hash, err := s.Hash(ctx, newPW)
	if err != nil {
		return err
	}
	active, activeVer := s.mk.Active()
	err = s.write(ctx, func(tx *sql.Tx) error {
		if _, uerr := tx.ExecContext(ctx, sqlUpdatePassword, hash, userID); uerr != nil {
			return uerr
		}
		return s.sealAndStoreNT(ctx, tx, userID, newPW, active, activeVer)
	})
	if err != nil {
		return err
	}
	s.bumpGeneration()
	return s.republishPassdb(ctx)
}

// DisableAccount disables an account, killing its sessions and cached
// credentials in one generation bump and taking it out of the SMB passdb.
func (s *Service) DisableAccount(ctx context.Context, userID int64) error {
	return s.setAccountDisabled(ctx, userID, true)
}

// EnableAccount restores a disabled account and puts it back in the passdb.
func (s *Service) EnableAccount(ctx context.Context, userID int64) error {
	return s.setAccountDisabled(ctx, userID, false)
}

func (s *Service) setAccountDisabled(ctx context.Context, userID int64, disabled bool) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlUpdateDisabled, disabled, userID); err != nil {
			return err
		}
		if !disabled {
			return nil
		}
		_, err := tx.ExecContext(ctx, sqlDeleteUserSessions, userID)
		return err
	})
	if err != nil {
		return err
	}
	s.bumpGeneration()
	return s.republishPassdb(ctx)
}

// SetSMBAccess turns an account's SMB access on or off, which is the "set SMB
// settings" path the passdb sink has to reach.
func (s *Service) SetSMBAccess(ctx context.Context, userID int64, enabled bool) error {
	return s.setSMBEnabled(ctx, userID, enabled)
}

// LinkOIDC may disable local password login for an account, which is one of
// the six passdb paths. The OIDC flow itself arrives in Phase 11; this phase
// owns the sink that path calls.
func (s *Service) LinkOIDC(ctx context.Context, userID int64) error {
	return s.setSMBEnabled(ctx, userID, false)
}

// UnlinkOIDC restores local password login after an OIDC link is removed,
// which is another of the six passdb paths.
func (s *Service) UnlinkOIDC(ctx context.Context, userID int64) error {
	return s.setSMBEnabled(ctx, userID, true)
}

func (s *Service) setSMBEnabled(ctx context.Context, userID int64, enabled bool) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlUpdateSMBEnabled, enabled, userID)
		return err
	})
	if err != nil {
		return err
	}
	s.bumpGeneration()
	return s.republishPassdb(ctx)
}
