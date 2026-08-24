package auth

import (
	"context"
	"database/sql"
	"errors"

	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// roleAdmin is the stored value of the administrator role. It comes from the
// reference implementation's role column: 0 is a plain account, 1 is an
// administrator, and the first account is promoted at migration.
const roleAdmin = 1

// userRow is one account row as this package reads it.
type userRow struct {
	id         int64
	name       string
	display    string
	pwHash     string
	disabled   bool
	smbEnabled bool
	role       int64
}

// errUserMissing is an account that does not exist. Login maps it to the
// decoy-verify path; a caller that must not reveal existence treats it as a
// credential failure.
var errUserMissing = errors.New("no such account")

// userByName reads an account by its login name.
func (s *Service) userByName(ctx context.Context, name string) (userRow, error) {
	var u userRow
	err := s.st.SQL().QueryRowContext(ctx, sqlReadUserByName, name).
		Scan(&u.id, &u.name, &u.display, &u.pwHash, &u.disabled, &u.smbEnabled, &u.role)
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
		Scan(&u.id, &u.name, &u.display, &u.pwHash, &u.disabled, &u.smbEnabled, &u.role)
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
	return s.createUser(ctx, name, display, pw, 0)
}

// CreateAdmin makes the deployment's first administrator, the one thing the
// first-run bootstrap exists to do. It goes through the same hashing and the
// same write path as any account, and it is the only caller that sets the
// role column to roleAdmin.
func (s *Service) CreateAdmin(ctx context.Context, name, display string, pw secret.Secret) (int64, error) {
	return s.createUser(ctx, name, display, pw, roleAdmin)
}

func (s *Service) createUser(ctx context.Context, name, display string, pw secret.Secret, role int64) (int64, error) {
	hash, err := s.Hash(ctx, pw)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertUser, name, display, hash, role, 1, s.now())
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

// IsAdmin reports whether an account holds the administrator role. The admin
// surface reads it at the top of every route it guards.
func (s *Service) IsAdmin(ctx context.Context, userID int64) (bool, error) {
	u, err := s.userByID(ctx, userID)
	if err != nil {
		return false, err
	}
	return u.role == roleAdmin, nil
}

// HasAdmin reports whether an administrator exists. It is the setup gate's
// read: the gate closes permanently the moment one does, so a token recovered
// from a log or a backup after setup is worth nothing.
func (s *Service) HasAdmin(ctx context.Context) (bool, error) {
	var n int64
	err := s.st.SQL().QueryRowContext(ctx, sqlHasAdmin).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// CountUsers reports how many accounts exist, for the setup gate's "is the
// deployment fresh" question.
func (s *Service) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.st.SQL().QueryRowContext(ctx, sqlCountUsers).Scan(&n)
	return n, err
}

// SessionRow is one live session as the owner sees it. IDHash is the stored
// SHA-256 of the token, never the token itself, so a listing leak yields
// nothing that can be used.
type SessionRow struct {
	IDHash     []byte
	CreatedNs  int64
	LastSeenNs int64
	AbsoluteNs int64
	IP, UA     string
	AMR        int
}

// Sessions lists the caller's own live sessions, newest use first.
func (s *Service) Sessions(ctx context.Context, userID int64) ([]SessionRow, error) {
	rows, err := s.st.SQL().QueryContext(ctx, sqlListSessions, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // rows.Close on a fully read set reports nothing.
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(&r.IDHash, &r.CreatedNs, &r.LastSeenNs, &r.AbsoluteNs, &r.IP, &r.UA, &r.AMR); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RevokeSessionByHash destroys one of the caller's sessions, identified by
// the stored hash. It is the "sign out this device" path, distinct from
// RevokeSession, which is the caller's own cookie.
func (s *Service) RevokeSessionByHash(ctx context.Context, userID int64, hash []byte) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteUserSession, userID, hash)
		return err
	})
}

// AppPasswordRow is one app password as its owner sees it. ScopeShares is
// decoded; the token itself is never stored and therefore never listed.
type AppPasswordRow struct {
	ID         int64
	Name       string
	ScopePerms uint16
	Shares     []string
	CreatedNs  int64
	ExpiresNs  *int64
	LastUsedNs *int64
}

// AppPasswords lists the caller's own app passwords, newest first.
func (s *Service) AppPasswords(ctx context.Context, userID int64) ([]AppPasswordRow, error) {
	rows, err := s.st.SQL().QueryContext(ctx, sqlListAppPasswords, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // rows.Close on a fully read set reports nothing.
	var out []AppPasswordRow
	for rows.Next() {
		var r AppPasswordRow
		var shares []byte
		var expires, last any
		if err := rows.Scan(&r.ID, &r.Name, &r.ScopePerms, &shares, &r.CreatedNs, &expires, &last); err != nil {
			return nil, err
		}
		if e, ok := expires.(int64); ok {
			r.ExpiresNs = &e
		}
		if l, ok := last.(int64); ok {
			r.LastUsedNs = &l
		}
		for _, part := range splitShareScope(shares) {
			r.Shares = append(r.Shares, string(part))
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
