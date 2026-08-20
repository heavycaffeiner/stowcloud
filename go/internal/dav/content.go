//go:build linux

package dav

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The content methods: GET, HEAD, PUT, DELETE, MKCOL, COPY and MOVE.
//
// Each one is a thin shell over the core. The value this layer adds is the
// WebDAV framing: the lock guard, the precondition headers, and the status
// vocabulary. None of it re-checks a grant, because the resolution it was
// handed already did.

func (h *Handler) get(w http.ResponseWriter, r *http.Request, res core.Resolved, body bool) {
	st, err := res.Root().Stat(res.Path())
	if err != nil {
		h.fail(w, r, core.ErrNotFound)
		return
	}
	// A GET of a collection has no defined body in RFC 4918. Refusing is
	// better than inventing an index page a client would try to parse.
	if st.Kind.IsDir() {
		h.methodNotAllowed(w, r, res)
		return
	}

	e := h.core.EntryAt(res, st)
	if done := h.checkReadPreconditions(w, r, e); done {
		return
	}

	entry, stream, err := h.core.OpenStream(r.Context(), res, nil)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer func() {
		if cerr := stream.Close(); cerr != nil {
			h.logger(r).Warn("closing the stream", "error", cerr)
		}
	}()

	w.Header().Set("ETag", etagHeader(e))
	w.Header().Set("Last-Modified", httpDate(e.MTimeNs))
	w.Header().Set("Content-Type", contentTypeOf(entry.Name))
	w.Header().Set("Content-Length", strconv.FormatUint(stream.Remaining(), 10))
	w.Header().Set("Accept-Ranges", "bytes")

	if !body {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	if _, cerr := io.Copy(w, stream); cerr != nil {
		// The status is already sent, so this cannot become an error response.
		h.logger(r).Warn("the body could not be completed", "error", cerr)
	}
}

// checkReadPreconditions applies If-None-Match and reports whether the request
// is already answered.
func (h *Handler) checkReadPreconditions(w http.ResponseWriter, r *http.Request, e core.Entry) bool {
	inm := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if inm == "" {
		return false
	}
	if inm == "*" || matchesAnyETag(inm, e) {
		w.Header().Set("ETag", etagHeader(e))
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

// matchesAnyETag compares a comma-separated validator list against an entry.
//
// If-None-Match uses the weak comparison function, so a weak validator does
// match here. That is the opposite of the If header's rule and is deliberate:
// one guards a cache revalidation, the other guards a write.
func matchesAnyETag(header string, e core.Entry) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "W/")
		if len(part) >= 2 && part[0] == '"' && part[len(part)-1] == '"' {
			if part[1:len(part)-1] == e.ETag {
				return true
			}
		}
	}
	return false
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	if err := res.Require(acl.Write | acl.Create); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.guardWrite(r, res, string(res.Path().Share().String())); err != nil {
		h.fail(w, r, err)
		return
	}

	existed := false
	if st, err := res.Root().Stat(res.Path()); err == nil {
		if st.Kind.IsDir() {
			// A PUT onto a collection would have to remove it first, which is
			// a delete the client did not ask for.
			h.fail(w, r, core.ErrExists)
			return
		}
		existed = true
	}

	ifMatch, hasIfMatch := parseValidator(r.Header.Get("If-Match"))
	if r.Header.Get("If-Match") != "" && !hasIfMatch {
		h.fail(w, r, ErrPreconditionFailed)
		return
	}

	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	// A new file is group-writable and world-readable, matching what the native
	// API creates. A zero mode here would produce a file nobody can read,
	// including the process that just wrote it. When the destination already
	// exists its own mode wins, which is the VFS's rule rather than this one's.
	e, err := h.core.CreateFile(r.Context(), res, vfs.DurableOpts{Mode: 0o664}, ifMatch,
		func(f *vfs.File) error {
			_, cerr := io.Copy(&fileWriter{f: f}, body)
			return cerr
		})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	w.Header().Set("ETag", etagHeader(e))
	if existed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// fileWriter adapts a vfs.File to io.Writer.
//
// The file is written positionally (pwrite), which has no implicit cursor, so
// the offset is tracked here rather than by the descriptor.
type fileWriter struct {
	f   *vfs.File
	off int64
}

func (w *fileWriter) Write(p []byte) (int, error) {
	n, err := w.f.WriteAt(p, w.off)
	w.off += int64(n)
	return n, err
}

func (h *Handler) mkcol(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	// RFC 4918: a MKCOL with a body is 415, because no body format is defined
	// and honouring an unknown one would be inventing semantics.
	if r.ContentLength > 0 {
		h.fail(w, r, errUnsupportedMedia)
		return
	}
	if err := h.guardWrite(r, res, string(res.Path().Share().String())); err != nil {
		h.fail(w, r, err)
		return
	}
	if _, err := h.core.Mkdir(r.Context(), res); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) del(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	if err := h.guardWrite(r, res, string(res.Path().Share().String())); err != nil {
		h.fail(w, r, err)
		return
	}

	st, serr := res.Root().Stat(res.Path())
	if serr != nil {
		h.fail(w, r, core.ErrNotFound)
		return
	}
	e := h.core.EntryAt(res, st)

	if err := h.core.Delete(r.Context(), res, false); err != nil {
		h.fail(w, r, err)
		return
	}
	// The properties go with the resource. Leaving them behind would attach
	// them to whatever next occupies the inode.
	if h.state != nil {
		if err := h.state.DropDavProps(r.Context(), identOf(e)); err != nil {
			h.logger(r).Warn("the dead properties outlived their resource", "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// MoveTarget is the destination of a COPY or a MOVE, already resolved by the
// caller from the Destination header.
type MoveTarget struct {
	Resolved core.Resolved
	// Overwrite is the header's value, defaulting to true per RFC 4918.
	Overwrite bool
}

// ServeMove answers a MOVE, and ServeCopy a COPY. Both are separate from
// ServeMethod because the destination arrives as a URL in a header, and turning
// that into a resolution is the router's job: this package is handed resolved
// paths and does not parse virtual ones.
func (h *Handler) ServeMove(w http.ResponseWriter, r *http.Request, from core.Resolved, to MoveTarget) {
	h.move(w, r, from, to)
}

// ServeCopy answers a COPY.
//
// A recursive copy is started as a background operation and answered 202,
// because a copy of a large tree cannot be done inside a request and a client
// waiting minutes on a socket is the failure mode this avoids.
func (h *Handler) ServeCopy(w http.ResponseWriter, r *http.Request, from core.Resolved, to MoveTarget) {
	if err := h.guardWrite(r, to.Resolved, string(to.Resolved.Path().Share().String())); err != nil {
		h.fail(w, r, err)
		return
	}
	if _, err := to.Resolved.Root().Stat(to.Resolved.Path()); err == nil && !to.Overwrite {
		h.fail(w, r, ErrPreconditionFailed)
		return
	}

	st, serr := from.Root().Stat(from.Path())
	if serr != nil {
		h.fail(w, r, core.ErrNotFound)
		return
	}

	if st.Kind.IsDir() {
		if _, err := h.core.StartCopy(r.Context(), from.User(), from, to.Resolved); err != nil {
			h.fail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		return
	}

	// A single file is copied inline: it is one read and one durable write, and
	// a client would rather have the answer than a job to poll.
	if err := h.copyFile(r, from, to.Resolved); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) copyFile(r *http.Request, from, to core.Resolved) error {
	_, stream, err := h.core.OpenStream(r.Context(), from, nil)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := stream.Close(); cerr != nil {
			h.logger(r).Warn("closing the source", "error", cerr)
		}
	}()

	_, err = h.core.CreateFile(r.Context(), to, vfs.DurableOpts{Mode: 0o664}, nil,
		func(f *vfs.File) error {
			_, cerr := io.Copy(&fileWriter{f: f}, stream)
			return cerr
		})
	return err
}

// ParseOverwrite reads the Overwrite header. RFC 4918 defaults it to true, and
// only "F" turns it off.
func ParseOverwrite(h string) bool {
	return !strings.EqualFold(strings.TrimSpace(h), "F")
}

func (h *Handler) move(w http.ResponseWriter, r *http.Request, res core.Resolved, to MoveTarget) {
	if err := h.guardWrite(r, res, string(res.Path().Share().String())); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.guardWrite(r, to.Resolved, string(to.Resolved.Path().Share().String())); err != nil {
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

	if _, err := h.core.Move(r.Context(), res, to.Resolved,
		core.MoveOpts{Overwrite: to.Overwrite}); err != nil {
		h.fail(w, r, err)
		return
	}
	if existed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// parseValidator reads a single-value If-Match into the core's token type.
//
// A weak validator is refused rather than accepted: the core's precondition
// rule is that a weak tag can never match, so passing one through would be a
// check that cannot succeed dressed as one that might.
func parseValidator(h string) (*core.Token, bool) {
	h = strings.TrimSpace(h)
	switch {
	case h == "":
		return nil, false
	case h == "*":
		// Any current representation satisfies it, which is no constraint.
		return nil, true
	case strings.HasPrefix(h, "W/"):
		return nil, false
	case len(h) >= 2 && h[0] == '"' && h[len(h)-1] == '"':
		t := core.Token(h[1 : len(h)-1])
		return &t, true
	}
	return nil, false
}

var errUnsupportedMedia = errors.New("dav: a body in a request that defines none")
