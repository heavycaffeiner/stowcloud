//go:build linux && compat_nc

package nc

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/upload"
)

// The chunked upload collection: MKCOL opens a session, PUT appends a
// numbered chunk, PROPFIND lists what has arrived so a client can resume,
// and MOVE of the magic ".file" member assembles and publishes.
//
// A client addresses the session by a transfer id it chose itself, bound to
// the engine's own session through BindAlias. The bind is what makes the id
// safe to trust: every call below resolves it through LookupAlias, which is
// scoped to the caller's own account, so a guessed or shared id names
// nothing to anyone but the account that opened it.
//
// The session's own destination, captured at bind time, is what a chunk
// write and a listing resolve through. The Destination header a client
// repeats on every chunk PUT is ignored: the upload engine places a
// session's part file beside the destination it was opened against, so a
// root taken from a header is one where the assembly would not find the
// bytes.

// davUpload serves the whole chunked upload collection. Deps.Uploads absent
// means this deployment has nowhere to put a chunk, and a session half-served
// without an engine behind it would accept bytes it could never assemble.
func (s *Server) davUpload(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	if s.deps.Uploads == nil {
		// Not a wrong method: the collection is absent from this deployment.
		// The capabilities document already says chunking is unavailable, and
		// one client ignores that and tries anyway, so the refusal it gets
		// has to name the real reason rather than blame the verb.
		WriteDAVError(w, http.StatusNotImplemented,
			"Sabre\\DAV\\Exception\\NotImplemented", "This server has no chunked upload collection")
		return
	}
	switch r.Method {
	case "MKCOL":
		s.uploadCreate(w, r, p, t)
	case "PROPFIND":
		s.uploadList(w, r, p, t)
	case http.MethodPut:
		s.uploadPutChunk(w, r, p, t)
	case "MOVE":
		s.uploadAssemble(w, r, p, t)
	case http.MethodDelete:
		s.uploadAbort(w, r, p, t)
	default:
		s.davMethodNotAllowed(w)
	}
}

// uploadCreate answers MKCOL: it opens a session against the Destination
// header and binds the client's own session name to it.
//
// A name already bound answers 201 again rather than an error. A client
// that lost the first response has no way to tell a dropped reply from a
// session that was never opened, so the retry has to look like success.
func (s *Server) uploadCreate(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	if t.Member != "" {
		s.davMethodNotAllowed(w)
		return
	}
	if r.ContentLength > 0 {
		WriteDAVError(w, http.StatusUnsupportedMediaType,
			"Sabre\\DAV\\Exception\\UnsupportedMediaType", "MKCOL defines no request body")
		return
	}
	ctx := r.Context()

	if _, err := s.deps.Uploads.LookupAlias(ctx, t.Session, user(p)); err == nil {
		w.WriteHeader(http.StatusCreated)
		return
	} else if !errors.Is(err, upload.ErrNotFound) {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}

	dest, ok := parseDestination(r)
	if !ok {
		WriteDAVError(w, http.StatusBadGateway,
			"Sabre\\DAV\\Exception\\BadGateway", "The destination could not be used")
		return
	}
	res, err := s.resolveComponents(ctx, p, dest.Path, acl.Write|acl.Create)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}

	sess, cerr := s.deps.Uploads.Create(ctx, res, upload.SessionSpec{
		Mode:     upload.SpoolNameOrdered,
		TotalLen: totalLenPtr(r.Header.Get("OC-Total-Length")),
	})
	if cerr != nil {
		s.failDav(w, r, cerr, apierr.VisibilityHidden)
		return
	}
	if berr := s.deps.Uploads.BindAlias(ctx, t.Session, user(p), sess.ID); berr != nil {
		if errors.Is(berr, upload.ErrAliasTaken) {
			// Lost a race with a concurrent retry of this same MKCOL: the
			// name is bound to whichever request won it, and this session
			// is an orphan nothing will ever address again.
			if aerr := s.deps.Uploads.Abort(ctx, sess.ID, user(p)); aerr != nil {
				s.log.Warn("an orphaned upload session could not be aborted", "error", aerr)
			}
			w.WriteHeader(http.StatusCreated)
			return
		}
		s.failDav(w, r, berr, apierr.VisibilityHidden)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// uploadList answers PROPFIND on the session: the collection itself, then
// one member per chunk already received, named the way a resuming client
// recognises them.
//
// A session that does not exist answers 404: one client probes this before
// it decides to MKCOL.
func (s *Server) uploadList(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	if t.Member != "" {
		s.davMethodNotAllowed(w)
		return
	}
	ctx := r.Context()

	query, qerr := ParsePropfind(r.Body)
	if qerr != nil {
		s.failDav(w, r, qerr, apierr.VisibilityKnown)
		return
	}

	alias, aerr := s.deps.Uploads.LookupAlias(ctx, t.Session, user(p))
	if aerr != nil {
		s.failDav(w, r, aerr, apierr.VisibilityHidden)
		return
	}
	res, rerr := s.aliasTarget(ctx, p, alias, acl.Read)
	if rerr != nil {
		s.failDav(w, r, rerr, apierr.VisibilityHidden)
		return
	}
	chunks, cerr := s.deps.Uploads.ListChunks(ctx, res.Root(), alias.Session, user(p))
	if cerr != nil {
		s.failDav(w, r, cerr, apierr.VisibilityHidden)
		return
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	m := NewMulti(w)
	m.Open()

	sessionHref := t.Prefix + "/" + escapeSegment(t.Session) + "/"
	found, missing := uploadCollectionProps(query, true, 0)
	m.Response(sessionHref, found, missing)

	for _, c := range chunks {
		href := t.Prefix + "/" + escapeSegment(t.Session) + "/" + escapeSegment(FormatChunkName(c.Name))
		found, missing := uploadCollectionProps(query, false, c.Size)
		m.Response(href, found, missing)
	}

	if cerr := m.Close(); cerr != nil {
		s.log.Warn("a chunk listing was not delivered", "error", cerr)
	}
}

// uploadCollectionPropNames is what a chunk listing PROPFIND can answer:
// the shape a member has and, for a chunk, how large it is. Neither client
// asks this collection about anything else.
func uploadCollectionPropNames() []PropName {
	return []PropName{PropResourceType(), PropContentType(), PropContentLength()}
}

// uploadCollectionProps answers one member of the upload collection: the
// session itself (a directory) or a received chunk (a file of the given
// size).
func uploadCollectionProps(query PropQuery, isDir bool, size uint64) ([]Prop, []PropName) {
	resolve := func(n PropName) (Prop, bool) {
		switch n {
		case PropResourceType():
			if isDir {
				return Prop{Name: n, Children: []Node{{Name: dav("collection")}}}, true
			}
			return Prop{Name: n}, true
		case PropContentType():
			if isDir {
				return Prop{}, false
			}
			return TextProp(n, "application/octet-stream"), true
		case PropContentLength():
			if isDir {
				return Prop{}, false
			}
			return TextProp(n, strconv.FormatUint(size, 10)), true
		default:
			return Prop{}, false
		}
	}

	switch {
	case query.NamesOnly:
		var found []Prop
		for _, n := range uploadCollectionPropNames() {
			if _, ok := resolve(n); ok {
				found = append(found, Prop{Name: n})
			}
		}
		return found, nil
	case query.All:
		var found []Prop
		for _, n := range uploadCollectionPropNames() {
			if p, ok := resolve(n); ok {
				found = append(found, p)
			}
		}
		return found, nil
	default:
		var found []Prop
		var missing []PropName
		for _, n := range query.Names {
			if p, ok := resolve(n); ok {
				found = append(found, p)
			} else {
				missing = append(missing, n)
			}
		}
		return found, missing
	}
}

// uploadPutChunk answers PUT of a member whose name parses as a chunk
// number, appending it to the session in order.
//
// OC-Total-Length is never required here: Android's own chunked protocol
// does not send it on a chunk PUT, only on the plain single-shot upload and
// on the assembly MOVE.
func (s *Server) uploadPutChunk(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	name, ok := ChunkName(t.Member)
	if !ok {
		s.davMethodNotAllowed(w)
		return
	}
	ctx := r.Context()

	alias, aerr := s.deps.Uploads.LookupAlias(ctx, t.Session, user(p))
	if aerr != nil {
		s.failDav(w, r, aerr, apierr.VisibilityHidden)
		return
	}
	res, rerr := s.uploadWriteTarget(ctx, p, alias)
	if rerr != nil {
		s.failDav(w, r, rerr, apierr.VisibilityHidden)
		return
	}

	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	if perr := s.deps.Uploads.PutNamed(ctx, res.Root(), alias.Session, user(p), name, body, nil); perr != nil {
		s.failDav(w, r, perr, apierr.VisibilityHidden)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// uploadAssemble answers MOVE of the AssemblyMember: it merges whatever
// chunks remain and publishes the result at the Destination header.
//
// The destination is resolved directly, not through the bound alias, so
// the write runs the same acl.Write|acl.Create capability check every
// other create on this surface runs; the alias only supplies the session.
func (s *Server) uploadAssemble(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	if t.Member != AssemblyMember {
		s.davMethodNotAllowed(w)
		return
	}
	ctx := r.Context()

	alias, aerr := s.deps.Uploads.LookupAlias(ctx, t.Session, user(p))
	if aerr != nil {
		s.failDav(w, r, aerr, apierr.VisibilityHidden)
		return
	}
	dest, ok := parseDestination(r)
	if !ok {
		WriteDAVError(w, http.StatusBadGateway,
			"Sabre\\DAV\\Exception\\BadGateway", "The destination could not be used")
		return
	}
	res, rerr := s.resolveComponents(ctx, p, dest.Path, acl.Write|acl.Create)
	if rerr != nil {
		s.failDav(w, r, rerr, apierr.VisibilityHidden)
		return
	}

	// The core refuses every validator it is handed by design (every etag
	// it mints is metadata-derived, so a strong comparison always fails),
	// which is why this layer does the comparison itself and calls Assemble
	// with no If-Match of its own.
	existed := true
	st, statErr := res.Root().Stat(res.Path())
	if statErr != nil {
		existed = false
	}
	var currentToken string
	if existed {
		currentToken, _ = core.FileETag(st)
	}
	switch header := strings.TrimSpace(r.Header.Get("If-Match")); header {
	case "":
	case "*":
		if !existed {
			WriteDAVError(w, http.StatusPreconditionFailed,
				"Sabre\\DAV\\Exception\\PreconditionFailed", "The precondition failed")
			return
		}
	default:
		if !existed || ParseETag(header) != currentToken {
			WriteDAVError(w, http.StatusPreconditionFailed,
				"Sabre\\DAV\\Exception\\PreconditionFailed", "The precondition failed")
			return
		}
	}

	var total uint64
	if v, ok := parseTotalLength(r.Header.Get("OC-Total-Length")); ok {
		total = v
	}
	var mtimeNs *int64
	if ns, ok := ParseUnixHeader(r.Header.Get("X-OC-Mtime")); ok {
		mtimeNs = &ns
	}

	entry, err := s.deps.Uploads.Assemble(ctx, res, alias.Session, total, mtimeNs)
	if err != nil {
		// An incomplete assembly classifies as Unprocessable, which
		// davStatusOf answers 400: the client resends the holes, never
		// reads this as a completed upload.
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}

	// The desktop client hard-fails an otherwise successful assembly whose
	// response carries no OC-FileId or an empty etag, on the theory that a
	// write it cannot identify might as well not have happened.
	s.setEntryHeaders(ctx, w, entry)
	w.WriteHeader(http.StatusCreated)

	if uerr := s.deps.Uploads.UnbindAlias(ctx, t.Session, user(p)); uerr != nil {
		s.log.Warn("an assembled upload's alias could not be unbound", "error", uerr)
	}
}

// uploadAbort answers DELETE on the session: it terminates the engine
// session and releases the client's own name for it, so a later MKCOL under
// the same name opens a fresh session rather than finding a dead one still
// bound.
func (s *Server) uploadAbort(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	if t.Member != "" {
		s.davMethodNotAllowed(w)
		return
	}
	ctx := r.Context()

	alias, aerr := s.deps.Uploads.LookupAlias(ctx, t.Session, user(p))
	if aerr != nil {
		s.failDav(w, r, aerr, apierr.VisibilityHidden)
		return
	}
	if err := s.deps.Uploads.Abort(ctx, alias.Session, user(p)); err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}
	if uerr := s.deps.Uploads.UnbindAlias(ctx, t.Session, user(p)); uerr != nil {
		s.log.Warn("an aborted upload's alias could not be unbound", "error", uerr)
	}
	w.WriteHeader(http.StatusNoContent)
}

// aliasTarget resolves the capability an alias's own bound destination
// grants, applying the caller's mask and share allowlist exactly as any
// other path on this surface does. VpathOf crosses the alias's share and
// share-relative destination back into the client-facing path; nothing here
// trusts the alias's Share/Dest fields directly, since both came from
// BindAlias rather than from this request.
func (s *Server) aliasTarget(ctx context.Context, p Principal, alias upload.Alias, need acl.Perms) (core.Resolved, error) {
	if s.deps.VpathOf == nil {
		return core.Resolved{}, core.ErrNotFound
	}
	vpath, err := s.deps.VpathOf(user(p), alias.Share, alias.Dest)
	if err != nil {
		return core.Resolved{}, core.ErrNotFound
	}
	return s.resolve(ctx, p, vpath, need)
}

// uploadWriteTarget resolves the root a chunk write lands through, which is
// always the destination the session was opened against.
//
// No fallback to the Destination header a client repeats on every chunk. The
// upload engine places a session's part file beside that bound destination,
// so a root from any other share is one where the assembly will not find the
// bytes, and in the worst case is another share's filesystem. A session whose
// share can no longer be resolved is genuinely unusable, and saying so lets
// the client start a fresh transfer instead of filling one that can never
// publish.
func (s *Server) uploadWriteTarget(
	ctx context.Context, p Principal, alias upload.Alias,
) (core.Resolved, error) {
	return s.aliasTarget(ctx, p, alias, acl.Write|acl.Create)
}

// parseTotalLength reads OC-Total-Length: the whole-file size a client
// declares at assembly time. Absent or unparseable answers false, so the
// caller falls back to whatever the session already recorded.
func parseTotalLength(v string) (uint64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// totalLenPtr reads OC-Total-Length into the pointer SessionSpec wants. Nil
// when the header is absent or unparseable, which is a deferred length: the
// desktop client sends this at MKCOL time, the Android client never does,
// and a session opened without it declares its length later, at assembly.
func totalLenPtr(v string) *uint64 {
	n, ok := parseTotalLength(v)
	if !ok {
		return nil
	}
	return &n
}
