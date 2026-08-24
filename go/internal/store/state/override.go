package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// fileid_override records which of two colliding files took the derived id, for
// both of them.
//
// The derivation is a proposal and this table is the authority. Which file
// takes the base id depends on which was seen first, which is insertion order,
// which a rebuild does not reproduce: so the order decides once, the rows make
// it permanent, and every later rebuild reads them instead of racing again.
//
// Both sides are recorded because both are insertion-order decisions once a
// collision exists. Recording only the newcomer reproduces a two-file case and
// nothing larger: after the cache is deleted, a third colliding identity walked
// before the original holder would find the holder's id unclaimed and take it.
//
// It is expected to be empty. A row in it is the first evidence that a corpus
// has reached the size where a 63-bit collision stops being abstract.

// ErrOverrideConflict is a recorded assignment that disagrees with the one
// being written. Nothing rewrites a row here: a past decision that no longer
// matches is corruption in the durable half, and answering a sync client with
// either value would be a guess.
var ErrOverrideConflict = errors.New("a recorded file id override disagrees")

// LookupFileID reports the recorded id for ident, if one was ever recorded.
func (d *DB) LookupFileID(ctx context.Context, ident cache.Ident) (cache.FileID, bool, error) {
	empty, err := d.noOverrides(ctx)
	if err != nil || empty {
		return 0, false, err
	}

	present, btime := btimeColumns(ident)
	var id int64
	err = d.f.SQL().QueryRowContext(ctx, sqlReadFileIDOverride,
		int64(ident.Share), toSQL(ident.Dev), toSQL(ident.Ino), present, btime).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("reading a fileid override: %w", err)
	}
	return cache.FileID(id), true, nil
}

// LookupFileIDOwner reports which identity reserved id, if any. It is what
// makes a reservation hold while the cache is empty: after a rebuild the owner
// may not have been walked yet, and an id nothing currently holds is still not
// free.
func (d *DB) LookupFileIDOwner(ctx context.Context, id cache.FileID) (cache.Ident, bool, error) {
	empty, err := d.noOverrides(ctx)
	if err != nil || empty {
		return cache.Ident{}, false, err
	}

	var share, dev, ino, present, btime int64
	err = d.f.SQL().QueryRowContext(ctx, sqlReadFileIDOverrideOwner, int64(id)).
		Scan(&share, &dev, &ino, &present, &btime)
	if errors.Is(err, sql.ErrNoRows) {
		return cache.Ident{}, false, nil
	}
	if err != nil {
		return cache.Ident{}, false, fmt.Errorf("reading the owner of file id %d: %w", id, err)
	}
	owner, err := identFrom(share, dev, ino, present, btime)
	if err != nil {
		return cache.Ident{}, false, err
	}
	return owner, true, nil
}

// RecordFileIDs writes every assignment in one transaction on this database. It
// returns once that transaction has committed, which is what puts it ahead of
// the node rows that use the ids.
//
// A row that already says the same thing is left alone, so a rebuild that
// reaches the same decision writes nothing. A row that says something else is
// refused rather than replaced.
func (d *DB) RecordFileIDs(ctx context.Context, assignments ...cache.Assignment) error {
	if len(assignments) == 0 {
		return nil
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

func recordOne(ctx context.Context, tx *sql.Tx, a cache.Assignment) error {
	present, btime := btimeColumns(a.Ident)
	args := []any{int64(a.Ident.Share), toSQL(a.Ident.Dev), toSQL(a.Ident.Ino), present, btime}

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

	// The id column is UNIQUE, so a second identity claiming this id fails here
	// rather than producing two answers to the same question.
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
// consults this table twice for every id it allocates, so on a cold walk of a
// large tree the counter is what stands between two queries and several million
// against a table that is almost always empty.
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

// btimeColumns splits a birth time into the present flag and the value, so
// that an absent btime and a zero one stay different rows.
func btimeColumns(ident cache.Ident) (present int64, btime int64) {
	if ident.Btime == nil {
		return 0, 0
	}
	return 1, *ident.Btime
}

// identFrom rebuilds an identity from a row. The share is narrowed rather than
// reinterpreted: it was written from a share id, and a value that no longer
// fits one is a corrupt row, which is worth saying rather than truncating.
func identFrom(share, dev, ino, present, btime int64) (cache.Ident, error) {
	s, err := num.Narrow[uint32](share)
	if err != nil {
		return cache.Ident{}, fmt.Errorf("a fileid override carries share %d: %w", share, err)
	}
	out := cache.Ident{Share: vfs.ShareID(s), Dev: fromSQL(dev), Ino: fromSQL(ino)}
	if present != 0 {
		v := btime
		out.Btime = &v
	}
	return out, nil
}

// SQLite has one integer type and it is signed, so a device or inode number
// with the top bit set is stored as its two's complement and read back the
// same way. Nothing orders these as numbers.
//
//nolint:gosec // the reinterpretation is the point; see above.
func toSQL(v uint64) int64 { return int64(v) }

//nolint:gosec // as above, in the other direction.
func fromSQL(v int64) uint64 { return uint64(v) }
