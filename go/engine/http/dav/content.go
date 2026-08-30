//go:build linux

package dav

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// KeyOf is how a caller turns an entry into the key its store understands.
//
// Supplied by the assembly, which sees both tiers. Nil means no properties are
// kept, the same as a nil store.
type KeyOf func(core.Entry) ResourceKey

// GET, HEAD, PUT, MKCOL and DELETE.
//
// Each is a shell around a core call. What this layer contributes is the
// protocol framing: which status a result answers with, the validator headers,
// and the byte range. No grant is evaluated here, because the resolution these
// take was produced by a mount that already did.

// StoredProp is one dead property as this package handles it.
//
// Its own type rather than the database row, because a protocol package may
// not import the storage tier. The assembly, which sees both, converts.
type StoredProp struct {
	// NS is the property's namespace URI, never a prefix: a prefix is local
	// to the document it appeared in and means nothing once stored.
	NS string
	// Name is the local name.
	Name string
	// Value is the text content.
	Value string
}

// Store is the dead-property half of the durable state.
//
// The resource is named by an opaque key rather than by a filesystem identity,
// so this package never learns how identity is spelled. A deployment that
// keeps no properties passes nil: PROPFIND then returns the live ones alone
// and a delete has nothing to clean up.
type Store interface {
	// Props reads what is stored against a resource.
	Props(ctx context.Context, key ResourceKey) ([]StoredProp, error)
	// SetProps applies a whole instruction set, or none of it. RFC 4918 makes
	// PROPPATCH atomic across its instructions, so a store that applied a
	// prefix would leave a resource in a state the response does not describe.
	SetProps(ctx context.Context, key ResourceKey, ops []PropWrite) error
	// DropProps discards them. A delete does this so the rows do not outlive
	// the resource and attach to whatever next occupies the inode.
	DropProps(ctx context.Context, key ResourceKey) error
}

// PropWrite is one instruction against the property store.
type PropWrite struct {
	// NS and Name identify the property.
	NS   string
	Name string
	// Value is what a set writes, and is ignored by a removal.
	Value string
	// Remove deletes rather than writes.
	Remove bool
}

// ResourceKey identifies the resource a property belongs to.
//
// The identity travels as an opaque value this package only passes through:
// what makes a resource the same resource across a rename is the storage
// tier's business, and a protocol handler that understood it could not avoid
// depending on how it is spelled.
type ResourceKey any

// Get answers GET and HEAD.
//
// body is false for HEAD, which is otherwise the same request: the headers a
// client reads to decide whether to fetch have to match what GET would send,
// so they are produced by the same path rather than by a second one that can
// drift.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request, res core.Resolved, body bool) {
	st, err := res.Root().Stat(res.Path())
	if err != nil {
		h.fail(w, r, core.ErrNotFound)
		return
	}
	// RFC 4918 defines no body for a GET of a collection. Refusing beats
	// inventing an index page that a client would then try to parse.
	if st.Kind.IsDir() {
		h.methodNotAllowed(w, r)
		return
	}

	entry := h.core.EntryAt(res, st)
	if h.notModified(w, r, entry) {
		return
	}

	rng, rerr := parseByteRange(r.Header.Get("Range"), entry.Size)
	if rerr != nil {
		// The unsatisfiable answer carries the full size, which is what lets a
		// client that guessed wrong correct its next request.
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatUint(entry.Size, 10))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}

	opened, stream, oerr := h.core.OpenStream(r.Context(), res, rng)
	if oerr != nil {
		h.fail(w, r, oerr)
		return
	}
	defer h.closing(r, stream, "the source stream")

	w.Header().Set("ETag", ETagHeader(entry.ETag, entry.ETagWeak))
	w.Header().Set("Last-Modified", httpDateOf(entry.MTimeNs))
	w.Header().Set("Content-Type", ContentTypeOf(opened.Name))
	w.Header().Set("Content-Length", strconv.FormatUint(stream.Remaining(), 10))
	w.Header().Set("Accept-Ranges", "bytes")

	status := http.StatusOK
	if rng != nil {
		status = http.StatusPartialContent
		w.Header().Set("Content-Range",
			fmt.Sprintf("bytes %d-%d/%d", rng[0], rng[1], entry.Size))
	}
	w.WriteHeader(status)

	if !body {
		return
	}
	if _, cerr := io.Copy(w, stream); cerr != nil {
		// The status line is already gone, so this cannot become an error
		// response. It is logged and the connection carries a short body.
		h.log(r).Warn("the response body stopped early", "error", cerr)
	}
}

// notModified applies If-None-Match and reports whether it answered.
func (h *Handler) notModified(w http.ResponseWriter, r *http.Request, e core.Entry) bool {
	header := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if header == "" {
		return false
	}
	if header != "*" && !anyValidatorMatches(header, e.ETag) {
		return false
	}
	w.Header().Set("ETag", ETagHeader(e.ETag, e.ETagWeak))
	w.WriteHeader(http.StatusNotModified)
	return true
}

// anyValidatorMatches compares a comma-separated validator list to a tag.
//
// The weak comparison, so "W/" is stripped and a weak tag does match. That is
// the reverse of the If header's rule and is deliberate: this decides whether
// a cached copy is still good, and that one decides whether a write may land.
func anyValidatorMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		part = strings.TrimPrefix(part, "W/")
		if len(part) < 2 || part[0] != '"' || part[len(part)-1] != '"' {
			continue
		}
		if part[1:len(part)-1] == etag {
			return true
		}
	}
	return false
}

// Put writes a file.
func (h *Handler) Put(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	if err := h.guard(r, res); err != nil {
		h.fail(w, r, err)
		return
	}

	existed := false
	if st, err := res.Root().Stat(res.Path()); err == nil {
		if st.Kind.IsDir() {
			// Replacing a collection would mean removing it and everything
			// under it, which the client did not ask for.
			h.fail(w, r, core.ErrExists)
			return
		}
		existed = true
	}

	ifMatch, usable := parseValidator(r.Header.Get("If-Match"))
	if r.Header.Get("If-Match") != "" && !usable {
		h.fail(w, r, ErrPreconditionFailed)
		return
	}

	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	entry, err := h.core.WriteStream(r.Context(), res, body, ifMatch)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	w.Header().Set("ETag", ETagHeader(entry.ETag, entry.ETagWeak))
	if existed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// Mkcol creates a collection.
func (h *Handler) Mkcol(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	// RFC 4918 gives MKCOL no body format, so one that arrives cannot be
	// honoured without inventing a meaning for it.
	if r.ContentLength > 0 {
		h.fail(w, r, ErrUnsupportedMedia)
		return
	}
	if err := h.guard(r, res); err != nil {
		h.fail(w, r, err)
		return
	}

	if _, err := h.core.Mkdir(r.Context(), res); err != nil {
		// An absent parent answers 409 and not 404. The difference is real to
		// a client: 404 says the target is missing, which is the point of
		// creating it, while 409 says the parent is. A client that creates
		// parents on demand branches on exactly that.
		if errors.Is(err, core.ErrNotFound) && !parentExists(res) {
			h.fail(w, r, core.ErrConflict)
			return
		}
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// parentExists reports whether the enclosing collection is there. A share root
// always is, so anything one level inside it has a parent.
func parentExists(res core.Resolved) bool {
	p := res.Path()
	if p.IsRoot() || p.Parent().IsRoot() {
		return true
	}
	st, err := res.Root().Stat(p.Parent())
	return err == nil && st.Kind.IsDir()
}

// Delete removes a resource.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	if err := h.guard(r, res); err != nil {
		h.fail(w, r, err)
		return
	}

	st, serr := res.Root().Stat(res.Path())
	if serr != nil {
		h.fail(w, r, core.ErrNotFound)
		return
	}
	entry := h.core.EntryAt(res, st)

	if err := h.core.Delete(r.Context(), res, false); err != nil {
		h.fail(w, r, err)
		return
	}

	// Stored properties go with the resource. Left behind they would attach
	// to whatever next occupies the inode, so a new file would be born
	// carrying a deleted one's properties.
	if h.store != nil && h.keyOf != nil {
		if err := h.store.DropProps(r.Context(), h.keyOf(entry)); err != nil {
			h.log(r).Warn("a deleted resource's properties remain", "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseValidator reads a single-value If-Match into the core's token.
//
// A weak validator is refused rather than passed through. The core's rule is
// that a weak tag never satisfies a precondition, so forwarding one would be a
// check that cannot pass presented as one that might.
func parseValidator(header string) (*core.Token, bool) {
	header = strings.TrimSpace(header)
	switch {
	case header == "":
		return nil, false
	case header == "*":
		// Any current representation satisfies it, which constrains nothing.
		return nil, true
	case strings.HasPrefix(header, "W/"):
		return nil, false
	case len(header) >= 2 && header[0] == '"' && header[len(header)-1] == '"':
		t := core.Token(header[1 : len(header)-1])
		return &t, true
	default:
		return nil, false
	}
}

// parseByteRange reads a single-range Range header.
//
// One range only. A multipart response is a format this server does not write,
// and answering the whole file to a request for part of it is what a resuming
// download reads as the transfer restarting.
func parseByteRange(header string, size uint64) (*[2]uint64, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, nil
	}
	spec, ok := strings.CutPrefix(header, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return nil, ErrBadRange
	}

	first, last, found := strings.Cut(spec, "-")
	if !found {
		return nil, ErrBadRange
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)

	switch {
	case first == "" && last == "":
		return nil, ErrBadRange

	case first == "":
		// A suffix range: the last n bytes. Larger than the file is the whole
		// file, which RFC 9110 allows and a client uses to mean "from the
		// start" without knowing the size.
		n, err := strconv.ParseUint(last, 10, 64)
		if err != nil || n == 0 {
			return nil, ErrBadRange
		}
		if n > size {
			n = size
		}
		return &[2]uint64{size - n, size - 1}, nil

	case last == "":
		start, err := strconv.ParseUint(first, 10, 64)
		if err != nil || start >= size {
			return nil, ErrBadRange
		}
		return &[2]uint64{start, size - 1}, nil

	default:
		start, serr := strconv.ParseUint(first, 10, 64)
		end, eerr := strconv.ParseUint(last, 10, 64)
		if serr != nil || eerr != nil || start > end || start >= size {
			return nil, ErrBadRange
		}
		// A range running past the end is clamped rather than refused, which
		// is what RFC 9110 asks for.
		if end >= size {
			end = size - 1
		}
		return &[2]uint64{start, end}, nil
	}
}

// ErrBadRange is a Range header that cannot be satisfied.
var ErrBadRange = errors.New("the requested range is not satisfiable")
