package state

import (
	"context"
	"database/sql"
	"fmt"
)

// Favourites, keyed by the identity tuple.
//
// A star follows the file rather than a path, so renaming a starred file keeps
// it starred and creating a new file at the old name does not inherit one. The
// path column is what was last seen, for a client that asks for a list and
// wants somewhere to send the user.

// Favorite is one starred entry.
type Favorite struct {
	Share int64
	Dev   uint64
	Ino   uint64
	Btime *int64
	Path  string
}

func (f Favorite) parts() (present, btime int64) {
	if f.Btime == nil {
		return 0, 0
	}
	return 1, *f.Btime
}

// Favorites returns everything a user has starred.
func (d *DB) Favorites(ctx context.Context, user int64) ([]Favorite, error) {
	rows, err := d.SQL().QueryContext(ctx, sqlSelectFavorites, user)
	if err != nil {
		return nil, fmt.Errorf("reading favorites: %w", err)
	}
	defer func() {
		_ = rows.Close() //nolint:errcheck // the scan error below is the answer.
	}()

	var out []Favorite
	for rows.Next() {
		var (
			f        Favorite
			dev, ino int64
			present  int64
			btime    int64
		)
		if serr := rows.Scan(&f.Share, &dev, &ino, &present, &btime, &f.Path); serr != nil {
			return nil, fmt.Errorf("reading favorites: %w", serr)
		}
		f.Dev, f.Ino = identFromSQL(dev), identFromSQL(ino)
		if present != 0 {
			b := btime
			f.Btime = &b
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SetFavorite stars or unstars an entry.
func (d *DB) SetFavorite(ctx context.Context, user int64, f Favorite, on bool) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	present, btime := f.parts()
	dev, ino := identToSQL(f.Dev), identToSQL(f.Ino)

	return d.Write(ctx, func(tx *sql.Tx) error {
		if !on {
			_, err := tx.ExecContext(ctx, sqlDeleteFavorite,
				user, f.Share, dev, ino, present, btime)
			return err
		}
		// The path is refreshed on every star, so a file starred, renamed and
		// starred again reports where it is now rather than where it was.
		_, err := tx.ExecContext(ctx, sqlUpsertFavorite,
			user, f.Share, dev, ino, present, btime, f.Path)
		return err
	})
}
