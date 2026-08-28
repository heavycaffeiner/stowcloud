package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// Dead properties and locks, both keyed by the identity tuple rather than by
// a cache-minted id. A property that followed a file id would move when the
// cache was rebuilt; one that follows the inode moves when the file does.

// ErrNoSuchLock is a token that holds no live row.
var ErrNoSuchLock = errors.New("no such lock")

// DavProp holds a single stored dead property.
type DavProp struct {
	NS    string
	Name  string
	Value string
}

// DavPropOp is one property write. A removal names no value.
type DavPropOp struct {
	NS     string
	Name   string
	Value  string
	Remove bool
}

// DavLock represents a row in dav_lock.
type DavLock struct {
	Token string
	Ident ident.Ident
	// Path is the virtual path at lock time. A depth-infinity lock covers
	// its descendants by path prefix, so the path is what the ancestor check
	// reads rather than the identity.
	Path      string
	Principal int64
	// Owner carries the text of the client's owner element, re-serialized when
	// emitted and never echoed back as markup.
	Owner     string
	Depth     int64
	Scope     int64
	ExpiresNs int64
	TimeoutS  int64
}

// DavProps yields all dead properties attached to a resource.
func (d *DB) DavProps(ctx context.Context, id ident.Ident) (out []DavProp, err error) {
	dev, ino, present, btime := id.ToSQL()
	rows, err := d.f.SQL().QueryContext(ctx, sqlSelectDavProps,
		int64(id.Share), dev, ino, present, btime)
	if err != nil {
		return nil, fmt.Errorf("reading dead properties: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var p DavProp
		if serr := rows.Scan(&p.NS, &p.Name, &p.Value); serr != nil {
			return nil, fmt.Errorf("reading a dead property: %w", serr)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading dead properties: %w", err)
	}
	return out, nil
}

// SetDavProps performs a group of property writes in a single transaction, so a
// PROPPATCH failing midway leaves nothing behind. RFC 4918 mandates precisely
// this.
func (d *DB) SetDavProps(ctx context.Context, id ident.Ident, ops []DavPropOp) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	dev, ino, present, btime := id.ToSQL()
	share := int64(id.Share)

	return d.Write(ctx, func(tx *sql.Tx) error {
		for _, op := range ops {
			if op.Remove {
				if _, err := tx.ExecContext(ctx, sqlDeleteDavProp,
					share, dev, ino, present, btime, op.NS, op.Name); err != nil {
					return err
				}
				continue
			}
			// The count is checked before the insert rather than after, so
			// the bound is what refuses rather than a row nobody wanted.
			var n int64
			if err := tx.QueryRowContext(ctx, sqlCountDavProps,
				share, dev, ino, present, btime).Scan(&n); err != nil {
				return err
			}
			var exists int64
			if err := tx.QueryRowContext(ctx, sqlDavPropExists,
				share, dev, ino, present, btime, op.NS, op.Name).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 && n >= limits.DavPropsPerResource {
				return limits.Exceed("dav properties per resource",
					limits.DavPropsPerResource, n+1)
			}
			if _, err := tx.ExecContext(ctx, sqlUpsertDavProp,
				share, dev, ino, present, btime, op.NS, op.Name, op.Value); err != nil {
				return err
			}
		}
		return nil
	})
}

// DropDavProps removes every property on a resource, which is what a delete
// of the resource itself has to do.
func (d *DB) DropDavProps(ctx context.Context, id ident.Ident) error {
	dev, ino, present, btime := id.ToSQL()
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteDavPropsAll,
			int64(id.Share), dev, ino, present, btime)
		return err
	})
}

// DavLocks yields every lock still within its lifetime.
//
// Expiry is evaluated at read time rather than by a sweep. A lock past its
// deadline is gone regardless of whether anything has deleted the row, and a
// reader trusting the table blindly would enforce a lock nobody holds.
func (d *DB) DavLocks(ctx context.Context, nowNs int64) (out []DavLock, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlSelectDavLocks, nowNs)
	if err != nil {
		return nil, fmt.Errorf("reading locks: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		l, serr := scanDavLock(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading locks: %w", err)
	}
	return out, nil
}

// PutDavLock records a new lock and rejects any beyond the per-account cap.
func (d *DB) PutDavLock(ctx context.Context, l DavLock, nowNs int64) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	dev, ino, present, btime := l.Ident.ToSQL()

	return d.Write(ctx, func(tx *sql.Tx) error {
		var n int64
		if err := tx.QueryRowContext(ctx, sqlCountDavLocks, l.Principal, nowNs).Scan(&n); err != nil {
			return err
		}
		if n >= limits.DavLocksPerUser {
			return limits.Exceed("dav locks per user", limits.DavLocksPerUser, n+1)
		}
		_, err := tx.ExecContext(ctx, sqlInsertDavLock,
			l.Token, int64(l.Ident.Share), dev, ino, present, btime,
			l.Path, l.Principal, l.Owner, l.Depth, l.Scope, l.ExpiresNs, l.TimeoutS)
		return err
	})
}

// RefreshDavLock moves a lock's deadline. A token that holds no live row is
// ErrNoSuchLock rather than a lock silently created.
func (d *DB) RefreshDavLock(ctx context.Context, token string, expiresNs, timeoutS, nowNs int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlRefreshDavLock, expiresNs, timeoutS, token, nowNs)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNoSuchLock
		}
		return nil
	})
}

// DropDavLock deletes a lock identified by token.
func (d *DB) DropDavLock(ctx context.Context, token string) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteDavLock, token)
		return err
	})
}

// SweepDavLocks removes rows past their deadline. Reads already skip them, so
// this recovers space without altering any answer, which is also why the size
// guard never applies to it.
func (d *DB) SweepDavLocks(ctx context.Context, nowNs int64) (int64, error) {
	var n int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlSweepDavLocks, nowNs)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

func scanDavLock(s interface{ Scan(...any) error }) (DavLock, error) {
	var (
		l                               DavLock
		share, dev, ino, present, btime int64
	)
	if err := s.Scan(&l.Token, &share, &dev, &ino,
		&present, &btime, &l.Path, &l.Principal, &l.Owner,
		&l.Depth, &l.Scope, &l.ExpiresNs, &l.TimeoutS); err != nil {
		return DavLock{}, fmt.Errorf("reading a lock: %w", err)
	}
	id, err := ident.FromSQL(share, dev, ino, present, btime)
	if err != nil {
		return DavLock{}, fmt.Errorf("lock %q is corrupt: %w", l.Token, err)
	}
	l.Ident = id
	return l, nil
}
