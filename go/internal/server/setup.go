// Package server assembles the product: the typed config, the TLS listener,
// the wiring that binds the chain to the route table, the setup gate, and the
// shutdown and health surfaces. It is the only package that owns a listener.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/httpapi/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// The setup token's lifetime. Fifteen minutes, and single use: a token that
// is only single-use sits valid in a log forever until someone finds it.
const setupTokenTTL = 15 * time.Minute

// minSetupPassword is the floor for the first administrator's password, and
// the number the weak-password refusal names in its detail.
const minSetupPassword = 10

// SetupOutcome is a completed bootstrap's result.
type SetupOutcome = handler.SetupOutcome

// SetupError is a bootstrap refusal.
type SetupError = handler.SetupError

// SetupGate is the first-run bootstrap: it holds the one-time token, the
// expiry, the durable "an account exists" check and the single-use slot.
type SetupGate struct {
	mu   sync.Mutex
	auth *auth.Service
	clk  clock.Clock
	dir  string

	issued bool
	hash   [32]byte
	expiry int64
	plain  string // held only until the file is written and the line is printed
	used   bool
}

// NewSetupGate reports the gate's state without minting anything: a fresh
// deployment gets a gate ready to Issue, one with an account already closes.
// The caller decides when the token is minted, so the serve path and the
// stowcloud setup CLI each issue exactly once.
func NewSetupGate(ctx context.Context, svc *auth.Service, clk clock.Clock, dataDir string) (*SetupGate, error) {
	return &SetupGate{auth: svc, clk: clk, dir: dataDir}, nil
}

// required is the durable gate, erring on the side of closed: an account
// table that cannot be read shows a login screen nobody can use rather than
// exposing admin creation on a database we could not inspect.
func (g *SetupGate) required(ctx context.Context) bool {
	n, err := g.auth.CountUsers(ctx)
	if err != nil {
		return false
	}
	return n == 0
}

// Issue mints the token, persists it and prints it. The plaintext is held
// only long enough for those two acts. out may be nil when the caller only
// wants the file.
func (g *SetupGate) Issue(out *os.File) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.issue(out)
}

func (g *SetupGate) issue(out *os.File) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("minting a setup token: %w", err)
	}
	plain := hex.EncodeToString(raw)
	// The token a client presents is the hex string; the store holds a
	// verification hash of exactly that string.
	hash := sha256.Sum256([]byte(plain))
	g.hash = hash
	g.expiry = g.clk.Nanos() + setupTokenTTL.Nanoseconds()
	g.issued = true
	g.plain = plain
	if err := os.WriteFile(filepath.Join(g.dir, "setup-token"), []byte(plain+"\n"), 0o600); err != nil {
		return fmt.Errorf("persisting the setup token: %w", err)
	}
	if out != nil {
		if _, err := fmt.Fprintf(out, "setup token (valid for %s): %s\n", setupTokenTTL, plain); err != nil {
			return err
		}
	}
	g.plain = ""
	return nil
}

// IsRequired is what GET /api/setup answers: a bare boolean, nothing else.
func (g *SetupGate) IsRequired(ctx context.Context) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.required(ctx)
}

// Complete verifies the token and creates the first administrator. One lock
// covers the whole sequence: two requests arriving with the same valid token
// cannot both pass, so the token works exactly once even when the second is
// in flight as the first commits.
func (g *SetupGate) Complete(ctx context.Context, token, username string, pw secret.Secret, ip string) (SetupOutcome, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// The durable gate first: an account exists, so setup is over, no matter
	// what this process holds in memory.
	if !g.required(ctx) {
		g.used = true
		g.removeTokenFile()
		return SetupOutcome{}, SetupError{Kind: handler.SetupCompleted}
	}

	// Expiry is checked before the token is compared, so a caller cannot time
	// the comparison and learn whether a token is merely old.
	if !g.issued || g.used || g.clk.Nanos() > g.expiry {
		return SetupOutcome{}, SetupError{Kind: handler.SetupExpired}
	}
	tokHash := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(tokHash[:], g.hash[:]) != 1 {
		return SetupOutcome{}, SetupError{Kind: handler.SetupInvalidToken}
	}

	if err := validateUsername(username); err != "" {
		return SetupOutcome{}, SetupError{Kind: handler.SetupInvalidUsername, Field: err}
	}
	if pw.Len() < minSetupPassword {
		return SetupOutcome{}, SetupError{Kind: handler.SetupWeakPassword}
	}

	id, err := g.auth.CreateAdmin(ctx, username, username, pw)
	if err != nil {
		return SetupOutcome{}, err
	}
	g.used = true
	g.removeTokenFile()
	return SetupOutcome{UserID: id, Username: username}, nil
}

// Reissue is the stowcloud setup CLI: re-print and re-persist a token for the
// case where the first one scrolled out of a log before anyone read it. It is
// refused when an account already exists, for the same reason the gate is.
func (g *SetupGate) Reissue(ctx context.Context, out *os.File) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.required(ctx) {
		return errors.New("setup is complete; an administrator already exists")
	}
	g.used = false
	return g.issue(out)
}

func (g *SetupGate) removeTokenFile() {
	_ = os.Remove(filepath.Join(g.dir, "setup-token")) //nolint:errcheck // a leftover token file is cleaned next start.
}

// validateUsername returns "" for a usable name and a fixed reason otherwise.
// The reason is never an echo of the input.
func validateUsername(name string) string {
	const extra = ".-_@+"
	if name == "" {
		return "must not be empty"
	}
	if len(name) > 64 {
		return "must be at most 64 characters"
	}
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		found := false
		for _, e := range extra {
			if c == e {
				found = true
				break
			}
		}
		if !found {
			return "may contain only letters, digits and . - _ @ +"
		}
	}
	return ""
}
