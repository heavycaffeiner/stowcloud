package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// Aggregate holds a directory's cached ETag together with the recursive size and
// count computed at the same time.
//
// Producing one requires walking children and hashing the results, which is not
// this package's role. It stores the outcome, returns it, and reports when it
// can no longer be trusted.
type Aggregate struct {
	Etag   string
	RSize  uint64
	RCount uint64
}

// DirEtag returns a directory's cached aggregate, or false when recomputation is
// required: no row exists, the row is dirty, or it was computed against a share
// generation that has since advanced. Callers cannot distinguish "never
// computed" from "computed against an old generation", and have no need to,
// since both call for recomputation.
func (d *DB) DirEtag(
	ctx context.Context, share vfs.ShareID, id ident.FileID,
) (Aggregate, bool, error) {
	current, err := d.ShareGen(ctx, share)
	if err != nil {
		return Aggregate{}, false, err
	}

	var (
		etag          string
		rsize, rcount int64
		gen, valid    int64
	)
	err = d.st.readDiretag.QueryRowContext(ctx, int64(share), int64(id)).
		Scan(&etag, &rsize, &rcount, &gen, &valid)
	if errors.Is(err, sql.ErrNoRows) {
		return Aggregate{}, false, nil
	}
	if err != nil {
		return Aggregate{}, false, fmt.Errorf("reading the aggregate for node %d: %w", id, err)
	}
	if valid == 0 || sizeFromSQL(gen) != current {
		return Aggregate{}, false, nil
	}
	return Aggregate{Etag: etag, RSize: sizeFromSQL(rsize), RCount: sizeFromSQL(rcount)}, true, nil
}

// PutDirEtag records a newly computed aggregate, stamping it with the share
// generation in force at computation time.
//
// The size guard deliberately does not apply. Rejecting the write would leave a
// stale cached ETag still marked valid, telling clients nothing changed when it
// had. Saving a page is not worth returning a wrong answer.
func (d *DB) PutDirEtag(
	ctx context.Context, tx *sql.Tx,
	share vfs.ShareID, id ident.FileID, agg Aggregate, gen uint64,
) error {
	_, err := tx.StmtContext(ctx, d.st.putDiretag).ExecContext(ctx,
		int64(share), int64(id), agg.Etag,
		sizeToSQL(agg.RSize), sizeToSQL(agg.RCount), sizeToSQL(gen))
	if err != nil {
		return fmt.Errorf("storing the aggregate for node %d: %w", id, err)
	}
	return nil
}

// MarkDirty invalidates every id in the chain, normally a node's ancestors
// up to the share root. An id with no row yet gets a placeholder that is
// already invalid, so the next read correctly says "recompute" rather than
// failing on a missing row.
//
// Not gated by the size guard, for the same reason PutDirEtag is not:
// refusing it leaves a directory's rollup wrongly marked fresh after a real
// write, which is worse than the row it writes.
func (d *DB) MarkDirty(
	ctx context.Context, tx *sql.Tx, share vfs.ShareID, chain []ident.FileID,
) error {
	for _, id := range chain {
		if _, err := tx.StmtContext(ctx, d.st.dirtyDiretag).
			ExecContext(ctx, int64(share), int64(id)); err != nil {
			return fmt.Errorf("invalidating the aggregate for node %d: %w", id, err)
		}
	}
	return nil
}

// BumpShareGen invalidates every cached aggregate within a share without
// modifying any row. Each aggregate stores the generation it was computed
// against, so advancing the counter renders all of them invalid at once. The
// watcher resorts to this after losing a batch of events, when it can no longer
// identify which paths changed.
func (d *DB) BumpShareGen(
	ctx context.Context, tx *sql.Tx, share vfs.ShareID,
) (uint64, error) {
	if _, err := tx.StmtContext(ctx, d.st.bumpShareGen).
		ExecContext(ctx, int64(share)); err != nil {
		return 0, fmt.Errorf("bumping the generation of share %d: %w", share, err)
	}
	var gen int64
	if err := tx.StmtContext(ctx, d.st.readShareGen).
		QueryRowContext(ctx, int64(share)).Scan(&gen); err != nil {
		return 0, fmt.Errorf("reading the generation of share %d: %w", share, err)
	}
	return sizeFromSQL(gen), nil
}

// ShareGen gives a share's current generation, zero for one never invalidated.
// Zero is not a sentinel meaning "never invalidated": newly written rows are
// stamped 0 as well, so freshness means a valid row with matching generations
// rather than a non-zero value.
func (d *DB) ShareGen(ctx context.Context, share vfs.ShareID) (uint64, error) {
	var gen int64
	err := d.st.readShareGen.QueryRowContext(ctx, int64(share)).Scan(&gen)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading the generation of share %d: %w", share, err)
	}
	return sizeFromSQL(gen), nil
}
