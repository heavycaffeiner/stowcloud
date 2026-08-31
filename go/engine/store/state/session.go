package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Sessions. A row holds the SHA-256 of the token and never the token, so a
// read of this table is a list of who is signed in rather than a way to sign
// in as them.

// ErrNoSuchSession is a hash that holds no row.
var ErrNoSuchSession = errors.New("no such session")

// Session is one row. IDHash is the stored digest, which is what the owner's
// own session list is keyed by: a listing leak yields nothing usable.
type Session struct {
	IDHash     []byte
	User       int64
	CreatedNs  int64
	LastSeenNs int64
	AbsoluteNs int64
	IP         string
	UA         string
	AMR        int64
}

// CreateSession stores one session.
func (d *DB) CreateSession(ctx context.Context, s Session) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlInsertSession,
			s.IDHash, s.User, s.CreatedNs, s.LastSeenNs, s.AbsoluteNs,
			textArg(s.IP), textArg(s.UA), s.AMR)
		return err
	}); err != nil {
		return fmt.Errorf("storing a session: %w", err)
	}
	return nil
}

// SessionByHash reads one session.
func (d *DB) SessionByHash(ctx context.Context, hash []byte) (Session, error) {
	var (
		s      Session
		ip, ua sql.NullString
	)
	err := d.f.SQL().QueryRowContext(ctx, sqlSelectSession, hash).Scan(
		&s.IDHash, &s.User, &s.CreatedNs, &s.LastSeenNs, &s.AbsoluteNs, &ip, &ua, &s.AMR)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNoSuchSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("reading a session: %w", err)
	}
	s.IP, s.UA = ip.String, ua.String
	return s, nil
}

// SessionsOf lists one account's live rows, most recently used first.
func (d *DB) SessionsOf(ctx context.Context, user int64) (out []Session, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlListSessions, user)
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var (
			s      Session
			ip, ua sql.NullString
		)
		if serr := rows.Scan(&s.IDHash, &s.CreatedNs, &s.LastSeenNs, &s.AbsoluteNs,
			&ip, &ua, &s.AMR); serr != nil {
			return nil, fmt.Errorf("reading a session: %w", serr)
		}
		s.User = user
		s.IP, s.UA = ip.String, ua.String
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	return out, nil
}

// TouchSession stamps the last-seen column. A failure here costs a cold
// stamp that the next request refreshes, so the caller treats it as such.
func (d *DB) TouchSession(ctx context.Context, hash []byte, nowNs int64) error {
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlTouchSession, nowNs, hash)
		return err
	}); err != nil {
		return fmt.Errorf("stamping a session: %w", err)
	}
	return nil
}

// DeleteSession removes one session by its hash.
func (d *DB) DeleteSession(ctx context.Context, hash []byte) error {
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteSession, hash)
		return err
	}); err != nil {
		return fmt.Errorf("revoking a session: %w", err)
	}
	return nil
}

// DeleteSessionOfUser removes one of an account's own sessions. The predicate
// carries both the owner and the hash, so the ownership check and the delete
// cannot disagree.
func (d *DB) DeleteSessionOfUser(ctx context.Context, user int64, hash []byte) error {
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteSessionOfUser, user, hash)
		return err
	}); err != nil {
		return fmt.Errorf("revoking a session: %w", err)
	}
	return nil
}

// DeleteSessionsOf ends every session an account holds and reports how many.
// The count is what an administrator is told: the person is signed out
// everywhere, and saying how many places is the difference between an action
// that appears to have done nothing and one that clearly did.
func (d *DB) DeleteSessionsOf(ctx context.Context, user int64) (int64, error) {
	var n int64
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlDeleteSessionsOfUser, user)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	}); err != nil {
		return 0, fmt.Errorf("revoking sessions: %w", err)
	}
	return n, nil
}
