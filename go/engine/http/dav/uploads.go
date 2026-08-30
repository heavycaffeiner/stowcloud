//go:build linux

package dav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The refusals this collection makes.
//
// Named separately rather than sharing one bad-request sentinel, because the
// status table reads them and a reader of a log needs to know which rule the
// request broke.
var (
	// ErrChunkOnCollection reports a method aimed at a member that only the
	// collection accepts, or the reverse.
	ErrChunkOnCollection = errors.New("this method does not apply at that level")
	// ErrNoUploadLength reports an assembly with no declared total length.
	ErrNoUploadLength = errors.New("no declared length for the assembled file")
	// ErrBadUploadLength reports a total length that did not parse.
	ErrBadUploadLength = errors.New("an unusable declared length")
	// ErrBadUploadMTime reports a modification time that did not parse or
	// would not fit.
	ErrBadUploadMTime = errors.New("an unusable modification time")
	// ErrNoBody reports a chunk PUT carrying nothing to store.
	ErrNoBody = errors.New("no body to store")
)

// The chunked upload collection.
//
// This package treats it as a collection with unusual rules rather than as any
// vendor's feature: MKCOL opens a session, a PUT of a numerically named member
// contributes a chunk, and a MOVE of the collection publishes it onto the
// destination. Pointing a client at it is the compatibility layer's job.
//
// Members are named by number because the backing is the upload engine's
// name-ordered spool: assembly follows the order of the names.

// Uploads is the seam onto the upload engine.
//
// An interface so this package does not import that engine, which keeps its
// vocabulary out of the protocol layer and lets a test drive the collection
// without a spool directory.
type Uploads interface {
	// Open starts a session for a destination, under a client-chosen name.
	Open(ctx context.Context, res core.Resolved, name string, total *uint64) error
	// PutChunk contributes one numerically named member.
	PutChunk(ctx context.Context, res core.Resolved, name string, member uint32, body io.Reader) error
	// Assemble publishes the collection onto res and returns the entry.
	Assemble(ctx context.Context, res core.Resolved, name string, total uint64, mtimeNs *int64) (core.Entry, error)
	// Discard abandons a session.
	Discard(ctx context.Context, res core.Resolved, name string) error
	// Held is which members are currently stored, for a PROPFIND that asks a
	// resuming client what it still owes.
	Held(ctx context.Context, res core.Resolved, name string) ([]uint32, error)
}

// UploadHeaders supplies the header names this collection looks for.
//
// Supplied by the caller, never written down here. Header names belong to
// whichever vendor's client sends them, and hardcoding a set would mean this
// package had learned that vocabulary. What it knows is narrower: a declared
// size and a timestamp reach it under names somebody else chose.
type UploadHeaders struct {
	// TotalLength is where the client states how large the finished file is.
	TotalLength string
	// MTime is where the timestamp for the published file arrives, counted in
	// whole seconds.
	MTime string
	// ETag names the extra response header that repeats the finished entry's
	// validator alongside the standard one.
	ETag string
}

// complete reports whether every name is set.
//
// A partly configured set is refused rather than half honoured: reading one
// header and ignoring another because nobody named it is how a client's
// declared length disappears without a word.
func (u UploadHeaders) complete() bool {
	return u.TotalLength != "" && u.MTime != "" && u.ETag != ""
}

// UploadPath is what one request against the upload collection resolved to.
type UploadPath struct {
	// Session is what the collection itself is called.
	Session string
	// Member is the chunk number, valid only when Chunk is set. A request
	// against the collection itself names no member.
	Member uint32
	Chunk  bool
}

// uploadMethods is what the collection answers, for an Allow header.
const uploadMethods = "DELETE, MKCOL, MOVE, OPTIONS, PROPFIND, PUT"

// chunkMemberRange is what a member name may denote. Numbering starts at one:
// zero is not a member, so a name of "0" is a mistake rather than the first
// chunk.
func chunkMemberRange() ChunkRange { return ChunkRange{Min: 1, Max: int64(^uint32(0))} }

// chunkMember narrows a parsed number to the width a member is stored in.
//
// The range above already bounds it, so this cannot fail today. It is checked
// anyway because the range is a variable one edit away from admitting a number
// that would wrap: a silent wrap turns chunk 4294967297 into chunk 1 and
// overwrites data the client already sent.
func chunkMember(n int64) (uint32, bool) {
	if n < 0 || n > int64(^uint32(0)) {
		return 0, false
	}
	return uint32(n), true
}

// ParseUploadPath reads a path below the upload collection root.
//
// Only a leading slash is stripped. A trailing one means the request named a
// member and left off the name, which is not the collection: reading it as one
// would turn a malformed PUT into a MKCOL of a session that already exists.
func ParseUploadPath(raw string) (UploadPath, error) {
	segments, err := SplitPath(raw)
	if err != nil {
		return UploadPath{}, err
	}
	switch len(segments) {
	case 0:
		return UploadPath{}, core.ErrNotFound
	case 1:
		return UploadPath{Session: segments[0]}, nil
	case 2:
		// The shared parser, so this and the compatibility mount cannot
		// disagree about whether "00001" and "1" are the same member.
		n, perr := ParseChunkName(segments[1], chunkMemberRange())
		if perr != nil {
			// The parser's own sentinel, which the status table already knows,
			// so a padded name and a non-decimal one stay distinguishable.
			return UploadPath{}, perr
		}
		member, ok := chunkMember(n)
		if !ok {
			return UploadPath{}, ErrChunkRange
		}
		return UploadPath{Session: segments[0], Member: member, Chunk: true}, nil
	default:
		return UploadPath{}, core.ErrNotFound
	}
}

// ServeUpload answers one method against the upload collection.
//
// res resolves the destination the assembled file lands on, which is what
// carries the permission check. The collection is not a directory and has no
// path of its own on disk.
func (h *Handler) ServeUpload(
	w http.ResponseWriter, r *http.Request, res core.Resolved, up UploadPath,
) {
	// No engine, or no names for the headers it reads: either way this build
	// does not have the collection, and saying so beats half-answering.
	if h.uploads == nil || !h.uploadHeaders.complete() {
		w.Header().Set("Allow", h.allowFor(res))
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	switch r.Method {
	case "MKCOL":
		h.uploadOpen(w, r, res, up)
	case http.MethodPut:
		h.uploadPut(w, r, res, up)
	case "MOVE":
		h.uploadAssemble(w, r, res, up)
	case http.MethodDelete:
		h.uploadDiscard(w, r, res, up)
	case "PROPFIND":
		h.uploadPropfind(w, r, res, up)
	case http.MethodOptions:
		w.Header().Set("DAV", "1, 2")
		w.Header().Set("Allow", uploadMethods)
		w.WriteHeader(http.StatusOK)
	default:
		w.Header().Set("Allow", uploadMethods)
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// uploadOpen starts a session.
func (h *Handler) uploadOpen(
	w http.ResponseWriter, r *http.Request, res core.Resolved, up UploadPath,
) {
	if up.Chunk {
		// A chunk comes into being through PUT. MKCOL of one would be a
		// collection inside a collection, which means nothing here.
		h.fail(w, r, ErrChunkOnCollection)
		return
	}

	total, err := h.uploadTotal(r, false)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if oerr := h.uploads.Open(r.Context(), res, up.Session, total); oerr != nil {
		h.fail(w, r, oerr)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// uploadPut contributes one chunk.
func (h *Handler) uploadPut(
	w http.ResponseWriter, r *http.Request, res core.Resolved, up UploadPath,
) {
	if !up.Chunk {
		// A PUT of the collection names no member, so there is no position to
		// write at.
		h.fail(w, r, ErrChunkOnCollection)
		return
	}
	if r.Body == nil {
		h.fail(w, r, ErrNoBody)
		return
	}
	if err := h.uploads.PutChunk(r.Context(), res, up.Session, up.Member, r.Body); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// uploadAssemble publishes the collection onto its destination.
//
// The destination arrived in the Destination header and the caller has already
// resolved it into res, the same way COPY and MOVE receive theirs.
func (h *Handler) uploadAssemble(
	w http.ResponseWriter, r *http.Request, res core.Resolved, up UploadPath,
) {
	if up.Chunk {
		h.fail(w, r, ErrChunkOnCollection)
		return
	}

	// Required here, unlike at open: assembly writes exactly this many bytes
	// and a missing length would publish whatever happened to have arrived.
	total, err := h.uploadTotal(r, true)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	mtime, merr := h.uploadMTime(r)
	if merr != nil {
		h.fail(w, r, merr)
		return
	}

	// The publish is a write at the destination, so it answers to a lock held
	// there like any other. Nothing guards the collection itself: it is this
	// caller's own session and no other client can name it.
	if gerr := h.guard(r, res); gerr != nil {
		h.fail(w, r, gerr)
		return
	}

	entry, aerr := h.uploads.Assemble(r.Context(), res, up.Session, *total, mtime)
	if aerr != nil {
		h.fail(w, r, aerr)
		return
	}

	tag := ETagHeader(entry.ETag, entry.ETagWeak)
	w.Header().Set("ETag", tag)
	w.Header().Set(h.uploadHeaders.ETag, tag)
	w.WriteHeader(http.StatusCreated)
}

// uploadDiscard abandons a session.
func (h *Handler) uploadDiscard(
	w http.ResponseWriter, r *http.Request, res core.Resolved, up UploadPath,
) {
	if up.Chunk {
		// A single chunk is not separately removable: assembly has already
		// consumed the ones before it.
		h.fail(w, r, ErrChunkOnCollection)
		return
	}
	if err := h.uploads.Discard(r.Context(), res, up.Session); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// uploadPropfind answers with the members in hand. A client picking up an
// interrupted transfer reads this to work out what is left to send.
func (h *Handler) uploadPropfind(
	w http.ResponseWriter, r *http.Request, res core.Resolved, up UploadPath,
) {
	if up.Chunk {
		h.fail(w, r, core.ErrNotFound)
		return
	}

	req, err := ParsePropFind(http.MaxBytesReader(w, r.Body, h.limits.Bytes), h.limits)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	depth, derr := ParseDepth(r.Header.Get("Depth"), DepthInfinity,
		DepthZero, DepthOne, DepthInfinity)
	if derr != nil {
		h.fail(w, r, derr)
		return
	}

	held, herr := h.uploads.Held(r.Context(), res, up.Session)
	if herr != nil {
		h.fail(w, r, herr)
		return
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	m := NewMultistatus(w, requestedNamespaces(req))
	base := EncodeHref([]string{up.Session}, true)
	m.Response(base, []PropStat{{
		Status: http.StatusOK,
		Props:  []Prop{collectionType()},
	}})

	if depth != DepthZero {
		for _, n := range held {
			name := ChunkName(int64(n))
			m.Response(base+name, []PropStat{{
				Status: http.StatusOK,
				// A member is a file, so its resourcetype is present and
				// empty. Omitting it would leave a client unable to tell a
				// chunk from another collection.
				Props: []Prop{{Name: davName("resourcetype")}},
			}})
			if m.Err() != nil {
				h.log(r).Warn("the member listing stopped early", "error", m.Err())
				break
			}
		}
	}

	h.closeMultistatus(r, m)
}

// uploadTotal reads the declared assembled length.
//
// required separates the two callers: at open the length may be deferred, and
// at assembly it decides how many bytes are published.
func (h *Handler) uploadTotal(r *http.Request, required bool) (*uint64, error) {
	raw := r.Header.Get(h.uploadHeaders.TotalLength)
	if raw == "" {
		if required {
			return nil, ErrNoUploadLength
		}
		return nil, nil
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return nil, ErrBadUploadLength
	}
	return &n, nil
}

// uploadMTime reads the modification time to stamp on the published file.
//
// Whole seconds on the wire, nanoseconds inside. A value that does not parse
// is refused rather than dropped: publishing with the wrong timestamp makes a
// sync client believe the file changed after it uploaded it.
func (h *Handler) uploadMTime(r *http.Request) (*int64, error) {
	raw := r.Header.Get(h.uploadHeaders.MTime)
	if raw == "" {
		return nil, nil
	}
	secs, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, ErrBadUploadMTime
	}
	// Seconds far enough out to overflow the nanosecond conversion would wrap
	// into a plausible-looking time rather than fail.
	const maxSeconds = (1 << 62) / 1_000_000_000
	if secs < -maxSeconds || secs > maxSeconds {
		return nil, ErrBadUploadMTime
	}
	ns := secs * 1_000_000_000
	return &ns, nil
}
