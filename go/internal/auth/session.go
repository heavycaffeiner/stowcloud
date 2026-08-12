package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// sessionTokenLen is the entropy of a session token: 256 bits from
// crypto/rand, returned once and stored only as its SHA-256 so a state.db
// read does not yield a live credential.
const sessionTokenLen = 32

// defaultSessionIdle is the idle window a session survives without use. The
// absolute lifetime is set by CreateSession's caller.
const defaultSessionIdle = 30 * time.Minute

// Session is a freshly minted session. Token is shown once and never stored.
type Session struct {
	Token  secret.Secret
	UserID int64
}

// CreateSession mints a session for an authenticated account.
func (s *Service) CreateSession(ctx context.Context, userID int64, ip, ua string, amr int, absoluteTTL, idleTTL time.Duration) (Session, error) {
	token := make([]byte, sessionTokenLen)
	if _, err := rand.Read(token); err != nil {
		return Session{}, fmt.Errorf("minting a session token: %w", err)
	}
	if idleTTL <= 0 {
		idleTTL = defaultSessionIdle
	}
	hash := sha256.Sum256(token)
	now := s.now()
	err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlInsertSession,
			hash[:], userID, now, now, now+idleTTL.Nanoseconds(), ip, ua, amr)
		return err
	})
	if err != nil {
		return Session{}, err
	}
	return Session{Token: secret.New(token), UserID: userID}, nil
}

// RevokeSession destroys a session and bumps the generation, so a client that
// reuses its connection memo cannot come back through a cached principal.
func (s *Service) RevokeSession(ctx context.Context, token secret.Secret) error {
	hash := sha256.Sum256(token.Reveal())
	err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteSession, hash[:])
		return err
	})
	if err != nil {
		return err
	}
	s.bumpGeneration()
	return nil
}

// LookupSession resolves a presented token to a principal, checking absolute
// and idle expiry and refusing an account that was disabled since the session
// began. The presented hash is compared in constant time against the stored
// row, so a timing side channel cannot distinguish a real from a forged
// token.
func (s *Service) LookupSession(ctx context.Context, token secret.Secret) (Principal, error) {
	hash := sha256.Sum256(token.Reveal())

	var (
		idHash                              []byte
		userID, created, lastSeen, absolute int64
		ip, ua                              string
		amr                                 int
	)
	err := s.st.SQL().QueryRowContext(ctx, sqlReadSession, hash[:]).
		Scan(&idHash, &userID, &created, &lastSeen, &absolute, &ip, &ua, &amr)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrCredentials
	}
	if err != nil {
		return Principal{}, err
	}
	if subtle.ConstantTimeCompare(hash[:], idHash) != 1 {
		return Principal{}, ErrCredentials
	}

	now := s.now()
	if now >= absolute || now > lastSeen+defaultSessionIdle.Nanoseconds() {
		if werr := s.write(ctx, func(tx *sql.Tx) error {
			_, derr := tx.ExecContext(ctx, sqlDeleteSession, hash[:])
			return derr
		}); werr != nil {
			// The session is dead either way; failing to sweep it only costs
			// an expired row, which is not worth failing this request over.
			slog.Warn("an expired session could not be removed", slog.Any("error", werr))
		}
		return Principal{}, ErrCredentials
	}

	user, err := s.userByID(ctx, userID)
	if err != nil {
		return Principal{}, ErrCredentials
	}
	if user.disabled {
		return Principal{}, ErrAccountDisabled
	}

	if werr := s.write(ctx, func(tx *sql.Tx) error {
		_, terr := tx.ExecContext(ctx, sqlTouchSession, s.now(), hash[:])
		return terr
	}); werr != nil {
		// The session still validates; only its last-seen stamp is cold, which
		// a later request will refresh.
		slog.Warn("a session's last-used stamp could not be updated", slog.Any("error", werr))
	}
	return Principal{UserID: userID, Display: user.display, Disabled: user.disabled}, nil
}
