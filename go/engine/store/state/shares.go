package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The persisted share registry. Every share an administrator created is a
// row here: there is no config file declaring any, so there is one kind of
// share and one place it lives. None of it can be rebuilt from the
// filesystem, which is why it is durable rather than cached.

// ShareRow holds a single persisted share definition.
type ShareRow struct {
	ID   int64
	Name string
	Host string
	// SharedExternally marks a folder another program also writes. Nothing
	// on a filesystem says so, which is why it is the operator who says it.
	SharedExternally bool
	// TrashEnabled keeps deleted items in the share rather than removing
	// them. Off by default, because trash is disk somebody has to reclaim.
	TrashEnabled bool
	// SymlinkPolicy is the share's own answer to a symlink, as the vfs
	// package spells it. Stored as its name rather than its number so a
	// renumbering of the enum cannot silently change what a share does.
	SymlinkPolicy string
	Created       int64
}

// ListShares yields all shares ordered by id.
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

// InsertShare stores a new share and returns its row id.
func (d *DB) InsertShare(ctx context.Context, s ShareRow, createdNs int64) (int64, error) {
	// A new row is what grows the file.
	if err := d.f.EnsureWritable(); err != nil {
		return 0, err
	}
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

// UpdateShare replaces a share's definition.
func (d *DB) UpdateShare(ctx context.Context, rowid int64, s ShareRow) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlUpdateShare,
			s.Name, s.Host, boolInt(s.SharedExternally), boolInt(s.TrashEnabled),
			s.SymlinkPolicy, rowid)
		return ierr
	})
}

// DeleteShare removes one share and every grant naming it, in one
// transaction, so a crash cannot leave a grant behind that outlives its
// share.
//
// shareID is the external id a grant's share column stores, which is a
// different number from the row id: the mapping between the two is the
// core's id scheme, and this package must not import the core to reach it.
// There is no foreign key doing this instead, because not every valid
// grant.share value is ever a share_definition row: the homes share is
// registered live under a reserved id that no row id can produce, and an
// immediately-enforced foreign key would refuse every home grant.
func (d *DB) DeleteShare(ctx context.Context, rowid, shareID int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlDeleteGrantsForShare, shareID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, sqlDeleteShare, rowid)
		return err
	})
}

// boolInt stores a flag as the integer SQLite holds it as.
func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
