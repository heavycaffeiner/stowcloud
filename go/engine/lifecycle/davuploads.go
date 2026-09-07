//go:build linux

// The dav upload collection, backed by the real upload engine.
//
// The protocol package defines what a chunked upload collection is and the
// engine owns the spool. This is the join: one adapter, so neither side learns
// the other's vocabulary.
package lifecycle

import (
	"context"
	"errors"
	"io"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/upload"
)

// davUploads adapts the upload engine to the collection the dav package
// serves.
type davUploads struct {
	engine *upload.Engine
}

// NewDavUploads builds the adapter. Nil engine is a deployment without the
// collection, which the dav package answers 405 for.
func NewDavUploads(engine *upload.Engine) dav.Uploads {
	if engine == nil {
		return nil
	}
	return davUploads{engine: engine}
}

// Open starts a session in name-ordered spool mode.
//
// The collection name becomes the session's alias rather than its key. A
// transfer id arrives from a client, so it is both guessable and prone to
// collision; the binding is per account, which is what keeps one account from
// naming its way into another's in-flight upload.
//
// Opening a collection the account already holds resumes it rather than
// failing. The reference client re-runs the whole operation to resume, so it
// opens the same collection again, reads back the chunks already there and
// sends only what is missing. Refusing the second open ended every resumed
// transfer before its first chunk, and a client that had lost the response to
// the first open could never proceed at all. The resume is confined to a
// session whose share and destination match: a collection naming somewhere
// else is a different transfer that happens to share a name, and adopting it
// would publish these bytes at that destination.
func (a davUploads) Open(
	ctx context.Context, res core.Resolved, name string, total *uint64,
) error {
	if alias, lerr := a.engine.LookupAlias(ctx, name, res.User()); lerr == nil {
		if alias.Share != res.Share() || alias.Dest != res.Path().String() {
			return core.ErrExists
		}
		// The alias outlives the session it names: expiry and abort leave the
		// row, and only a discard unbinds it. Reusing one of those would
		// answer 201 to an open and then refuse every chunk that followed,
		// which tells the client the collection is there while nothing can be
		// written into it. A session that is no longer receiving is replaced
		// instead, so the name the client keeps addressing goes on working.
		if sess, gerr := a.engine.Get(ctx, alias.Session, res.User()); gerr == nil &&
			sess.State == upload.StateReceiving {
			return nil
		}
		if uerr := a.engine.UnbindAlias(ctx, name, res.User()); uerr != nil {
			return translateUploadError(uerr)
		}
	}

	spec := upload.SessionSpec{
		TotalLen: total,
		Mode:     upload.SpoolNameOrdered,
		Meta:     upload.Meta{Filename: res.Path().Name()},
	}
	sess, err := a.engine.Create(ctx, res, spec)
	if err != nil {
		return translateUploadError(err)
	}
	if berr := a.engine.BindAlias(ctx, name, res.User(), sess.ID); berr != nil {
		// The session exists with no alias naming it, so it is abandoned rather
		// than left as a spool nothing can reach. An abandonment failure rides
		// along with the bind failure: both are real, and dropping either would
		// hide a spool nobody can name.
		if aerr := a.engine.Abort(ctx, sess.ID, res.User()); aerr != nil {
			return translateUploadError(errors.Join(berr, aerr))
		}
		return translateUploadError(berr)
	}
	return nil
}

// lookup resolves a collection name inside the caller's account.
//
// The share and destination come back from what bind time captured. A path
// that has since moved or a share since unmounted is answered with what the
// session was actually opened against, not with a fresh resolution that may
// now denote somewhere else.
func (a davUploads) lookup(
	ctx context.Context, res core.Resolved, name string,
) (upload.SessionID, error) {
	alias, err := a.engine.LookupAlias(ctx, name, res.User())
	if err != nil {
		return upload.SessionID{}, translateUploadError(err)
	}
	// The session was opened against one share. A collection resolved
	// somewhere else is a different collection, even when the name matches:
	// the alias is scoped by account only, so without this check two shares
	// holding the same transfer id share one spool, and a chunk meant for one
	// lands in the other's and publishes there.
	if alias.Share != res.Share() {
		return upload.SessionID{}, core.ErrNotFound
	}
	return alias.Session, nil
}

// PutChunk stores one member.
//
// The engine's refusal is translated like every other: a chunk aimed at a
// session that has expired or been abandoned is the client's to recover from
// by opening a new collection, and reporting it as a server fault has a sync
// client retry the whole file against a session that will never take it.
func (a davUploads) PutChunk(
	ctx context.Context, res core.Resolved, name string, member uint32, body io.Reader,
) error {
	id, err := a.lookup(ctx, res, name)
	if err != nil {
		return err
	}
	return translateUploadError(
		a.engine.PutNamed(ctx, res.Root(), id, res.User(), member, body, nil))
}

// malformedUpload marks an engine refusal as the request's fault.
//
// The dav status table cannot import the upload engine, so the mark travels
// as a capability its own interface recognises rather than as a sentinel that
// would have to be declared there.
type malformedUpload struct{ cause error }

func (m malformedUpload) Error() string    { return m.cause.Error() }
func (m malformedUpload) Unwrap() error    { return m.cause }
func (m malformedUpload) BadRequest() bool { return true }

func markBadRequest(err error) error { return malformedUpload{cause: err} }

// Assemble publishes the collection onto the destination.
func (a davUploads) Assemble(
	ctx context.Context, res core.Resolved, name string,
	total uint64, mtimeNs *int64,
) (core.Entry, error) {
	id, err := a.lookup(ctx, res, name)
	if err != nil {
		return core.Entry{}, err
	}
	entry, aerr := a.engine.Assemble(ctx, res, id, total, mtimeNs)
	return entry, translateUploadError(aerr)
}

// Discard abandons a session and its alias.
//
// The session is abandoned before the alias is unbound: if the unbind fails,
// the leftover alias names a session that is gone, and a lookup of it answers
// not-found. The reverse order would leave a spool an alias still names.
func (a davUploads) Discard(ctx context.Context, res core.Resolved, name string) error {
	id, err := a.lookup(ctx, res, name)
	if err != nil {
		return err
	}
	if aerr := a.engine.Abort(ctx, id, res.User()); aerr != nil {
		return translateUploadError(aerr)
	}
	return translateUploadError(a.engine.UnbindAlias(ctx, name, res.User()))
}

// Held lists the members stored so far, with the size of each.
//
// The size is what a resuming client sums to decide where to start, so it
// crosses the seam beside the name rather than being left for the protocol
// layer to guess at.
func (a davUploads) Held(
	ctx context.Context, res core.Resolved, name string,
) ([]dav.Chunk, error) {
	id, err := a.lookup(ctx, res, name)
	if err != nil {
		return nil, err
	}
	held, lerr := a.engine.ListChunks(ctx, res.Root(), id, res.User())
	if lerr != nil {
		return nil, translateUploadError(lerr)
	}
	out := make([]dav.Chunk, len(held))
	for i, c := range held {
		out[i] = dav.Chunk{Name: c.Name, Size: c.Size}
	}
	return out, nil
}

// The engine's refusals cross into the dav package through translation, not
// by being passed raw: its sentinels are its own, and the dav package cannot
// import them, which is what keeps the layer gate honest.
func translateUploadError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, upload.ErrNotFound):
		return core.ErrNotFound
	case errors.Is(err, upload.ErrBadRequest), errors.Is(err, upload.ErrIncomplete),
		errors.Is(err, upload.ErrChunkTooSmall), errors.Is(err, upload.ErrTooLarge),
		errors.Is(err, upload.ErrAliasTaken), errors.Is(err, upload.ErrOffsetConflict),
		errors.Is(err, upload.ErrChecksum), errors.Is(err, upload.ErrFragmented),
		errors.Is(err, upload.ErrUnknownAlgo):
		// There is no single bad-request sentinel on the dav side; the status
		// table answers one for the client's faults it knows. The upload
		// engine's message carries which rule broke, and losing it would tell
		// the client only that something was wrong, so it travels wrapped.
		//
		// An assembly across a gap belongs here rather than in the default: a
		// transfer missing a chunk is the client's to finish, and answering
		// 500 tells it the server broke, which is what makes a sync client
		// retry the whole file instead of sending the piece it still owes.
		return markBadRequest(err)
	case errors.Is(err, upload.ErrSessionExpired), errors.Is(err, upload.ErrSessionState):
		// The session is gone as far as the client is concerned, and the
		// collection it names no longer exists. Not-found is what makes it
		// open a new one rather than retry into a spool that has been swept.
		return core.ErrNotFound
	default:
		return err
	}
}
