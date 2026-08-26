package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// RotationReport is what one rotation did, printed by the CLI so an operator
// sees how many rows moved rather than only "done".
type RotationReport struct {
	OldVersion   uint32
	NewVersion   uint32
	SMBBrought   int
	TOTPBrought  int
	LinksBrought int
	// ConfigSecretsBrought is the settings that are credentials: the
	// single-sign-on client secret is one.
	ConfigSecretsBrought int
}

// RotateMasterKey is the recovery protocol that re-seals every encrypted row
// under a new key. It cannot be one transaction, because SQLite cannot commit
// atomically with a key-file rename, so it is three steps under an exclusive
// hold:
//
//  1. persist a ring containing both the old and the new key;
//  2. in one state transaction, decrypt and re-seal every NT hash, TOTP
//     secret and recoverable share-link token under the new key, then set the
//     database key version to the new one. Any row that cannot open aborts
//     this transaction, which changes nothing;
//  3. compact the ring to the new key only.
//
// A crash at any boundary leaves at least the key the committed database
// requires; startup's alignRing finishes or rolls back whichever step never
// landed.
func (s *Service) RotateMasterKey(ctx context.Context) (RotationReport, error) {
	var rep RotationReport
	rep.OldVersion = s.mk.newest

	extended, next := s.mk.withNewKey()
	if err := extended.persist(); err != nil {
		return rep, fmt.Errorf("persisting the new master key: %w", err)
	}
	rep.NewVersion = next

	err := s.write(ctx, func(tx *sql.Tx) error {
		oldKey, _ := s.mk.Active()
		newKey, ok := extended.Get(next)
		if !ok {
			return fmt.Errorf("the extended ring lacks version %d", next)
		}

		n, err := s.resealSMB(ctx, tx, oldKey, newKey, next)
		rep.SMBBrought = n
		if err != nil {
			return err
		}
		n, err = s.resealTOTP(ctx, tx, oldKey, newKey, next)
		rep.TOTPBrought = n
		if err != nil {
			return err
		}
		n, err = s.resealLinks(ctx, tx, oldKey, newKey, next)
		rep.LinksBrought = n
		if err != nil {
			return err
		}
		n, err = s.resealConfigSecrets(ctx, tx, oldKey, newKey, next)
		rep.ConfigSecretsBrought = n
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, sqlWriteKeyVersion, next)
		return err
	})
	if err != nil {
		return rep, err
	}

	compacted, ok := extended.compactTo(next)
	if !ok {
		return rep, fmt.Errorf("compacting the master key ring: no key at version %d", next)
	}
	compacted.filePath = s.mk.filePath
	s.mk = compacted
	if err := s.mk.persist(); err != nil {
		return rep, fmt.Errorf("compacting the master key ring: %w", err)
	}
	return rep, nil
}

func (s *Service) resealSMB(ctx context.Context, tx *sql.Tx, oldKey, newKey [keyLen]byte, ver uint32) (n int, err error) {
	rows, err := tx.QueryContext(ctx, sqlForEachSMB)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var user int64
		var ct []byte
		var keyVer uint32
		if serr := rows.Scan(&user, &ct, &keyVer); serr != nil {
			return n, serr
		}
		nt, oerr := openNT(oldKey, ct, user, keyVer)
		if oerr != nil {
			return n, fmt.Errorf("decrypting the SMB NT hash for user %d: %w", user, oerr)
		}
		sealed, serr := sealNT(newKey, nt, user, ver)
		if serr != nil {
			return n, serr
		}
		if _, uerr := tx.ExecContext(ctx, sqlUpsertSMBSecret, user, sealed, ver); uerr != nil {
			return n, uerr
		}
		n++
	}
	return n, rows.Err()
}

func (s *Service) resealTOTP(ctx context.Context, tx *sql.Tx, oldKey, newKey [keyLen]byte, ver uint32) (n int, err error) {
	rows, err := tx.QueryContext(ctx, sqlForEachTOTP)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var user int64
		var ct []byte
		var keyVer uint32
		if serr := rows.Scan(&user, &ct, &keyVer); serr != nil {
			return n, serr
		}
		secret, oerr := openTOTP(oldKey, ct, user, keyVer)
		if oerr != nil {
			return n, fmt.Errorf("decrypting the TOTP secret for user %d: %w", user, oerr)
		}
		sealed, serr := sealTOTP(newKey, secret, user, ver)
		if serr != nil {
			return n, serr
		}
		if _, uerr := tx.ExecContext(ctx, sqlUpsertTOTP, user, sealed, ver, s.now()); uerr != nil {
			return n, uerr
		}
		n++
	}
	return n, rows.Err()
}

// resealConfigSecrets brings the settings that are credentials across.
//
// Without it a rotation would leave the single-sign-on secret sealed under a
// key the ring no longer holds, and the failure would appear at the next
// start as single sign-on quietly not working.
func (s *Service) resealConfigSecrets(ctx context.Context, tx *sql.Tx, oldKey, newKey [keyLen]byte, ver uint32) (n int, err error) {
	rows, err := tx.QueryContext(ctx, sqlForEachConfigSecret)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	type entry struct {
		name  string
		plain []byte
	}
	var opened []entry
	for rows.Next() {
		var (
			name   string
			ct     []byte
			keyVer uint32
		)
		if serr := rows.Scan(&name, &ct, &keyVer); serr != nil {
			return 0, serr
		}
		plain, oerr := open(oldKey, ct, aadConfig(name, keyVer))
		if oerr != nil {
			return 0, fmt.Errorf("decrypting the configuration secret %q: %w", name, oerr)
		}
		opened = append(opened, entry{name: name, plain: plain})
	}
	if rerr := rows.Err(); rerr != nil {
		return 0, rerr
	}
	// Written after the cursor is drained rather than inside the loop: this
	// updates the table it is reading, and the two other reseals above walk
	// tables keyed by a column they do not touch.
	for _, e := range opened {
		sealed, serr := seal(newKey, e.plain, aadConfig(e.name, ver))
		if serr != nil {
			return n, serr
		}
		if _, uerr := tx.ExecContext(ctx, sqlUpsertConfigSecret, e.name, sealed, ver); uerr != nil {
			return n, uerr
		}
		n++
	}
	return n, nil
}

func (s *Service) resealLinks(ctx context.Context, tx *sql.Tx, oldKey, newKey [keyLen]byte, ver uint32) (n int, err error) {
	rows, err := tx.QueryContext(ctx, sqlForEachLink)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var (
			id        int64
			tokenHash []byte
			ct        []byte
			keyVer    uint32
		)
		if serr := rows.Scan(&id, &tokenHash, &ct, &keyVer); serr != nil {
			return n, serr
		}
		token, oerr := openLink(oldKey, ct, tokenHash, keyVer)
		if oerr != nil {
			return n, fmt.Errorf("decrypting the owner copy of share link %d: %w", id, oerr)
		}
		sealed, serr := sealLink(newKey, token, tokenHash, ver)
		if serr != nil {
			return n, serr
		}
		if _, uerr := tx.ExecContext(ctx, sqlSealLink, sealed, ver, id); uerr != nil {
			return n, uerr
		}
		n++
	}
	return n, rows.Err()
}
