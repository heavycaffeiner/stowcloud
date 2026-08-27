package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// Favorites, keyed by the identity tuple.
//
// A star follows the file rather than a path, so renaming a starred file
// keeps it starred and creating a new file at the old name does not inherit
// one. The path column is what was last seen, for a client that asks for a
// list and wants somewhere to send the user.

// Favorite is one starred entry. It carries the shared identity type rather
// than inlining the tuple again, so there is one place that knows how a
// device and inode number cross into SQLite.
type Favorite struct {
	Ident ident.Ident
	Path  string
}

// Favorites returns everything a user has starred.
func (d *DB) Favorites(ctx context.Context, user int64) (out []Favorite, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlSelectFavorites, user)
	if err != nil {
		return nil, fmt.Errorf("reading favorites: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var (
			f                               Favorite
			share, dev, ino, present, btime int64
		)
		if serr := rows.Scan(&share, &dev, &ino, &present, &btime, &f.Path); serr != nil {
			return nil, fmt.Errorf("reading a favorite: %w", serr)
		}
		id, ierr := ident.FromSQL(share, dev, ino, present, btime)
		if ierr != nil {
			return nil, fmt.Errorf("a favorite is corrupt: %w", ierr)
		}
		f.Ident = id
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading favorites: %w", err)
	}
	return out, nil
}

// SetFavorite stars or unstars an entry.
func (d *DB) SetFavorite(ctx context.Context, user int64, f Favorite, on bool) error {
	// Starring inserts a row; unstarring does not, but the guard is checked
	// once here rather than split across the two branches below, because the
	// call a caller makes is "set", and refusing only half of it under a
	// full volume is harder to reason about than refusing the call.
	if on {
		if err := d.f.EnsureWritable(); err != nil {
			return err
		}
	}
	dev, ino, present, btime := f.Ident.ToSQL()
	share := int64(f.Ident.Share)

	return d.Write(ctx, func(tx *sql.Tx) error {
		if !on {
			_, err := tx.ExecContext(ctx, sqlDeleteFavorite,
				user, share, dev, ino, present, btime)
			return err
		}
		// The path is refreshed on every star, so a file starred, renamed
		// and starred again reports where it is now rather than where it
		// was.
		_, err := tx.ExecContext(ctx, sqlUpsertFavorite,
			user, share, dev, ino, present, btime, f.Path)
		return err
	})
}
