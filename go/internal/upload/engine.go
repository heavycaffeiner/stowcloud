//go:build linux

package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// writeBufBytes is the reused body buffer. The chunk body flows through this
// into pwrite and nothing buffers a whole chunk anywhere, so a 1 GiB chunk
// leaves resident memory unchanged. That is why there is no memory argument
// for dynamic chunk sizing.
const writeBufBytes = 256 << 10

// Engine is the upload state machine. It is threadsafe and one per server.
type Engine struct {
	core  *core.Core
	state *state.DB
	clk   clock.Clock
	log   *slog.Logger

	settings *Settings

	// handles holds the lazily opened part-file descriptor per session. The
	// two locks are separate on purpose: the part-file handle is guarded here
	// and the bookkeeping row is guarded in rows, so the rare and brief
	// metadata write never blocks the common and potentially large disk write.
	handlesMu sync.Mutex
	handles   map[SessionID]*handle

	// rows serialises the bookkeeping for one session against itself. A chunk
	// write takes the handle lock and then this one, in that order and never
	// nested the other way.
	rowsMu sync.Mutex
	rows   map[SessionID]*sync.Mutex

	// cache is the spool chunks land in before they reach the destination, and
	// nil for a deployment that has none.
	cache *Cache

	// mergeCtx is the parent of every merger, so Close stops them all. The
	// mergers themselves are one per cached session in flight.
	mergeCtx  context.Context
	mergeStop context.CancelFunc
	mergersMu sync.Mutex
	mergers   map[SessionID]*merger
}

// handle is one session's part-file descriptor and the lock guarding it.
type handle struct {
	mu sync.Mutex
	f  *vfs.File
}

// Options is what New cannot work out from the store.
type Options struct {
	// Clock stamps every lifetime in this package. Nil takes the system clock.
	Clock clock.Clock
	// ChunkMin and ChunkDefault are the config file's seeds. A persisted admin
	// override beats them, and the hard floor beats both.
	ChunkMin     uint64
	ChunkDefault uint64
	Logger       *slog.Logger
	// CacheDir is where chunks spool when the cache is turned on. Empty means
	// this deployment has no spool and the switch is unavailable, which is what
	// a test harness with no data directory gets.
	CacheDir string
}

// New wires an engine over the core and the durable half, and loads the
// persisted chunk settings so a restart does not lose an admin's write.
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
	// The merger context is detached from ctx on purpose: ctx is the caller's
	// startup context, and a merger has to outlive it and stop at Close.
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
		cache, cerr := openCache(opt.CacheDir)
		if cerr != nil {
			// A spool that will not open is a degradation and not a refusal to
			// start: every upload still works by writing to the destination,
			// which is what this server did before the spool existed.
			log.Error("the upload cache spool is unavailable; uploads write straight to their destination",
				slog.String("dir", opt.CacheDir), slog.Any("error", cerr))
		} else {
			on, rerr := st.ReadUploadCacheEnabled(ctx)
			if rerr != nil {
				mergeStop()
				return nil, errors.Join(rerr, cache.Close())
			}
			cache.enabled.Store(on)
			e.cache = cache
		}
	}
	return e, nil
}

// Close stops every merger and releases the spool. A merger is waited for
// rather than abandoned: it writes part files, and leaving one running past
// the shutdown is a write into a descriptor the process is closing.
func (e *Engine) Close() error {
	e.mergeStop()
	e.mergersMu.Lock()
	live := make([]*merger, 0, len(e.mergers))
	for id, m := range e.mergers {
		live = append(live, m)
		delete(e.mergers, id)
	}
	e.mergersMu.Unlock()
	for _, m := range live {
		<-m.done
	}
	return e.cache.Close()
}

// Settings is the live chunk floor and default.
func (e *Engine) Settings() *Settings { return e.settings }

// expiry is when a session touched now would run out.
func (e *Engine) expiry() int64 { return e.clk.Nanos() + int64(limits.UploadSessionTTL) }

// Create opens a session against an already-resolved destination.
//
// It takes a core.Resolved rather than a path, so the permission check has
// already happened and cannot be reached around: there is no way to construct
// one outside the core package.
func (e *Engine) Create(ctx context.Context, r core.Resolved, spec SessionSpec) (Session, error) {
	if err := r.Require(acl.Write | acl.Create); err != nil {
		return Session{}, err
	}
	dest := r.Path()
	if dest.IsRoot() {
		return Session{}, fmt.Errorf("%w: the destination has no file name", ErrBadRequest)
	}
	if err := e.checkAccountLimits(ctx, r.User(), spec.TotalLen); err != nil {
		return Session{}, err
	}
	// The directory the part file is created in is the filesystem this upload
	// actually consumes, which is not the share root the moment anything is
	// mounted inside the share.
	if err := e.checkFreeSpace(r.Root(), dest.Parent(), spec.TotalLen); err != nil {
		return Session{}, err
	}

	id, err := NewSessionID()
	if err != nil {
		return Session{}, err
	}
	part, err := partPath(dest, partName(id))
	if err != nil {
		return Session{}, err
	}

	// The part file is created and sized up front: an O_EXCL create plus a
	// sparse truncate, so nothing is copied and the destination directory
	// holds exactly one unlistable entry for the session's whole life.
	f, err := e.createPart(r.Root(), part, spec.TotalLen)
	if err != nil {
		return Session{}, err
	}

	minAtCreation, chunkSize := e.settings.Snapshot()
	sess := state.UploadSession{
		ID:       id.Bytes(),
		User:     int64(r.User()),
		Share:    int64(r.Share()),
		Dest:     dest.String(),
		PartName: partName(id),
		Mode:     int64(spec.Mode),
		// The floor is snapshotted rather than read live on every chunk: an
		// admin moving it mid-upload must not retroactively make a chunk that
		// was legal when it was sent illegal now.
		ChunkMinAtCreation: int64(minAtCreation), //nolint:gosec // G115 reads the conversion: both are bounded by the settings floor, which is a compile-time constant.
		ChunkSize:          int64(chunkSize),     //nolint:gosec // as above.
		RandomAccess:       spec.RandomAccess,
		NextName:           1,
		IfMatch:            spec.IfMatch,
		Filename:           spec.Meta.Filename,
		MtimeNs:            spec.Meta.MtimeNs,
		Mime:               spec.Meta.Mime,
		RelativePath:       spec.Meta.RelativePath,
		CreatedNs:          e.clk.Nanos(),
		ExpiresNs:          e.expiry(),
		State:              int64(StateReceiving),
	}
	if spec.Mode == SpoolNameOrdered {
		sess.SpoolDir = spoolDirName(id)
	}
	// The mode is fixed here and read from the row afterwards. A switch flipped
	// mid-upload must not change where a session in flight looks for its bytes:
	// they are in one place or the other and no setting moves them.
	//
	// Name-ordered sessions are excluded. They already spool out-of-order
	// chunks to files of their own and assemble by name, so the cache would be
	// a second staging layer under the first.
	if e.cacheEnabled() && spec.Mode == SpoolOffsetAddressed {
		sess.CacheDir = cacheDirName(id)
	}
	if spec.TotalLen != nil {
		n, nerr := num.Narrow[int64](*spec.TotalLen)
		if nerr != nil {
			return Session{}, errors.Join(fmt.Errorf("%w: the declared length does not fit", ErrBadRequest), e.discardPart(r.Root(), part, f))
		}
		sess.TotalLen = &n
	}
	if v := spec.Meta.Verify; v != nil {
		if err := checkDigestLen(v.Algo, len(v.Digest)); err != nil {
			return Session{}, errors.Join(err, e.discardPart(r.Root(), part, f))
		}
		algo := int64(v.Algo)
		sess.Verify = &algo
		sess.VerifyDigest = v.Digest
	}

	if err := e.state.CreateUploadSession(ctx, sess); err != nil {
		// The row is what makes the part file findable. Without one the file
		// is an orphan the sweep would have to clean up, so it goes now.
		return Session{}, errors.Join(err, e.discardPart(r.Root(), part, f))
	}
	// The directory is recorded before the first byte arrives, and it outlives
	// the session. An orphan is a part file whose row is gone, so the rows
	// cannot be what tells the sweep where to look. A failure here costs the
	// sweep its record and not the upload.
	if terr := e.state.TouchUploadDir(ctx, int64(r.Share()), dest.Parent().String()); terr != nil {
		e.log.Warn("could not record the directory an upload writes into; the sweep may miss an orphan there",
			slog.String("dir", dest.Parent().String()), slog.Any("error", terr))
	}

	e.putHandle(id, f)
	return e.session(&row{sess: sess, set: NewIntervalSet()})
}

// createPart makes the part file and sizes it sparsely for a declared length.
func (e *Engine) createPart(root *vfs.ShareRoot, part vfs.SafePath, total *uint64) (*vfs.File, error) {
	f, err := root.CreatePart(part)
	if err != nil {
		return nil, mapVFSErr(err)
	}
	if total != nil {
		n, nerr := num.Narrow[int64](*total)
		if nerr != nil {
			return nil, errors.Join(fmt.Errorf("%w: the declared length does not fit", ErrBadRequest), f.Close())
		}
		if terr := f.Truncate(n); terr != nil {
			return nil, errors.Join(mapVFSErr(terr), f.Close())
		}
	}
	return f, nil
}

// discardPart closes and removes a part file a failed creation left behind.
func (e *Engine) discardPart(root *vfs.ShareRoot, part vfs.SafePath, f *vfs.File) error {
	err := f.Close()
	if uerr := root.Unlink(part); uerr != nil && !errors.Is(uerr, vfs.ErrNotFound) {
		err = errors.Join(err, uerr)
	}
	return err
}

// Get reports one session, scoped to the account that owns it.
func (e *Engine) Get(ctx context.Context, id SessionID, user core.UserID) (Session, error) {
	r, err := e.load(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if err := requireOwner(r, user); err != nil {
		return Session{}, err
	}
	return e.session(r)
}

// Offset reports the resumable offset: the end of the first range when the set
// starts at zero, and zero otherwise.
//
// A client that asks for this after a failed chunk gets the truth rather than
// the part file's size, which on a sparse file says where the last write
// landed and not what is in it.
func (e *Engine) Offset(ctx context.Context, id SessionID, user core.UserID) (uint64, error) {
	s, err := e.Get(ctx, id, user)
	if err != nil {
		return 0, err
	}
	return s.Offset, nil
}

// SetLength supplies a deferred length, which finalize requires and the
// interval set needs before it can report completeness.
func (e *Engine) SetLength(ctx context.Context, id SessionID, user core.UserID, total uint64) error {
	unlock := e.lockRow(id)
	defer unlock()

	r, err := e.load(ctx, id)
	if err != nil {
		return err
	}
	if err := requireOwner(r, user); err != nil {
		return err
	}
	if err := e.requireReceiving(r); err != nil {
		return err
	}
	if have, ok := r.totalLen(); ok {
		if have != total {
			return fmt.Errorf("%w: this session already declared a length of %d", ErrBadRequest, have)
		}
		return nil
	}
	// Whatever has already landed has to fit inside the length being declared,
	// or the session would be complete over bytes past its own end.
	if received := r.set.Runs(); len(received) > 0 && received[len(received)-1].Hi > total {
		return fmt.Errorf("%w: %d bytes have already landed, past the declared length of %d",
			ErrBadRequest, received[len(received)-1].Hi, total)
	}
	n, nerr := num.Narrow[int64](total)
	if nerr != nil {
		return fmt.Errorf("%w: the declared length does not fit", ErrBadRequest)
	}
	r.sess.TotalLen = &n
	r.sess.ExpiresNs = e.expiry()
	return e.save(ctx, r)
}

// PatchAt writes the body at off in the session's part file and records the
// range. It is the only writer.
//
// The ordering rule is not negotiable: the disk write completes before the
// range is committed. A crash between the two leaves the set reporting the
// smaller prefix, so the client resends the same bytes at the same offset and
// they land identically. Reversing it would let the set claim bytes that were
// never durably written, which is silent corruption rather than a slow upload.
func (e *Engine) PatchAt(
	ctx context.Context, root *vfs.ShareRoot, id SessionID, user core.UserID,
	off uint64, body io.Reader, sum *Checksum,
) (uint64, error) {
	// The lock covers the bookkeeping, not the body.
	//
	// A client sends several chunks of one file at once, and each writes at
	// its own offset through pwrite, so the writes do not need serialising
	// against each other. Holding the row lock across the body read did
	// serialise them, and under HTTP/2 that deadlocks rather than queues: the
	// blocked handlers never read their streams, the connection's flow-control
	// window fills, and the chunk holding the lock cannot receive the rest of
	// its own body. Every upload stopped after its first chunk and the browser
	// eventually gave up.
	unlock := e.lockRow(id)
	r, err := e.load(ctx, id)
	if err != nil {
		unlock()
		return 0, err
	}
	if oerr := requireOwner(r, user); oerr != nil {
		unlock()
		return 0, oerr
	}
	if serr := e.requireReceiving(r); serr != nil {
		unlock()
		return 0, serr
	}
	if r.mode() != SpoolOffsetAddressed {
		unlock()
		return 0, fmt.Errorf("%w: this session is name-ordered", ErrBadRequest)
	}
	if verr := e.validateOffset(r, off); verr != nil {
		unlock()
		return 0, verr
	}

	part, err := e.partPathOf(r)
	if err != nil {
		unlock()
		return 0, err
	}
	cached := r.sess.CacheDir != "" && e.cache != nil
	var f *vfs.File
	if !cached {
		f, err = e.handleFor(root, id, part)
		if err != nil {
			unlock()
			return 0, err
		}
	}
	unlock()

	// The body is written and hashed in one pass. Nothing accumulates the
	// whole chunk, so a per-chunk checksum costs a hasher and not a copy of
	// the chunk.
	var (
		n      uint64
		digest []byte
		werr   error
	)
	if cached {
		n, digest, werr = e.patchCached(ctx, root, r, id, part, off, body, sum)
	} else {
		n, digest, werr = e.writeBody(f, off, body, r, sum)
	}
	if werr != nil {
		// A cancel closes the part file while chunks of the same session are
		// still writing, and this is what those writes hit. The session is
		// gone on purpose, so it is reported as gone: answering 500 made the
		// client read a deliberate cancellation as a server fault and retry
		// it, which is what left a cancelled upload saying "retrying".
		if errors.Is(werr, os.ErrClosed) {
			return 0, ErrNotFound
		}
		return 0, werr
	}

	// The checksum is verified before the range is recorded. A chunk that
	// fails leaves the set untouched, so the client resends the same range
	// rather than resuming past a hole it thinks is filled. The bytes are
	// already on disk and are simply overwritten by the resend.
	if sum != nil && digest != nil {
		if !constantTimeEqual(digest, sum.Digest) {
			return 0, fmt.Errorf("%w: the %s digest does not match the %d bytes received",
				ErrChecksum, sum.Algo, n)
		}
	}

	// Retaken to record what landed. The row is re-read under the lock rather
	// than reusing the copy from above: another chunk of this same file has
	// very likely recorded its own range in the meantime, and writing back a
	// stale set would drop it.
	relock := e.lockRow(id)
	defer relock()

	fresh, err := e.load(ctx, id)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		if err := fresh.set.Insert(off, off+n); err != nil {
			return 0, err
		}
	}
	if err := e.commitRange(ctx, fresh); err != nil {
		return 0, err
	}
	return fresh.set.ContiguousPrefix(), nil
}

// writeBody streams body into f at off, looping over short writes, and hashes
// as it goes when the client supplied a checksum.
//
// A zero-length write with no error is a failure rather than a retry: pwrite
// returning zero means the file cannot take the bytes, and looping on it spins
// forever.
//
// r is nil for a write that no session rule applies to, which is a spooled
// chunk: it is measured against the assembled file rather than against its own
// offset, and a name-ordered client does not choose the offsets the floor and
// the declared length are about.
func (e *Engine) writeBody(
	f *vfs.File, off uint64, body io.Reader, r *row, sum *Checksum,
) (uint64, []byte, error) {
	return e.writeBodyAt(f, off, off, body, r, sum)
}

// writeBodyAt is writeBody with the two offsets separated: where the bytes go,
// and where they belong in the finished file.
//
// They differ for exactly one caller. A cached chunk is a file of its own and
// its bytes start at zero in it, while the declared length and the chunk floor
// are rules about the assembled file and have to be measured against the
// offset the client sent.
func (e *Engine) writeBodyAt(
	f *vfs.File, at, logical uint64, body io.Reader, r *row, sum *Checksum,
) (uint64, []byte, error) {
	var hasher *hasherFunc
	if sum != nil {
		if err := checkDigestLen(sum.Algo, len(sum.Digest)); err != nil {
			return 0, nil, err
		}
		hasher = newHasherFunc(sum.Algo)
	}

	buf := make([]byte, writeBufBytes)
	var written uint64
	for {
		read, rerr := body.Read(buf)
		if read > 0 {
			got, nerr := num.Narrow[uint64](read)
			if nerr != nil {
				return written, nil, nerr
			}
			// The declared length is enforced as the bytes arrive rather than
			// from a header, because a header is a claim and this is the
			// stream. A body longer than declared is refused before it is
			// written past the end of the file.
			if err := e.checkWithinDeclared(r, logical, written+got); err != nil {
				return written, nil, err
			}
			if err := writeAllAt(f, buf[:read], at+written); err != nil {
				return written, nil, err
			}
			if hasher != nil {
				hasher.Write(buf[:read])
			}
			written += got
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return written, nil, rerr
		}
		if read == 0 {
			break
		}
	}
	if err := e.checkChunkFloor(r, logical, written); err != nil {
		return written, nil, err
	}
	if hasher == nil {
		return written, nil, nil
	}
	return written, hasher.Sum(), nil
}

// writeAllAt loops over a short write, which pwrite is allowed to produce even
// on success.
func writeAllAt(f *vfs.File, b []byte, off uint64) error {
	for len(b) > 0 {
		at, err := num.Narrow[int64](off)
		if err != nil {
			return err
		}
		n, werr := f.WriteAt(b, at)
		if n == 0 && werr == nil {
			return errors.New("the part file accepted no bytes and reported no error")
		}
		if werr != nil {
			return mapVFSErr(werr)
		}
		wrote, nerr := num.Narrow[uint64](n)
		if nerr != nil {
			return nerr
		}
		b = b[n:]
		off += wrote
	}
	return nil
}

// validateOffset applies the ordering rule for a session that is not
// random-access: a chunk has to land at the resumable offset.
func (e *Engine) validateOffset(r *row, off uint64) error {
	if r.sess.RandomAccess {
		return nil
	}
	if expected := r.set.ContiguousPrefix(); off != expected {
		return &ConflictError{Expected: expected, Got: off}
	}
	return nil
}

// checkWithinDeclared refuses a body that runs past the length the session
// declared.
func (e *Engine) checkWithinDeclared(r *row, off, written uint64) error {
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

// checkChunkFloor refuses a mid-stream chunk below the session's own floor.
//
// The last chunk is exempt, and so is a whole file smaller than the floor:
// neither can be made bigger. The comparison is against the floor snapshotted
// at creation rather than the live one, so an admin's write does not
// retroactively refuse a chunk that was legal when it was sent.
func (e *Engine) checkChunkFloor(r *row, off, n uint64) error {
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

// checkAccountLimits applies the two per-account bounds before anything is
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

// checkFreeSpace refuses a session whose declared length would not leave the
// destination filesystem its margin.
//
// A probe that fails is not itself a refusal: an unsupported statfs is a fact
// about the filesystem, not a reason to stop accepting uploads.
func (e *Engine) checkFreeSpace(root *vfs.ShareRoot, dir vfs.SafePath, total *uint64) error {
	space, err := root.Space(dir)
	if err != nil {
		return nil //nolint:nilerr // a probe that could not run is not a refusal; see the comment above.
	}
	need := uint64(limits.UploadFreeSpaceMargin)
	if total != nil {
		need += *total
	}
	// Available rather than Free: the blocks the filesystem reserves for root
	// are not ours to write into, and counting them admits an upload that is
	// going to hit ENOSPC part way.
	if space.Available < need {
		return &ExhaustedError{Limit: "free space on the destination filesystem"}
	}
	return nil
}

// Abort terminates a session. The part file stays on disk for the sweep, which
// is what keeps a termination from racing a write already in flight.
func (e *Engine) Abort(ctx context.Context, id SessionID, user core.UserID) error {
	// Outside the row lock, because stopping a merger means waiting for a step
	// that takes it.
	e.stopMerger(id)

	unlock := e.lockRow(id)
	defer unlock()

	r, err := e.load(ctx, id)
	if err != nil {
		return err
	}
	if err := requireOwner(r, user); err != nil {
		return err
	}
	if SessionState(r.sess.State) == StateDone {
		return ErrNotFound
	}
	r.sess.State = int64(StateAborted)
	// Immediately eligible for the sweep, which is what takes the part file.
	r.sess.ExpiresNs = e.clk.Nanos()
	if err := e.save(ctx, r); err != nil {
		return err
	}
	// The cache goes now rather than at the sweep. Its contents can never be
	// finished, and the spool is the small volume: holding a cancelled
	// upload's window there for a day is what fills it.
	e.releaseCache(r.sess.CacheDir)
	e.closeHandle(id)
	return nil
}

// lockRow serialises one session's bookkeeping against itself and returns the
// release. The map entry outlives the call because a second caller may already
// be waiting on it; the sweep is what drops entries for sessions that are gone.
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

// forgetRow drops a session's bookkeeping lock once nothing can reach it.
func (e *Engine) forgetRow(id SessionID) {
	e.rowsMu.Lock()
	delete(e.rows, id)
	e.rowsMu.Unlock()
}

// handleFor is the lazy reopen of a part file, and the one place in the tree
// that takes a writable descriptor on a read path.
//
// The same descriptor takes chunk writes and is read back at finalize to
// verify the whole-file digest, which is exactly why IntentReadWrite exists: a
// read-only reopen would fail the verification it was opened for.
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

// closeHandle releases a session's descriptor. It is called before the rename
// that publishes, so nothing depends on the semantics of renaming a file that
// is still open.
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
			slog.String("session", id.String()), slog.Any("error", err))
	}
	h.f = nil
}

// partPathOf is a session's part-file path.
func (e *Engine) partPathOf(r *row) (vfs.SafePath, error) {
	dest, err := r.dest()
	if err != nil {
		return vfs.SafePath{}, err
	}
	return partPath(dest, r.sess.PartName)
}

// spoolDirOf is a name-ordered session's spool directory.
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

// mapVFSErr converts a filesystem refusal into this package's vocabulary,
// leaving anything it has no word for as it came.
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
