package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// An account's own ordering over the roots it can see, keyed by label rather
// than share id.
//
// A label, not a share, is what the sidebar draws and what a stored order
// names: the same share can carry two labels through two grants, and an
// order over share ids could not tell those apart. A user who never sets an
// order stores no rows here, and the caller falls back to whatever order the
// evaluator already produces.

// RootOrder reads the caller's stored order, in position order. Empty when
// the account never set one.
func (d *DB) RootOrder(ctx context.Context, user int64) (out []string, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlSelectRootOrder, user)
	if err != nil {
		return nil, fmt.Errorf("reading a root order: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var label string
		if serr := rows.Scan(&label); serr != nil {
			return nil, fmt.Errorf("reading a root order entry: %w", serr)
		}
		out = append(out, label)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading a root order: %w", err)
	}
	return out, nil
}

// SetRootOrder replaces the caller's whole stored order in one transaction,
// so a reader never sees part of the old list beside part of the new one.
func (d *DB) SetRootOrder(ctx context.Context, user int64, labels []string) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlDeleteRootOrder, user); err != nil {
			return fmt.Errorf("clearing the old root order: %w", err)
		}
		for i, label := range labels {
			if _, err := tx.ExecContext(ctx, sqlInsertRootOrderEntry, user, label, i); err != nil {
				return fmt.Errorf("storing a root order entry: %w", err)
			}
		}
		return nil
	})
}
