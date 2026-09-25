//go:build linux && compat_nc

package nc

import "net/http"

// LOCK and UNLOCK.
//
// The iOS client sends both with X-User-Lock: 1 before it opens a file for
// editing, and probes class 2 compliance in OPTIONS first to decide whether
// to bother. davAllow and davCompliance stay as they are so that probe keeps
// answering yes: an absent LOCK method turns editing off for every client
// that checks, and the honest 501 below is a better failure than that.
//
// The engine does hold a real lock table (service/davlock, reached by the
// native WebDAV surface through Deps.LockGuard), but nothing in Deps hands
// this package a way to mint or release a token from it. Answering LOCK as
// though it worked would claim a guarantee this layer cannot keep: a client
// that believes it holds an exclusive lock stops checking for a conflicting
// writer.

// davLock refuses to mint a lock this surface cannot record.
func (s *Server) davLock(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	WriteDAVError(w, http.StatusNotImplemented,
		"Sabre\\DAV\\Exception\\NotImplemented", "Locking is not available on this surface")
}

// davUnlock answers success for a token this surface never issued.
//
// A client that never received a token from LOCK has nothing to release, and
// refusing its cleanup call leaves it retrying the release forever over a
// lock that was never taken.
func (s *Server) davUnlock(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	w.WriteHeader(http.StatusNoContent)
}
