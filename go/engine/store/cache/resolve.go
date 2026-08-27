package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// ErrNoNode is an id this cache holds no row for: never allocated, or
// discarded with a rebuild. It is not "no such file": the filesystem is the
// source of truth and this database is allowed not to know.
var ErrNoNode = errors.New("no such node")

// maxResolveHops bounds the parent-chain walk. A cyclic chain should never
// happen, and "should not happen" is not a proof for data a disk or a bug
// could have corrupted, so a corrupt one fails loudly instead of looping
// forever.
const maxResolveHops = 8192

// Resolve walks the parent chain to a path relative to the share root. There
// is no path column and there will not be one: a directory rename is one
// UPDATE because of that, instead of a write for every row underneath it.
//
// The walk is not a snapshot. A rename landing part way through yields the
// path the tree had at some point during it, which every caller has to
// tolerate anyway: the filesystem is the truth and this is a hint.
//
// A resolved component this server's own grammar would refuse, written by
// another program sharing the directory or corrupted on disk, is an error
// rather than a silently repaired string.
func (d *DB) Resolve(ctx context.Context, id ident.FileID) (vfs.ShareID, vfs.SharePath, error) {
	var (
		names []string
		share int64
		found bool
	)
	for cur := id; cur != ident.RootID; {
		if len(names) > maxResolveHops {
			return 0, vfs.SharePath{}, fmt.Errorf(
				"resolving node %d: the parent chain passed %d hops, so it is cyclic",
				id, maxResolveHops)
		}
		var (
			rowShare, parent int64
			name             string
		)
		err := d.st.nodeRowByID.QueryRowContext(ctx, int64(cur)).Scan(&rowShare, &parent, &name)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, vfs.SharePath{}, fmt.Errorf("resolving node %d: %w", id, ErrNoNode)
		}
		if err != nil {
			return 0, vfs.SharePath{}, fmt.Errorf("resolving node %d: %w", id, err)
		}
		if !found {
			share, found = rowShare, true
		}
		names = append(names, name)
		cur = ident.FileID(parent)
	}
	if !found {
		return 0, vfs.SharePath{}, fmt.Errorf("resolving node %d: %w", id, ErrNoNode)
	}

	s, err := num.Narrow[uint32](share)
	if err != nil {
		return 0, vfs.SharePath{}, fmt.Errorf("node %d carries share %d: %w", id, share, err)
	}
	slices.Reverse(names)
	path, err := vfs.ParseSharePath(strings.Join(names, "/"))
	if err != nil {
		return 0, vfs.SharePath{}, fmt.Errorf(
			"node %d resolves to a path this server would refuse: %w", id, err)
	}
	return vfs.ShareID(s), path, nil
}

// Rename moves a node: one row, whatever is underneath it. Descendants
// reference their parent by id, so the next resolve of every one of them is
// correct with no further writes.
//
// It is not gated by the size guard. The row already exists, so this cannot
// grow the file, and leaving the id pointing at the old path would be worse
// than a database slightly over the floor.
func (d *DB) Rename(
	ctx context.Context, tx *sql.Tx, id, newParent ident.FileID, newName string,
) error {
	res, err := tx.StmtContext(ctx, d.st.renameNode).
		ExecContext(ctx, int64(newParent), newName, int64(id))
	if err != nil {
		return fmt.Errorf("renaming node %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("renaming node %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("renaming node %d: %w", id, ErrNoNode)
	}
	return nil
}
