package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// A share whose file content is encrypted by its clients, in rclone's crypt
// format so that rclone can mount it directly.
//
// The row's existence is the setting: there is no mode column, so the state
// cannot desynchronise from the material it describes. None of the columns is
// a secret. The scheme names the format, the salt is rclone's password2 and a
// salt is public by construction, and the verifier is a fixed known string
// encrypted under a key this server was never given. The passphrase that
// derives that key never arrives here at all, which is what makes the
// encryption zero-knowledge and also why nothing in this table is worth
// sealing under the master key.

// ShareEncryptionRow is one share's client-held encryption settings.
type ShareEncryptionRow struct {
	Share int64

	// Scheme names the on-disk format, so a future second format is a new
	// value here rather than a guess about what the bytes are.
	Scheme string

	// Salt is what the client passes rclone as password2. It is stored and
	// displayed in the clear because the user has to type it into their own
	// rclone configuration, and because a salt's job is to stop one
	// precomputation covering every deployment, not to stay hidden.
	Salt string

	// Verifier is a fixed known plaintext encrypted under the derived key.
	// The client decrypts it to prove a typed passphrase is the one this
	// share was set up with, which is what stops a mistyped passphrase from
	// silently writing half a share under a second key.
	Verifier []byte

	Created int64
}

// ListShareEncryption yields every encrypted share, ordered by share id. It is
// one query rather than one per share because a client needs the whole set
// before it can decide whether an upload has to be encrypted.
func (d *DB) ListShareEncryption(ctx context.Context) (out []ShareEncryptionRow, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlListShareEncryption)
	if err != nil {
		return nil, fmt.Errorf("listing share encryption: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var r ShareEncryptionRow
		if serr := rows.Scan(&r.Share, &r.Scheme, &r.Salt, &r.Verifier, &r.Created); serr != nil {
			return nil, fmt.Errorf("scanning share encryption: %w", serr)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReadShareEncryption retrieves one share's settings. A share with no row is
// not an error: that is every unencrypted share, which is the default.
func (d *DB) ReadShareEncryption(ctx context.Context, share int64) (ShareEncryptionRow, bool, error) {
	var r ShareEncryptionRow
	err := d.f.SQL().QueryRowContext(ctx, sqlReadShareEncryption, share).
		Scan(&r.Share, &r.Scheme, &r.Salt, &r.Verifier, &r.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return ShareEncryptionRow{}, false, nil
	}
	if err != nil {
		return ShareEncryptionRow{}, false, fmt.Errorf("reading share encryption: %w", err)
	}
	return r, true, nil
}

// WriteShareEncryption stores or replaces one share's settings.
func (d *DB) WriteShareEncryption(ctx context.Context, r ShareEncryptionRow) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlWriteShareEncryption,
			r.Share, r.Scheme, r.Salt, r.Verifier, r.Created)
		return err
	})
}

// DeleteShareEncryption drops one share's settings, which is what turning the
// setting off amounts to. The caller is responsible for having established
// that nothing encrypted is left: this server cannot decrypt it, so a row
// removed over stored ciphertext strands the salt the passphrase needs and
// makes the content unreadable by anyone.
func (d *DB) DeleteShareEncryption(ctx context.Context, share int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteShareEncryption, share)
		return err
	})
}
