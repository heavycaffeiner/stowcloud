//go:build linux

package dav

import (
	"io"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// COPY and MOVE.
//
// Both are separate from the single-endpoint methods because their destination
// arrives as a URL in a header. Turning that into a resolution is the mount's
// work, so what is here takes two resolutions and never parses a path.

// Target is a COPY or MOVE destination the caller has already resolved.
type Target struct {
	// Resolved is the destination, resolved and permission-checked by the
	// mount exactly as the source was.
	Resolved core.Resolved
	// Overwrite is the header's value. RFC 4918 defaults it to true, so only
	// an explicit "F" turns it off.
	Overwrite bool
}

// Copy duplicates a resource.
//
// A recursive copy becomes a background operation and answers 202. A tree of
// any size cannot be copied inside one request, and a client left holding a
// socket for minutes is the failure this avoids. A single file is done inline,
// because that is one read and one durable write and the client would rather
// have the answer than a job to poll.
func (h *Handler) Copy(w http.ResponseWriter, r *http.Request, from core.Resolved, to Target) {
	if err := h.guard(r, to.Resolved); err != nil {
		h.fail(w, r, err)
		return
	}
	// A destination inside the source never terminates: each pass copies what
	// the previous one just wrote. RFC 4918 refuses it outright.
	if err := core.RefuseSelfDescendant(from, to.Resolved); err != nil {
		h.fail(w, r, err)
		return
	}

	if _, dstErr := to.Resolved.Root().Stat(to.Resolved.Path()); dstErr == nil && !to.Overwrite {
		h.fail(w, r, ErrPreconditionFailed)
		return
	}

	srcSt, serr := from.Root().Stat(from.Path())
	if serr != nil {
		h.fail(w, r, core.ErrNotFound)
		return
	}

	// Replacing an existing destination collection is the core's, which does
	// it inside the same conflict decision that picks the destination. Doing
	// it here as well was a second delete of the same directory, and the one
	// that matters is the core's: a copy skipped or renamed by policy never
	// reaches this line, so a delete here would remove a collection the
	// transfer then declined to replace.
	if srcSt.Kind.IsDir() {
		policy := core.ConflictFail
		if to.Overwrite {
			policy = core.ConflictOverwrite
		}
		if _, err := h.core.StartCopy(r.Context(), from.User(), from, to.Resolved, policy); err != nil {
			h.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if err := h.copyFile(r, from, to.Resolved); err != nil {
		h.fail(w, r, err)
		return
	}
	// 201 whether or not something was replaced. RFC 4918 answers a
	// single-resource COPY this way, and the 204 that distinguishes a
	// replacement belongs to PUT.
	w.WriteHeader(http.StatusCreated)
}

// copyFile streams one file to a new durable write.
func (h *Handler) copyFile(r *http.Request, from, to core.Resolved) error {
	_, stream, err := h.core.OpenStream(r.Context(), from, nil)
	if err != nil {
		return err
	}
	defer h.closing(r, stream, "the copy source")

	_, err = h.core.CreateFile(r.Context(), to, vfs.DurableOpts{Mode: newFileMode}, nil,
		func(f *vfs.File) error {
			_, cerr := io.Copy(&fileWriter{f: f}, stream)
			return cerr
		})
	return err
}

// Move relocates a resource.
//
// Both endpoints are guarded, because a move writes at both: it removes the
// source and creates the destination, and a lock over either has to refuse.
func (h *Handler) Move(w http.ResponseWriter, r *http.Request, from core.Resolved, to Target) {
	if err := h.guard(r, from); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.guard(r, to.Resolved); err != nil {
		h.fail(w, r, err)
		return
	}
	// Same reasoning as COPY, and one case more: a move onto itself would
	// otherwise report success having done nothing, and a move into a
	// descendant would relocate a tree underneath itself.
	if err := core.RefuseSelfDescendant(from, to.Resolved); err != nil {
		h.fail(w, r, err)
		return
	}

	existed := false
	if _, err := to.Resolved.Root().Stat(to.Resolved.Path()); err == nil {
		if !to.Overwrite {
			h.fail(w, r, ErrPreconditionFailed)
			return
		}
		existed = true
	}

	if _, err := h.core.Move(r.Context(), from, to.Resolved,
		core.MoveOpts{Overwrite: to.Overwrite}); err != nil {
		h.fail(w, r, err)
		return
	}

	// 204 when something was replaced and 201 when nothing was. A client
	// reads the difference to know whether it destroyed anything.
	if existed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
