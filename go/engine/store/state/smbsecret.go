package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// The file-sharing protocol's credential row and the singleton key version
// every sealed ciphertext in this database names.
//
// The two live together because they are read together: opening a stored NT
// hash needs the version it was sealed under, and the startup check that
// refuses a wrong key file reads one row of each kind.

// ErrNoSMBSecret is an account with no stored credential for the protocol.
var ErrNoSMBSecret = errors.New("no stored SMB credential")

// SMBSecret is one sealed NT hash and the key version that sealed it.
type SMBSecret struct {
	Ciphertext []byte
	KeyVer     uint32
}

// PassdbRow is what rendering the credential file needs about one account:
// the facts, with no decision folded in. Eligibility is a policy the service
// applies, because the policy can change without any row changing.
type PassdbRow struct {
	User         int64
	Name         string
	SMBEnabled   bool
	Disabled     bool
	TOTPEnrolled bool
	// Secret is absent for an account that has never had a credential
	// sealed, which is a different fact from one whose credential cannot be
	// opened.
	Secret *SMBSecret
}

// PutSMBSecret stores or replaces one account's sealed credential.
func (d *DB) PutSMBSecret(ctx context.Context, user int64, s SMBSecret) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlUpsertSMBSecret, user, s.Ciphertext, int64(s.KeyVer))
		return err
	}); err != nil {
		return fmt.Errorf("storing an SMB credential: %w", err)
	}
	return nil
}

// PutSMBSecretAndClearOptOut is what setting a separate password does: the
// credential and the withdrawal it undoes move together, because storing one
// while the other still says the account holds none would publish nothing.
func (d *DB) PutSMBSecretAndClearOptOut(ctx context.Context, user int64, s SMBSecret) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		if _, uerr := tx.ExecContext(ctx, sqlClearSMBOptOut, user); uerr != nil {
			return uerr
		}
		_, serr := tx.ExecContext(ctx, sqlUpsertSMBSecret, user, s.Ciphertext, int64(s.KeyVer))
		return serr
	}); err != nil {
		return fmt.Errorf("storing an SMB credential: %w", err)
	}
	return nil
}

// SMBSecretOf reads one account's sealed credential.
func (d *DB) SMBSecretOf(ctx context.Context, user int64) (SMBSecret, error) {
	var (
		s   SMBSecret
		ver int64
	)
	err := d.f.SQL().QueryRowContext(ctx, sqlSelectSMBSecret, user).Scan(&s.Ciphertext, &ver)
	if errors.Is(err, sql.ErrNoRows) {
		return SMBSecret{}, ErrNoSMBSecret
	}
	if err != nil {
		return SMBSecret{}, fmt.Errorf("reading an SMB credential: %w", err)
	}
	v, nerr := num.Narrow[uint32](ver)
	if nerr != nil {
		return SMBSecret{}, fmt.Errorf(
			"the SMB credential of account %d carries key version %d: %w", user, ver, nerr)
	}
	s.KeyVer = v
	return s, nil
}

// DeleteSMBSecret removes one account's credential.
func (d *DB) DeleteSMBSecret(ctx context.Context, user int64) error {
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteSMBSecret, user)
		return err
	}); err != nil {
		return fmt.Errorf("clearing an SMB credential: %w", err)
	}
	return nil
}

// PassdbRows is every account that has not withdrawn from the protocol, name
// order, with whatever credential it holds. The order is the file's, so two
// renders of unchanged state produce identical bytes.
func (d *DB) PassdbRows(ctx context.Context) (out []PassdbRow, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlSelectPassdbRows)
	if err != nil {
		return nil, fmt.Errorf("reading the SMB accounts: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var (
			r   PassdbRow
			ct  []byte
			ver sql.NullInt64
		)
		if serr := rows.Scan(&r.User, &r.Name, &r.SMBEnabled, &r.Disabled,
			&r.TOTPEnrolled, &ct, &ver); serr != nil {
			return nil, fmt.Errorf("reading an SMB account: %w", serr)
		}
		if ct != nil && ver.Valid {
			v, nerr := num.Narrow[uint32](ver.Int64)
			if nerr != nil {
				// A version outside the range one can take cannot name a key,
				// and guessing would try the wrong one. The account is
				// reported without a credential, which is what publishing it
				// would amount to anyway.
				out = append(out, r)
				continue
			}
			r.Secret = &SMBSecret{Ciphertext: ct, KeyVer: v}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the SMB accounts: %w", err)
	}
	return out, nil
}

// SMBRevertible is what clearing a separate password has to know: whether the
// account password can serve over the protocol afterwards.
type SMBRevertible struct {
	OptOut       bool
	TOTPEnrolled bool
	ProviderLink bool
}

// SMBRevertibleOf reads those three facts for one account.
func (d *DB) SMBRevertibleOf(ctx context.Context, user int64) (SMBRevertible, error) {
	var r SMBRevertible
	err := d.f.SQL().QueryRowContext(ctx, sqlSelectSMBRevertible, user).
		Scan(&r.OptOut, &r.TOTPEnrolled, &r.ProviderLink)
	if errors.Is(err, sql.ErrNoRows) {
		return SMBRevertible{}, ErrNoSuchAccount
	}
	if err != nil {
		return SMBRevertible{}, fmt.Errorf("reading an account's SMB state: %w", err)
	}
	return r, nil
}

// MissingKeyVersion stands for a database that has never established one,
// which is what a fresh deployment looks like before its first sealed write.
const MissingKeyVersion = ^uint32(0)

// KeyVersionState reads the committed key version, or MissingKeyVersion.
func (d *DB) KeyVersionState(ctx context.Context) (uint32, error) {
	return readKeyVersion(ctx, d.f.SQL(), sqlSelectKeyVersion)
}

// SetKeyVersion records the active version. It is the one write a fresh
// deployment's startup makes, and the last write a rotation makes inside its
// own transaction.
func (d *DB) SetKeyVersion(ctx context.Context, ver uint32) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlUpsertKeyVersion, int64(ver))
		return err
	}); err != nil {
		return fmt.Errorf("recording the key version: %w", err)
	}
	return nil
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readKeyVersion(ctx context.Context, q rowQuerier, query string) (uint32, error) {
	var ver int64
	err := q.QueryRowContext(ctx, query).Scan(&ver)
	if errors.Is(err, sql.ErrNoRows) {
		return MissingKeyVersion, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading the key version: %w", err)
	}
	v, nerr := num.Narrow[uint32](ver)
	if nerr != nil {
		return 0, fmt.Errorf("the key version row carries %d: %w", ver, nerr)
	}
	return v, nil
}
