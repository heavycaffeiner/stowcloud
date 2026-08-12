package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
)

// startupKeyState brings the durable auth state in line with the loaded key
// ring, then runs the decryption check that refuses a wrong key at startup
// rather than discovering it one failing login at a time.
//
// The migration is one state transaction, because it establishes the key
// version and re-seals every TOTP and recoverable share-link ciphertext into
// version-bound AAD together. AAD is authenticated, so adding the version to
// it makes every old ciphertext fail to open; that is exactly the migration's
// reason for existing, and doing it as one transaction means a failure part
// way leaves the old shape and the old version, not a half-migrated mix.
func (s *Service) startupKeyState(ctx context.Context) error {
	active, activeVer := s.mk.Active()

	var migrated bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		ver, err := readKeyVer(ctx, tx)
		if err != nil {
			return err
		}
		if ver == missingKeyVer {
			if err := s.migrateLegacySeals(ctx, tx, active, activeVer); err != nil {
				return err
			}
			migrated = true
			if _, err := tx.ExecContext(ctx, sqlWriteKeyVersion, activeVer); err != nil {
				return fmt.Errorf("recording the key version: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if migrated {
		slog.Info("re-sealed the auth state under version-bound AAD",
			slog.Uint64("key_version", uint64(activeVer)))
	}

	dbVer, err := readKeyVer(ctx, s.st.SQL())
	if err != nil {
		return err
	}
	if !s.mk.Has(dbVer) {
		return fmt.Errorf("%w: the database names %d", ErrKeyVersionMissing, dbVer)
	}

	// An interrupted rotation leaves a key the database never committed to.
	// If the database names a version older than the newest ring key, the
	// ring is compacted back to what the database requires; if it names the
	// newest, an incomplete step-3 compaction is finished. Either way the
	// ring ends aligned with the committed database.
	if err := s.alignRing(dbVer); err != nil {
		return err
	}

	return s.checkMasterKey(ctx)
}

// missingKeyVer stands for a key version that has never been established.
const missingKeyVer = ^uint32(0)

// readKeyVer reads the committed key version, and missingKeyVer for one that
// has never been established.
func readKeyVer(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (uint32, error) {
	var ver int64
	err := q.QueryRowContext(ctx, sqlReadKeyVersion).Scan(&ver)
	if errors.Is(err, sql.ErrNoRows) {
		return missingKeyVer, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading the key version: %w", err)
	}
	return uint32(ver), nil //nolint:gosec // a key version is a small counter made positive by construction.
}

// migrateLegacySeals re-seals every ciphertext that was sealed before key
// versions were bound into its AAD. The NT hash already binds its version, so
// only TOTP secrets and share-link tokens move.
func (s *Service) migrateLegacySeals(ctx context.Context, tx *sql.Tx, key [keyLen]byte, ver uint32) (err error) {
	// TOTP. A secret the key cannot open means a second factor is about to be
	// locked out silently, which is the one case that is fatal.
	rows, err := tx.QueryContext(ctx, sqlForEachTOTP)
	if err != nil {
		return fmt.Errorf("reading TOTP secrets: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	type totpRow struct {
		user int64
		ct   []byte
	}
	var totps []totpRow
	for rows.Next() {
		var r totpRow
		if serr := rows.Scan(&r.user, &r.ct); serr != nil {
			return serr
		}
		totps = append(totps, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range totps {
		secret, oerr := openTOTPLegacy(key, r.ct, r.user)
		if oerr != nil {
			return fmt.Errorf("migrating the TOTP secret for user %d: %w", r.user, oerr)
		}
		resealed, serr := sealTOTP(key, secret, r.user, ver)
		if serr != nil {
			return serr
		}
		if _, uerr := tx.ExecContext(ctx, sqlUpsertTOTP, r.user, resealed, ver, s.now()); uerr != nil {
			return uerr
		}
	}

	// Share-link owner copies. A version-0 token the current key cannot open
	// was already broken by a Rust key rotation: it is cleared, its token hash
	// and public link kept, and the degraded row reported. Anything else that
	// fails rolls back the whole migration.
	return s.migrateLegacyLinks(ctx, tx, key, ver)
}

func (s *Service) migrateLegacyLinks(ctx context.Context, tx *sql.Tx, key [keyLen]byte, ver uint32) (err error) {
	rows, err := tx.QueryContext(ctx, sqlForEachLink)
	if err != nil {
		return fmt.Errorf("reading share-link ciphertexts: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var degraded []int64
	for rows.Next() {
		var (
			id        int64
			tokenHash []byte
			ct        []byte
			keyVer    any
		)
		if serr := rows.Scan(&id, &tokenHash, &ct, &keyVer); serr != nil {
			return serr
		}
		isZero, hasKV := true, keyVer != nil
		if v, ok := keyVer.(int64); hasKV && ok && v > 0 {
			isZero = false
		}
		if !isZero {
			continue // already version-bound
		}
		token, oerr := openLinkLegacy(key, ct, tokenHash)
		if oerr != nil {
			// The owner's recoverable copy is already gone; its hash still
			// authenticates a bearer who has the URL. Clear the copy, keep the
			// link, report the row.
			degraded = append(degraded, id)
			if _, cerr := tx.ExecContext(ctx, sqlClearLink, id); cerr != nil {
				return cerr
			}
			continue
		}
		resealed, serr := sealLink(key, token, tokenHash, ver)
		if serr != nil {
			return serr
		}
		if _, uerr := tx.ExecContext(ctx, sqlSealLink, resealed, ver, id); uerr != nil {
			return uerr
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range degraded {
		slog.Warn("cleared an unrecoverable share-link owner copy",
			slog.Int64("link", id))
	}
	return nil
}

// alignRing compacts the key ring file to exactly the version the database
// commits to, so a crash at every boundary of the rotation protocol leaves at
// least the key the committed database requires and never a key the database
// never named.
func (s *Service) alignRing(dbVer uint32) error {
	if len(s.mk.order) == 1 && s.mk.order[0] == dbVer {
		return nil // the ring already is exactly what the database names
	}
	compacted, ok := s.mk.compactTo(dbVer)
	if !ok {
		return fmt.Errorf("%w: the database names %d and the ring does not hold it",
			ErrKeyVersionMissing, dbVer)
	}
	s.mk = compacted
	if perr := s.mk.persist(); perr != nil {
		return fmt.Errorf("aligning the master key ring: %w", perr)
	}
	slog.Info("aligned the master key ring to the committed database version",
		slog.Uint64("key_version", uint64(dbVer)))
	return nil
}

// checkMasterKey is the asymmetric startup decrypt check: fatal on a TOTP
// secret the key cannot open, a warning on an SMB NT hash or a share-link
// ciphertext it cannot. A TOTP secret is the only copy of a second factor;
// an NT hash is derived material one password change regenerates, and a
// share-link ciphertext is a recoverable copy whose hash still authenticates
// a bearer.
func (s *Service) checkMasterKey(ctx context.Context) (err error) {
	active, _ := s.mk.Active()

	// A wrong key is discovered here, on the first existing record of each
	// kind, rather than one failing login at a time with no common cause.
	if totp, qerr := s.st.SQL().QueryContext(ctx, sqlForEachTOTP); qerr == nil {
		defer func() { err = errors.Join(err, totp.Close()) }()
		if totp.Next() {
			var user int64
			var ct []byte
			var keyVer uint32
			if serr := totp.Scan(&user, &ct, &keyVer); serr != nil {
				return serr
			}
			if _, oerr := openTOTP(active, ct, user, keyVer); oerr != nil {
				return fmt.Errorf("the master key cannot decrypt an existing TOTP secret (user %d): %w", user, oerr)
			}
		} else if terr := totp.Err(); terr != nil {
			return terr
		}
	} else {
		return fmt.Errorf("checking TOTP secrets: %w", qerr)
	}

	if smb, qerr := s.st.SQL().QueryContext(ctx, sqlForEachSMB); qerr == nil {
		defer func() { err = errors.Join(err, smb.Close()) }()
		if smb.Next() {
			var user int64
			var ct []byte
			var keyVer uint32
			if serr := smb.Scan(&user, &ct, &keyVer); serr != nil {
				return serr
			}
			if _, oerr := openNT(active, ct, user, keyVer); oerr != nil {
				slog.Warn("the master key cannot decrypt a stored SMB credential; SMB fails for it until the password is set again",
					slog.Int64("user", user), slog.Any("error", oerr))
			}
		} else if serr := smb.Err(); serr != nil {
			return serr
		}
	} else {
		return fmt.Errorf("checking SMB secrets: %w", qerr)
	}
	return nil
}
