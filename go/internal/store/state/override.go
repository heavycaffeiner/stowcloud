package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
)

// fileid_override records which of two colliding files took the derived id.
//
// The derivation is a proposal and this table is the authority. Which file
// takes the base id depends on which was seen first, which is insertion order,
// which a rebuild does not reproduce: so the order decides once, the row makes
// it permanent, and every later rebuild reads the row instead of racing again.
//
// It is expected to be empty. A row in it is the first evidence that a corpus
// has reached the size where a 63-bit collision stops being abstract.

// LookupFileID reports the recorded id for ident, if one was ever recorded.
func (d *DB) LookupFileID(ctx context.Context, ident cache.Ident) (cache.FileID, bool, error) {
	n := d.overrides.Load()
	if n < 0 {
		var err error
		if n, err = d.CountFileIDOverrides(ctx); err != nil {
			return 0, false, err
		}
		d.overrides.Store(n)
	}
	if n == 0 {
		return 0, false, nil
	}

	present, btime := btimeColumns(ident)
	var id int64
	err := d.f.SQL().QueryRowContext(ctx, sqlReadFileIDOverride,
		int64(ident.Share), toSQL(ident.Dev), toSQL(ident.Ino), present, btime).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("reading a fileid override: %w", err)
	}
	return cache.FileID(id), true, nil
}

// RecordFileID writes the decision, in its own transaction on this database.
// It returns once that transaction has committed, which is what puts it ahead
// of the node row that uses the id.
func (d *DB) RecordFileID(ctx context.Context, ident cache.Ident, id cache.FileID) error {
	present, btime := btimeColumns(ident)
	err := d.Write(ctx, func(tx *sql.Tx) error {
		_, werr := tx.ExecContext(ctx, sqlWriteFileIDOverride,
			int64(ident.Share), toSQL(ident.Dev), toSQL(ident.Ino), present, btime, int64(id))
		if werr != nil {
			return fmt.Errorf("writing a fileid override: %w", werr)
		}
		return nil
	})
	d.overrides.Store(-1)
	return err
}

// CountFileIDOverrides is how many collisions this install has recorded. It is
// a diagnostic: the number is expected to be zero and a non-zero one is worth
// an operator seeing.
func (d *DB) CountFileIDOverrides(ctx context.Context) (int64, error) {
	var n int64
	if err := d.f.SQL().QueryRowContext(ctx, sqlCountFileIDOverrides).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting fileid overrides: %w", err)
	}
	return n, nil
}

// btimeColumns splits a birth time into the present flag and the value, so
// that an absent btime and a zero one stay different rows.
func btimeColumns(ident cache.Ident) (present int64, btime int64) {
	if ident.Btime == nil {
		return 0, 0
	}
	return 1, *ident.Btime
}

// SQLite has one integer type and it is signed, so a device or inode number
// with the top bit set is stored as its two's complement and read back the
// same way. Nothing orders these as numbers.
//
//nolint:gosec // the reinterpretation is the point; see above.
func toSQL(v uint64) int64 { return int64(v) }
