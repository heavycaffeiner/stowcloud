package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// The login flow, and the account-enumeration defence that protects it.
//
// An unknown username verifies against a fixed decoy hash carrying the
// configured parameters, so the cost and the timing match a real account and
// the response is indistinguishable from the wrong-password case. A lookup
// that short-circuits would make the response 80 ms faster, and a response
// identical in content and faster is still an oracle.

const (
	loginWindow      = 5 * time.Minute
	loginMaxAttempts = 10
)

// LoginRequest is everything Login needs that the account does not carry.
type LoginRequest struct {
	Name     string
	Password secret.Secret

	// Factor is a presented second-factor code. Empty when the client has not
	// been offered one yet.
	Factor string

	IP  string
	UA  string
	AMR int
}

// decoyPHC returns the one-time hash of a random secret, computed under the
// gate exactly like any other hash, once per process.
func (s *Service) decoyPHC(ctx context.Context) (string, error) {
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

// Login runs the whole flow: rate-limit check, user lookup, password
// verification (against a decoy for an unknown user, so cost and timing do not
// distinguish), second factor if enrolled, session creation and an audit
// write. It returns one error for every credential failure, because
// distinguishing them is an oracle.
func (s *Service) Login(ctx context.Context, req LoginRequest, sessionTTL time.Duration) (Session, error) {
	if !s.ratelimit.Allow(req.IP) {
		return Session{}, ErrRateLimited
	}

	user, err := s.userByName(ctx, req.Name)
	if err == errUserMissing {
		// Burn the same Argon2 cost as a real account before answering, so the
		// timing does not reveal that no such account exists.
		if decoy, derr := s.decoyPHC(ctx); derr == nil {
			s.Verify(ctx, decoy, req.Password) //nolint:errcheck // the answer is discarded by design; only the cost is wanted.
		}
		return Session{}, ErrCredentials
	}
	if err != nil {
		return Session{}, err
	}

	ok, stale, err := s.Verify(ctx, user.pwHash, req.Password)
	if err != nil {
		return Session{}, err
	}
	if !ok {
		return Session{}, ErrCredentials
	}

	// A validated hash under older parameters is upgraded now, so raising the
	// cost protects existing accounts and not only new ones.
	if stale {
		if perr := s.SetPassword(ctx, user.id, req.Password); perr != nil {
			return Session{}, perr
		}
	}

	if user.disabled {
		return Session{}, ErrAccountDisabled
	}

	has2fa, err := s.hasTOTP(ctx, user.id)
	if err != nil {
		return Session{}, err
	}
	if has2fa {
		if req.Factor == "" {
			return Session{}, ErrSecondFactor
		}
		accepted, ferr := s.VerifyTOTP(ctx, user.id, req.Factor, s.now())
		if ferr != nil {
			return Session{}, ferr
		}
		if !accepted {
			return Session{}, ErrCredentials
		}
	}

	sess, err := s.CreateSession(ctx, user.id, req.IP, req.UA, req.AMR, sessionTTL, 0)
	if err != nil {
		return Session{}, err
	}
	if err := s.audit(ctx, sql.NullInt64{Int64: user.id, Valid: true},
		"login", "", req.IP, req.UA, 1, ""); err != nil {
		return Session{}, err
	}
	return sess, nil
}

// hasTOTP reports whether an account has a second factor enrolled.
func (s *Service) hasTOTP(ctx context.Context, userID int64) (bool, error) {
	var n int
	err := s.st.SQL().QueryRowContext(ctx, sqlCountTOTP, userID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
