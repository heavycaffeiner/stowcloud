package auth

import (
	"context"
	"errors"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// Creating, changing and removing accounts. Every path here that changes a
// credential bumps the generation and republishes, because a change that
// stops at one surface is a change that has not happened on the others.

// CreateUser makes an ordinary account.
func (s *Service) CreateUser(
	ctx context.Context, name, display string, pw secret.Secret,
) (int64, error) {
	return s.createAccount(ctx, name, display, pw, 0)
}

// CreateAdmin makes the deployment's first administrator, which is what the
// first-run bootstrap exists to do. It is the only caller that sets the role.
func (s *Service) CreateAdmin(
	ctx context.Context, name, display string, pw secret.Secret,
) (int64, error) {
	return s.createAccount(ctx, name, display, pw, state.RoleAdmin)
}

func (s *Service) createAccount(
	ctx context.Context, name, display string, pw secret.Secret, role int64,
) (int64, error) {
	// Validated here rather than at whichever screen happens to call: an
	// account created through the administrative surface used to bypass the
	// rule entirely, and one name the credential file cannot carry cost every
	// account its file-sharing access.
	if err := ValidUsername(name); err != nil {
		return 0, err
	}
	if pw.Len() < MinPasswordLen {
		return 0, ErrWeakPassword
	}
	hash, err := s.Hash(ctx, pw)
	if err != nil {
		return 0, err
	}

	// The credential for the file-sharing protocol is derived and sealed
	// inside the creating transaction, because it comes from the plaintext,
	// which exists only now. An account created without one had no way to
	// reach that protocol until it changed its password, and the interface's
	// "set a separate password" framing made that defect read as a policy.
	id, err := s.store.CreateAccount(ctx, state.NewAccount{
		Name:      name,
		Display:   display,
		PwHash:    hash,
		Role:      role,
		CreatedNs: s.now(),
	}, func(userID int64) ([]byte, uint32, error) {
		sealed, serr := s.sealNTFor(userID, pw)
		if serr != nil {
			return nil, 0, serr
		}
		return sealed.Ciphertext, sealed.KeyVer, nil
	})
	if errors.Is(err, state.ErrNameTaken) {
		return 0, ErrNameTaken
	}
	if err != nil {
		return 0, err
	}
	if perr := s.republishCredentials(ctx); perr != nil {
		return id, perr
	}
	return id, nil
}

// SetPassword rehashes, re-derives and re-seals the file-sharing credential,
// invalidates every cached decision and republishes.
//
// Sessions survive: a session is not a credential, and signing somebody out
// of every device because they changed their own password is a surprise
// rather than a security property.
func (s *Service) SetPassword(ctx context.Context, userID int64, newPW secret.Secret) error {
	if newPW.Len() < MinPasswordLen {
		return ErrWeakPassword
	}
	hash, err := s.Hash(ctx, newPW)
	if err != nil {
		return err
	}
	sealed, err := s.sealNTFor(userID, newPW)
	if err != nil {
		return err
	}
	if err := s.store.SetAccountPassword(ctx, userID, hash, sealed.Ciphertext, sealed.KeyVer); err != nil {
		if errors.Is(err, state.ErrNoSuchAccount) {
			return ErrCredentials
		}
		return err
	}
	s.bumpGeneration()
	return s.republishCredentials(ctx)
}

// VerifyAccountPassword is the check every credential-changing screen runs
// first.
//
// A live session is not enough on its own: a session is what somebody who
// walked past an unlocked screen already has, and the credentials these
// screens create outlive the session that created them.
//
// An account that does not exist is a failed check rather than an error, and
// costs the same as a real one, so the two cannot be told apart by timing.
func (s *Service) VerifyAccountPassword(
	ctx context.Context, userID int64, pw secret.Secret,
) (bool, error) {
	acct, err := s.store.AccountByID(ctx, userID)
	if errors.Is(err, state.ErrNoSuchAccount) {
		return false, s.burnDecoy(ctx, pw)
	}
	if err != nil {
		return false, err
	}
	ok, _, err := s.Verify(ctx, acct.PwHash, pw)
	return ok, err
}

// DisableAccount stops an account signing in. The generation bump kills its
// live sessions and cached decisions; the republish takes it out of the
// credential file.
func (s *Service) DisableAccount(ctx context.Context, userID int64) error {
	return s.setDisabled(ctx, userID, true)
}

// EnableAccount restores one.
func (s *Service) EnableAccount(ctx context.Context, userID int64) error {
	return s.setDisabled(ctx, userID, false)
}

func (s *Service) setDisabled(ctx context.Context, userID int64, disabled bool) error {
	if err := s.store.SetAccountDisabled(ctx, userID, disabled); err != nil {
		return mapAccountErr(err)
	}
	s.bumpGeneration()
	return s.republishCredentials(ctx)
}

// DeleteUser erases an account together with everything it owned.
func (s *Service) DeleteUser(ctx context.Context, userID int64) error {
	if err := s.store.DeleteAccount(ctx, userID); err != nil {
		return mapAccountErr(err)
	}
	s.bumpGeneration()
	// The credential has to leave the published file too, or the deleted
	// account keeps working over the older protocol.
	return s.republishCredentials(ctx)
}

// mapAccountErr turns the store's account sentinels into this package's.
func mapAccountErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, state.ErrNoSuchAccount):
		return ErrCredentials
	case errors.Is(err, state.ErrLastAdmin):
		return ErrLastAdmin
	case errors.Is(err, state.ErrNameTaken):
		return ErrNameTaken
	default:
		return err
	}
}
