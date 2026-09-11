//go:build linux && compat_nc

package nc

import (
	"context"
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The three facts every response about a file carries, in one place: its
// identity, its validator and its modification time.
//
// A sync client keys its journal on the identity and refuses an entry whose
// validator is empty. Both are read from a listing and from the headers of a
// create, and the two spellings have to agree: an identity that differs
// between the two makes a client treat the file it just uploaded as a
// different one and download it back.

// instanceTag is the fixed-width tag appended to a numeric id.
//
// Derived from the deployment's own instance identity so that two deployments
// never mint the same string for different files, and truncated to a width
// because one client stores the whole string and compares it for equality.
func (s *Server) instanceTag() string {
	id := s.deps.Features().InstanceID
	const width = 24
	if len(id) >= width {
		return id[:width]
	}
	return id + zeroPad("", width-len(id))
}

// fileID is the stable numeric identity of one entry.
//
// A failure answers zero, and the caller renders no identity at all rather
// than a placeholder: a fabricated id would key a client's journal to a file
// that does not exist, and the client already handles a missing one by
// skipping the entry.
func (s *Server) fileID(ctx context.Context, e core.Entry) uint64 {
	id, err := s.deps.Store.FileID(ctx, e)
	if err != nil {
		s.log.Warn("an entry has no stable identity", "name", e.Name, "error", err)
		return 0
	}
	return id
}

// recordIDs makes the ids about to be handed out resolvable later.
//
// Best effort: a refusal, such as the cache's free-space guard, leaves the
// response correct and the ids derived, and only a later request that names
// a file by id alone answers absent.
func (s *Server) recordIDs(ctx context.Context, entries []core.Entry) {
	if len(entries) == 0 {
		return
	}
	if err := s.deps.Store.RecordIDs(ctx, entries); err != nil {
		s.log.Warn("the ids of a listing could not be recorded", "count", len(entries), "error", err)
	}
}

// davIDOf renders an entry's identity as a client stores it, or the empty
// string when it has none.
func (s *Server) davIDOf(ctx context.Context, e core.Entry) string {
	id := s.fileID(ctx, e)
	if id == 0 {
		return ""
	}
	return DavID(id, s.instanceTag())
}

// setEntryHeaders writes what a client reads after a create or a change.
//
// Both spellings of the validator, because one client prefers the vendor
// header and another reads the standard one, and the identity header, without
// which one client fails an upload that already succeeded on the ground that
// it cannot tell what it just wrote.
func (s *Server) setEntryHeaders(ctx context.Context, w http.ResponseWriter, e core.Entry) {
	s.recordIDs(ctx, []core.Entry{e})
	if tag := ETagValue(e.ETag); tag != "" {
		w.Header().Set("ETag", tag)
		w.Header().Set("OC-ETag", tag)
	}
	if id := s.davIDOf(ctx, e); id != "" {
		w.Header().Set("OC-FileId", id)
	}
	if e.MTimeNs > 0 {
		w.Header().Set("Last-Modified", HTTPDate(e.MTimeNs))
	}
	if !e.IsDir {
		w.Header().Set("Content-Length", strconv.FormatUint(e.Size, 10))
	}
}

// noteMtimeAccepted reports that a requested modification time was applied.
//
// One client sets the header on an upload and reads this back to decide
// whether it has to correct the timestamp afterwards. Reporting it when the
// time was not applied costs a file whose local and remote stamps disagree
// forever, because the client stops checking.
func noteMtimeAccepted(w http.ResponseWriter, applied bool) {
	if applied {
		w.Header().Set("X-OC-MTime", "accepted")
	}
}

// loginNameOf is the account's sign-in name, which several responses carry.
//
// A failure answers the numeric id as text. That is never shown to a person:
// it is used where a client needs some stable string for the account it is
// signed in as, and an empty one there is a client that treats the account as
// unusable.
func (s *Server) loginNameOf(ctx context.Context, p Principal) string {
	info, err := s.deps.Auth.AccountInfo(ctx, p.UserID)
	if err != nil {
		return strconv.FormatInt(p.UserID, 10)
	}
	return info.LoginName
}

// guardLock refuses a write that a lock held elsewhere covers.
//
// A deployment with no lock table admits the write: refusing everything
// because a lock cannot be recorded turns an absent feature into an outage.
func (s *Server) guardLock(ctx context.Context, res core.Resolved, p Principal) error {
	if s.deps.LockGuard == nil {
		return nil
	}
	return s.deps.LockGuard(ctx, res, p.UserID)
}
