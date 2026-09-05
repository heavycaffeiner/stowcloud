package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The login flow and the account-enumeration defence around it.
//
// An unknown name verifies against a fixed decoy hash carrying the configured
// parameters, so the cost and the timing match a real account and the answer
// is indistinguishable from a wrong password. A lookup that short-circuited
// would make that answer arrive eighty milliseconds sooner, and a response
// identical in content but faster is still an oracle.

// LoginRequest supplies everything Login requires beyond what the account
// itself holds.
type LoginRequest struct {
	Name     string
	Password secret.Secret

	// Factor is a presented second-factor code, empty until the client has
	// been asked for one.
	Factor string

	IP  string
	UA  string
	AMR int64
}

// Login runs the whole flow and returns one error for every credential
// failure, because distinguishing them is an oracle.
func (s *Service) Login(
	ctx context.Context, req LoginRequest, sessionTTL time.Duration,
) (Session, error) {
	if !s.limit.Allow(req.IP) {
		return Session{}, ErrRateLimited
	}

	acct, err := s.store.AccountByName(ctx, req.Name)
	if errors.Is(err, state.ErrNoSuchAccount) {
		if derr := s.burnDecoy(ctx, req.Password); derr != nil {
			return Session{}, derr
		}
		// Logged without an actor, since no account can be credited, and with
		// the attempted name as the target. A sequence of guesses against a
		// single name is exactly what this log is consulted to reveal.
		s.recordLoginFailure(ctx, nil, req)
		return Session{}, ErrCredentials
	}
	if err != nil {
		return Session{}, err
	}

	// An account linked to an external single sign-on provider has its local
	// password disabled: the provider is the credential that replaced it.
	// Burn a decoy to prevent timing oracles.
	if acct.PwHash == "" {
		if derr := s.burnDecoy(ctx, req.Password); derr != nil {
			return Session{}, derr
		}
		s.recordLoginFailure(ctx, &acct.ID, req)
		return Session{}, ErrCredentials
	}
	if link, lerr := s.store.OIDCLinkOf(ctx, acct.ID); lerr == nil && link.Issuer != "" {
		if derr := s.burnDecoy(ctx, req.Password); derr != nil {
			return Session{}, derr
		}
		s.recordLoginFailure(ctx, &acct.ID, req)
		return Session{}, ErrCredentials
	}

	ok, stale, err := s.Verify(ctx, acct.PwHash, req.Password)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		s.recordLoginFailure(ctx, &acct.ID, req)
		return Session{}, ErrCredentials
	}

	// Hashes verified under older parameters are upgraded here, so increasing
	// the cost benefits existing accounts rather than only new ones.
	if stale {
		if perr := s.SetPassword(ctx, acct.ID, req.Password); perr != nil {
			return Session{}, perr
		}
	}

	// After the password, so a disabled account with a wrong password still
	// answers the credential error rather than revealing that it exists.
	if acct.Disabled {
		return Session{}, ErrAccountDisabled
	}

	if acct.TOTPEnrolled {
		if req.Factor == "" {
			// The one distinguishable refusal, by design: the client has to
			// know to ask, and the password has already verified, so nothing
			// is leaked to a caller who does not hold it.
			return Session{}, ErrSecondFactor
		}
		accepted, ferr := s.VerifyTOTP(ctx, acct.ID, req.Factor, s.now())
		if ferr != nil {
			return Session{}, ferr
		}
		if !accepted {
			s.recordLoginFailure(ctx, &acct.ID, req)
			return Session{}, ErrCredentials
		}
	}

	// The credential the protocol needs is derived from this plaintext, which
	// exists nowhere else. Best effort: a deployment with no sidecar, or a
	// write that fails, must not refuse a sign-in that has already verified.
	if berr := s.backfillSMBSecret(ctx, acct, req.Password); berr != nil {
		s.warn("the SMB credential could not be restored on sign-in", berr)
	}

	sess, err := s.CreateSession(ctx, acct.ID, req.IP, req.UA, req.AMR, sessionTTL)
	if err != nil {
		return Session{}, err
	}

	// The session is live from this point, so failing to record it must not be
	// surfaced as a failed sign-in. That happened once: the audit write's error
	// propagated, the caller responded with a rejection, and the user was told
	// their credentials were wrong while holding a working session.
	if aerr := s.store.AppendAudit(ctx, state.AuditEntry{
		TsNs:  s.now(),
		Actor: &acct.ID,
		Event: EventLogin,
		IP:    req.IP,
		UA:    req.UA,
		OK:    true,
	}); aerr != nil {
		s.warn("the login was not recorded in the audit log", aerr)
	}
	return sess, nil
}

// recordLoginFailure writes the refusal.
//
// A log holding only successes answers the wrong question: what an operator
// comes to it for is the attempt that should not have been made.
func (s *Service) recordLoginFailure(ctx context.Context, actor *int64, req LoginRequest) {
	if err := s.store.AppendAudit(ctx, state.AuditEntry{
		TsNs:   s.now(),
		Actor:  actor,
		Event:  EventLogin,
		Target: req.Name,
		IP:     req.IP,
		UA:     req.UA,
		OK:     false,
	}); err != nil {
		s.warn("a failed login was not recorded in the audit log", err)
	}
}

// burnDecoy verifies a password against the decoy hash and discards the
// answer, so an account that does not exist costs what a real one costs.
func (s *Service) burnDecoy(ctx context.Context, pw secret.Secret) error {
	decoy, err := s.decoyHash(ctx)
	if err != nil {
		return err
	}
	_, _, err = s.Verify(ctx, decoy, pw)
	return err
}

// decoyHash is the once-per-process hash of a random secret, computed under
// the gate exactly like any other.
func (s *Service) decoyHash(ctx context.Context) (string, error) {
	s.decoyOnce.Do(func() {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			s.decoyErr = err
			return
		}
		s.decoy, s.decoyErr = s.Hash(ctx, secret.New(b))
	})
	return s.decoy, s.decoyErr
}

// VerifyPassword resolves an account name and password to a principal,
// through the three tiers.
//
// It is the path a protocol that presents a credential per request takes: the
// cached decision is what keeps a sync client from paying for the memory-hard
// function hundreds of times a minute.
func (s *Service) VerifyPassword(ctx context.Context, name string, pw secret.Secret) (Principal, error) {
	gen := s.Generation()
	key, keyed := s.cache.credKey(name, pw.Reveal())
	if keyed {
		if out, ok := s.cache.credLookup(key, gen); ok {
			if !out.Accepted {
				return Principal{}, ErrCredentials
			}
			if out.Principal.Disabled {
				return Principal{}, ErrAccountDisabled
			}
			return out.Principal, nil
		}
	}

	acct, err := s.store.AccountByName(ctx, name)
	if errors.Is(err, state.ErrNoSuchAccount) {
		if derr := s.burnDecoy(ctx, pw); derr != nil {
			return Principal{}, derr
		}
		if keyed {
			s.cache.credStore(key, Outcome{}, gen)
		}
		return Principal{}, ErrCredentials
	}
	if err != nil {
		return Principal{}, err
	}

	if acct.PwHash == "" {
		if derr := s.burnDecoy(ctx, pw); derr != nil {
			return Principal{}, derr
		}
		if keyed {
			s.cache.credStore(key, Outcome{}, gen)
		}
		return Principal{}, ErrCredentials
	}
	if link, lerr := s.store.OIDCLinkOf(ctx, acct.ID); lerr == nil && link.Issuer != "" {
		if derr := s.burnDecoy(ctx, pw); derr != nil {
			return Principal{}, derr
		}
		if keyed {
			s.cache.credStore(key, Outcome{}, gen)
		}
		return Principal{}, ErrCredentials
	}

	ok, _, err := s.Verify(ctx, acct.PwHash, pw)
	if err != nil {
		return Principal{}, err
	}
	if !ok {
		if keyed {
			s.cache.credStore(key, Outcome{}, gen)
		}
		return Principal{}, ErrCredentials
	}

	principal := principalOf(acct)
	if keyed {
		s.cache.credStore(key, Outcome{Accepted: true, Principal: principal}, gen)
	}
	if acct.Disabled {
		return Principal{}, ErrAccountDisabled
	}
	return principal, nil
}

// RememberConnection records that a presented credential digest resolved to a
// principal, and RecallConnection reads it back.
//
// The digest is the transport's, of whatever header it saw. Keeping the memo
// here rather than in the transport is what makes the generation counter able
// to invalidate it: a revocation must not have to find and clear a cache that
// lives somewhere else.
func (s *Service) RememberConnection(digest [32]byte, p Principal) {
	s.cache.connStore(digest, p, s.Generation())
}

// RecallConnection returns the principal a digest already resolved to.
func (s *Service) RecallConnection(digest [32]byte) (Principal, bool) {
	return s.cache.connLookup(digest, s.Generation())
}
