// Linux only, because it assembles a Linux-only engine.
//go:build linux

// Constructing the engine: stores, then services, in dependency order.
//
// One place that knows what depends on what. Every service here is refused
// rather than defaulted when a dependency is missing, because a half-wired
// engine that starts is one that fails later, at a request, in front of a
// user.
package lifecycle

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/upload"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/journal"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The request rate a deployment starts under, before settings are read.
//
// Deliberately generous: a limit that throttles a normal client is a limit an
// operator disables, and then there is none. This stops a runaway loop, not a
// person.
const (
	defaultRatePerSecond = 50
	defaultBurst         = 200
)

// Options is what a caller supplies to build an engine.
type Options struct {
	// DataDir holds every database and the master key. Required.
	DataDir string

	// Clock stamps rows. Nil takes the system clock.
	Clock clock.Clock

	// Logger receives what construction could not do without failing. Nil
	// takes the default.
	Logger *slog.Logger
}

// Engine is a constructed set of services, and the files they hold open.
type Engine struct {
	// State is the durable half.
	State *state.DB
	// Cache is the rebuildable half.
	Cache *cache.DB
	// Journal records what an account wrote. May be nil: its absence costs
	// the recent listing and nothing else.
	Journal *journal.DB

	// ACL is the permission evaluator every service asks.
	ACL *acl.Evaluator
	// Core is the domain root.
	Core *core.Core
	// Auth owns credentials and the master key.
	Auth *auth.Service
	// Upload is the resumable transfer engine. May be nil: a deployment
	// without one serves everything except a resumable upload.
	Upload *upload.Engine

	clock  clock.Clock
	logger *slog.Logger

	// The chain reads these per request, so an operator's settings change
	// takes effect on the next request rather than at the next restart.
	settingsMu sync.RWMutex
	appHosts   middleware.Hosts
	trusted    []netip.Prefix
	csrf       []byte

	// limiter is shared across requests, since a per-request one would count
	// each request against an empty window and limit nothing.
	limiter *middleware.Limiter

	// files are the open databases, closed in reverse.
	files []*dbfile.DB
}

// Open constructs the engine.
//
// The order is the dependency order and not a preference: the evaluator is
// built before core because core loads the grant table into it during
// construction, and a core that does not know its own grants has been denying
// everything since it started.
func Open(ctx context.Context, opt Options) (*Engine, error) {
	if opt.DataDir == "" {
		return nil, errors.New("the engine needs a data directory")
	}

	clk := opt.Clock
	if clk == nil {
		clk = clock.System()
	}
	logger := opt.Logger
	if logger == nil {
		logger = slog.Default()
	}

	e := &Engine{
		clock:  clk,
		logger: logger,
		// Until settings are loaded, no proxy is trusted and no host is
		// named. An empty host list is what first boot looks like, and the
		// boundary admits only a private client in that state.
		limiter: middleware.NewLimiter(clk, defaultRatePerSecond, defaultBurst),
	}

	// A failure past this point closes what is already open. Leaving a
	// database open after refusing to start holds the data directory's lock
	// against the next attempt.
	fail := func(err error) (*Engine, error) {
		// The close error is joined rather than dropped: a failure to start
		// that also failed to release its files is two problems, and the
		// second one is why the next attempt cannot take the lock.
		if cerr := e.Close(); cerr != nil {
			return nil, errors.Join(err, fmt.Errorf("releasing what was open: %w", cerr))
		}
		return nil, err
	}

	stateFile, err := dbfile.Open(ctx, state.Spec(filepath.Join(opt.DataDir, "state.db")))
	if err != nil {
		return fail(fmt.Errorf("opening the state database: %w", err))
	}
	e.files = append(e.files, stateFile)
	e.State = state.New(stateFile)

	cacheFile, err := dbfile.Open(ctx, cache.Spec(filepath.Join(opt.DataDir, "cache.db")))
	if err != nil {
		return fail(fmt.Errorf("opening the cache database: %w", err))
	}
	e.files = append(e.files, cacheFile)

	// The cache takes the state database as its id-override source. That is
	// the edge that makes a file id survive a cache rebuild: the durable half
	// remembers which identity reserved an id, so a rebuilt cache reassigns
	// the same one rather than renumbering every file a client has bookmarked.
	cacheDB, cerr := cache.New(ctx, cacheFile, e.State)
	if cerr != nil {
		return fail(fmt.Errorf("preparing the cache database: %w", cerr))
	}
	e.Cache = cacheDB

	// The journal is the one database whose absence is a degradation rather
	// than a failure. A deployment whose disk refused this file still serves
	// every request; it loses the recent listing.
	journalFile, jerr := dbfile.Open(ctx, journal.Spec(filepath.Join(opt.DataDir, "journal.db")))
	if jerr != nil {
		logger.Warn("the journal is unavailable; the recent listing is off", "error", jerr)
	} else {
		e.files = append(e.files, journalFile)
		e.Journal = journal.New(journalFile, clk)
	}

	e.ACL = acl.NewEvaluator()

	e.Auth = auth.New(auth.Config{
		Store:    e.State,
		StoreDir: opt.DataDir,
		Clock:    clk,
		Logger:   logger,
		// The evaluator is reloaded when a membership changes. Wired here
		// rather than inside auth, so that package holds no dependency on
		// the evaluator it is telling about.
		OnMembership: func() { reloadMemberships(ctx, e, logger) },
	})

	coreSvc, kerr := core.New(ctx, core.Options{
		State:   e.State,
		Cache:   e.Cache,
		Journal: e.Journal,
		ACL:     e.ACL,
		// The share-link rows. Nil is allowed at construction and fails
		// every link operation with a wiring error, which is what an
		// unwired deployment got before this line existed.
		Links:  e.State,
		Clock:  clk,
		Logger: logger,
	})
	if kerr != nil {
		return fail(fmt.Errorf("constructing the core: %w", kerr))
	}
	e.Core = coreSvc

	// The master key is opened before anything that mints or reads a secret.
	// An account cannot be created without it, and a key that cannot decrypt
	// what is on disk is a refused startup rather than a cascade of failing
	// logins at request time.
	//
	// The refusal has no test: every path a test can reach either generates a
	// fresh key or loads one this process just wrote, so a failure here needs
	// a corrupted key file or an unreadable one, and neither is something the
	// service exposes a way to produce. What is tested is the consequence:
	// with the key opened, accounts can be created and credentials resolve.
	ring, rerr := e.Auth.OpenMasterKey(ctx)
	if rerr != nil {
		return fail(fmt.Errorf("opening the master key: %w", rerr))
	}

	// The link seams need the key, which is why they are attached after
	// construction rather than passed to it: the core is built before the
	// key is available, and each seam fails closed until this runs.
	active, _ := ring.Active()
	coreSvc.AttachLinkCrypto(
		auth.NewLinkCipher(active),
		func(ctx context.Context, plain string) (string, error) {
			return e.Auth.Hash(ctx, secret.New([]byte(plain)))
		},
		func(ctx context.Context, enc, candidate string) (bool, error) {
			ok, _, verr := e.Auth.Verify(ctx, enc, secret.New([]byte(candidate)))
			return ok, verr
		},
	)

	// The CSRF derivation key comes from the same ring. Derived rather than
	// the key itself, so a token that leaked reveals nothing about what
	// protects the data at rest.
	e.csrf = csrfKeyFrom(active)

	// The upload engine is last because it needs the core it uploads into.
	// Its absence is a degradation rather than a failure: a deployment whose
	// spool directory is unusable still serves everything else.
	up, uerr := upload.New(ctx, coreSvc, e.State, upload.Options{Clock: clk, Logger: logger})
	if uerr != nil {
		logger.Warn("resumable uploads are unavailable", "error", uerr)
	} else {
		e.Upload = up
	}

	return e, nil
}

// csrfKeyFrom derives the token key from the master key.
//
// A separate key rather than the master one: a CSRF token travels in a header
// a page can read, and deriving it directly from the key that protects data at
// rest would make every token a sample of that key's output.
func csrfKeyFrom(master [32]byte) []byte {
	sum := sha256.Sum256(append([]byte("stowcloud/csrf/v1"), master[:]...))
	return sum[:]
}

// reloadMemberships refreshes the evaluator from the durable state.
//
// Best effort by design: a membership change has already been written, and
// failing the caller's operation over a reload would report a failure for
// something that succeeded. The stale evaluator is corrected at the next
// successful reload or at restart.
func reloadMemberships(ctx context.Context, e *Engine, logger *slog.Logger) {
	rows, err := e.State.Memberships(ctx)
	if err != nil {
		logger.Warn("reloading group memberships", "error", err)
		return
	}

	byUser := make(map[int64][]int64, len(rows))
	for _, row := range rows {
		byUser[row.User] = append(byUser[row.User], row.Group)
	}
	e.ACL.SetMemberships(byUser)
}

// Close releases every open database, in reverse order.
//
// Every error is joined rather than the first returned: a caller shutting down
// wants to know about all of them, and stopping at the first leaves the rest
// open.
func (e *Engine) Close() error {
	var errs []error
	for i := len(e.files) - 1; i >= 0; i-- {
		if err := e.files[i].Close(); err != nil {
			errs = append(errs, err)
		}
	}
	e.files = nil
	return errors.Join(errs...)
}
