package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// fileid_override records which of two colliding files took the derived id,
// for both of them.
//
// The derivation is a proposal and this table is the authority. Which file
// takes the base id depends on which was seen first, which is insertion
// order, which a rebuild does not reproduce: so the order decides once, the
// rows make it permanent, and every later rebuild reads them instead of
// racing again.
//
// Both sides are recorded because both are insertion-order decisions once a
// collision exists. Recording only the newcomer reproduces a two-file case
// and nothing larger.
//
// The table is expected to be empty. A row in it is the first evidence that
// a corpus has reached the size where a 63-bit collision stops being
// abstract.

// ErrOverrideConflict is a recorded assignment that disagrees with the one
// being written. Nothing rewrites a row here: a past decision that no longer
// matches is corruption in the durable half, and answering a sync client
// with either value would be a guess.
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

// RecordFileIDs writes every assignment in one transaction on this database.
// It returns once that transaction has committed, which is what puts it
// ahead of the node rows that use the ids.
//
// A row that already says the same thing is left alone, so a rebuild that
// reaches the same decision writes nothing. A row that says something else
// is refused rather than replaced.
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

// CountFileIDOverrides is how many collision assignments this install has
// recorded. It is a diagnostic: the number is expected to be zero and a
// non-zero one is worth an operator seeing.
func (d *DB) CountFileIDOverrides(ctx context.Context) (int64, error) {
	var n int64
	if err := d.f.SQL().QueryRowContext(ctx, sqlCountFileIDOverrides).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting fileid overrides: %w", err)
	}
	return n, nil
}

// noOverrides answers the common case from a counter loaded once. The cache
// consults this table twice for every id it allocates, so on a cold walk of
// a large tree the counter is what stands between two queries and several
// million against a table that is almost always empty.
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
