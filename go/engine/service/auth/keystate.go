package auth

import (
	"context"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// Bringing the durable state in line with the loaded ring, and refusing a key
// that cannot open what is on disk.

// startupKeyState runs the three checks in order: establish the version for a
// fresh deployment, refuse a ring that lacks the version the database names,
// finish or roll back an interrupted rotation, then decrypt one row of each
// kind.
func (s *Service) startupKeyState(ctx context.Context) error {
	_, activeVer, err := s.activeKey()
	if err != nil {
		return err
	}

	dbVer, err := s.store.KeyVersionState(ctx)
	if err != nil {
		return err
	}
	if dbVer == state.MissingKeyVersion {
		// A database with no version row has never established one, which is
		// what a fresh deployment looks like before its first sealed write.
		if werr := s.store.SetKeyVersion(ctx, activeVer); werr != nil {
			return werr
		}
		s.log.Info("established the auth state key version", "key_version", activeVer)
		dbVer = activeVer
	}

	if !s.keyRing().Has(dbVer) {
		return fmt.Errorf("%w: the database names %d", ErrKeyVersionMissing, dbVer)
	}
	if err := s.alignRing(dbVer); err != nil {
		return err
	}
	return s.checkMasterKey(ctx)
}

// alignRing compacts the ring file to exactly the version the database
// commits to.
//
// An interrupted rotation leaves the two disagreeing in one of exactly two
// ways, and this recovers both: the database names an older version than the
// ring's newest, which is step two never committing, or the ring still holds
// both while the database names the newest, which is step three never
// running. Either way the ring ends holding what the committed database
// requires and never a key the database never named.
func (s *Service) alignRing(dbVer uint32) error {
	ring := s.keyRing()
	versions := ring.Versions()
	if len(versions) == 1 && versions[0] == dbVer {
		return nil
	}
	compacted, ok := ring.compactTo(dbVer)
	if !ok {
		return fmt.Errorf("%w: the database names %d and the ring does not hold it",
			ErrKeyVersionMissing, dbVer)
	}
	if err := compacted.persist(); err != nil {
		return fmt.Errorf("aligning the master key ring: %w", err)
	}
	s.setKeyRing(compacted)
	s.log.Info("aligned the master key ring to the committed database version",
		"key_version", dbVer)
	return nil
}

// checkMasterKey opens one stored row of each kind under the loaded ring, so
// a wrong key file refuses startup rather than surfacing as per-account
// failures nobody can connect to a common cause.
//
// The severity is asymmetric, and deliberately. A second factor's secret is
// the only copy of that factor, so a key that cannot open one is fatal. An NT
// hash is derived material one password change regenerates, and a share-link
// ciphertext is a recoverable copy whose hash still authenticates a bearer;
// both warn.
func (s *Service) checkMasterKey(ctx context.Context) error {
	if row, found, err := s.store.SampleSealedRow(ctx, state.SealedTOTP); err != nil {
		return err
	} else if found {
		key, kerr := s.keyAt(row.KeyVer)
		if kerr != nil {
			return kerr
		}
		if _, oerr := openTOTP(key, row.Ciphertext, row.User, row.KeyVer); oerr != nil {
			return fmt.Errorf(
				"the master key cannot decrypt an existing second factor (account %d): %w",
				row.User, oerr)
		}
	}

	if row, found, err := s.store.SampleSealedRow(ctx, state.SealedSMB); err != nil {
		return err
	} else if found {
		key, kerr := s.keyAt(row.KeyVer)
		if kerr != nil {
			s.log.Warn("a stored SMB credential names a key version the ring does not hold",
				"account", row.User, "key_version", row.KeyVer)
			return nil
		}
		if _, oerr := openNT(key, row.Ciphertext, row.User, row.KeyVer); oerr != nil {
			s.log.Warn("the master key cannot decrypt a stored SMB credential; "+
				"SMB fails for it until the password is set again",
				"account", row.User, "error", oerr)
		}
	}
	return nil
}
