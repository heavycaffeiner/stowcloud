//go:build linux

package upload

import (
	"context"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The crossing between the engine's shapes and the durable half's rows. It is
// its own file because the engine has enough to do without also knowing which
// column a nullable length lives in.

// row is one session as the engine holds it: the stored columns, plus the
// interval set the second table carries.
type row struct {
	sess state.UploadSession
	set  *IntervalSet
}

func (r *row) id() (SessionID, error) { return sessionIDFromBytes(r.sess.ID) }

func (r *row) dest() (vfs.SafePath, error) { return vfs.ParseSafePath(r.sess.Dest) }

func (r *row) mode() SpoolMode { return SpoolMode(r.sess.Mode) }

func (r *row) cached() bool { return r.sess.CacheDir != "" }

// totalLen reports the declared length, and false for a deferred one.
func (r *row) totalLen() (uint64, bool) {
	if r.sess.TotalLen == nil {
		return 0, false
	}
	n, err := num.Narrow[uint64](*r.sess.TotalLen)
	if err != nil {
		return 0, false
	}
	return n, true
}

// verify is the whole-file check, and nil for a session that asked for none.
//
// Both columns are written together at creation, so an algorithm with no
// digest beside it is a schema-level inconsistency rather than a legitimate
// "opt in but compare against nothing". It reads as no check at all, because
// the alternative is verifying against a fabricated expectation and refusing a
// perfectly good upload.
func (r *row) verify() *Verify {
	if r.sess.Verify == nil || len(r.sess.VerifyDigest) == 0 {
		return nil
	}
	return &Verify{Algo: Algo(*r.sess.Verify), Digest: r.sess.VerifyDigest}
}

// load reads one session and its interval set.
func (e *Engine) load(ctx context.Context, id SessionID) (*row, error) {
	sess, err := e.state.ReadUploadSession(ctx, id.Bytes())
	if errors.Is(err, state.ErrNoSuchUploadSession) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return e.rowOf(ctx, sess)
}

// rowOf attaches the interval set to a session already read.
func (e *Engine) rowOf(ctx context.Context, sess state.UploadSession) (*row, error) {
	runs, err := e.state.ReadUploadIntervals(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	set, err := LoadIntervalSet(rangesOf(runs))
	if err != nil {
		// A set that will not rebuild cannot say what the session holds, and
		// answering an offset from it would hand the client a hole. The session
		// refuses rather than silently resetting: the part file is still on
		// disk and the sweep is what decides its fate.
		return nil, fmt.Errorf("session for %s: %w", sess.Dest, err)
	}
	return &row{sess: sess, set: set}, nil
}

// save writes the mutable half of a session row back.
func (e *Engine) save(ctx context.Context, r *row) error {
	return e.state.UpdateUploadSession(ctx, r.sess)
}

// commitRange records a merged interval set and pushes the session's lifetime
// out, in one transaction.
//
// It is step two of the ordering rule and never runs before the bytes are on
// disk: a crash between the two leaves the set reporting the smaller prefix,
// so the client resends the same bytes at the same offset and they land
// identically. Reversing it would let the set claim bytes that were never
// durably written, which is silent corruption rather than a slow upload.
func (e *Engine) commitRange(ctx context.Context, r *row) error {
	runs := make([][2]uint64, 0, r.set.Count())
	for _, run := range r.set.Runs() {
		runs = append(runs, [2]uint64{run.Lo, run.Hi})
	}
	r.sess.ExpiresNs = e.expiry()
	return e.state.RecordUploadInterval(ctx, r.sess.ID, runs, r.sess.ExpiresNs)
}

// session is the caller-facing projection of a row.
func (e *Engine) session(r *row) (Session, error) {
	id, err := r.id()
	if err != nil {
		return Session{}, err
	}
	dest, err := r.dest()
	if err != nil {
		return Session{}, err
	}
	share, ok := shareIDOf(r.sess.Share)
	if !ok {
		return Session{}, fmt.Errorf("a session names a share id that does not fit: %d", r.sess.Share)
	}
	chunk, err := num.Narrow[uint64](r.sess.ChunkSize)
	if err != nil {
		return Session{}, fmt.Errorf("a session names a chunk size that does not fit: %w", err)
	}

	out := Session{
		ID:           id,
		User:         core.UserID(r.sess.User),
		Share:        share,
		Dest:         dest,
		State:        e.effectiveState(r),
		Offset:       r.set.ContiguousPrefix(),
		Received:     r.set.Received(),
		ChunkSize:    chunk,
		RunCount:     r.set.Count(),
		RandomAccess: r.sess.RandomAccess,
		Mode:         r.mode(),
		Cached:       r.cached(),
		ExpiresNs:    r.sess.ExpiresNs,
	}
	if n, declared := r.totalLen(); declared {
		out.TotalLen = &n
	}
	// A name-ordered session advances its write head and leaves the interval
	// set empty until assembly fills it, so reading the set here would report
	// zero for exactly the sessions that need an answer.
	if r.mode() == SpoolNameOrdered && r.set.Count() == 0 {
		head, herr := num.Narrow[uint64](r.sess.WriteHead)
		if herr != nil {
			return Session{}, fmt.Errorf("a session names a write head that does not fit: %w", herr)
		}
		out.Received = head
	}
	return out, nil
}

// effectiveState derives expiry from the clock rather than from a stored flag,
// so a session expires without needing a writer to notice.
func (e *Engine) effectiveState(r *row) SessionState {
	st := SessionState(r.sess.State)
	if st.live() && e.clk.Nanos() > r.sess.ExpiresNs {
		return StateExpired
	}
	return st
}

// requireOwner folds "belongs to someone else" into "does not exist".
//
// A session id is the whole of an upload URL, so telling a stranger that one
// is real but not theirs is an existence oracle.
func requireOwner(r *row, user core.UserID) error {
	if r.sess.User != int64(user) {
		return ErrNotFound
	}
	return nil
}

// requireReceiving refuses a call against a session that is not taking bytes.
func (e *Engine) requireReceiving(r *row) error {
	switch e.effectiveState(r) {
	case StateReceiving:
		return nil
	case StateExpired, StateAborted:
		return ErrSessionExpired
	default:
		return ErrSessionState
	}
}

func rangesOf(runs [][2]uint64) []Range {
	out := make([]Range, 0, len(runs))
	for _, r := range runs {
		out = append(out, Range{Lo: r[0], Hi: r[1]})
	}
	return out
}
