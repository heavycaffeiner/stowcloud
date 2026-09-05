package auth

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// SMBCredential is one publishable account, as facts.
//
// Auth owns the facts and the SMB phase owns the format: this package holds
// no opinion about what a credential file looks like, and the renderer holds
// none about which accounts are eligible.
type SMBCredential struct {
	Name   string
	UID    uint32
	NTHash [ntHashLen]byte
}

// AccessChangeSink is what a credential change tells, once it has committed.
//
// The implementation is the SMB publisher. It is an interface here rather
// than an import because the direction matters: this package must not know
// how a share is published, and the publisher has to ask this package for the
// credentials it publishes.
type AccessChangeSink interface {
	AccessChanged(ctx context.Context)
}

// Config carries what New requires beyond the store and the clock.
type Config struct {
	// Store is the durable half. Required.
	Store *state.DB

	// StoreDir gives the default location of the master key file. It is the
	// sole filesystem detail this package holds, and only for that key.
	StoreDir string

	// Clock stamps every row. Nil takes the system clock.
	Clock clock.Clock

	// RenderPassdb turns credential facts into the bytes the sidecar imports.
	// Nil means this deployment publishes no credential file, and every
	// credential change then stops at the database, which is the correct
	// behaviour for a deployment with no sidecar.
	RenderPassdb func(creds []SMBCredential) ([]byte, error)

	// PassdbPath is where those bytes are written. Empty has the same effect
	// as a nil renderer.
	PassdbPath string

	// RenderPasswd turns the same accounts into the account file that sits
	// beside the credential file. Nil means this deployment writes no account
	// file, and PublishPasswdEntries then does nothing.
	//
	// It is a second seam rather than one call producing both, because the
	// two files are written at different moments: the credential file follows
	// every credential change, and the account file is written by the
	// publisher pushing a whole configuration.
	RenderPasswd func(creds []SMBCredential, gid uint32) ([]byte, error)

	// OnMembership is the one crossing into the live permission evaluator. It
	// is wired by the layer that owns the evaluator, which keeps this package
	// free of a dependency on it.
	OnMembership func()

	// Logger receives what this package could not do without failing what the
	// caller asked for. Nil takes the default.
	Logger *slog.Logger
}

// Service implements the auth subsystem. Concurrent use is safe: the gate, the
// caches and the counter each synchronize internally, and every durable write
// travels the store's one write path.
type Service struct {
	store *state.DB
	dir   string
	clk   clock.Clock
	log   *slog.Logger

	gate  *gate
	cache *caches
	limit *limiter

	// gen invalidates every tier of the verification path at once. Any
	// credential change bumps it and every tier compares against it, so a
	// revocation is immediate on a surface that never re-reads the database.
	// It is in-process because the caches are: after a restart there is
	// nothing to invalidate.
	gen atomic.Int64

	ringMu sync.RWMutex
	ring   *KeyRing

	// rotateMu serializes rotations against each other. Two at once would
	// each persist a ring the other did not know about.
	rotateMu sync.Mutex

	renderPassdb func([]SMBCredential) ([]byte, error)
	renderPasswd func([]SMBCredential, uint32) ([]byte, error)

	// passdbMu guards the credential file's location, which a settings save
	// can change while the server runs.
	passdbMu   sync.RWMutex
	passdbPath string

	policyMu      sync.RWMutex
	smbTOTPPolicy TOTPPolicy

	sinkMu sync.RWMutex
	sink   AccessChangeSink

	// stampMu guards the last time each app password's use was written. A
	// sync client makes many requests a minute and the stamp is read as a
	// time, so one write a minute per credential says the same thing as one
	// per request without a write on every read.
	stampMu sync.Mutex
	stamped map[int64]int64
	auditOps atomic.Int64


	onMembership func()

	// decoy is the hash a login against an unknown account verifies against,
	// computed once per process so the cost and the timing of that answer
	// match a real one.
	decoyOnce sync.Once
	decoy     string
	decoyErr  error
}

// New builds the service.
//
// The master key and the database's key version are not opened here.
// OpenMasterKey does that once, before the first request, so a key that
// cannot decrypt what is on disk is a refused startup rather than a cascade of
// failing logins.
func New(cfg Config) *Service {
	clk := cfg.Clock
	if clk == nil {
		clk = clock.System()
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:        cfg.Store,
		dir:          cfg.StoreDir,
		clk:          clk,
		log:          log,
		gate:         newGate(),
		cache:        newCaches(clk),
		limit:        newLimiter(loginWindow, loginMaxAttempts, clk.Nanos),
		renderPassdb: cfg.RenderPassdb,
		passdbPath:   cfg.PassdbPath,
		renderPasswd: cfg.RenderPasswd,
		onMembership: cfg.OnMembership,
	}
}

// Generation is the current counter. Every cached decision carries the value
// it was made under.
func (s *Service) Generation() int64 { return s.gen.Load() }

// bumpGeneration clears all tiers along the verification path.
func (s *Service) bumpGeneration() { s.gen.Add(1) }

// SetAccessChangeSink wires the publisher after construction, because the
// publisher needs this service: it asks for the credentials only this package
// can open.
func (s *Service) SetAccessChangeSink(sink AccessChangeSink) {
	s.sinkMu.Lock()
	s.sink = sink
	s.sinkMu.Unlock()
}

func (s *Service) accessSink() AccessChangeSink {
	s.sinkMu.RLock()
	defer s.sinkMu.RUnlock()
	return s.sink
}

func (s *Service) keyRing() *KeyRing {
	s.ringMu.RLock()
	defer s.ringMu.RUnlock()
	return s.ring
}

func (s *Service) setKeyRing(r *KeyRing) {
	s.ringMu.Lock()
	s.ring = r
	s.ringMu.Unlock()
}

// now reports the service clock in nanoseconds for anything stamping a row.
func (s *Service) now() int64 { return s.clk.Nanos() }

// warn records a failure that must not fail the operation that hit it. The
// shape is always the same: what the caller asked for has happened, and the
// bookkeeping after it did not.
func (s *Service) warn(msg string, err error) {
	s.log.Warn(msg, "error", err)
}

// Principal is what a successful credential resolves to.
type Principal struct {
	UserID int64
	// Login holds the account's own name, which clients record as the identity
	// they signed in under. Display is an operator-assigned label and never a
	// replacement: the two diverge whenever any display name exists.
	Login    string
	Display  string
	Disabled bool
}

func principalOf(a state.Account) Principal {
	return Principal{UserID: a.ID, Login: a.Name, Display: a.Display, Disabled: a.Disabled}
}

// Outcome is a cached credential decision, positive or negative.
type Outcome struct {
	Accepted  bool
	Principal Principal
}

// The login limiter's window and budget.
const (
	loginWindow      = 5 * time.Minute
	loginMaxAttempts = 10
)

// limiter is a per-key sliding window over login attempts, keyed by the
// client address the proxy-trust rule resolved.
//
// It carries a mutex because it is hit concurrently, once per request: the
// version this replaces guarded its map and its eviction slice with nothing,
// and two logins arriving together could write the map at the same time,
// which ends the process.
type limiter struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	now    func() int64
	k      map[string]*limitBucket
	ord    []string
}

type limitBucket struct {
	count int
	reset int64
}

// limiterKeys bounds how many distinct clients are tracked, evicted oldest
// first. Without it, a client varying its address is a memory leak.
const limiterKeys = 65536

func newLimiter(window time.Duration, maxAttempts int, now func() int64) *limiter {
	return &limiter{window: window, max: maxAttempts, now: now, k: map[string]*limitBucket{}}
}

// Allow reports whether the key may try again. An empty key buckets as one
// name rather than passing free: a request whose address could not be
// resolved is still an attempt.
func (l *limiter) Allow(key string) bool {
	if key == "" {
		key = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.k[key]
	if !ok {
		if len(l.ord) >= limiterKeys {
			delete(l.k, l.ord[0])
			l.ord = l.ord[1:]
		}
		l.k[key] = &limitBucket{count: 1, reset: now + l.window.Nanoseconds()}
		l.ord = append(l.ord, key)
		return true
	}
	if now >= b.reset {
		b.count = 1
		b.reset = now + l.window.Nanoseconds()
		return true
	}
	if b.count >= l.max {
		return false
	}
	b.count++
	return true
}
