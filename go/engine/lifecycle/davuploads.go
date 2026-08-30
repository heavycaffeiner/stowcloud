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
func (a davUploads) Open(
	ctx context.Context, res core.Resolved, name string, total *uint64,
) error {
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
func (a davUploads) PutChunk(
	ctx context.Context, res core.Resolved, name string, member uint32, body io.Reader,
) error {
	id, err := a.lookup(ctx, res, name)
	if err != nil {
		return err
	}
	return a.engine.PutNamed(ctx, res.Root(), id, res.User(), member, body, nil)
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
	return a.engine.UnbindAlias(ctx, name, res.User())
}

// Held lists the members stored so far.
func (a davUploads) Held(
	ctx context.Context, res core.Resolved, name string,
) ([]uint32, error) {
	id, err := a.lookup(ctx, res, name)
	if err != nil {
		return nil, err
	}
	return a.engine.ListChunks(ctx, id, res.User())
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
	case errors.Is(err, upload.ErrBadRequest):
		// There is no single bad-request sentinel on the dav side; the status
		// table answers one for the client's faults it knows. The upload
		// engine's message carries which rule broke, and losing it would tell
		// the client only that something was wrong, so it travels wrapped.
		return markBadRequest(err)
	default:
		return err
	}
}
