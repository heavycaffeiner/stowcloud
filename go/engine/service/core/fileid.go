//go:build linux

package core

import (
	"context"
	"database/sql"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// RecordFileIDs makes the stable ids of listed entries resolvable back to
// their paths.
//
// A listing reports an entry's id from its identity tuple alone, so reading
// one costs no write. A client that later names a file by that id alone, for
// a preview or a direct link, needs the reverse: the cache row that says which
// directory the id sits in and under what name. Rows are minted here, by the
// surface that handed the ids out, rather than on every listing: the native
// interface addresses files by sealed path and never asks the question.
//
// Entries with no identity, such as trash rows built without a live inode,
// and a share root, which has no row of its own, are skipped. The filesystem
// stays authoritative: a row here is a hint a later lookup revalidates.
func (c *Core) RecordFileIDs(ctx context.Context, entries []Entry) error {
	type parentKey struct {
		share ShareID
		dir   string
	}
	groups := make(map[parentKey][]Entry, 1)
	dirs := make(map[parentKey]vfs.SafePath, 1)
	for _, e := range entries {
		if e.Name == "" || (e.Ident.Dev == 0 && e.Ident.Ino == 0) {
			continue
		}
		safe, err := e.Path.Safe()
		if err != nil || safe.Name() == "" {
			continue
		}
		key := parentKey{share: e.Ident.Share, dir: safe.Parent().String()}
		groups[key] = append(groups[key], e)
		dirs[key] = safe.Parent()
	}

	for key, members := range groups {
		root, ok := c.ShareRoot(key.share)
		if !ok {
			return errf(ErrNotFound, "record ids under an unregistered share")
		}
		parent, err := c.ensureFileIDChain(ctx, root, key.share, dirs[key])
		if err != nil {
			return err
		}
		if werr := c.cache.Write(ctx, func(tx *sql.Tx) error {
			for _, e := range members {
				st := vfs.Stat{
					Dev: e.Ident.Dev, Ino: e.Ident.Ino, BtimeNs: e.Ident.Btime,
					MtimeNs: e.MTimeNs, Size: e.Size, Kind: e.Kind,
				}
				if _, uerr := c.cache.Upsert(ctx, tx, key.share, parent, e.Name, st); uerr != nil {
					return uerr
				}
			}
			return nil
		}); werr != nil {
			return werr
		}
	}
	return nil
}
