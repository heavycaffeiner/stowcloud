//go:build linux

package upload

import (
	"context"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The transfer-id alias. It is what makes a named chunk collection resumable
// after a restart: the client addresses its upload by an id it chose, and
// without a stored binding that id means nothing to a process that did not
// create it.
//
// The alias is never a session key on its own. A transfer id is the client's
// own string, so it is guessable and collidable; every lookup is scoped to the
// authenticated account, and that scoping is the whole of the security.

// aliasMaxBytes bounds a transfer id. It arrives in a URL path segment, so it
// is untrusted input and is bounded before it reaches a query.
const aliasMaxBytes = limits.NameBytes

// BindAlias binds a client-chosen transfer id to a session inside one
// account's namespace.
//
// It is called after Create succeeds, so a failed creation leaves no binding
// pointing at a session that does not exist. An id the account already holds
// is refused rather than rebound: a silent rebind would orphan the first
// session's spool directory with nothing left naming it.
func (e *Engine) BindAlias(ctx context.Context, tid string, user core.UserID, id SessionID) error {
	if err := checkTransferID(tid); err != nil {
		return err
	}
	r, err := e.load(ctx, id)
	if err != nil {
		return err
	}
	if oerr := requireOwner(r, user); oerr != nil {
		return oerr
	}
	bound, err := e.state.BindUploadAlias(ctx, tid, int64(user), state.UploadAlias{
		Session: id.Bytes(),
		Share:   r.sess.Share,
		Dest:    r.sess.Dest,
	}, e.clk.Nanos())
	if err != nil {
		return err
	}
	if !bound {
		return fmt.Errorf("%w: %q", ErrAliasTaken, tid)
	}
	return nil
}

// Alias is a resolved transfer id: the session it names, and the share and
// destination captured when it was bound.
//
// The share and destination are captured at bind time rather than re-resolved,
// so a later call reuses exactly what the session was created against rather
// than a path that may since have come to mean something else.
type Alias struct {
	Session SessionID
	Share   core.ShareID
	Dest    vfs.SharePath
}

// LookupAlias resolves a transfer id within one account's namespace.
//
// An id belonging to another account resolves to ErrNotFound, identically to
// one that never existed, so the lookup is not an existence oracle.
func (e *Engine) LookupAlias(ctx context.Context, tid string, user core.UserID) (Alias, error) {
	if err := checkTransferID(tid); err != nil {
		return Alias{}, err
	}
	a, err := e.state.LookupUploadAlias(ctx, tid, int64(user))
	if errors.Is(err, state.ErrNoSuchUploadSession) {
		return Alias{}, ErrNotFound
	}
	if err != nil {
		return Alias{}, err
	}
	id, ierr := sessionIDFromBytes(a.Session)
	if ierr != nil {
		return Alias{}, ErrNotFound
	}
	share, ok := shareIDOf(a.Share)
	if !ok {
		return Alias{}, ErrNotFound
	}
	dest, derr := vfs.ParseSharePath(a.Dest)
	if derr != nil {
		return Alias{}, ErrNotFound
	}
	return Alias{Session: id, Share: share, Dest: dest}, nil
}

// UnbindAlias drops one transfer id from an account's namespace. The session
// itself is untouched: a client that unbinds an id has stopped addressing the
// upload that way, not abandoned it.
func (e *Engine) UnbindAlias(ctx context.Context, tid string, user core.UserID) error {
	if err := checkTransferID(tid); err != nil {
		return err
	}
	return e.state.UnbindUploadAlias(ctx, tid, int64(user))
}

// checkTransferID is the trust boundary on a client-chosen id: it arrives in a
// URL and is bounded and rejected for the shapes that cannot name anything,
// before it reaches a query.
func checkTransferID(tid string) error {
	switch {
	case tid == "":
		return fmt.Errorf("%w: a transfer id cannot be empty", ErrBadRequest)
	case len(tid) > aliasMaxBytes:
		return limits.Exceed("transfer id bytes", aliasMaxBytes, int64(len(tid)))
	}
	for i := 0; i < len(tid); i++ {
		if b := tid[i]; b <= 0x1F || b == 0x7F || b == '/' {
			return fmt.Errorf("%w: a transfer id carries a control character or a separator", ErrBadRequest)
		}
	}
	return nil
}
