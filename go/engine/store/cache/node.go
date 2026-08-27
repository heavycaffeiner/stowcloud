package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// node.flags bits. There is no pinned bit: what used to need pinning was a
// durable row keyed by an id only this database mints, and those rows key by
// the identity tuple now, so there is nothing left to hold a node down.
const flagIsDir int64 = 1 << 0

// Upsert returns the stable id for the file st names, inserting the row the
// first time the file is seen and refreshing what has moved since.
//
// Allocation is lazy: this is the only thing that inserts into node, so a
// deployment that never asks for a stable id creates no rows at all.
func (d *DB) Upsert(
	ctx context.Context, tx *sql.Tx,
	share vfs.ShareID, parent ident.FileID, name string, st vfs.Stat,
) (ident.FileID, error) {
	id := ident.Of(share, st)

	var (
		row, curParent, flags int64
		curName               string
	)
	lookup := d.st.nodeByIdent
	if id.Btime == nil {
		lookup = d.st.nodeByIdentNoBtime
	}
	err := tx.StmtContext(ctx, lookup).QueryRowContext(ctx, identArgs(id)...).
		Scan(&row, &curParent, &curName, &flags)

	switch {
	case err == nil:
		want := flags &^ flagIsDir
		if st.Kind.IsDir() {
			want |= flagIsDir
		}
		size := sizeToSQL(st.Size)
		if curParent != int64(parent) || curName != name || want != flags {
			// The filesystem is the source of truth and this is the cache
			// catching up with a rename or a write that happened out of
			// band.
			_, err = tx.StmtContext(ctx, d.st.moveNode).ExecContext(ctx,
				int64(parent), name, size, st.MtimeNs, want, row)
		} else {
			// Size and mtime are here to spare a stat per entry on a
			// listing, so they refresh even when nothing about identity
			// moved.
			_, err = tx.StmtContext(ctx, d.st.touchNode).ExecContext(ctx, size, st.MtimeNs, row)
		}
		if err != nil {
			return 0, fmt.Errorf("refreshing node %d: %w", row, err)
		}
		return ident.FileID(row), nil

	case errors.Is(err, sql.ErrNoRows):
		// The guard is here rather than at the top: refreshing a row cannot
		// grow the file, and a file that already has an id keeps working
		// while the free-space floor holds. A new row is what adds a page.
		if blocked := d.f.EnsureWritable(); blocked != nil {
			return 0, blocked
		}
		newID, aerr := d.AllocateID(ctx, tx, id)
		if aerr != nil {
			return 0, aerr
		}
		var newFlags int64
		if st.Kind.IsDir() {
			newFlags = flagIsDir
		}
		dev, ino, _, _ := id.ToSQL()
		if _, ierr := tx.StmtContext(ctx, d.st.insertNode).ExecContext(ctx,
			int64(newID), int64(share), int64(parent), name,
			dev, ino, btimeArg(id),
			newFlags, sizeToSQL(st.Size), st.MtimeNs,
		); ierr != nil {
			return 0, fmt.Errorf("inserting a node for (dev %d, ino %d): %w", id.Dev, id.Ino, ierr)
		}
		return newID, nil

	default:
		return 0, fmt.Errorf("looking up a node by identity: %w", err)
	}
}

// Lookup reports the id this file already has, without allocating one. It is
// how a caller asks whether a file has a stable id without the side effect
// of giving it one.
func (d *DB) Lookup(
	ctx context.Context, share vfs.ShareID, st vfs.Stat,
) (ident.FileID, bool, error) {
	id := ident.Of(share, st)
	lookup := d.st.nodeIDByIdent
	if id.Btime == nil {
		lookup = d.st.nodeIDByIdentNoBtime
	}
	var row int64
	err := lookup.QueryRowContext(ctx, identArgs(id)...).Scan(&row)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("looking up a node by identity: %w", err)
	}
	return ident.FileID(row), true, nil
}

// sizeToSQL stores a file size in SQLite's one signed integer type. A size
// is bounded by what a filesystem can hold rather than by the type, so it
// crosses as its bit pattern, a byte at a time, and comes back the same way.
func sizeToSQL(v uint64) int64 {
	var out int64
	for i := range 8 {
		out = out<<8 | int64(v>>(56-8*i)&0xff)
	}
	return out
}

// sizeFromSQL is the same reinterpretation in the other direction.
func sizeFromSQL(v int64) uint64 {
	var out uint64
	for i := range 8 {
		out = out<<8 | uint64(v>>(56-8*i)&0xff)
	}
	return out
}
