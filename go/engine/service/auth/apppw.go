package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// App passwords live on the side a person types them, so they are minted in
// an alphabet that survives being read off a screen and stored only as their
// digest. They carry 256 bits, so a verified one skips the memory-hard path
// through a short cache rather than paying for a function their entropy does
// not need.

// appPWTokenLen is the entropy of one token.
const appPWTokenLen = 32

// Scope bounds what an app password can reach: a permission bitmask together
// with an optional set of share labels. An empty set means every share visible
// to the account.
//
// It rides in the request context as a typed value the route table inspects, so
// routes reject by default until their required scope is stated. Performing that
// check is the presentation layer's job; the type lives here because the
// credential does.
type Scope struct {
	Perms  uint16
	Shares []string
}

// AppPasswordRow is one credential as its owner sees it. The token is not
// stored and therefore never listed.
type AppPasswordRow struct {
	ID         int64
	Name       string
	ScopePerms uint16
	Shares     []string
	CreatedNs  int64
	ExpiresNs  *int64
	LastUsedNs *int64
}

// CreateAppPassword mints one, stores its digest and scope, and returns the
// token once.
func (s *Service) CreateAppPassword(
	ctx context.Context, userID int64, name string, scope Scope, expires time.Duration,
) (string, error) {
	token, _, err := s.mintAppPassword(ctx, userID, name, scope, expires)
	return token, err
}

// CreateSyncCredential is the device-login policy, owned here rather than by
// whatever wiring happens to call it: a client that completed an approved
// device login gets the full filesystem scope, every share it can currently
// see, and no silent expiry.
//
// It returns the row id as well as the token, so a delivery that fails can
// revoke exactly that credential without anybody retaining the plaintext.
func (s *Service) CreateSyncCredential(
	ctx context.Context, userID int64, name string,
) (token string, id int64, err error) {
	return s.mintAppPassword(ctx, userID, name, Scope{Perms: SyncScopePerms}, 0)
}

// SyncScopePerms is the permission set a device-login credential carries: the
// whole filesystem surface, because the client it is minted for is a file
// sync client and a narrower set would be a credential that cannot do what it
// was created to do.
const SyncScopePerms = 0xFFFF

func (s *Service) mintAppPassword(
	ctx context.Context, userID int64, name string, scope Scope, expires time.Duration,
) (string, int64, error) {
	raw := make([]byte, appPWTokenLen)
	if _, err := rand.Read(raw); err != nil {
		return "", 0, fmt.Errorf("minting an app password: %w", err)
	}
	token := crockfordEncode(raw)
	hash := sha256.Sum256([]byte(token))

	var expiresNs *int64
	if expires > 0 {
		v := s.now() + expires.Nanoseconds()
		expiresNs = &v
	}
	id, err := s.store.CreateAppPassword(ctx, state.NewAppPassword{
		TokenHash:  hash[:],
		User:       userID,
		Name:       name,
		ScopePerms: scope.Perms,
		Shares:     scope.Shares,
		CreatedNs:  s.now(),
		ExpiresNs:  expiresNs,
	})
	if err != nil {
		return "", 0, err
	}
	s.bumpGeneration()
	return token, id, nil
}

// VerifyAppPassword resolves a presented token to its principal and scope.
//
// Every refusal is one answer: a token that folds to nothing, a digest with
// no row, a requested wipe, an expiry passed, an owner missing or disabled.
// Distinguishing them would say which of those a presented string was.
func (s *Service) VerifyAppPassword(ctx context.Context, token string) (Principal, Scope, error) {
	folded, err := crockfordFold(token)
	if err != nil {
		return Principal{}, Scope{}, ErrCredentials
	}
	hash := sha256.Sum256([]byte(folded))
	gen := s.Generation()
	if p, scope, ok := s.cache.tokenLookup(hash, gen); ok {
		return p, scope, nil
	}

	row, err := s.store.AppPasswordByHash(ctx, hash[:])
	if errors.Is(err, state.ErrNoSuchAppPassword) {
		return Principal{}, Scope{}, ErrCredentials
	}
	if err != nil {
		return Principal{}, Scope{}, err
	}
	if row.WipeWanted {
		return Principal{}, Scope{}, ErrCredentials
	}
	if row.ExpiresNs != nil && s.now() > *row.ExpiresNs {
		return Principal{}, Scope{}, ErrCredentials
	}

	acct, err := s.store.AccountByID(ctx, row.User)
	if errors.Is(err, state.ErrNoSuchAccount) {
		return Principal{}, Scope{}, ErrCredentials
	}
	if err != nil {
		return Principal{}, Scope{}, err
	}
	if acct.Disabled {
		return Principal{}, Scope{}, ErrCredentials
	}

	principal := principalOf(acct)
	scope := Scope{Perms: row.ScopePerms, Shares: row.Shares}
	s.cache.tokenStore(hash, principal, scope, gen)
	return principal, scope, nil
}

// RevokeAppPassword destroys one and bumps the generation, so the bypass
// cache is inert the moment the revocation lands rather than a minute later.
func (s *Service) RevokeAppPassword(ctx context.Context, userID, id int64) error {
	if err := s.store.DeleteAppPassword(ctx, userID, id); err != nil {
		if errors.Is(err, state.ErrNoSuchAppPassword) {
			// Another account's credential and a nonexistent one produce the
			// same answer, so ids cannot be probed.
			return ErrNotFound
		}
		return err
	}
	s.bumpGeneration()
	return nil
}

// RequestWipe asks the device holding a credential to erase its local copy,
// and revokes the credential in the same statement.
//
// The message only arrives when the device next connects, so it is a request
// rather than a guarantee; revoking as well is what makes a device that never
// comes back stop working anyway.
func (s *Service) RequestWipe(ctx context.Context, userID, id int64) error {
	if err := s.store.RequestAppPasswordWipe(ctx, userID, id); err != nil {
		if errors.Is(err, state.ErrNoSuchAppPassword) {
			return ErrNotFound
		}
		return err
	}
	s.bumpGeneration()
	return nil
}

// AppPasswords lists an account's own, newest first.
func (s *Service) AppPasswords(ctx context.Context, userID int64) ([]AppPasswordRow, error) {
	rows, err := s.store.AppPasswordsOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]AppPasswordRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, AppPasswordRow{
			ID:         r.ID,
			Name:       r.Name,
			ScopePerms: r.ScopePerms,
			Shares:     r.Shares,
			CreatedNs:  r.CreatedNs,
			ExpiresNs:  r.ExpiresNs,
			LastUsedNs: r.LastUsedNs,
		})
	}
	return out, nil
}
