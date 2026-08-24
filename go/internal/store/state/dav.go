package state

import (
	"context"
	"database/sql"
	"errors"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// Dead properties and locks, both keyed by the identity tuple rather than by a
// cache-minted id. A property that followed a fileid would move when the cache
// was rebuilt; one that follows the inode moves when the file does.

// ErrNoSuchLock is a token that holds no row.
var ErrNoSuchLock = errors.New("no such lock")

// Ident is the identity tuple a property or a lock hangs off.
//
// Btime is a pointer because a filesystem that does not report one is
// different from one reporting zero, and folding them together would let two
// files share a key.
type Ident struct {
	Share int64
	// Dev and Ino are stored as their bit pattern. SQLite has no unsigned
	// integer, and a device or inode number above the signed range comes back
	// unchanged, so reinterpreting is lossless where narrowing would refuse a
	// legitimate file.
	Dev   uint64
	Ino   uint64
	Btime *int64
}

func (i Ident) parts() (present, btime int64) {
	if i.Btime == nil {
		return 0, 0
	}
	return 1, *i.Btime
}

//nolint:gosec // G115: the bit pattern is the storage form; see the field comment.
func identToSQL(v uint64) int64 { return int64(v) }

//nolint:gosec // G115: as above, reading the same bit pattern back.
func identFromSQL(v int64) uint64 { return uint64(v) }

// DavProp is one stored dead property.
type DavProp struct {
	NS    string
	Name  string
	Value string
}

// DavLock is one row of dav_lock.
type DavLock struct {
	Token string
	Ident Ident
	// Path is the virtual path at lock time. A depth-infinity lock covers its
	// descendants by path prefix, so the path is what the ancestor check reads
	// rather than the identity.
	Path      string
	Principal int64
	// Owner is the text content of the client's owner element, re-serialised
	// on the way out and never echoed as markup.
	Owner     string
	Depth     int64
	Scope     int64
	ExpiresNs int64
	TimeoutS  int64
}

// DavProps returns every dead property on one resource.
func (d *DB) DavProps(ctx context.Context, id Ident) ([]DavProp, error) {
	present, btime := id.parts()
	rows, err := d.SQL().QueryContext(ctx, sqlSelectDavProps,
		id.Share, identToSQL(id.Dev), identToSQL(id.Ino), present, btime)
	if err != nil {
		return nil, err
	}
	defer func() {
		// The rows were read to completion or the scan already failed; the
		// close error carries nothing the caller can act on.
		_ = rows.Close() //nolint:errcheck // the scan error above is the answer.
	}()

	var out []DavProp
	for rows.Next() {
		var p DavProp
		if err := rows.Scan(&p.NS, &p.Name, &p.Value); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetDavProps applies a set of property writes in one transaction, so a
// PROPPATCH that fails partway leaves none of its changes behind. RFC 4918
// requires exactly that.
//
// A nil value removes.
func (d *DB) SetDavProps(ctx context.Context, id Ident, ops []DavPropOp) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	present, btime := id.parts()

	return d.Write(ctx, func(tx *sql.Tx) error {
		for _, op := range ops {
			dev, ino := identToSQL(id.Dev), identToSQL(id.Ino)
			if op.Remove {
				if _, err := tx.ExecContext(ctx, sqlDeleteDavProp,
					id.Share, dev, ino, present, btime, op.NS, op.Name); err != nil {
					return err
				}
				continue
			}
			// The count is checked before the insert rather than after, so the
			// bound is what refuses rather than a row nobody wanted.
			var n int64
			if err := tx.QueryRowContext(ctx, sqlCountDavProps,
				id.Share, dev, ino, present, btime).Scan(&n); err != nil {
				return err
			}
			var exists int64
			if err := tx.QueryRowContext(ctx, sqlDavPropExists,
				id.Share, dev, ino, present, btime, op.NS, op.Name).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 && n >= limits.DavPropsPerResource {
				return limits.Exceed("dav properties per resource",
					limits.DavPropsPerResource, n+1)
			}
			if _, err := tx.ExecContext(ctx, sqlUpsertDavProp,
				id.Share, dev, ino, present, btime, op.NS, op.Name, op.Value); err != nil {
				return err
			}
		}
		return nil
	})
}

// DavPropOp is one property write.
type DavPropOp struct {
	NS     string
	Name   string
	Value  string
	Remove bool
}

// DropDavProps removes every property on a resource, which is what a delete of
// the resource itself has to do.
func (d *DB) DropDavProps(ctx context.Context, id Ident) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	present, btime := id.parts()
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteDavPropsAll,
			id.Share, identToSQL(id.Dev), identToSQL(id.Ino), present, btime)
		return err
	})
}

// DavLocks returns every lock that has not expired.
//
// Expiry is applied on read rather than by a sweep: a lock whose deadline
// passed is gone whether or not anything has got round to deleting the row,
// and a reader that trusted the table would honour a lock nobody holds.
func (d *DB) DavLocks(ctx context.Context, nowNs int64) ([]DavLock, error) {
	rows, err := d.SQL().QueryContext(ctx, sqlSelectDavLocks, nowNs)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close() //nolint:errcheck // the scan error above is the answer.
	}()

	var out []DavLock
	for rows.Next() {
		l, serr := scanDavLock(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PutDavLock stores a new lock, refusing past the per-user cap.
func (d *DB) PutDavLock(ctx context.Context, l DavLock, nowNs int64) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	present, btime := l.Ident.parts()

	return d.Write(ctx, func(tx *sql.Tx) error {
		var n int64
		if err := tx.QueryRowContext(ctx, sqlCountDavLocks, l.Principal, nowNs).Scan(&n); err != nil {
			return err
		}
		if n >= limits.DavLocksPerUser {
			return limits.Exceed("dav locks per user", limits.DavLocksPerUser, n+1)
		}
		_, err := tx.ExecContext(ctx, sqlInsertDavLock,
			l.Token, l.Ident.Share, identToSQL(l.Ident.Dev), identToSQL(l.Ident.Ino),
			present, btime,
			l.Path, l.Principal, l.Owner, l.Depth, l.Scope, l.ExpiresNs, l.TimeoutS)
		return err
	})
}

// RefreshDavLock moves a lock's deadline. A token that holds no live row
// returns ErrNoSuchLock rather than silently creating one.
func (d *DB) RefreshDavLock(ctx context.Context, token string, expiresNs, timeoutS, nowNs int64) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
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

// DropDavLock removes one lock by token.
func (d *DB) DropDavLock(ctx context.Context, token string) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteDavLock, token)
		return err
	})
}

// SweepDavLocks deletes the rows whose deadline has passed. Reads already
// ignore them, so this reclaims space rather than changing an answer.
func (d *DB) SweepDavLocks(ctx context.Context, nowNs int64) (int64, error) {
	if err := d.f.EnsureWritable(); err != nil {
		return 0, err
	}
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

// rowScanner is what both *sql.Row and *sql.Rows satisfy, so one scan helper
// serves the single-row and the multi-row reads.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanDavLock(s rowScanner) (DavLock, error) {
	var (
		l        DavLock
		present  int64
		btime    int64
		dev, ino int64
	)
	if err := s.Scan(&l.Token, &l.Ident.Share, &dev, &ino,
		&present, &btime, &l.Path, &l.Principal, &l.Owner,
		&l.Depth, &l.Scope, &l.ExpiresNs, &l.TimeoutS); err != nil {
		return DavLock{}, err
	}
	l.Ident.Dev, l.Ident.Ino = identFromSQL(dev), identFromSQL(ino)
	if present != 0 {
		b := btime
		l.Ident.Btime = &b
	}
	return l, nil
}
