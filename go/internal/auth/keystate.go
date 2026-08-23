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
// A database with no key version row has never had one established, which is
// what a fresh deployment looks like before the first write. Recording the
// active version is the whole of it: every ciphertext this build writes binds
// its version into the AAD from the moment it is sealed.
func (s *Service) startupKeyState(ctx context.Context) error {
	_, activeVer := s.mk.Active()

	var established bool
	err := s.write(ctx, func(tx *sql.Tx) error {
		ver, err := readKeyVer(ctx, tx)
		if err != nil {
			return err
		}
		if ver == missingKeyVer {
			established = true
			if _, err := tx.ExecContext(ctx, sqlWriteKeyVersion, activeVer); err != nil {
				return fmt.Errorf("recording the key version: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if established {
		slog.Info("established the auth state key version",
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
