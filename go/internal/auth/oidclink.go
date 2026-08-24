package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The stored link between a provider identity and an account here.
//
// Link-only is the position: the provider authenticates and never creates an
// account, so authority over who has one stays in this database. That is what
// makes a revocation here total.
//
// The identity is the issuer and the subject together, never the address. A
// provider may reassign an address to a different person, and matching on one
// would hand that person the account.

// ErrNoOIDCLink means the account has no link, or the identity is not linked to
// any account.
var ErrNoOIDCLink = errors.New("auth: no single-sign-on link")

// ErrOIDCLinkTaken means the identity is already linked to a different account.
var ErrOIDCLinkTaken = errors.New("auth: that identity is already linked to another account")

const (
	sqlInsertOIDCLink = `
INSERT INTO oidc_link(issuer, subject, user, linked_ns) VALUES (?, ?, ?, ?)`

	sqlReadOIDCLinkByUser = `
SELECT issuer, subject, linked_ns, last_login_ns FROM oidc_link WHERE user = ?`

	sqlReadOIDCLinkByIdentity = `
SELECT user FROM oidc_link WHERE issuer = ? AND subject = ?`

	sqlDeleteOIDCLinkByUser = `DELETE FROM oidc_link WHERE user = ?`

	sqlTouchOIDCLink = `
UPDATE oidc_link SET last_login_ns = ? WHERE issuer = ? AND subject = ?`
)

// OIDCLink is one account's link.
type OIDCLink struct {
	Issuer   string
	Subject  string
	LinkedNs int64
	// LastLoginNs is nil when the link has never been used to sign in, which
	// is what a link created and never exercised looks like.
	LastLoginNs *int64
}

// OIDCLinkOf reads an account's link.
func (s *Service) OIDCLinkOf(ctx context.Context, userID int64) (OIDCLink, error) {
	var (
		link OIDCLink
		last sql.NullInt64
	)
	row := s.st.SQL().QueryRowContext(ctx, sqlReadOIDCLinkByUser, userID)
	switch err := row.Scan(&link.Issuer, &link.Subject, &link.LinkedNs, &last); {
	case errors.Is(err, sql.ErrNoRows):
		return OIDCLink{}, ErrNoOIDCLink
	case err != nil:
		return OIDCLink{}, fmt.Errorf("reading the single-sign-on link: %w", err)
	}
	if last.Valid {
		link.LastLoginNs = &last.Int64
	}
	return link, nil
}

// UserForOIDCIdentity resolves a provider identity to the account it is linked
// to.
//
// Not-found is its own answer rather than an error the caller has to read a
// message out of: an unlinked identity is an ordinary outcome, because this
// server never creates an account from one.
func (s *Service) UserForOIDCIdentity(ctx context.Context, issuer, subject string) (int64, error) {
	var id int64
	row := s.st.SQL().QueryRowContext(ctx, sqlReadOIDCLinkByIdentity, issuer, subject)
	switch err := row.Scan(&id); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, ErrNoOIDCLink
	case err != nil:
		return 0, fmt.Errorf("resolving the single-sign-on identity: %w", err)
	}
	return id, nil
}

// CreateOIDCLink attaches an identity to an account.
//
// It replaces whatever the account had, so re-linking after a provider
// migration works without a separate unlink. It refuses when the identity
// already belongs to somebody else, because taking it would move an account's
// only way in to a different person.
//
// The password credential is dropped as part of the same act: leaving it means
// the account still answers to the password the provider was just put in front
// of, which is the factor being replaced.
func (s *Service) CreateOIDCLink(ctx context.Context, userID int64, issuer, subject string) error {
	if issuer == "" || subject == "" {
		return errors.New("auth: a single-sign-on link needs both an issuer and a subject")
	}

	owner, err := s.UserForOIDCIdentity(ctx, issuer, subject)
	switch {
	case err == nil && owner != userID:
		return ErrOIDCLinkTaken
	case err != nil && !errors.Is(err, ErrNoOIDCLink):
		return err
	}

	now := s.clk.Nanos()
	if werr := s.write(ctx, func(tx *sql.Tx) error {
		// Replaced rather than updated in place: the identity is the primary
		// key, so a changed subject is a different row and an update would
		// leave the old one linked.
		if _, derr := tx.ExecContext(ctx, sqlDeleteOIDCLinkByUser, userID); derr != nil {
			return derr
		}
		_, ierr := tx.ExecContext(ctx, sqlInsertOIDCLink, issuer, subject, userID, now)
		return ierr
	}); werr != nil {
		return fmt.Errorf("storing the single-sign-on link: %w", werr)
	}

	// The account's SMB credential goes with it, for the same reason: it is
	// derived from the password this link replaces.
	return s.LinkOIDC(ctx, userID)
}

// RemoveOIDCLink detaches an identity from an account.
//
// The caller restores local password login afterwards. Removing the link alone
// would leave an account with no way in at all.
func (s *Service) RemoveOIDCLink(ctx context.Context, userID int64) error {
	if werr := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteOIDCLinkByUser, userID)
		return err
	}); werr != nil {
		return fmt.Errorf("removing the single-sign-on link: %w", werr)
	}
	return nil
}

// RevokeSessionsOf ends every session an account holds and reports how many.
//
// The count is what an administrator removing a link is told: the person is
// signed out everywhere, and saying how many places is the difference between
// an action that appears to have done nothing and one that clearly did.
func (s *Service) RevokeSessionsOf(ctx context.Context, userID int64) (int64, error) {
	var n int64
	if err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlDeleteUserSessions, userID)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	}); err != nil {
		return 0, fmt.Errorf("revoking the account's sessions: %w", err)
	}
	s.bumpGeneration()
	return n, nil
}

// TouchOIDCLink records that an identity was just used to sign in.
//
// Best-effort by contract: the sign-in already succeeded, and failing it
// because a bookkeeping column would not update would refuse a login for a
// reason nobody can act on.
func (s *Service) TouchOIDCLink(ctx context.Context, issuer, subject string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlTouchOIDCLink, s.clk.Nanos(), issuer, subject)
		return err
	})
}
