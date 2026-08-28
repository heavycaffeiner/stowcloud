package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// fileid_override records, for both files in a collision, which one received the
// derived id.
//
// Derivation only proposes; this table decides. Whichever file claims the base
// id is determined by which was encountered first, an insertion order no rebuild
// reproduces. So the order settles the question once, these rows preserve it,
// and every later rebuild consults them rather than racing again.
//
// Both sides are stored because once a collision exists, both placements are
// insertion-order decisions. Recording only the newcomer would handle a two-file
// case and nothing beyond it.
//
// The table should normally be empty. Any row is the first indication that a
// corpus has grown to the point where a 63-bit collision is no longer
// theoretical.

// ErrOverrideConflict reports a stored assignment contradicting the one being
// written. Rows here are never rewritten: an earlier decision that no longer
// agrees indicates corruption in the durable half, and returning either value to
// a sync client would be guesswork.
var ErrOverrideConflict = errors.New("a recorded file id override disagrees")

// LookupFileID reports the recorded id for an identity, if one was ever
// recorded.
func (d *DB) LookupFileID(ctx context.Context, id ident.Ident) (ident.FileID, bool, error) {
	empty, err := d.noOverrides(ctx)
	if err != nil || empty {
		return 0, false, err
	}

	dev, ino, present, btime := id.ToSQL()
	var got int64
	err = d.f.SQL().QueryRowContext(ctx, sqlReadFileIDOverride,
		int64(id.Share), dev, ino, present, btime).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("reading a fileid override: %w", err)
	}
	return ident.FileID(got), true, nil
}

// LookupFileIDOwner reports which identity reserved an id, if any. It is
// what makes a reservation hold while the cache is empty: after a rebuild
// the owner may not have been walked yet, and an id nothing currently holds
// is still not free.
func (d *DB) LookupFileIDOwner(ctx context.Context, id ident.FileID) (ident.Ident, bool, error) {
	empty, err := d.noOverrides(ctx)
	if err != nil || empty {
		return ident.Ident{}, false, err
	}

	var share, dev, ino, present, btime int64
	err = d.f.SQL().QueryRowContext(ctx, sqlReadFileIDOverrideOwner, int64(id)).
		Scan(&share, &dev, &ino, &present, &btime)
	if errors.Is(err, sql.ErrNoRows) {
		return ident.Ident{}, false, nil
	}
	if err != nil {
		return ident.Ident{}, false, fmt.Errorf("reading the owner of file id %d: %w", id, err)
	}
	owner, err := ident.FromSQL(share, dev, ino, present, btime)
	if err != nil {
		return ident.Ident{}, false, fmt.Errorf("a fileid override is corrupt: %w", err)
	}
	return owner, true, nil
}

// RecordFileIDs commits every assignment in a single transaction against this
// database, returning only after that transaction lands, which is what places it
// ahead of the node rows referencing those ids.
//
// Rows already stating the same thing are left untouched, so a rebuild arriving
// at an identical decision writes nothing. Rows stating something different are
// rejected rather than overwritten.
func (d *DB) RecordFileIDs(ctx context.Context, assignments ...ident.Assignment) error {
	if len(assignments) == 0 {
		return nil
	}
	// A new row is what grows the file, so the guard applies here.
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	err := d.Write(ctx, func(tx *sql.Tx) error {
		for _, a := range assignments {
			if err := recordOne(ctx, tx, a); err != nil {
				return err
			}
		}
		return nil
	})
	d.overrides.Store(-1)
	return err
}

func recordOne(ctx context.Context, tx *sql.Tx, a ident.Assignment) error {
	dev, ino, present, btime := a.Ident.ToSQL()
	args := []any{int64(a.Ident.Share), dev, ino, present, btime}

	var have int64
	err := tx.QueryRowContext(ctx, sqlReadFileIDOverride, args...).Scan(&have)
	switch {
	case err == nil && have == int64(a.ID):
		return nil
	case err == nil:
		return fmt.Errorf("%w: (share %d, dev %d, ino %d) is recorded as %d and would be written as %d",
			ErrOverrideConflict, a.Ident.Share, a.Ident.Dev, a.Ident.Ino, have, a.ID)
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("reading a fileid override: %w", err)
	}

	// The id column is UNIQUE, so a second identity claiming this id fails
	// here rather than producing two answers to the same question.
	if _, err := tx.ExecContext(ctx, sqlWriteFileIDOverride, append(args, int64(a.ID))...); err != nil {
		return fmt.Errorf("writing a fileid override for id %d: %w", a.ID, err)
	}
	return nil
}

// CountFileIDOverrides reports how many collision assignments this installation
// has stored. It serves as a diagnostic: zero is expected, and anything else
// merits an operator's attention.
func (d *DB) CountFileIDOverrides(ctx context.Context) (int64, error) {
	var n int64
	if err := d.f.SQL().QueryRowContext(ctx, sqlCountFileIDOverrides).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting fileid overrides: %w", err)
	}
	return n, nil
}

// noOverrides resolves the common case using a counter read once. The cache
// queries this table twice per allocated id, so during a cold walk of a large
// tree that counter is the difference between two queries and several million
// against a table that is nearly always empty.
func (d *DB) noOverrides(ctx context.Context) (bool, error) {
	n := d.overrides.Load()
	if n < 0 {
		var err error
		if n, err = d.CountFileIDOverrides(ctx); err != nil {
			return false, err
		}
		d.overrides.Store(n)
	}
	return n == 0, nil
}
