package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The persisted share registry. Every share this server serves is a row here:
// there is no config file declaring any, so there is one kind of share and one
// place it lives. None of it can be rebuilt from the filesystem, which is why
// it is durable rather than in the cache.

// ShareRow is one persisted share definition.
type ShareRow struct {
	ID   int64
	Name string
	Host string
	// SharedExternally marks a folder another program also writes. Nothing on
	// a filesystem says so, which is why it is the operator who says it.
	SharedExternally bool
	// TrashEnabled keeps deleted items in the share rather than removing them.
	// Off by default, because trash is disk somebody has to reclaim.
	TrashEnabled bool
	// SymlinkPolicy is the share's own answer to a symlink, as the vfs package
	// spells it. Stored as its name rather than its number so a renumbering of
	// the enum cannot silently change what a share does.
	SymlinkPolicy string
	Created       int64
}

// ListShares returns every share, in id order.
func (d *DB) ListShares(ctx context.Context) (out []ShareRow, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlListShares)
	if err != nil {
		return nil, fmt.Errorf("listing persisted shares: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		r, serr := scanShare(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanShare(row interface{ Scan(...any) error }) (ShareRow, error) {
	var (
		r        ShareRow
		external int64
		trash    int64
	)
	if err := row.Scan(&r.ID, &r.Name, &r.Host, &external, &trash,
		&r.SymlinkPolicy, &r.Created); err != nil {
		return ShareRow{}, err
	}
	r.SharedExternally = external != 0
	r.TrashEnabled = trash != 0
	return r, nil
}

// InsertShare records a new share and returns its row id.
func (d *DB) InsertShare(ctx context.Context, s ShareRow, createdNs int64) (int64, error) {
	var id int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertShare,
			s.Name, s.Host, boolInt(s.SharedExternally), boolInt(s.TrashEnabled),
			s.SymlinkPolicy, createdNs)
		if ierr != nil {
			return ierr
		}
		var rerr error
		id, rerr = res.LastInsertId()
		return rerr
	})
	if err != nil {
		return 0, fmt.Errorf("storing a share: %w", err)
	}
	return id, nil
}

// UpdateShare rewrites one share's definition.
func (d *DB) UpdateShare(ctx context.Context, rowid int64, s ShareRow) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlUpdateShare,
			s.Name, s.Host, boolInt(s.SharedExternally), boolInt(s.TrashEnabled),
			s.SymlinkPolicy, rowid)
		return ierr
	})
}

// DeleteShare removes one share. Grants on it are deleted by the caller,
// which owns the cascade policy.
func (d *DB) DeleteShare(ctx context.Context, rowid int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlDeleteShare, rowid)
		return ierr
	})
}
