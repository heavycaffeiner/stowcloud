package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Single sign-on's durable half: the flows in progress and the identity
// links. The protocol half talks to the internet and holds no state; this
// holds state and talks to nobody.
//
// What rests as a digest and what rests whole is the design. The state and
// the browser binding go to the browser, so storing them whole would make a
// read of this table enough to complete somebody else's link, and what the
// callback checks is equality. The nonce and the code verifier rest whole
// because both have to be handed back out, and neither authenticates
// anything on its own.

// The refusals this aggregate answers with.
var (
	// ErrNoOIDCFlow is a state that names no live flow. An unknown state and
	// an expired one are one answer: distinguishing them would say whether a
	// state value was ever real.
	ErrNoOIDCFlow = errors.New("no such single-sign-on flow")

	// ErrNoOIDCLink is an account with no link, or an identity linked to no
	// account.
	ErrNoOIDCLink = errors.New("no single-sign-on link")

	// ErrOIDCLinkTaken is an identity already linked to a different account.
	// It is typed because the screen offers "sign in there and unlink" as the
	// fix and has to know that is the case.
	ErrOIDCLinkTaken = errors.New("that identity is already linked to another account")
)

// OIDCFlow is one link in progress, as it rests.
type OIDCFlow struct {
	User          int64
	Nonce         string
	BindingDigest []byte
	CodeVerifier  string
	RedirectURI   string
	ReturnTo      string
	CreatedNs     int64
}

// NewOIDCFlow is what starting one needs. Both digests are computed by the
// caller, which is the layer that holds the plaintext values.
type NewOIDCFlow struct {
	StateDigest   []byte
	User          int64
	Nonce         string
	BindingDigest []byte
	CodeVerifier  string
	RedirectURI   string
	ReturnTo      string
	CreatedNs     int64
}

// StartOIDCFlow records a flow and sweeps the expired ones in the same
// transaction. Sweeping here rather than on a timer means a deployment nobody
// links on accumulates nothing, and there is no timer to forget.
func (d *DB) StartOIDCFlow(ctx context.Context, in NewOIDCFlow, cutoffNs int64) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		if _, serr := tx.ExecContext(ctx, sqlSweepOIDCFlows, cutoffNs); serr != nil {
			return serr
		}
		_, ierr := tx.ExecContext(ctx, sqlInsertOIDCFlow,
			in.StateDigest, in.User, in.Nonce, in.BindingDigest,
			in.CodeVerifier, in.RedirectURI, in.ReturnTo, in.CreatedNs)
		return ierr
	}); err != nil {
		return fmt.Errorf("recording a single-sign-on flow: %w", err)
	}
	return nil
}

// TakeOIDCFlow reads a flow and deletes it in one transaction, whatever the
// caller then decides about the binding.
//
// Consumed rather than read: a flow that can be redeemed twice is a code that
// can be replayed. The binding comparison is the caller's, because comparing
// in constant time is a property of the comparison rather than of the row,
// and a flow whose binding failed is still not one to leave redeemable.
func (d *DB) TakeOIDCFlow(ctx context.Context, stateDigest []byte, cutoffNs int64) (OIDCFlow, error) {
	var f OIDCFlow
	err := d.Write(ctx, func(tx *sql.Tx) error {
		serr := tx.QueryRowContext(ctx, sqlSelectOIDCFlow, stateDigest, cutoffNs).Scan(
			&f.User, &f.Nonce, &f.BindingDigest, &f.CodeVerifier,
			&f.RedirectURI, &f.ReturnTo, &f.CreatedNs)
		if errors.Is(serr, sql.ErrNoRows) {
			// Still deleted: an expired row found here is one nobody will
			// redeem, and leaving it costs a sweep later.
			if _, derr := tx.ExecContext(ctx, sqlDeleteOIDCFlow, stateDigest); derr != nil {
				return derr
			}
			return ErrNoOIDCFlow
		}
		if serr != nil {
			return serr
		}
		_, derr := tx.ExecContext(ctx, sqlDeleteOIDCFlow, stateDigest)
		return derr
	})
	if errors.Is(err, ErrNoOIDCFlow) {
		return OIDCFlow{}, err
	}
	if err != nil {
		return OIDCFlow{}, fmt.Errorf("consuming a single-sign-on flow: %w", err)
	}
	return f, nil
}

// OIDCLink is one account's link.
type OIDCLink struct {
	Issuer   string
	Subject  string
	LinkedNs int64
	// LastLoginNs is nil for a link that exists and was never used to sign
	// in, which the screen shows differently.
	LastLoginNs *int64
}

// OIDCLinkOf reads an account's link.
func (d *DB) OIDCLinkOf(ctx context.Context, user int64) (OIDCLink, error) {
	var (
		l    OIDCLink
		last sql.NullInt64
	)
	err := d.f.SQL().QueryRowContext(ctx, sqlSelectOIDCLinkByUser, user).
		Scan(&l.Issuer, &l.Subject, &l.LinkedNs, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return OIDCLink{}, ErrNoOIDCLink
	}
	if err != nil {
		return OIDCLink{}, fmt.Errorf("reading a single-sign-on link: %w", err)
	}
	if last.Valid {
		v := last.Int64
		l.LastLoginNs = &v
	}
	return l, nil
}

// UserForOIDCIdentity resolves a provider identity to the account it names.
// The identity is the issuer and the subject together, never an address: a
// provider may reassign an address to a different person, and matching on one
// would hand that person the account.
func (d *DB) UserForOIDCIdentity(ctx context.Context, issuer, subject string) (int64, error) {
	var id int64
	err := d.f.SQL().QueryRowContext(ctx, sqlSelectOIDCLinkByIdentity, issuer, subject).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoOIDCLink
	}
	if err != nil {
		return 0, fmt.Errorf("resolving a single-sign-on identity: %w", err)
	}
	return id, nil
}

// CreateOIDCLink attaches an identity to an account, replacing whatever that
// account had, and refuses an identity that belongs to somebody else.
//
// The ownership check runs inside the write transaction rather than before
// it, so two accounts claiming one identity at the same time cannot both pass
// a check made before either wrote.
func (d *DB) CreateOIDCLink(ctx context.Context, user int64, issuer, subject string, nowNs int64) error {
	if issuer == "" || subject == "" {
		return errors.New("a single-sign-on link needs both an issuer and a subject")
	}
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	err := d.Write(ctx, func(tx *sql.Tx) error {
		var owner int64
		switch serr := tx.QueryRowContext(ctx, sqlSelectOIDCLinkByIdentity, issuer, subject).
			Scan(&owner); {
		case serr == nil && owner != user:
			return ErrOIDCLinkTaken
		case serr != nil && !errors.Is(serr, sql.ErrNoRows):
			return serr
		}
		// Replaced rather than updated in place: the identity is the primary
		// key, so a changed subject is a different row and an update would
		// leave the old one linked.
		if _, derr := tx.ExecContext(ctx, sqlDeleteOIDCLinkByUser, user); derr != nil {
			return derr
		}
		_, ierr := tx.ExecContext(ctx, sqlInsertOIDCLink, issuer, subject, user, nowNs)
		return ierr
	})
	if errors.Is(err, ErrOIDCLinkTaken) {
		return err
	}
	if err != nil {
		return fmt.Errorf("storing a single-sign-on link: %w", err)
	}
	return nil
}

// DeleteOIDCLink detaches an identity from an account.
func (d *DB) DeleteOIDCLink(ctx context.Context, user int64) error {
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteOIDCLinkByUser, user)
		return err
	}); err != nil {
		return fmt.Errorf("removing a single-sign-on link: %w", err)
	}
	return nil
}

// TouchOIDCLink stamps a link as just used to sign in. Best effort by
// contract: the sign-in already succeeded, and failing it because a
// bookkeeping column would not update refuses a login for a reason nobody can
// act on.
func (d *DB) TouchOIDCLink(ctx context.Context, issuer, subject string, nowNs int64) error {
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlTouchOIDCLink, nowNs, issuer, subject)
		return err
	}); err != nil {
		return fmt.Errorf("stamping a single-sign-on link: %w", err)
	}
	return nil
}
