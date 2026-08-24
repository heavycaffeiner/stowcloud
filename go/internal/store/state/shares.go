package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The persisted share registry. Admin-created shares live in share_definition;
// editable properties of config-defined shares live in the two override
// tables. None of it can be rebuilt from the filesystem, which is why it is
// durable here: rebuilding the cache cannot recreate an admin's share or the
// admin's edit to a config-defined one.

// ShareRow is one persisted share definition.
type ShareRow struct {
	ID      int64
	Name    string
	Host    string
	Created int64
}

// IdentityOverride is an edit to a config-defined share's name and host path.
type IdentityOverride struct {
	ShareID int64
	Name    string
	Host    string
}

// ListShares returns every admin-created share, in id order. Nil host is used
// by created_ns consumers that do not need it.
func (d *DB) ListShares(ctx context.Context) (out []ShareRow, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlListShares)
	if err != nil {
		return nil, fmt.Errorf("listing persisted shares: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var r ShareRow
		if serr := rows.Scan(&r.ID, &r.Name, &r.Host, &r.Created); serr != nil {
			return nil, serr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertShare records a new admin-created share and returns its row id.
func (d *DB) InsertShare(ctx context.Context, name, host string, createdNs int64) (int64, error) {
	var id int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertShare, name, host, createdNs)
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

// UpdateShare rewrites one admin-created share. It is only called for a
// dynamic share, which is the one that owns a row.
func (d *DB) UpdateShare(ctx context.Context, rowid int64, name, host string) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlUpdateShare, name, host, rowid)
		return ierr
	})
}

// DeleteShare removes one admin-created share. Grants on it are deleted by
// the caller, which owns the cascade policy.
func (d *DB) DeleteShare(ctx context.Context, rowid int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlDeleteShare, rowid)
		return ierr
	})
}

// IdentityOverrideFor reports the persisted name/host-path edit for a share,
// if one was ever set. Only a config-defined share has one.
func (d *DB) IdentityOverrideFor(ctx context.Context, shareID int64) (IdentityOverride, bool, error) {
	var o IdentityOverride
	err := d.f.SQL().QueryRowContext(ctx, sqlIdentityOverrideFor, shareID).
		Scan(&o.ShareID, &o.Name, &o.Host)
	if errors.Is(err, sql.ErrNoRows) {
		return IdentityOverride{}, false, nil
	}
	if err != nil {
		return IdentityOverride{}, false, fmt.Errorf("reading a share identity override: %w", err)
	}
	return o, true, nil
}

// SetIdentityOverride upserts a config-defined share's name and host path.
func (d *DB) SetIdentityOverride(ctx context.Context, shareID int64, name, host string) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlUpsertIdentityOverride, shareID, name, host)
		return ierr
	})
}

// TrashOverrideFor reports a share's persisted trash on/off toggle, if one was
// ever set. It applies to config-defined and admin-created shares alike.
func (d *DB) TrashOverrideFor(ctx context.Context, shareID int64) (bool, bool, error) {
	var enabled int64
	err := d.f.SQL().QueryRowContext(ctx, sqlTrashOverrideFor, shareID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("reading a share trash override: %w", err)
	}
	return enabled != 0, true, nil
}

// SetTrashOverride upserts a share's trash toggle.
func (d *DB) SetTrashOverride(ctx context.Context, shareID int64, enabled bool) error {
	v := int64(0)
	if enabled {
		v = 1
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlUpsertTrashOverride, shareID, v)
		return ierr
	})
}

// CountOverrides reports how many identity or trash overrides exist. It exists
// so the importer can tell an operator whether anything was carried at all.
func (d *DB) CountOverrides(ctx context.Context) (int64, error) {
	var n int64
	err := d.f.SQL().QueryRowContext(ctx, sqlCountOverrides).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting share overrides: %w", err)
	}
	return n, nil
}
