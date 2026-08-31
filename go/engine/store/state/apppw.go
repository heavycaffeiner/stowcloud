package state

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// App passwords: the long-lived credentials a client stores. A row holds the
// SHA-256 of the token and the scope it may reach, never the token.
//
// The scope's share list is stored NUL-separated in one column rather than in
// a side table. It is read whole on every verification and written whole on
// every mint, so a second table would be a join for a value that has no
// meaning apart from its row.

// ErrNoSuchAppPassword is a token hash or an id that holds no row.
var ErrNoSuchAppPassword = errors.New("no such app password")

// AppPassword is one row. Token is absent by construction.
type AppPassword struct {
	ID         int64
	User       int64
	Name       string
	ScopePerms uint16
	Shares     []string
	CreatedNs  int64
	ExpiresNs  *int64
	LastUsedNs *int64
	WipeWanted bool
}

// NewAppPassword is what minting one needs.
type NewAppPassword struct {
	TokenHash  []byte
	User       int64
	Name       string
	ScopePerms uint16
	Shares     []string
	CreatedNs  int64
	ExpiresNs  *int64
}

// CreateAppPassword stores one and returns its id, which is what a failed
// delivery revokes without holding the plaintext.
func (d *DB) CreateAppPassword(ctx context.Context, in NewAppPassword) (int64, error) {
	if err := d.f.EnsureWritable(); err != nil {
		return 0, err
	}
	var id int64
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertAppPassword,
			in.TokenHash, in.User, in.Name, int64(in.ScopePerms),
			joinShareScope(in.Shares), in.CreatedNs, idArg(in.ExpiresNs))
		if ierr != nil {
			return ierr
		}
		var rerr error
		id, rerr = res.LastInsertId()
		return rerr
	}); err != nil {
		return 0, fmt.Errorf("storing an app password: %w", err)
	}
	return id, nil
}

// AppPasswordByHash reads one by the digest of a presented token.
func (d *DB) AppPasswordByHash(ctx context.Context, hash []byte) (AppPassword, error) {
	row := d.f.SQL().QueryRowContext(ctx, sqlSelectAppPasswordByHash, hash)
	a, err := scanAppPassword(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return AppPassword{}, ErrNoSuchAppPassword
	}
	if err != nil {
		return AppPassword{}, fmt.Errorf("reading an app password: %w", err)
	}
	return a, nil
}

// AppPasswordsOf lists an account's own, newest first.
func (d *DB) AppPasswordsOf(ctx context.Context, user int64) (out []AppPassword, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlListAppPasswords, user)
	if err != nil {
		return nil, fmt.Errorf("listing app passwords: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		a, serr := scanAppPassword(rows, false)
		if serr != nil {
			return nil, fmt.Errorf("reading an app password: %w", serr)
		}
		a.User = user
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing app passwords: %w", err)
	}
	return out, nil
}

// scanAppPassword reads one row. withUser distinguishes the verification
// read, which needs the owner, from the owner's own listing, which already
// knows it.
func scanAppPassword(row interface{ Scan(...any) error }, withUser bool) (AppPassword, error) {
	var (
		a                 AppPassword
		perms             int64
		shares            []byte
		expires, lastUsed sql.NullInt64
		err               error
	)
	if withUser {
		err = row.Scan(&a.ID, &a.User, &a.Name, &perms, &shares,
			&a.CreatedNs, &expires, &lastUsed, &a.WipeWanted)
	} else {
		err = row.Scan(&a.ID, &a.Name, &perms, &shares,
			&a.CreatedNs, &expires, &lastUsed, &a.WipeWanted)
	}
	if err != nil {
		return AppPassword{}, err
	}
	p, nerr := num.Narrow[uint16](perms)
	if nerr != nil {
		return AppPassword{}, fmt.Errorf("app password %d carries scope bits %d: %w", a.ID, perms, nerr)
	}
	a.ScopePerms = p
	a.Shares = splitShareScope(shares)
	if expires.Valid {
		e := expires.Int64
		a.ExpiresNs = &e
	}
	if lastUsed.Valid {
		l := lastUsed.Int64
		a.LastUsedNs = &l
	}
	return a, nil
}

// DeleteAppPassword revokes one, scoped to its owner in the same statement.
func (d *DB) DeleteAppPassword(ctx context.Context, user, id int64) error {
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, derr := tx.ExecContext(ctx, sqlDeleteAppPassword, id, user)
		if derr != nil {
			return derr
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return rerr
		}
		if n == 0 {
			return ErrNoSuchAppPassword
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNoSuchAppPassword) {
		return fmt.Errorf("revoking an app password: %w", err)
	}
	return err
}

// RequestAppPasswordWipe marks the credential and revokes it in one
// statement. A device that never reconnects to hear the request must not keep
// working, so the expiry is moved to the epoch rather than left alone.
func (d *DB) RequestAppPasswordWipe(ctx context.Context, user, id int64) error {
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, uerr := tx.ExecContext(ctx, sqlRequestAppPasswordWipe, user, id)
		if uerr != nil {
			return uerr
		}
		n, rerr := res.RowsAffected()
		if rerr != nil {
			return rerr
		}
		if n == 0 {
			return ErrNoSuchAppPassword
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNoSuchAppPassword) {
		return fmt.Errorf("requesting a wipe: %w", err)
	}
	return err
}

// TouchAppPassword records that a credential was just used.
func (d *DB) TouchAppPassword(ctx context.Context, id, nowNs int64, ip, ua string) error {
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlTouchAppPassword, nowNs, textArg(ip), textArg(ua), id)
		return err
	}); err != nil {
		return fmt.Errorf("stamping an app password: %w", err)
	}
	return nil
}

// joinShareScope renders the share list as the NUL-separated column value. An
// empty list is an empty value, which means every share the account can see.
func joinShareScope(shares []string) []byte {
	var out []byte
	for i, s := range shares {
		if i > 0 {
			out = append(out, 0)
		}
		out = append(out, s...)
	}
	return out
}

// splitShareScope reads it back. An empty column is no list rather than a
// list holding one empty name.
func splitShareScope(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	parts := bytes.Split(b, []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, string(p))
	}
	return out
}
