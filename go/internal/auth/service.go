// Package auth is the three-tier verification path and the durable state that
// surrounds it: accounts, sessions, app passwords, TOTP, recovery codes,
// groups, the NT hash and the master key that seals them.
//
// The whole subsystem answers one question, which is "how few times must the
// KDF run", not "how strong a KDF". WebDAV sends hundreds of requests a
// minute with the same Basic credential; running Argon2id per request would
// make the server slower than the disk it fronts. The three tiers (a
// connection memo, an HMAC-keyed credential cache, and Argon2 itself) are the
// answer, and the permetted bound keeps their peak memory at 48 MiB times
// GateConcurrency.
package auth

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// Config is what New needs that the store and the clock do not imply.
type Config struct {
	Store *state.DB
	// StoreDir is where the master key file lives by default. It is the one
	// piece of the filesystem this package knows, and only for that key.
	StoreDir string

	Clock clock.Clock
	// PassdbPath is the file every SMB credential change republishes, or empty
	// when the deployment has no SMB sidecar. Phase 11 owns the file's format
	// for smbd; this phase owns the sink that rewrites it on every path.
	PassdbPath string

	// OnMembership, when set, is the one crossing that pushes a membership
	// change into the live ACL engine. It is wired by the layer that owns the
	// engine, keeping auth free of an ACL dependency.
	OnMembership func()
}

// Service is the auth subsystem. It is safe for concurrent use: the gate, the
// caches and the generation counter are each internally synchronised, and
// durable state goes through the store's single write path.
type Service struct {
	st    *state.DB
	dir   string
	clk   clock.Clock
	gate  *Gate
	cache *caches

	// gen is the auth_generation counter. Any credential change bumps it, and
	// every tier of the verification path compares its own generation against
	// it, so revocation is immediate on a surface that never re-reads the
	// database. It is in-process because the caches are in-process: after a
	// restart there is nothing to invalidate.
	gen atomic.Int64

	mk           *KeyRing
	passdb       string
	onMembership func()

	// ratelimit bounds login attempts per client address.
	ratelimit *limiter

	// decoy is a PHC hash of a random done-once at startup, verified against
	// for an unknown account so its cost and timing match a real one.
	decoyOnce sync.Once
	decoy     string
	decoyErr  error
}

// New builds the service. The master key and the database version are not
// opened here; OpenMasterKey does that once, before the first request, so
// that a key that cannot decrypt what is on disk is a refused startup rather
// than a cascade of failing logins.
func New(cfg Config) *Service {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.System()
	}
	return &Service{
		st:           cfg.Store,
		dir:          cfg.StoreDir,
		clk:          clk,
		gate:         NewGate(),
		cache:        newCaches(clk),
		passdb:       cfg.PassdbPath,
		onMembership: cfg.OnMembership,
		ratelimit:    newLimiter(loginWindow, loginMaxAttempts, clk.Nanos),
	}
}

// Generation is the current auth_generation counter.
func (s *Service) Generation() int64 { return s.gen.Load() }

// bumpGeneration invalidates every tier of the verification path.
func (s *Service) bumpGeneration() { s.gen.Add(1) }

// write runs fn in the store's single serialised write path.
func (s *Service) write(ctx context.Context, fn func(*sql.Tx) error) error {
	return s.st.Write(ctx, fn)
}

// now is the service clock in nanoseconds, for anything that stamps a row.
func (s *Service) now() int64 { return s.clk.Nanos() }
