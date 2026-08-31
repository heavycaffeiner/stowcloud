package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// sessionTokenLen is the entropy of a session token: 256 bits, returned once
// and stored only as its digest, so a read of the database does not yield a
// live credential.
const sessionTokenLen = 32

// The two windows a session lives inside. The idle one is applied at lookup,
// because the schema keeps no per-session idle column and the last-seen stamp
// is what it would be derived from anyway.
const (
	defaultSessionIdle     = 30 * time.Minute
	defaultSessionLifetime = 30 * 24 * time.Hour
)

// Session describes a newly issued session. The token is displayed once and
// never persisted.
type Session struct {
	Token  secret.Secret
	UserID int64
}

// SessionRow is one live session as its owner sees it. IDHash is the stored
// digest, never the token, so a listing that leaks yields nothing usable.
type SessionRow struct {
	IDHash     []byte
	CreatedNs  int64
	LastSeenNs int64
	AbsoluteNs int64
	IP, UA     string
	AMR        int64
}

// CreateSession mints a session for an account that has just authenticated.
// lifetime is the absolute window; zero takes the default.
func (s *Service) CreateSession(
	ctx context.Context, userID int64, ip, ua string, amr int64, lifetime time.Duration,
) (Session, error) {
	token := make([]byte, sessionTokenLen)
	if _, err := rand.Read(token); err != nil {
		return Session{}, fmt.Errorf("minting a session token: %w", err)
	}
	if lifetime <= 0 {
		lifetime = defaultSessionLifetime
	}
	hash := sha256.Sum256(token)
	now := s.now()
	if err := s.store.CreateSession(ctx, state.Session{
		IDHash:     hash[:],
		User:       userID,
		CreatedNs:  now,
		LastSeenNs: now,
		AbsoluteNs: now + lifetime.Nanoseconds(),
		IP:         ip,
		UA:         ua,
		AMR:        amr,
	}); err != nil {
		return Session{}, err
	}
	return Session{Token: secret.New(token), UserID: userID}, nil
}

// LookupSession resolves a presented token to a principal.
//
// The presented digest is compared against the stored one in constant time,
// so a timing side channel cannot tell a real token from a forged one; both
// expiry windows are checked; an expired row is swept best-effort, because
// the session is dead either way and failing this request over a sweep would
// be a refusal nobody can act on.
func (s *Service) LookupSession(ctx context.Context, token secret.Secret) (Principal, error) {
	hash := sha256.Sum256(token.Reveal())

	row, err := s.store.SessionByHash(ctx, hash[:])
	if errors.Is(err, state.ErrNoSuchSession) {
		return Principal{}, ErrCredentials
	}
	if err != nil {
		return Principal{}, err
	}
	if subtle.ConstantTimeCompare(hash[:], row.IDHash) != 1 {
		return Principal{}, ErrCredentials
	}

	now := s.now()
	if now >= row.AbsoluteNs || now > row.LastSeenNs+defaultSessionIdle.Nanoseconds() {
		if derr := s.store.DeleteSession(ctx, hash[:]); derr != nil {
			s.warn("an expired session could not be removed", derr)
		}
		return Principal{}, ErrCredentials
	}

	acct, err := s.store.AccountByID(ctx, row.User)
	if errors.Is(err, state.ErrNoSuchAccount) {
		return Principal{}, ErrCredentials
	}
	if err != nil {
		return Principal{}, err
	}
	if acct.Disabled {
		return Principal{}, ErrAccountDisabled
	}

	if terr := s.store.TouchSession(ctx, hash[:], now); terr != nil {
		// The session still validates; only its stamp is cold, and the next
		// request refreshes it.
		s.warn("a session's last-used stamp could not be updated", terr)
	}
	return principalOf(acct), nil
}

// RevokeSession destroys the caller's own session and bumps the generation,
// so a client reusing its connection memo cannot come back through a cached
// principal.
func (s *Service) RevokeSession(ctx context.Context, token secret.Secret) error {
	hash := sha256.Sum256(token.Reveal())
	if err := s.store.DeleteSession(ctx, hash[:]); err != nil {
		return err
	}
	s.bumpGeneration()
	return nil
}

// RevokeSessionByHash destroys one of an account's own sessions, named by the
// stored digest. It is the "sign out this device" path: the client holds row
// digests, not tokens, and the predicate carries the owner too.
func (s *Service) RevokeSessionByHash(ctx context.Context, userID int64, hash []byte) error {
	if err := s.store.DeleteSessionOfUser(ctx, userID, hash); err != nil {
		return err
	}
	s.bumpGeneration()
	return nil
}

// RevokeSessionsOf terminates all sessions belonging to an account and returns
// the count.
func (s *Service) RevokeSessionsOf(ctx context.Context, userID int64) (int64, error) {
	n, err := s.store.DeleteSessionsOf(ctx, userID)
	if err != nil {
		return 0, err
	}
	s.bumpGeneration()
	return n, nil
}

// Sessions lists an account's own live sessions, most recently used first.
func (s *Service) Sessions(ctx context.Context, userID int64) ([]SessionRow, error) {
	rows, err := s.store.SessionsOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]SessionRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, SessionRow{
			IDHash:     r.IDHash,
			CreatedNs:  r.CreatedNs,
			LastSeenNs: r.LastSeenNs,
			AbsoluteNs: r.AbsoluteNs,
			IP:         r.IP,
			UA:         r.UA,
			AMR:        r.AMR,
		})
	}
	return out, nil
}
