package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// The persisted share registry. Every share an administrator created is a
// row here: there is no config file declaring any, so there is one kind of
// share and one place it lives. None of it can be rebuilt from the
// filesystem, which is why it is durable rather than cached.

// ShareRow holds a single persisted share definition.
type ShareRow struct {
	ID   int64
	Name string
	Host string
	// SharedExternally marks a folder another program also writes. Nothing
	// on a filesystem says so, which is why it is the operator who says it.
	SharedExternally bool
	// TrashEnabled keeps deleted items in the share rather than removing
	// them. Off by default, because trash is disk somebody has to reclaim.
	TrashEnabled bool
	// SymlinkPolicy is the share's own answer to a symlink, as the vfs
	// package spells it. Stored as its name rather than its number so a
	// renumbering of the enum cannot silently change what a share does.
	SymlinkPolicy string
	Created       int64

	// Backend names which package opens this share's storage. "local" for
	// a row written before backends existed, since the column defaults to
	// it.
	Backend string
	// BackendConfig is the backend's own JSON, secret-free, persisted
	// verbatim. Empty for a local share.
	BackendConfig string
	// BackendSecret is the one credential a non-local backend needs,
	// sealed under BackendSecretKeyVer. Nil for a local share. Written and
	// read separately from the rest of the row; see UpdateShareSecret.
	BackendSecret       []byte
	BackendSecretKeyVer uint32
}

// ListShares yields all shares ordered by id.
func (d *DB) ListShares(ctx context.Context) (out []ShareRow, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlListShares)
	if err != nil {
		return nil, fmt.Errorf("listing persisted shares: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		r, serr := scanShare(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanShare(row interface{ Scan(...any) error }) (ShareRow, error) {
	var (
		r        ShareRow
		external int64
		trash    int64
		keyVer   int64
	)
	if err := row.Scan(&r.ID, &r.Name, &r.Host, &external, &trash,
		&r.SymlinkPolicy, &r.Created, &r.Backend, &r.BackendConfig,
		&r.BackendSecret, &keyVer); err != nil {
		return ShareRow{}, err
	}
	r.SharedExternally = external != 0
	r.TrashEnabled = trash != 0
	v, err := num.Narrow[uint32](keyVer)
	if err != nil {
		return ShareRow{}, fmt.Errorf(
			"share %d carries backend secret key version %d: %w", r.ID, keyVer, err)
	}
	r.BackendSecretKeyVer = v
	return r, nil
}

// InsertShare stores a new share and returns its row id.
//
// BackendSecret and BackendSecretKeyVer are absent from this statement on
// purpose: a fresh share's credential binds to the row id this insert has
// not minted yet, so it is sealed and written afterwards, by
// UpdateShareSecret.
func (d *DB) InsertShare(ctx context.Context, s ShareRow, createdNs int64) (int64, error) {
	// A new row is what grows the file.
	if err := d.f.EnsureWritable(); err != nil {
		return 0, err
	}
	var id int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertShare,
			s.Name, s.Host, boolInt(s.SharedExternally), boolInt(s.TrashEnabled),
			s.SymlinkPolicy, createdNs, s.Backend, s.BackendConfig)
		if ierr != nil {
			return ierr
		}
		var rerr error
		id, rerr = res.LastInsertId()
		return rerr
	})
	if err != nil {
		return 0, fmt.Errorf("storing a share: %w", err)
	}
	return id, nil
}

// UpdateShare replaces a share's definition, except its credential: see
// UpdateShareSecret.
func (d *DB) UpdateShare(ctx context.Context, rowid int64, s ShareRow) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlUpdateShare,
			s.Name, s.Host, boolInt(s.SharedExternally), boolInt(s.TrashEnabled),
			s.SymlinkPolicy, s.Backend, s.BackendConfig, rowid)
		return ierr
	})
}

// UpdateShareSecret replaces a share's sealed credential, in its own
// statement rather than the general UpdateShare's.
//
// A caller that leaves a patch's secret unset must not touch the stored
// one, and UpdateShare's single statement cannot express "leave this
// column alone" against an unset value the way an absent SQL parameter
// would need to. This is also where a fresh share's credential lands: its
// at-rest binding names the row's id, which InsertShare's own statement
// cannot yet supply because the id is what that statement is still
// minting.
func (d *DB) UpdateShareSecret(ctx context.Context, rowid int64, sealed []byte, keyVer uint32) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlUpdateShareSecret, sealed, int64(keyVer), rowid)
		return ierr
	})
}

// DeleteShare removes one share and every grant naming it, in one
// transaction, so a crash cannot leave a grant behind that outlives its
// share.
//
// shareID is the external id a grant's share column stores, which is a
// different number from the row id: the mapping between the two is the
// core's id scheme, and this package must not import the core to reach it.
// There is no foreign key doing this instead, because not every valid
// grant.share value is ever a share_definition row: the homes share is
// registered live under a reserved id that no row id can produce, and an
// immediately-enforced foreign key would refuse every home grant.
func (d *DB) DeleteShare(ctx context.Context, rowid, shareID int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlDeleteGrantsForShare, shareID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, sqlDeleteShare, rowid)
		return err
	})
}

// boolInt stores a flag as the integer SQLite holds it as.
func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
