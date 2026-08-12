package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// App passwords live on the side the user types them, so they are minted in
// Crockford Base32 (no I, L, O, U) and stored only as their SHA-256. They are
// high-entropy, so they bypass Argon2 through a short tier-3 cache instead of
// the memory-hard path their low-frequency use would not need.

// appPWTokenLen is the entropy of an app password token.
const appPWTokenLen = 32

// recoverableCodeLen is the entropy of a recovery code.
const recoverableCodeLen = 16

// Scope is what an app password may reach: a permission bitmask and an
// optional set of share labels. It travels in the request context as a typed
// value the route table consults, so a new route is refused by default until
// its required scope is declared.
type Scope struct {
	Perms  uint16
	Shares []string
}

// CreateAppPassword mints an app password, stores its hash and scope, and
// returns the token once.
func (s *Service) CreateAppPassword(ctx context.Context, userID int64, name string, scope Scope, expires time.Duration) (string, error) {
	raw := make([]byte, appPWTokenLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("minting an app password: %w", err)
	}
	token := crockfordEncode(raw)
	hash := sha256.Sum256([]byte(token))
	var shares []byte
	for i, sh := range scope.Shares {
		if i > 0 {
			shares = append(shares, 0)
		}
		shares = append(shares, sh...)
	}
	var expiresNs any
	if expires > 0 {
		expiresNs = s.now() + expires.Nanoseconds()
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlInsertAppPassword,
			hash[:], userID, name, scope.Perms, shares, s.now(), expiresNs)
		return err
	})
	if err != nil {
		return "", err
	}
	s.bumpGeneration()
	return token, nil
}

// VerifyAppPassword resolves a presented token to its principal and scope,
// consulting the tier-3 cache first and falling back to the database for a
// token the cache does not hold.
func (s *Service) VerifyAppPassword(ctx context.Context, token string) (Principal, Scope, error) {
	folded, err := crockfordFold(token)
	if err != nil {
		return Principal{}, Scope{}, ErrCredentials
	}
	hash := sha256.Sum256([]byte(folded))
	if p, ok := s.cache.TokenLookup(hash, s.Generation()); ok {
		return p, Scope{}, nil
	}

	var (
		user, id      int64
		name          string
		scopePerms    uint16
		scopeShares   []byte
		expires       any
		wipeRequested bool
	)
	err = s.st.SQL().QueryRowContext(ctx, sqlReadAppPassword, hash[:]).
		Scan(&id, &user, &name, &scopePerms, &scopeShares, &expires, &wipeRequested)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, Scope{}, ErrCredentials
	}
	if err != nil {
		return Principal{}, Scope{}, err
	}
	if wipeRequested {
		return Principal{}, Scope{}, ErrCredentials
	}
	if expires != nil {
		if exp, ok := expires.(int64); ok && s.now() > exp {
			return Principal{}, Scope{}, ErrCredentials
		}
	}

	u, err := s.userByID(ctx, user)
	if err != nil || u.disabled {
		return Principal{}, Scope{}, ErrCredentials
	}
	scope := Scope{Perms: scopePerms}
	for _, part := range splitShareScope(scopeShares) {
		scope.Shares = append(scope.Shares, string(part))
	}
	principal := Principal{UserID: user, Display: u.display, Disabled: u.disabled}
	s.cache.TokenStore(hash, principal, s.Generation())
	return principal, scope, nil
}

// RevokeAppPassword destroys an app password and bumps the generation, so the
// tier-3 cache is inert the moment revocation lands.
func (s *Service) RevokeAppPassword(ctx context.Context, userID, id int64) error {
	err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlDeleteAppPassword, id, userID)
		return err
	})
	if err != nil {
		return err
	}
	s.bumpGeneration()
	return nil
}

// splitShareScope splits the NUL-separated share label list back out.
func splitShareScope(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == 0 {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
