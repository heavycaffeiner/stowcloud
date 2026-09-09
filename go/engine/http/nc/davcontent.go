//go:build linux && compat_nc

package nc

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// GET, HEAD, PUT, MKCOL, DELETE, MOVE and COPY against a file or a folder.
//
// Every conditional check here runs before any bytes move, and every
// mutation runs the lock guard before it writes: a write this surface cannot
// see a foreign lock over would silently clobber whatever a person is
// editing through the office suite's own lock.

// copyBufferSize bounds one read/write cycle of a streamed body. A download
// or an inline copy holds this much memory whatever the file's size is;
// io.Copy's own default buffer is smaller than a stat block on some paths,
// and naming the size here keeps it one deliberate choice instead of a
// standard-library default nobody chose.
const copyBufferSize = 64 * 1024

// davGet answers GET and HEAD. body is false for HEAD, which is otherwise
// the same request: the headers a client reads to decide whether to fetch
// have to match what GET would send, so both are built by the same path.
func (s *Server) davGet(w http.ResponseWriter, r *http.Request, p Principal, t Target, body bool) {
	ctx := r.Context()
	res, err := s.resolveComponents(ctx, p, t.Path, acl.Read|acl.Download)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}
	entry, err := s.deps.Core.Stat(ctx, res)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}
	if entry.IsDir {
		// A collection carries no body to send, but it does exist, and one
		// client asks exactly this question before every upload: it probes
		// the target folder with HEAD and accepts only 200. Answering the
		// method as not allowed failed that probe, and the upload was
		// abandoned before a single byte was sent. So the answer is the
		// entry's own headers and nothing else.
		s.setEntryHeaders(ctx, w, entry)
		w.Header().Set("Content-Type", ContentTypeOf(true, entry.Name))
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return
	}
	token := entry.ETag

	// The desktop client aborts a download that carries no etag, so both
	// conditional checks compare against the value setEntryHeaders is about
	// to send, never against a placeholder. If-Match is evaluated first,
	// per RFC 7232: a client that sends both is asking two different
	// questions and the precondition is the stronger claim.
	if h := r.Header.Get("If-Match"); ifMatchFails(h, token) {
		WriteDAVError(w, http.StatusPreconditionFailed,
			"Sabre\\DAV\\Exception\\PreconditionFailed", "The precondition failed")
		return
	}
	if h := r.Header.Get("If-None-Match"); ifNoneMatchSatisfied(h, token) {
		w.Header().Set("ETag", ETagValue(token))
		w.Header().Set("OC-ETag", ETagValue(token))
		w.WriteHeader(http.StatusNotModified)
		return
	}

	rng, ok := parseRange(r.Header.Get("Range"), entry.Size)
	if !ok {
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", "bytes */"+strconv.FormatUint(entry.Size, 10))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}

	_, stream, err := s.deps.Core.OpenStream(ctx, res, rng)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}
	defer func() {
		if cerr := stream.Close(); cerr != nil {
			s.log.Warn("a download stream did not close cleanly", "error", cerr)
		}
	}()

	s.setEntryHeaders(ctx, w, entry)
	w.Header().Set("Content-Type", ContentTypeOf(false, entry.Name))
	// setEntryHeaders wrote the whole file's size; a ranged answer's body is
	// shorter than that, and Content-Length has to match what actually
	// follows.
	w.Header().Set("Content-Length", strconv.FormatUint(stream.Remaining(), 10))
	w.Header().Set("Accept-Ranges", "bytes")

	status := http.StatusOK
	if rng != nil {
		status = http.StatusPartialContent
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rng[0], rng[1], entry.Size))
	}
	w.WriteHeader(status)

	if !body {
		return
	}
	buf := make([]byte, copyBufferSize)
	if _, cerr := io.CopyBuffer(w, stream, buf); cerr != nil {
		// The status line already went out; nothing left to answer with but
		// a short body and a log line.
		s.log.Warn("a download body stopped early", "error", cerr)
	}
}

// ifNoneMatchSatisfied reports whether a comma-separated If-None-Match list
// names the current token (or "*"), which is what turns a GET into a 304.
func ifNoneMatchSatisfied(header, token string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || ParseETag(part) == token {
			return true
		}
	}
	return false
}

// ifMatchFails reports whether an If-Match header names a token other than
// the current one, which is what turns a request into a 412. An absent
// header or a bare "*" imposes no condition.
func ifMatchFails(header, token string) bool {
	header = strings.TrimSpace(header)
	if header == "" || header == "*" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		if ParseETag(strings.TrimSpace(part)) == token {
			return false
		}
	}
	return true
}

// davPut writes a file, creating it or replacing whatever is there.
func (s *Server) davPut(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	ctx := r.Context()
	// The full path, leaf included: the leaf need not exist yet, and
	// resolution validates syntax rather than checking the filesystem, so a
	// name about to be created resolves exactly like one already there.
	res, err := s.resolveComponents(ctx, p, t.Path, acl.Write|acl.Create)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}

	st, statErr := res.Root().Stat(res.Path())
	existed := statErr == nil
	if existed && st.Kind.IsDir() {
		// Replacing a collection would mean removing it and everything
		// under it, which a PUT never asks for.
		s.davMethodNotAllowed(w)
		return
	}
	var currentToken string
	if existed {
		currentToken, _ = core.FileETag(st)
	}

	// The core refuses every validator it is handed by design (every etag it
	// mints is metadata-derived, so a strong comparison always fails), which
	// is why this layer does the comparison itself and calls the core with
	// ifMatch = nil below.
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
	if strings.TrimSpace(r.Header.Get("If-None-Match")) == "*" && existed {
		WriteDAVError(w, http.StatusPreconditionFailed,
			"Sabre\\DAV\\Exception\\PreconditionFailed", "The resource already exists")
		return
	}

	if lerr := s.guardLock(ctx, res, p); lerr != nil {
		WriteDAVError(w, http.StatusLocked, "Sabre\\DAV\\Exception\\Locked", "The resource is locked")
		return
	}

	if r.Header.Get("X-NC-WebDAV-Auto-Mkcol") == "1" {
		if merr := s.deps.Core.MkdirParents(ctx, res); merr != nil {
			s.failDav(w, r, merr, apierr.VisibilityHidden)
			return
		}
	}

	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	// OC-Checksum is read by no code here. This deployment cannot verify
	// every algorithm a client might name, and echoing the header back as if
	// the upload had been checked would claim a guarantee nothing here kept.
	entry, werr := s.deps.Core.WriteStream(ctx, res, body, nil)
	if werr != nil {
		s.failDav(w, r, werr, apierr.VisibilityHidden)
		return
	}

	// X-OC-Mtime is the only one of the pair the engine can apply: SetTimes
	// sets a file's modification time and nothing else. X-OC-Ctime carries a
	// client-side creation timestamp this deployment has no field to hold,
	// so it is read nowhere here, the same treatment OC-Checksum gets above.
	if ns, ok := ParseUnixHeader(r.Header.Get("X-OC-Mtime")); ok {
		if terr := res.Root().SetTimes(res.Path(), ns); terr != nil {
			s.log.Warn("could not apply the client's modification time", "error", terr)
		} else {
			noteMtimeAccepted(w, true)
			if newSt, serr := res.Root().Stat(res.Path()); serr == nil {
				entry = s.deps.Core.EntryAt(res, newSt)
			}
		}
	}

	// The desktop client hard-fails an upload whose response carries no
	// OC-FileId or no etag, on the theory that a write it cannot identify
	// might as well not have happened.
	s.setEntryHeaders(ctx, w, entry)
	if existed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// davMkcol creates a collection.
func (s *Server) davMkcol(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	// RFC 4918 gives MKCOL no body format, so one that arrived cannot be
	// honoured without inventing a meaning for it.
	if r.ContentLength > 0 {
		WriteDAVError(w, http.StatusUnsupportedMediaType,
			"Sabre\\DAV\\Exception\\UnsupportedMediaType", "MKCOL defines no request body")
		return
	}

	ctx := r.Context()
	res, err := s.resolveComponents(ctx, p, t.Path, acl.Create)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}

	entry, merr := s.deps.Core.Mkdir(ctx, res)
	if merr != nil {
		// An absent parent answers 409, not 404: the target is what MKCOL is
		// about to create, and 404 gives a client creating parents on
		// demand no reason to. Android maps exactly this to "create the
		// parent and retry".
		if errors.Is(merr, core.ErrNotFound) && !parentExists(res) {
			s.failDav(w, r, core.ErrConflict, apierr.VisibilityHidden)
			return
		}
		// core.ErrExists classifies as apierr.Exists, which davStatusOf
		// answers with 405: the Android client maps that status, and only
		// that status, to "the folder is already there".
		s.failDav(w, r, merr, apierr.VisibilityHidden)
		return
	}
	// One client stores the returned OC-FileId as the folder's own identity.
	s.setEntryHeaders(ctx, w, entry)
	w.WriteHeader(http.StatusCreated)
}

// parentExists reports whether the enclosing collection is there. A share
// root always is, so anything one level inside it has a parent.
func parentExists(res core.Resolved) bool {
	p := res.Path()
	if p.IsRoot() || p.Parent().IsRoot() {
		return true
	}
	st, err := res.Root().Stat(p.Parent())
	return err == nil && st.Kind.IsDir()
}

// davDelete removes a resource, through the deployment's own trash policy.
func (s *Server) davDelete(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	ctx := r.Context()
	res, err := s.resolveComponents(ctx, p, t.Path, acl.Delete)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}
	if lerr := s.guardLock(ctx, res, p); lerr != nil {
		WriteDAVError(w, http.StatusLocked, "Sabre\\DAV\\Exception\\Locked", "The resource is locked")
		return
	}
	// permanent=false: the share's own trash setting decides, not this
	// surface. core.Delete stats the target itself, so an absent one answers
	// core.ErrNotFound without a stat of our own first.
	if derr := s.deps.Core.Delete(ctx, res, false); derr != nil {
		s.failDav(w, r, derr, apierr.VisibilityHidden)
		return
	}
	// Never 200 with a body: the desktop client treats anything but 204 or
	// 404 as a hard error.
	w.WriteHeader(http.StatusNoContent)
}

// parseDestination reads a MOVE or COPY's Destination header into a Target.
//
// A destination naming another host is refused: this server cannot act on a
// resource that lives on a different one. A destination outside the files
// layout, or a path ParseTarget does not recognise at all, is refused the
// same way an unparseable request path is refused everywhere else on this
// surface.
func parseDestination(r *http.Request) (Target, bool) {
	raw := strings.TrimSpace(r.Header.Get("Destination"))
	if raw == "" {
		return Target{}, false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Target{}, false
	}
	if u.Host != "" && !strings.EqualFold(hostWithoutPort(u.Host), hostWithoutPort(r.Host)) {
		return Target{}, false
	}
	dest, ok := ParseTarget(collapseSlashes(u.EscapedPath()))
	if !ok || dest.Kind != KindFiles {
		return Target{}, false
	}
	return dest, true
}

// hostWithoutPort strips a trailing ":port" for the host comparison a
// Destination header's authority is checked against.
func hostWithoutPort(h string) string {
	if i := strings.LastIndexByte(h, ':'); i >= 0 {
		return h[:i]
	}
	return h
}

// davTransfer answers MOVE and COPY.
func (s *Server) davTransfer(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	dest, ok := parseDestination(r)
	if !ok {
		WriteDAVError(w, http.StatusBadGateway,
			"Sabre\\DAV\\Exception\\BadGateway", "The destination could not be used")
		return
	}
	// RFC 4918 defaults Overwrite to true; only an explicit "F" turns it off.
	overwrite := !strings.EqualFold(strings.TrimSpace(r.Header.Get("Overwrite")), "F")

	if r.Method == "COPY" {
		s.davCopy(w, r, p, t, dest, overwrite)
		return
	}
	s.davMove(w, r, p, t, dest, overwrite)
}

// davMove relocates a resource, by rename where the parent is unchanged and
// by a cross-directory move otherwise.
func (s *Server) davMove(w http.ResponseWriter, r *http.Request, p Principal, t Target, dest Target, overwrite bool) {
	ctx := r.Context()

	// Only the last component differs: a rename within its own directory,
	// which is a narrower grant than a move across directories.
	sameParent := len(t.Path) > 0 && len(dest.Path) > 0 && len(t.Path) == len(dest.Path) &&
		slices.Equal(t.Path[:len(t.Path)-1], dest.Path[:len(dest.Path)-1])
	srcNeed := acl.Move | acl.Delete
	if sameParent {
		srcNeed = acl.Rename
	}
	from, err := s.resolveComponents(ctx, p, t.Path, srcNeed)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}
	to, err := s.resolveComponents(ctx, p, dest.Path, acl.Create)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}

	if serr := core.RefuseSelfDescendant(from, to); serr != nil {
		// 409, not the 403 the sentinel would otherwise answer with: the
		// android client's own status table maps 409 to a conflict result,
		// which is the closer fit for a request that conflicts with the
		// tree's own shape rather than with a permission.
		s.failDav(w, r, core.ErrConflict, apierr.VisibilityHidden)
		return
	}

	existed := false
	if _, derr := to.Root().Stat(to.Path()); derr == nil {
		existed = true
	}
	if existed && !overwrite {
		WriteDAVError(w, http.StatusPreconditionFailed,
			"Sabre\\DAV\\Exception\\PreconditionFailed", "The destination exists and overwrite is off")
		return
	}

	if lerr := s.guardLock(ctx, from, p); lerr != nil {
		WriteDAVError(w, http.StatusLocked, "Sabre\\DAV\\Exception\\Locked", "The resource is locked")
		return
	}
	if lerr := s.guardLock(ctx, to, p); lerr != nil {
		WriteDAVError(w, http.StatusLocked, "Sabre\\DAV\\Exception\\Locked", "The resource is locked")
		return
	}

	if _, merr := s.deps.Core.Move(ctx, from, to, core.MoveOpts{Overwrite: overwrite}); merr != nil {
		if errors.Is(merr, core.ErrExists) {
			// A collision the stat above did not see, because something
			// created the destination in between. The standard answers a
			// refused overwrite with 412, and one client maps exactly that
			// onto its own "the target is in the way"; the shared ladder
			// answers 405 here, which is right for a create and wrong for
			// this.
			WriteDAVError(w, http.StatusPreconditionFailed,
				"Sabre\\DAV\\Exception\\PreconditionFailed", "The destination exists")
			return
		}
		s.failDav(w, r, merr, apierr.VisibilityHidden)
		return
	}

	if st, eerr := to.Root().Stat(to.Path()); eerr == nil {
		s.setEntryHeaders(ctx, w, s.deps.Core.EntryAt(to, st))
	}
	// 204 only when something was replaced, 201 otherwise. The desktop
	// client treats any other status on a plain rename as a hard error.
	if existed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// davCopy duplicates a resource where it can finish inside this response,
// and refuses everything else honestly.
func (s *Server) davCopy(w http.ResponseWriter, r *http.Request, p Principal, t Target, dest Target, overwrite bool) {
	ctx := r.Context()

	from, err := s.resolveComponents(ctx, p, t.Path, acl.Read|acl.Download)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}
	to, err := s.resolveComponents(ctx, p, dest.Path, acl.Write|acl.Create)
	if err != nil {
		s.failDav(w, r, err, apierr.VisibilityHidden)
		return
	}
	if serr := core.RefuseSelfDescendant(from, to); serr != nil {
		s.failDav(w, r, core.ErrConflict, apierr.VisibilityHidden)
		return
	}

	srcSt, serr := from.Root().Stat(from.Path())
	if serr != nil {
		s.failDav(w, r, core.ErrNotFound, apierr.VisibilityHidden)
		return
	}
	if srcSt.Kind.IsDir() || from.Share() != to.Share() {
		// core.StartCopy runs a directory walk on a detached goroutine and
		// answers with a job to poll; this handler's caller waits on the
		// HTTP response instead of polling one, so only a single file
		// copying within one share, an open and a durable write, finishes
		// before a response can be written at all. Everything larger is
		// refused honestly rather than answered with a status that claims a
		// completed copy that has not happened; the clients use COPY rarely
		// and handle a 501.
		WriteDAVError(w, http.StatusNotImplemented, "Sabre\\DAV\\Exception\\NotImplemented",
			"COPY of a folder or across shares is not available on this surface")
		return
	}

	existed := false
	if _, derr := to.Root().Stat(to.Path()); derr == nil {
		existed = true
	}
	if existed && !overwrite {
		WriteDAVError(w, http.StatusPreconditionFailed,
			"Sabre\\DAV\\Exception\\PreconditionFailed", "The destination exists and overwrite is off")
		return
	}

	if lerr := s.guardLock(ctx, from, p); lerr != nil {
		WriteDAVError(w, http.StatusLocked, "Sabre\\DAV\\Exception\\Locked", "The resource is locked")
		return
	}
	if lerr := s.guardLock(ctx, to, p); lerr != nil {
		WriteDAVError(w, http.StatusLocked, "Sabre\\DAV\\Exception\\Locked", "The resource is locked")
		return
	}

	_, stream, oerr := s.deps.Core.OpenStream(ctx, from, nil)
	if oerr != nil {
		s.failDav(w, r, oerr, apierr.VisibilityHidden)
		return
	}
	defer func() {
		if cerr := stream.Close(); cerr != nil {
			s.log.Warn("a copy source stream did not close cleanly", "error", cerr)
		}
	}()

	entry, werr := s.deps.Core.WriteStream(ctx, to, stream, nil)
	if werr != nil {
		s.failDav(w, r, werr, apierr.VisibilityHidden)
		return
	}

	s.setEntryHeaders(ctx, w, entry)
	if existed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
