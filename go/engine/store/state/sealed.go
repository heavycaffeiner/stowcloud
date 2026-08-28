package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// Everything in this database that rests as ciphertext, walked in one place.
//
// The rows live in four tables and are opened with four different bindings,
// so a rotation that walked them from four call sites would be four chances
// to miss one; the one that was missed last time was the configuration
// secret, and single sign-on quietly stopped working at the next start.
//
// The crypto is not here. This aggregate hands each row's ciphertext to the
// caller and stores what comes back, so the key material stays in the layer
// that owns it and every statement stays in the layer that owns those.

// SealedKind says which binding a row's additional authenticated data uses.
type SealedKind uint8

const (
	// SealedSMB is a user_smb_secret row, bound to the account id.
	SealedSMB SealedKind = iota
	// SealedTOTP is a totp_secret row, bound to the account id.
	SealedTOTP
	// SealedLink is a share_link owner copy, bound to the token hash.
	SealedLink
	// SealedConfig is a config_secret row, bound to the setting's name.
	SealedConfig
)

// String names the kind for a message an operator reads.
func (k SealedKind) String() string {
	switch k {
	case SealedSMB:
		return "SMB credential"
	case SealedTOTP:
		return "second factor"
	case SealedLink:
		return "share link"
	case SealedConfig:
		return "configuration secret"
	default:
		return "sealed row"
	}
}

// SealedRow is one ciphertext with whatever identifies it. Only the fields
// its kind uses are set: an account id for the two credential kinds, a token
// hash for a link, a name for a configuration secret.
type SealedRow struct {
	Kind       SealedKind
	User       int64
	LinkID     int64
	TokenHash  []byte
	Name       string
	Ciphertext []byte
	KeyVer     uint32
}

// ResealCounts is what one rotation moved, per kind, so the operator sees how
// many rows rather than only "done".
type ResealCounts struct {
	SMB, TOTP, Links, ConfigSecrets int
}

// Reseal walks every sealed row in one transaction, replaces each ciphertext
// with what reseal returns, and records newVer as the database's key version
// as the last statement of that transaction.
//
// One transaction is what makes a rotation recoverable: a row that will not
// open aborts it and changes nothing, so the database never names a version
// some of its rows were not brought to.
func (d *DB) Reseal(
	ctx context.Context, newVer uint32, reseal func(SealedRow) ([]byte, error),
) (ResealCounts, error) {
	var counts ResealCounts
	if err := d.f.EnsureWritable(); err != nil {
		return counts, err
	}
	err := d.Write(ctx, func(tx *sql.Tx) error {
		var rerr error
		if counts.SMB, rerr = resealSMB(ctx, tx, newVer, reseal); rerr != nil {
			return rerr
		}
		if counts.TOTP, rerr = resealTOTP(ctx, tx, newVer, reseal); rerr != nil {
			return rerr
		}
		if counts.Links, rerr = resealLinks(ctx, tx, newVer, reseal); rerr != nil {
			return rerr
		}
		if counts.ConfigSecrets, rerr = resealConfig(ctx, tx, newVer, reseal); rerr != nil {
			return rerr
		}
		_, werr := tx.ExecContext(ctx, sqlUpsertKeyVersion, int64(newVer))
		return werr
	})
	if err != nil {
		return ResealCounts{}, fmt.Errorf("re-sealing under key version %d: %w", newVer, err)
	}
	return counts, nil
}

// Each of the four walks reads its rows to completion before writing any of
// them. Reading and writing one table through the same transaction while its
// cursor is open is behavior this package does not want to depend on, and the
// row counts here are small enough that holding them costs nothing.

func resealSMB(
	ctx context.Context, tx *sql.Tx, newVer uint32, reseal func(SealedRow) ([]byte, error),
) (int, error) {
	rows, err := collectSealed(ctx, tx, sqlForEachSMBSecret, SealedSMB)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		sealed, serr := reseal(r)
		if serr != nil {
			return 0, serr
		}
		if _, uerr := tx.ExecContext(ctx, sqlUpsertSMBSecret, r.User, sealed, int64(newVer)); uerr != nil {
			return 0, uerr
		}
	}
	return len(rows), nil
}

func resealTOTP(
	ctx context.Context, tx *sql.Tx, newVer uint32, reseal func(SealedRow) ([]byte, error),
) (int, error) {
	rows, err := collectSealed(ctx, tx, sqlForEachTOTPSecret, SealedTOTP)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		sealed, serr := reseal(r)
		if serr != nil {
			return 0, serr
		}
		if _, uerr := tx.ExecContext(ctx, sqlResealTOTPSecret, sealed, int64(newVer), r.User); uerr != nil {
			return 0, uerr
		}
	}
	return len(rows), nil
}

func resealLinks(
	ctx context.Context, tx *sql.Tx, newVer uint32, reseal func(SealedRow) ([]byte, error),
) (int, error) {
	rows, err := collectSealed(ctx, tx, sqlForEachSealedLink, SealedLink)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		sealed, serr := reseal(r)
		if serr != nil {
			return 0, serr
		}
		if _, uerr := tx.ExecContext(ctx, sqlResealLink, sealed, int64(newVer), r.LinkID); uerr != nil {
			return 0, uerr
		}
	}
	return len(rows), nil
}

func resealConfig(
	ctx context.Context, tx *sql.Tx, newVer uint32, reseal func(SealedRow) ([]byte, error),
) (int, error) {
	rows, err := collectSealed(ctx, tx, sqlForEachConfigSecret, SealedConfig)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		sealed, serr := reseal(r)
		if serr != nil {
			return 0, serr
		}
		if _, uerr := tx.ExecContext(ctx, sqlWriteConfigSecret, r.Name, sealed, int64(newVer)); uerr != nil {
			return 0, uerr
		}
	}
	return len(rows), nil
}

func collectSealed(
	ctx context.Context, tx *sql.Tx, query string, kind SealedKind,
) (out []SealedRow, err error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("reading every %s: %w", kind, err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		r, serr := scanSealed(rows, kind)
		if serr != nil {
			return nil, serr
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading every %s: %w", kind, err)
	}
	return out, nil
}

func scanSealed(row interface{ Scan(...any) error }, kind SealedKind) (SealedRow, error) {
	r := SealedRow{Kind: kind}
	var ver int64
	var err error
	switch kind {
	case SealedSMB, SealedTOTP:
		err = row.Scan(&r.User, &r.Ciphertext, &ver)
	case SealedLink:
		err = row.Scan(&r.LinkID, &r.TokenHash, &r.Ciphertext, &ver)
	case SealedConfig:
		err = row.Scan(&r.Name, &r.Ciphertext, &ver)
	}
	if err != nil {
		return SealedRow{}, fmt.Errorf("reading a %s: %w", kind, err)
	}
	v, nerr := num.Narrow[uint32](ver)
	if nerr != nil {
		return SealedRow{}, fmt.Errorf("a %s carries key version %d: %w", kind, ver, nerr)
	}
	r.KeyVer = v
	return r, nil
}

// SampleSealedRow returns one row of the given kind, or reports that there is
// none. It is what the startup check reads: a wrong key file is discovered
// against one existing record of each kind rather than one failing login at a
// time.
func (d *DB) SampleSealedRow(ctx context.Context, kind SealedKind) (SealedRow, bool, error) {
	var query string
	switch kind {
	case SealedSMB:
		query = sqlFirstSMBSecret
	case SealedTOTP:
		query = sqlFirstTOTPSecret
	case SealedLink:
		query = sqlFirstSealedLink
	case SealedConfig:
		query = sqlFirstConfigSecret
	default:
		return SealedRow{}, false, fmt.Errorf("unknown sealed kind %d", kind)
	}
	r, err := scanSealed(d.f.SQL().QueryRowContext(ctx, query), kind)
	if errors.Is(err, sql.ErrNoRows) {
		return SealedRow{}, false, nil
	}
	if err != nil {
		return SealedRow{}, false, err
	}
	return r, true, nil
}
