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

// Resolve follows the parent chain to produce a path relative to the share root.
// No path column exists and none will: that absence is why renaming a directory
// costs one UPDATE rather than a write per row beneath it.
//
// The traversal is not atomic. A rename arriving midway yields whatever path the
// tree held at some instant during the walk, which callers must tolerate
// regardless: the filesystem is authoritative and this is only a hint.
//
// A resolved component that this server's own grammar would reject, whether
// written by another program sharing the directory or corrupted on disk, raises
// an error instead of being quietly repaired.
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

// Rename relocates a node by updating a single row, no matter how much sits
// beneath it. Descendants refer to their parent by id, so every subsequent
// resolve is correct without additional writes.
//
// The size guard does not apply. The row already exists so this cannot enlarge
// the file, and leaving the id aimed at the former path would be worse than a
// database marginally above the floor.
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
