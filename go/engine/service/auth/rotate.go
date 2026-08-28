package auth

import (
	"context"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// RotationReport is what one rotation did, so the operator sees how many rows
// moved rather than only "done".
type RotationReport struct {
	OldVersion           uint32
	NewVersion           uint32
	SMBBrought           int
	TOTPBrought          int
	LinksBrought         int
	ConfigSecretsBrought int
}

// RotateMasterKey re-seals every encrypted row under a new key.
//
// It cannot be one transaction, because the database cannot commit atomically
// with a key-file rename, so it is three steps:
//
//  1. persist a ring holding both the old and the new key;
//  2. in one database transaction, open and re-seal every sealed row under
//     the new key, then record the new version. A row that will not open
//     aborts that transaction, which changes nothing;
//  3. compact the ring to the new key alone.
//
// A crash at any boundary leaves at least the key the committed database
// requires, and the next startup's alignment finishes or rolls back whichever
// step never landed.
func (s *Service) RotateMasterKey(ctx context.Context) (RotationReport, error) {
	s.rotateMu.Lock()
	defer s.rotateMu.Unlock()

	var rep RotationReport
	old := s.keyRing()
	if old == nil {
		return rep, ErrNoKeyRing
	}
	_, oldVer := old.Active()
	rep.OldVersion = oldVer

	extended, next, err := old.withNewKey()
	if err != nil {
		return rep, err
	}
	if perr := extended.persist(); perr != nil {
		return rep, fmt.Errorf("persisting the new master key: %w", perr)
	}
	rep.NewVersion = next

	newKey, ok := extended.Get(next)
	if !ok {
		return rep, fmt.Errorf("the extended ring lacks version %d", next)
	}

	counts, err := s.store.Reseal(ctx, next, func(row state.SealedRow) ([]byte, error) {
		// Opened under the key that sealed the row rather than under the
		// newest old key: a deployment whose previous rotation was
		// interrupted can hold rows at more than one version, and guessing
		// one of them would fail every row at the other.
		rowKey, held := extended.Get(row.KeyVer)
		if !held {
			return nil, fmt.Errorf("%w: a %s names version %d",
				ErrKeyVersionMissing, row.Kind, row.KeyVer)
		}
		return resealRow(row, rowKey, newKey, next)
	})
	if err != nil {
		return rep, err
	}
	rep.SMBBrought = counts.SMB
	rep.TOTPBrought = counts.TOTP
	rep.LinksBrought = counts.Links
	rep.ConfigSecretsBrought = counts.ConfigSecrets

	compacted, ok := extended.compactTo(next)
	if !ok {
		return rep, fmt.Errorf("compacting the master key ring: no key at version %d", next)
	}
	if perr := compacted.persist(); perr != nil {
		return rep, fmt.Errorf("compacting the master key ring: %w", perr)
	}
	s.setKeyRing(compacted)
	return rep, nil
}

// resealRow opens one row under the key that sealed it and returns it sealed
// under the new one, with the binding its kind requires.
func resealRow(row state.SealedRow, oldKey, newKey [keyLen]byte, newVer uint32) ([]byte, error) {
	switch row.Kind {
	case state.SealedSMB:
		nt, err := openNT(oldKey, row.Ciphertext, row.User, row.KeyVer)
		if err != nil {
			return nil, fmt.Errorf("decrypting the SMB credential of account %d: %w", row.User, err)
		}
		return sealNT(newKey, nt, row.User, newVer)
	case state.SealedTOTP:
		sec, err := openTOTP(oldKey, row.Ciphertext, row.User, row.KeyVer)
		if err != nil {
			return nil, fmt.Errorf("decrypting the second factor of account %d: %w", row.User, err)
		}
		return sealTOTP(newKey, sec, row.User, newVer)
	case state.SealedLink:
		token, err := openWith(oldKey, row.Ciphertext, aadBytes(bindShareLink, row.TokenHash, row.KeyVer))
		if err != nil {
			return nil, fmt.Errorf("decrypting the owner copy of share link %d: %w", row.LinkID, err)
		}
		return sealWith(newKey, token, aadBytes(bindShareLink, row.TokenHash, newVer))
	case state.SealedConfig:
		plain, err := openWith(oldKey, row.Ciphertext, aadName(bindConfig, row.Name, row.KeyVer))
		if err != nil {
			return nil, fmt.Errorf("decrypting the configuration secret %q: %w", row.Name, err)
		}
		return sealWith(newKey, plain, aadName(bindConfig, row.Name, newVer))
	default:
		return nil, fmt.Errorf("unknown sealed kind %d", row.Kind)
	}
}
