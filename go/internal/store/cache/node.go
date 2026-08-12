package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// node.flags bits. PINNED is gone: what used to need pinning was a durable row
// keyed by an id only this database mints, and those rows key by the identity
// tuple now, so there is nothing left to hold a node down.
const flagIsDir int64 = 1 << 0

// Upsert returns the stable id for the file st names, inserting the row the
// first time the file is seen and refreshing what has moved since.
//
// Allocation is lazy: this is the only thing that inserts into node, so a
// deployment that never asks for a stable id creates no rows at all.
func (d *DB) Upsert(
	ctx context.Context, tx *sql.Tx,
	share vfs.ShareID, parent FileID, name string, st vfs.Stat,
) (FileID, error) {
	ident := IdentOf(share, st)

	var (
		id, curParent, flags int64
		curName              string
	)
	err := tx.StmtContext(ctx, d.st.nodeByIdent).QueryRowContext(ctx,
		int64(share), toSQL(ident.Dev), toSQL(ident.Ino), btimeArg(ident),
	).Scan(&id, &curParent, &curName, &flags)

	switch {
	case err == nil:
		want := flags &^ flagIsDir
		if st.Kind.IsDir() {
			want |= flagIsDir
		}
		size, mtime := toSQL(st.Size), st.MtimeNs
		if curParent != int64(parent) || curName != name || want != flags {
			// The filesystem is the source of truth and this is the cache
			// catching up with a rename or a write that happened out of band.
			_, err = tx.StmtContext(ctx, d.st.moveNode).ExecContext(ctx,
				int64(parent), name, size, mtime, want, id)
		} else {
			// Size and mtime are here to spare a stat per entry on a listing,
			// so they are refreshed even when nothing about identity moved.
			_, err = tx.StmtContext(ctx, d.st.touchNode).ExecContext(ctx, size, mtime, id)
		}
		if err != nil {
			return 0, fmt.Errorf("refreshing node %d: %w", id, err)
		}
		return FileID(id), nil

	case errors.Is(err, sql.ErrNoRows):
		// The guard is here rather than at the top: refreshing a row cannot
		// grow the file, and a file that already has an id keeps working while
		// the free-space floor holds. A new row is what adds a page.
		if blocked := d.f.EnsureWritable(); blocked != nil {
			return 0, blocked
		}
		newID, aerr := d.AllocateID(ctx, tx, ident)
		if aerr != nil {
			return 0, aerr
		}
		var flags int64
		if st.Kind.IsDir() {
			flags = flagIsDir
		}
		if _, ierr := tx.StmtContext(ctx, d.st.insertNode).ExecContext(ctx,
			int64(newID), int64(share), int64(parent), name,
			toSQL(ident.Dev), toSQL(ident.Ino), btimeArg(ident),
			flags, toSQL(st.Size), st.MtimeNs,
		); ierr != nil {
			return 0, fmt.Errorf("inserting a node for (dev %d, ino %d): %w", ident.Dev, ident.Ino, ierr)
		}
		return newID, nil

	default:
		return 0, fmt.Errorf("looking up a node by identity: %w", err)
	}
}

// Lookup reports the id this file already has, without allocating one. It is
// how a caller asks whether a file has a stable id without the side effect of
// giving it one.
func (d *DB) Lookup(ctx context.Context, share vfs.ShareID, st vfs.Stat) (FileID, bool, error) {
	ident := IdentOf(share, st)
	var id int64
	err := d.st.nodeIDByIdent.QueryRowContext(ctx,
		int64(share), toSQL(ident.Dev), toSQL(ident.Ino), btimeArg(ident)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("looking up a node by identity: %w", err)
	}
	return FileID(id), true, nil
}

// SQLite has one integer type and it is signed, so a device or inode number
// with the top bit set is stored as its two's complement and read back the
// same way. Nothing orders these as numbers: they are compared for equality
// and hashed, and both survive the round trip exactly.
//
//nolint:gosec // the reinterpretation is the point; see above.
func toSQL(v uint64) int64 { return int64(v) }

//nolint:gosec // as above, the other direction.
func fromSQL(v int64) uint64 { return uint64(v) }

// identFromSQL rebuilds an identity from a row. The share is narrowed rather
// than reinterpreted: it was written from a share id and a value that no longer
// fits one is a corrupt row, which is worth saying rather than truncating.
func identFromSQL(share, dev, ino int64, btime *int64) (Ident, error) {
	s, err := num.Narrow[uint32](share)
	if err != nil {
		return Ident{}, fmt.Errorf("node row carries share %d: %w", share, err)
	}
	return Ident{
		Share: vfs.ShareID(s),
		Dev:   fromSQL(dev),
		Ino:   fromSQL(ino),
		Btime: btime,
	}, nil
}
