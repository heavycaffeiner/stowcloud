package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// Aggregate is a directory's cached ETag with the recursive size and count
// computed alongside it.
//
// Computing one walks children and hashes what it finds, which is not this
// package's job: it stores the result, hands it back, and says when it is no
// longer to be trusted.
type Aggregate struct {
	Etag   string
	RSize  uint64
	RCount uint64
}

// DirEtag reports the cached aggregate for a directory, and false when the
// caller has to recompute: there is no row, the row is marked dirty, or it
// was computed against a share generation that has since moved. A caller
// cannot tell "never computed" from "computed against an old generation" and
// does not need to, since both mean recompute.
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

// PutDirEtag stores a freshly computed aggregate, stamped with the share
// generation it was computed against.
//
// It is not gated by the size guard, and that is deliberate rather than an
// oversight: refusing it would leave a cached ETag that is stale and still
// flagged valid, so clients get told nothing changed when it did. A wrong
// answer is not an acceptable way to save a page.
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

// BumpShareGen invalidates every cached aggregate in one share without
// touching a row: each one carries the generation it was computed against,
// so moving the counter reads as invalid everywhere at once. It is what the
// watcher falls back to when it loses a batch of events and can no longer
// say which paths changed.
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

// ShareGen is the share's current generation, and zero for a share that has
// never been invalidated. Zero is not a "never invalidated" sentinel: a
// freshly written row is also stamped 0, so freshness is valid and equal
// generations, never a non-zero one.
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
