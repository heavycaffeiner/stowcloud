//go:build linux

package upload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// writeBufBytes is the buffer a body streams through. Nothing accumulates a
// whole chunk, so a one-gigabyte chunk leaves resident memory unchanged.
const writeBufBytes = 256 << 10

// Options is what New needs that the core and the store do not imply.
type Options struct {
	// Clock stamps every lifetime here. Nil takes the system clock.
	Clock clock.Clock

	// ChunkMin and ChunkDefault are the configuration's seeds. A persisted
	// administrative override beats them, and the compiled-in floor beats
	// both.
	ChunkMin     uint64
	ChunkDefault uint64

	// CacheDir is where chunks spool when the cache is on. Empty means this
	// deployment has no spool and the switch is unavailable, which is what a
	// test harness with no data directory gets.
	CacheDir string

	Logger *slog.Logger
}

// handle is one session's part-file descriptor, opened lazily and guarded on
// its own so a metadata write never waits for a disk write.
type handle struct {
	mu sync.Mutex
	f  *vfs.File
}

// Engine is the upload state machine.
type Engine struct {
	core  *core.Core
	state *state.DB
	clk   clock.Clock
	log   *slog.Logger

	settings *Settings

	// The two locks, in a fixed order: a chunk write takes the handle lock and
	// then the row lock, never nested the other way. The split exists so the
	// rare, brief bookkeeping write never blocks the common, potentially large
	// disk write.
	handlesMu sync.Mutex
	handles   map[SessionID]*handle

	rowsMu sync.Mutex
	rows   map[SessionID]*sync.Mutex

	// cache is the spool chunks pass through before reaching the destination,
	// and nil where a deployment has none.
	cache *cacheSpool

	// mergeCtx is the parent of every merger, so Close stops them all.
	mergeCtx  context.Context
	mergeStop context.CancelFunc
	mergersMu sync.Mutex
	mergers   map[SessionID]*merger
}

// New assembles an engine atop the core and the durable half, loading the stored
// chunk settings so a restart preserves an administrator's write.
func New(ctx context.Context, c *core.Core, st *state.DB, opt Options) (*Engine, error) {
	if c == nil || st == nil {
		return nil, errors.New("the upload engine requires a core and a state store")
	}
	clk := opt.Clock
	if clk == nil {
		clk = clock.System()
	}
	log := opt.Logger
	if log == nil {
		log = slog.Default()
	}
	settings, err := loadSettings(ctx, st, opt.ChunkMin, opt.ChunkDefault)
	if err != nil {
		return nil, err
	}

	// The merger context is detached from the caller's on purpose: that one is
	// a startup context, and a merger has to outlive it and stop at Close.
	mergeCtx, mergeStop := context.WithCancel(context.WithoutCancel(ctx))
	e := &Engine{
		core:      c,
		state:     st,
		clk:       clk,
		log:       log,
		settings:  settings,
		handles:   map[SessionID]*handle{},
		rows:      map[SessionID]*sync.Mutex{},
		mergeCtx:  mergeCtx,
		mergeStop: mergeStop,
		mergers:   map[SessionID]*merger{},
	}
	if opt.CacheDir != "" {
		spool, cerr := openCacheSpool(opt.CacheDir)
		if cerr != nil {
			mergeStop()
			return nil, cerr
		}
		e.cache = spool
		enabled, rerr := st.ReadUploadCacheEnabled(ctx)
		if rerr != nil {
			mergeStop()
			return nil, errors.Join(rerr, spool.close())
		}
		e.cache.setEnabled(enabled)
	}
	return e, nil
}

// Close stops every merger and releases the spool handle. A caller that does
// not call it leaks a goroutine per cached session.
func (e *Engine) Close() error {
	e.mergeStop()

	e.mergersMu.Lock()
	mergers := make([]*merger, 0, len(e.mergers))
	for _, m := range e.mergers {
		mergers = append(mergers, m)
	}
	e.mergers = map[SessionID]*merger{}
	e.mergersMu.Unlock()
	for _, m := range mergers {
		m.wait()
	}

	e.handlesMu.Lock()
	handles := e.handles
	e.handles = map[SessionID]*handle{}
	e.handlesMu.Unlock()

	var err error
	for id, h := range handles {
		h.mu.Lock()
		if h.f != nil {
			if cerr := h.f.Close(); cerr != nil {
				err = errors.Join(err, fmt.Errorf("closing the part file of %s: %w", id, cerr))
			}
			h.f = nil
		}
		h.mu.Unlock()
	}
	if e.cache != nil {
		err = errors.Join(err, e.cache.close())
	}
	return err
}

// Settings returns the current chunk floor and default.
func (e *Engine) Settings() *Settings { return e.settings }

// expiry is how long a session survives without a write.
func (e *Engine) expiry() int64 { return e.clk.Nanos() + int64(limits.UploadSessionTTL) }

// lockRow serializes one session's bookkeeping against itself and returns the
// release. The entry outlives the call because a second caller may already be
// waiting on it; forgetRow is what drops one for a session that is gone.
func (e *Engine) lockRow(id SessionID) func() {
	e.rowsMu.Lock()
	mu, ok := e.rows[id]
	if !ok {
		mu = &sync.Mutex{}
		e.rows[id] = mu
	}
	e.rowsMu.Unlock()

	mu.Lock()
	return mu.Unlock
}

// forgetRow releases a session's bookkeeping lock once it is unreachable.
//
// Every terminal path invokes it, Abort included. Leaving the entry for the
// sweep to collect kept an aborted session's mutex in the map for a day.
func (e *Engine) forgetRow(id SessionID) {
	e.rowsMu.Lock()
	delete(e.rows, id)
	e.rowsMu.Unlock()
}

// rowLockCount is how many bookkeeping locks are held, for the test that
// proves a terminal path forgets its own.
func (e *Engine) rowLockCount() int {
	e.rowsMu.Lock()
	defer e.rowsMu.Unlock()
	return len(e.rows)
}

// handleFor lazily reopens a part file and is the only place in the tree that
// acquires a writable descriptor along a read path.
//
// That single descriptor receives the chunk writes and is read back during
// finalize to check the whole-file digest, which is precisely why the read-write
// intent exists: a read-only reopen would fail the very verification it was
// opened for.
func (e *Engine) handleFor(root *vfs.ShareRoot, id SessionID, part vfs.SafePath) (*vfs.File, error) {
	e.handlesMu.Lock()
	h, ok := e.handles[id]
	if !ok {
		h = &handle{}
		e.handles[id] = h
	}
	e.handlesMu.Unlock()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.f != nil {
		return h.f, nil
	}
	f, err := root.OpenRead(part, vfs.IntentReadWrite)
	if err != nil {
		return nil, mapVFSErr(err)
	}
	h.f = f
	return f, nil
}

func (e *Engine) putHandle(id SessionID, f *vfs.File) {
	e.handlesMu.Lock()
	defer e.handlesMu.Unlock()
	e.handles[id] = &handle{f: f}
}

// closeHandle releases a session's descriptor. It runs before the rename that
// publishes, so nothing depends on the semantics of renaming an open file.
func (e *Engine) closeHandle(id SessionID) {
	e.handlesMu.Lock()
	h, ok := e.handles[id]
	delete(e.handles, id)
	e.handlesMu.Unlock()
	if !ok {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.f == nil {
		return
	}
	if err := h.f.Close(); err != nil {
		e.log.Warn("closing an upload part file failed",
			"session", id.String(), "error", err)
	}
	h.f = nil
}

// partPathOf gives a session's part-file path.
func (e *Engine) partPathOf(r *row) (vfs.SafePath, error) {
	dest, err := r.dest()
	if err != nil {
		return vfs.SafePath{}, err
	}
	return partPath(dest, r.sess.PartName)
}

// spoolDirOf gives a name-ordered session's spool directory.
func (e *Engine) spoolDirOf(r *row) (vfs.SafePath, error) {
	if r.sess.SpoolDir == "" {
		return vfs.SafePath{}, fmt.Errorf("%w: this session has no spool directory", ErrBadRequest)
	}
	dest, err := r.dest()
	if err != nil {
		return vfs.SafePath{}, err
	}
	return dest.Parent().JoinControl(r.sess.SpoolDir)
}

// checkAccountLimits enforces both per-account bounds before anything is
// created.
func (e *Engine) checkAccountLimits(ctx context.Context, user core.UserID, total *uint64) error {
	count, err := e.state.CountUploadSessionsForUser(ctx, int64(user))
	if err != nil {
		return err
	}
	if count >= limits.UploadSessionsPerUser {
		return &ExhaustedError{Limit: "sessions in flight for this account"}
	}
	reserved, err := e.state.SumUploadReservedForUser(ctx, int64(user))
	if err != nil {
		return err
	}
	want, nerr := num.Narrow[uint64](reserved)
	if nerr != nil {
		return nerr
	}
	if total != nil {
		want += *total
	}
	if want > limits.UploadReservedBytesPerUser {
		return &ExhaustedError{Limit: "reserved upload bytes for this account"}
	}
	return nil
}

// checkFreeSpace rejects a session whose declared length would consume the
// destination filesystem's margin.
//
// Measurement targets the directory holding the part file rather than the share
// root, because a mount inside the share is the filesystem the upload genuinely
// consumes.
//
// A probe that fails to run is not itself a rejection: an unsupported statfs
// describes the filesystem rather than justifying refusal of uploads.
func (e *Engine) checkFreeSpace(root *vfs.ShareRoot, dir vfs.SafePath, total *uint64) error {
	space, err := root.Space(dir)
	if err != nil {
		return nil //nolint:nilerr // a probe that could not run is not a refusal; see above.
	}
	need := uint64(limits.UploadFreeSpaceMargin)
	if total != nil {
		need += *total
	}
	// Available rather than free, because blocks a filesystem reserves for root
	// are not ours to use, and counting them would admit an upload destined to
	// run out midway.
	if space.Available < need {
		return &ExhaustedError{Limit: "free space on the destination filesystem"}
	}
	return nil
}

// validateOffset enforces the ordering rule for non-random-access sessions,
// where a chunk must arrive at the resumable offset.
func validateOffset(r *row, off uint64) error {
	if r.sess.RandomAccess {
		return nil
	}
	if expected := r.set.ContiguousPrefix(); off != expected {
		return &ConflictError{Expected: expected, Got: off}
	}
	return nil
}

// checkWithinDeclared rejects a body exceeding the session's declared length.
// Enforcement happens as bytes arrive rather than from a header, since a header
// only asserts while this observes.
func checkWithinDeclared(r *row, off, written uint64) error {
	if r == nil {
		return nil
	}
	total, ok := r.totalLen()
	if !ok {
		return nil
	}
	end := off + written
	if end < off {
		return fmt.Errorf("%w: the offset and length overflow", ErrBadRequest)
	}
	if end > total {
		return fmt.Errorf("%w: %d bytes at offset %d passes the declared length of %d",
			ErrTooLarge, written, off, total)
	}
	return nil
}

// checkChunkFloor rejects a mid-stream chunk beneath the session's floor.
//
// The final chunk is exempt, as is a whole file smaller than the floor; neither
// can be enlarged. Comparison uses the floor captured at creation rather than
// the current value, so an administrator's write cannot retroactively reject a
// chunk that was legal when sent.
func checkChunkFloor(r *row, off, n uint64) error {
	if r == nil || n == 0 {
		return nil
	}
	floor, err := num.Narrow[uint64](r.sess.ChunkMinAtCreation)
	if err != nil || floor == 0 {
		return nil
	}
	total, declared := r.totalLen()
	isLast := declared && off+n == total
	wholeFileSmall := declared && total < floor
	if isLast || wholeFileSmall || n >= floor {
		return nil
	}
	return &ChunkTooSmallError{Min: floor, Got: n}
}

// mapVFSErr translates a filesystem rejection into this package's vocabulary,
// passing through anything it has no term for.
func mapVFSErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, vfs.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, vfs.ErrNoSpace):
		return &ExhaustedError{Limit: "free space on the destination filesystem"}
	default:
		return err
	}
}

func shareIDOf(v int64) (core.ShareID, bool) {
	id, err := num.Narrow[uint32](v)
	if err != nil {
		return 0, false
	}
	return core.ShareID(id), true
}

// sessionIDOrZero is for a caller holding stored bytes that are a session id
// by construction. A row whose id will not parse names no merger, and the zero
// id matches none.
func sessionIDOrZero(b []byte) SessionID {
	id, err := sessionIDFromBytes(b)
	if err != nil {
		return SessionID{}
	}
	return id
}

// sessionTTL is the lifetime a session survives without a write, named here so
// a test can read the same number the engine uses.
func sessionTTL() time.Duration { return limits.UploadSessionTTL }
