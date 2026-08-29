// Linux only, because it serves a Linux-only engine.
//go:build linux

// The setup gate: the one window in which a deployment with no accounts can
// be given its first administrator.
//
// Two things close the gate and they are not the same. The token is what
// proves a request came from whoever started the process; the account count is
// what says setup is finished. The account count is the authority: a stale
// token file on disk cannot open a gate that an existing account has closed.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
)

// SetupTokenLifetime is how long an issued token stays usable.
//
// Short because the token is printed to a terminal and written to a file, and
// both outlive the moment it was needed. Fifteen minutes is long enough to
// read it off a console and short enough that a forgotten one is not a
// standing key to the deployment.
const SetupTokenLifetime = 15 * time.Minute

// Errors the gate answers with. They are separate because they call for
// different actions: the first says finish setting up, the second says the
// token is wrong or old, and the third says setup is over.
var (
	// ErrSetupNotIssued is a request arriving before any token was minted.
	ErrSetupNotIssued = errors.New("no setup token has been issued")
	// ErrSetupToken is a wrong or expired token.
	ErrSetupToken = errors.New("the setup token is not valid")
	// ErrSetupClosed is a deployment that already has an account.
	ErrSetupClosed = errors.New("setup is already complete")
)

// AccountCounter is how the gate asks whether setup is finished.
type AccountCounter interface {
	CountUsers(ctx context.Context) (int, error)
}

// SetupGate guards first-administrator creation.
//
// One mutex covers issuing, verifying and using a token, so two requests
// arriving together cannot both pass and create two first administrators.
type SetupGate struct {
	clk      clock.Clock
	accounts AccountCounter

	mu sync.Mutex
	// digest is the SHA-256 of the issued token. The plaintext is not kept:
	// it lives only long enough to be written and printed.
	digest    []byte
	expiresNs int64
	used      bool
}

// NewSetupGate builds the gate.
func NewSetupGate(clk clock.Clock, accounts AccountCounter) *SetupGate {
	return &SetupGate{clk: clk, accounts: accounts}
}

// Issue mints a token and returns its plaintext once.
//
// The caller writes it durably and prints it, then lets it go. Only the digest
// and the expiry stay here, so a memory dump of a running server does not
// yield a usable token.
func (g *SetupGate) Issue(ctx context.Context) (string, error) {
	open, err := g.Open(ctx)
	if err != nil {
		return "", err
	}
	if !open {
		// Reissue after an account exists is refused. The gate is closed by
		// then, so a token minted now could never be used, and printing one
		// would suggest otherwise.
		return "", ErrSetupClosed
	}

	var b [32]byte
	if _, rerr := rand.Read(b[:]); rerr != nil {
		return "", fmt.Errorf("minting a setup token: %w", rerr)
	}
	plaintext := hex.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(plaintext))

	g.mu.Lock()
	defer g.mu.Unlock()
	g.digest = sum[:]
	g.expiresNs = g.clk.Nanos() + int64(SetupTokenLifetime)
	g.used = false

	return plaintext, nil
}

// Open reports whether setup is still available.
//
// A read failure closes the gate rather than opening it. The question is
// whether any account exists, and a database that cannot answer is not an
// answer of "no".
func (g *SetupGate) Open(ctx context.Context) (bool, error) {
	n, err := g.accounts.CountUsers(ctx)
	if err != nil {
		return false, fmt.Errorf("%w: the account count could not be read: %w", ErrSetupClosed, err)
	}
	return n == 0, nil
}

// Verify checks a presented token without consuming it.
func (g *SetupGate) Verify(ctx context.Context, presented string) error {
	open, err := g.Open(ctx)
	if err != nil {
		return err
	}
	if !open {
		return ErrSetupClosed
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	return g.check(presented)
}

// Use verifies a token and consumes it, running fn while the gate is held.
//
// fn runs under the same mutex as the check, which is what stops two requests
// from both passing and creating two first administrators. It is the caller's
// job to keep fn short for that reason.
//
// The token is consumed only when fn succeeds. A failed attempt leaves it
// usable, because the alternative is an operator whose one token is spent on
// a password the server rejected.
func (g *SetupGate) Use(ctx context.Context, presented string, fn func(context.Context) error) error {
	open, err := g.Open(ctx)
	if err != nil {
		return err
	}
	if !open {
		return ErrSetupClosed
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if cerr := g.check(presented); cerr != nil {
		return cerr
	}
	if ferr := fn(ctx); ferr != nil {
		return ferr
	}
	g.used = true
	return nil
}

// check compares a presented token. The caller holds the mutex.
func (g *SetupGate) check(presented string) error {
	if len(g.digest) == 0 {
		// Nothing was issued. Distinct from a wrong token: a stale plaintext
		// file on disk must not pass a gate that never minted anything.
		return ErrSetupNotIssued
	}
	// Expiry first, so an expired token is refused without the comparison
	// running at all.
	if g.clk.Nanos() >= g.expiresNs {
		return fmt.Errorf("%w: it expired", ErrSetupToken)
	}
	if g.used {
		return fmt.Errorf("%w: it was already used", ErrSetupToken)
	}

	sum := sha256.Sum256([]byte(presented))
	if subtle.ConstantTimeCompare(sum[:], g.digest) != 1 {
		return ErrSetupToken
	}
	return nil
}
