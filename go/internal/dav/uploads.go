//go:build linux

package dav

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/core"
)

// The chunked-upload collection.
//
// This package knows it as a collection with unusual semantics, not as any
// vendor's feature: a MKCOL opens a session, a PUT of a numerically-named
// member contributes a chunk, and a MOVE of the collection assembles it onto
// the destination. The compat layer is what points a client at it.
//
// The backing is the upload engine's name-ordered spool mode, which is why the
// members are named by number: they are assembled in the order of their names.

// UploadCollection is the engine seam.
//
// It is an interface so this package does not import the upload engine, which
// keeps the protocol layer free of the engine's vocabulary and lets the tests
// drive it without a filesystem.
type UploadCollection interface {
	// Open starts a session for a destination and returns its id.
	Open(ctx context.Context, res core.Resolved, name string, total *uint64) (string, error)
	// PutChunk contributes one numerically-named member.
	PutChunk(ctx context.Context, res core.Resolved, id string, user core.UserID, name uint32, body io.Reader) error
	// Assemble publishes the collection onto dest and returns the entry.
	Assemble(ctx context.Context, dest core.Resolved, id string, total uint64, mtimeNs *int64) (core.Entry, error)
	// Discard abandons a session.
	Discard(ctx context.Context, id string, user core.UserID) error
	// Chunks is which members are currently held, for a PROPFIND of the
	// collection.
	Chunks(ctx context.Context, id string, user core.UserID) ([]uint32, error)
}

// UploadPath is a parsed request against the upload collection.
type UploadPath struct {
	// Session is the collection's own name.
	Session string
	// Member is the chunk name, and Chunk reports whether one was present. A
	// request against the collection itself has no member.
	Member uint32
	Chunk  bool
}

// ParseUploadPath reads a path under the upload collection root.
//
// The member name has to be a plain non-negative decimal. Anything else is
// refused rather than coerced: a name that parsed loosely would assemble in an
// order the client did not intend, and the ordering is the whole contract.
func ParseUploadPath(p string) (UploadPath, error) {
	// Only a leading slash is stripped. A trailing one means the request named
	// a member and left the name off, which is not the collection: treating it
	// as one would turn a malformed PUT into a MKCOL of a session that exists.
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return UploadPath{}, ErrNotFound
	}
	parts := strings.Split(p, "/")
	if len(parts) > 2 {
		return UploadPath{}, ErrNotFound
	}
	out := UploadPath{Session: parts[0]}
	if out.Session == "" {
		return UploadPath{}, ErrNotFound
	}
	if len(parts) == 1 {
		return out, nil
	}

	name := parts[1]
	if name == "" || len(name) > 10 {
		return UploadPath{}, ErrBadRequest
	}
	// A zero-padded name would parse to the same number as its bare form, and
	// the second one written would silently replace the first. One name per
	// chunk means the canonical spelling only.
	if len(name) > 1 && name[0] == '0' {
		return UploadPath{}, ErrBadRequest
	}
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return UploadPath{}, ErrBadRequest
		}
	}
	n, err := strconv.ParseUint(name, 10, 32)
	if err != nil {
		return UploadPath{}, ErrBadRequest
	}
	out.Member, out.Chunk = uint32(n), true
	return out, nil
}

// ServeUpload dispatches a method against the upload collection.
//
// res resolves the *destination* the assembled file lands on, which is what
// carries the permission check. The collection itself is not a real directory
// and has no path of its own on disk.
func (h *Handler) ServeUpload(w http.ResponseWriter, r *http.Request, res core.Resolved, up UploadPath) {
	if h.uploads == nil {
		h.methodNotAllowed(w, r, res)
		return
	}

	switch r.Method {
	case "MKCOL":
		h.uploadOpen(w, r, res, up)
	case "PUT":
		h.uploadPut(w, r, res, up)
	case "MOVE":
		h.uploadAssemble(w, r, res, up)
	case "DELETE":
		h.uploadDiscard(w, r, res, up)
	case "PROPFIND":
		h.uploadPropfind(w, r, res, up)
	case "OPTIONS":
		w.Header().Set("DAV", "1, 2")
		w.Header().Set("Allow", "OPTIONS, MKCOL, PUT, MOVE, DELETE, PROPFIND")
		w.WriteHeader(http.StatusOK)
	default:
		w.Header().Set("Allow", "OPTIONS, MKCOL, PUT, MOVE, DELETE, PROPFIND")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) uploadOpen(w http.ResponseWriter, r *http.Request, res core.Resolved, up UploadPath) {
	if up.Chunk {
		// A chunk is created by PUT. MKCOL of one would be a collection inside
		// a collection, which this has no meaning for.
		h.fail(w, r, ErrBadRequest)
		return
	}
	var total *uint64
	if v := r.Header.Get("OC-Total-Length"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			h.fail(w, r, ErrBadRequest)
			return
		}
		total = &n
	}
	if _, err := h.uploads.Open(r.Context(), res, up.Session, total); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) uploadPut(w http.ResponseWriter, r *http.Request, res core.Resolved, up UploadPath) {
	if !up.Chunk {
		// A PUT of the collection itself has no chunk name, so there is no
		// position to write it at.
		h.fail(w, r, ErrBadRequest)
		return
	}
	if r.Body == nil {
		h.fail(w, r, ErrBadRequest)
		return
	}
	if err := h.uploads.PutChunk(r.Context(), res, up.Session, res.User(), up.Member, r.Body); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// uploadAssemble is the MOVE that publishes the collection onto its
// destination. The destination comes from the Destination header, which the
// caller has already resolved into res.
func (h *Handler) uploadAssemble(w http.ResponseWriter, r *http.Request, res core.Resolved, up UploadPath) {
	if up.Chunk {
		h.fail(w, r, ErrBadRequest)
		return
	}

	total, err := strconv.ParseUint(r.Header.Get("OC-Total-Length"), 10, 64)
	if err != nil {
		h.fail(w, r, ErrBadRequest)
		return
	}
	var mtime *int64
	if v := r.Header.Get("X-OC-Mtime"); v != "" {
		secs, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil {
			h.fail(w, r, ErrBadRequest)
			return
		}
		ns := secs * 1_000_000_000
		mtime = &ns
	}

	if err := h.guardWrite(r, res, string(res.Path().Share().String())); err != nil {
		h.fail(w, r, err)
		return
	}

	e, aerr := h.uploads.Assemble(r.Context(), res, up.Session, total, mtime)
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}
	w.Header().Set("ETag", etagHeader(e))
	w.Header().Set("OC-ETag", etagHeader(e))
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) uploadDiscard(w http.ResponseWriter, r *http.Request, res core.Resolved, up UploadPath) {
	if up.Chunk {
		// A single chunk is not individually removable: the session's assembly
		// cursor has already consumed the ones before it.
		h.fail(w, r, ErrBadRequest)
		return
	}
	if err := h.uploads.Discard(r.Context(), up.Session, res.User()); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// uploadPropfind reports the members currently held, which is how a resuming
// client learns what it still has to send.
func (h *Handler) uploadPropfind(w http.ResponseWriter, r *http.Request, res core.Resolved, up UploadPath) {
	if up.Chunk {
		h.fail(w, r, ErrNotFound)
		return
	}
	held, err := h.uploads.Chunks(r.Context(), up.Session, res.User())
	if err != nil {
		h.fail(w, r, err)
		return
	}

	base := hrefOf(r.URL.Path, true)
	m := NewMultistatus(w, h.namespaces())
	if werr := m.Write(Response{
		Href:  base,
		Found: []Prop{{Name: DavName("resourcetype"), Raw: "<" + davPrefix + ":collection/>"}},
	}); werr != nil {
		h.logger(r).Warn("the collection could not be written", "error", werr)
	}
	for _, n := range held {
		name := strconv.FormatUint(uint64(n), 10)
		if werr := m.Write(Response{
			Href:  base + name,
			Found: []Prop{{Name: DavName("resourcetype")}},
		}); werr != nil {
			h.logger(r).Warn("a member could not be written", "error", werr)
			break
		}
	}
	if cerr := m.Close(); cerr != nil {
		h.logger(r).Warn("the multistatus could not be closed", "error", cerr)
	}
}
