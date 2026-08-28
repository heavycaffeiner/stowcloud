//go:build linux

package upload

import (
	"context"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// The transfer-id alias is what lets a named chunk collection resume after a
// restart. Clients address an upload by an id they chose, and absent a stored
// binding that id conveys nothing to a process that did not create it.
//
// An alias never serves as a session key by itself. A transfer id is a
// client-supplied string, so it is both guessable and prone to collision. Every
// lookup is confined to the authenticated account, and that confinement is the
// entirety of the security.

// aliasMaxBytes caps a transfer id. It arrives inside a URL path segment, making
// it untrusted input that is bounded before reaching any statement.
const aliasMaxBytes = limits.NameBytes

// BindAlias associates a client-chosen transfer id with a session inside one
// account's namespace.
//
// It runs only after Create succeeds, so a failed creation leaves no binding
// referencing a nonexistent session. An id the account already holds is rejected
// instead of rebound, since a silent rebind would strand the first session's
// spool with nothing naming it.
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

// LookupAlias resolves a transfer id inside one account's namespace.
//
// An id owned by a different account resolves exactly as one that never existed,
// keeping the lookup from acting as an existence oracle.
//
// Share and destination come from what bind time captured rather than being
// resolved again, so a later call reuses precisely what the session was created
// against instead of a path that may since denote something else.
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
	return Alias{Session: id, Share: share, Dest: a.Dest}, nil
}

// UnbindAlias removes a transfer id from an account's namespace, leaving the
// session intact. A client that unbinds an id has merely stopped addressing the
// upload that way rather than abandoning it.
func (e *Engine) UnbindAlias(ctx context.Context, tid string, user core.UserID) error {
	if err := checkTransferID(tid); err != nil {
		return err
	}
	return e.state.UnbindUploadAlias(ctx, tid, int64(user))
}

// checkTransferID is the trust boundary on a client-chosen id: it arrives in
// a URL and is bounded and refused for the shapes that cannot name anything,
// before it reaches a statement.
func checkTransferID(tid string) error {
	switch {
	case tid == "":
		return fmt.Errorf("%w: a transfer id cannot be empty", ErrBadRequest)
	case len(tid) > aliasMaxBytes:
		return limits.Exceed("transfer id bytes", aliasMaxBytes, int64(len(tid)))
	}
	for i := 0; i < len(tid); i++ {
		if b := tid[i]; b <= 0x1F || b == 0x7F || b == '/' {
			return fmt.Errorf("%w: a transfer id carries a control character or a separator",
				ErrBadRequest)
		}
	}
	return nil
}
