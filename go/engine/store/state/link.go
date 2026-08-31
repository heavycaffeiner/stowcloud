package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// A share link keeps a path and, when it was made against a file rather than
// a share root, the identity of that file. It does not follow a rename:
// access stats the stored path and requires the stored identity to match, so
// a rename or a replacement at that path makes the link gone.
//
// That contract is why there are exactly two representations and why a third
// has to be refused rather than guessed at.

// ErrLinkTargetMalformed reports a partial identity tuple: some columns
// populated and others not, or a birth time stored as absent. A link may hold
// exactly two shapes, path-only or a complete identity, and anything else
// indicates corruption in the durable half.
//
// Such rows are rejected rather than repaired. Interpreting a partial tuple as
// path-only would grant public access to whatever gets created at that path
// next, the one outcome worse than a link that stops working.
var ErrLinkTargetMalformed = errors.New("a share link carries a partial identity")

// linkOperatorFix accompanies a rejection. Nothing can recover the tuple's
// missing half, so the row must be removed. The link ceases to work, which is
// the correct behaviour for a corrupt link.
const linkOperatorFix = "restore state.db from a backup, or delete the link row and issue a new link"

// checkShareLinkTargets refuses a link whose target the next schema cannot
// represent, naming it. The CHECK constraints in step 2 refuse the same rows,
// but a constraint failure says which constraint and not which link, and an
// operator holding a durable database needs the link.
func checkShareLinkTargets(ctx context.Context, tx *sql.Tx) (err error) {
	rows, err := tx.QueryContext(ctx, sqlReadShareLinkTargets)
	if err != nil {
		return fmt.Errorf("reading share link targets: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var (
			id                       int64
			dev, ino, present, btime *int64
		)
		if serr := rows.Scan(&id, &dev, &ino, &present, &btime); serr != nil {
			return fmt.Errorf("reading a share link target: %w", serr)
		}
		set := 0
		for _, col := range []*int64{dev, ino, present, btime} {
			if col != nil {
				set++
			}
		}
		pathOnly := set == 0
		coherent := set == 4 && *present == 1 && (*dev != 0 || *ino != 0)

		if !pathOnly && !coherent {
			return fmt.Errorf("%w: share link %d. %s", ErrLinkTargetMalformed, id, linkOperatorFix)
		}
	}
	return rows.Err()
}

// LinkRow is one share_link row as it crosses this boundary. Pointer fields
// are nullable columns; nil is NULL. The types are primitives, so this
// package names no domain type it would otherwise have to import.
type LinkRow struct {
	ID          int64
	TokenHash   []byte
	TokenEnc    []byte
	TokenKeyVer *uint32
	Share       int64
	Path        string
	// Dev, Ino and Btime are the identity pin: a row carries all three or
	// none. A partial pin reads as no pin, because reading it as one would
	// mean matching a file this link was not made against.
	Dev          *int64
	Ino          *int64
	Btime        *int64
	Owner        int64
	Perms        uint16
	PasswordHash *string
	ExpiresNs    *int64
	MaxDown      *int64
	Downloads    int64
	Label        *string
	Note         *string
	CreatedNs    int64
}

// LinkRowPatch is an edit at the row level. An outer nil leaves the column
// alone; an inner nil sets it NULL. A non-nullable column takes one pointer.
type LinkRowPatch struct {
	Perms        *uint16
	PasswordHash **string
	ExpiresNs    **int64
	MaxDown      **int64
	Label        *string
	Note         *string
}

// Insert stores a new link with its download count at zero and returns its
// id. This store never sees a plaintext token or password: it writes what it
// is handed.
func (d *DB) Insert(ctx context.Context, row LinkRow) (int64, error) {
	// A new row is what grows the file.
	if err := d.f.EnsureWritable(); err != nil {
		return 0, err
	}

	var present, btime any
	if row.Btime != nil {
		present, btime = int64(1), *row.Btime
	}
	var keyVer any
	if row.TokenKeyVer != nil {
		keyVer = int64(*row.TokenKeyVer)
	}

	var id int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertLink,
			row.TokenHash, row.TokenEnc, keyVer, row.Share, row.Path,
			nullInt(row.Dev), nullInt(row.Ino), present, btime,
			row.Owner, int64(row.Perms), nullString(row.PasswordHash),
			nullInt(row.ExpiresNs), nullInt(row.MaxDown),
			nullString(row.Label), nullString(row.Note), row.CreatedNs)
		if ierr != nil {
			return ierr
		}
		var rerr error
		id, rerr = res.LastInsertId()
		return rerr
	})
	if err != nil {
		return 0, fmt.Errorf("storing a share link: %w", err)
	}
	return id, nil
}

// ByID reports the link with this id. Nothing matching is (row, false, nil):
// mapping that to a not-found error belongs one layer up.
func (d *DB) ByID(ctx context.Context, id int64) (LinkRow, bool, error) {
	return scanLinkRow(d.f.SQL().QueryRowContext(ctx, sqlLinkByID, id))
}

// ByHash reports the link with this token hash. The hash is what public
// access matches on, never the ciphertext, so a link works whether or not
// its token can be decrypted.
func (d *DB) ByHash(ctx context.Context, tokenHash []byte) (LinkRow, bool, error) {
	return scanLinkRow(d.f.SQL().QueryRowContext(ctx, sqlLinkByHash, tokenHash))
}

// ListByOwner returns the owner's links in id order.
func (d *DB) ListByOwner(ctx context.Context, owner int64) (out []LinkRow, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlListLinksByOwner, owner)
	if err != nil {
		return nil, fmt.Errorf("listing share links: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		row, _, serr := scanLinkRow(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing share links: %w", err)
	}
	return out, nil
}

// Delete removes the row only when both the id and the owner match.
func (d *DB) Delete(ctx context.Context, id, owner int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteLink, id, owner)
		return err
	})
}

// ConsumeDownload increments the count if the cap allows, in one conditional
// UPDATE. A false with a nil error is ambiguous by design (the cap is
// reached, or the row is gone); the caller disambiguates with ByID, and the
// ambiguity is the price of the check and the increment being one statement.
func (d *DB) ConsumeDownload(ctx context.Context, id int64) (bool, error) {
	var affected int64
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		res, xerr := tx.ExecContext(ctx, sqlConsumeLinkDownload, id)
		if xerr != nil {
			return xerr
		}
		affected, xerr = res.RowsAffected()
		return xerr
	}); err != nil {
		return false, fmt.Errorf("consuming a download of share link %d: %w", id, err)
	}
	return affected == 1, nil
}

// PasswordHash reads one column. Nil means no password is set. The stored
// value is whatever the hasher above this layer produced; nothing here
// interprets it.
func (d *DB) PasswordHash(ctx context.Context, id int64) (*string, error) {
	var hash *string
	err := d.f.SQL().QueryRowContext(ctx, sqlLinkPasswordHash, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the password of share link %d: %w", id, err)
	}
	return hash, nil
}

// Update applies every present patch field in one transaction, one constant
// statement per field.
func (d *DB) Update(ctx context.Context, id int64, patch LinkRowPatch) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		if patch.Perms != nil {
			if _, err := tx.ExecContext(ctx, sqlUpdateLinkPerms, int64(*patch.Perms), id); err != nil {
				return err
			}
		}
		if patch.PasswordHash != nil {
			if _, err := tx.ExecContext(ctx, sqlUpdateLinkPassword,
				nullString(*patch.PasswordHash), id); err != nil {
				return err
			}
		}
		if patch.ExpiresNs != nil {
			if _, err := tx.ExecContext(ctx, sqlUpdateLinkExpiry,
				nullInt(*patch.ExpiresNs), id); err != nil {
				return err
			}
		}
		if patch.MaxDown != nil {
			if _, err := tx.ExecContext(ctx, sqlUpdateLinkMaxDown,
				nullInt(*patch.MaxDown), id); err != nil {
				return err
			}
		}
		if patch.Label != nil {
			if _, err := tx.ExecContext(ctx, sqlUpdateLinkLabel, *patch.Label, id); err != nil {
				return err
			}
		}
		if patch.Note != nil {
			if _, err := tx.ExecContext(ctx, sqlUpdateLinkNote, *patch.Note, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// KeyVersion reads the singleton key_version row the auth package keeps in
// step with the key ring. A deployment that has never sealed a token has no
// row, which is version zero rather than an error.
func (d *DB) KeyVersion(ctx context.Context) (uint32, error) {
	var ver int64
	err := d.f.SQL().QueryRowContext(ctx, sqlLinkKeyVersion).Scan(&ver)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading the link key version: %w", err)
	}
	v, err := num.Narrow[uint32](ver)
	if err != nil {
		return 0, fmt.Errorf("the key version row carries %d: %w", ver, err)
	}
	return v, nil
}

// scanLinkRow reads one row and validates every narrowing. A row that does
// not fit is an error rather than a truncation: what is stored here is
// trust-boundary input to everything above.
func scanLinkRow(row interface{ Scan(...any) error }) (LinkRow, bool, error) {
	var (
		r       LinkRow
		keyVer  *int64
		present *int64
		btime   *int64
		perms   int64
	)
	err := row.Scan(&r.ID, &r.TokenHash, &r.TokenEnc, &keyVer, &r.Share, &r.Path,
		&r.Dev, &r.Ino, &present, &btime,
		&r.Owner, &perms, &r.PasswordHash, &r.ExpiresNs, &r.MaxDown,
		&r.Downloads, &r.Label, &r.Note, &r.CreatedNs)
	if errors.Is(err, sql.ErrNoRows) {
		return LinkRow{}, false, nil
	}
	if err != nil {
		return LinkRow{}, false, fmt.Errorf("reading a share link: %w", err)
	}

	if keyVer != nil {
		v, verr := num.Narrow[uint32](*keyVer)
		if verr != nil {
			return LinkRow{}, false, fmt.Errorf(
				"share link %d carries key version %d: %w", r.ID, *keyVer, verr)
		}
		r.TokenKeyVer = &v
	}
	p, err := num.Narrow[uint16](perms)
	if err != nil {
		return LinkRow{}, false, fmt.Errorf("share link %d carries perms %d: %w", r.ID, perms, err)
	}
	r.Perms = p

	// The pin is all four columns or none. A partial one reads as no pin,
	// which makes the link stop working rather than match a file it was
	// never made against.
	if r.Dev == nil || r.Ino == nil || present == nil || *present != 1 || btime == nil {
		r.Dev, r.Ino, r.Btime = nil, nil, nil
	} else {
		r.Btime = btime
	}
	return r, true, nil
}

// nullInt stores an unset optional column as SQL NULL.
func nullInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// nullString is the same for a text column.
func nullString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}
