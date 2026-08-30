//go:build linux

package dav

import (
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// OPTIONS, and the one entry point a mount calls.
//
// Everything above this file answers one method given a resolution. This is
// what turns a request into the method that answers it, so a mount resolves
// the path and hands both over without knowing the method table.

// Options answers OPTIONS.
func (h *Handler) Options(w http.ResponseWriter, _ *http.Request, res core.Resolved) {
	// Class 2 is locking. Advertising it without a lock table would have a
	// client take a lock it believes is recorded and write on the strength of
	// it, so the class follows what this deployment can actually do.
	compliance := "1"
	if h.taker != nil {
		compliance = "1, 2"
	}
	w.Header().Set("DAV", compliance)
	w.Header().Set("Allow", h.allowFor(res))
	// Some clients only mount a share that names this, and it costs a header.
	w.Header().Set("MS-Author-Via", "DAV")
	w.WriteHeader(http.StatusOK)
}

// MountOptions answers discovery for the mount itself.
//
// The virtual root has no resource behind it, so there is nothing to stat and
// the full method list is what it reports. This is how a client learns the
// server speaks the protocol, which it does before it has a credential.
func (h *Handler) MountOptions(w http.ResponseWriter) {
	compliance := "1"
	if h.taker != nil {
		compliance = "1, 2"
	}
	w.Header().Set("DAV", compliance)
	// A collection, because that is what the root is, and one nothing may be
	// written directly into: a share is created through the native surface.
	w.Header().Set("Allow", AllowHeader(AllowSet{
		Exists:  true,
		IsDir:   true,
		Locking: h.taker != nil,
	}))
	w.Header().Set("MS-Author-Via", "DAV")
	w.WriteHeader(http.StatusOK)
}

// allowFor describes what this resource accepts.
func (h *Handler) allowFor(res core.Resolved) string {
	set := AllowSet{Locking: h.taker != nil}
	if st, err := res.Root().Stat(res.Path()); err == nil {
		set.Exists = true
		set.IsDir = st.Kind.IsDir()
	}
	return AllowHeader(set)
}

// ServeMethod answers one request against an already-resolved path.
//
// COPY and MOVE are absent: their destination arrives as a URL in a header,
// and resolving it is the mount's work, so those two take a second resolution
// and are called directly.
func (h *Handler) ServeMethod(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	switch r.Method {
	case http.MethodOptions:
		h.Options(w, r, res)
	case http.MethodGet:
		h.Get(w, r, res, true)
	case http.MethodHead:
		h.Get(w, r, res, false)
	case http.MethodPut:
		h.Put(w, r, res)
	case http.MethodDelete:
		h.Delete(w, r, res)
	case "MKCOL":
		h.Mkcol(w, r, res)
	case "PROPFIND":
		h.Propfind(w, r, res)
	case "PROPPATCH":
		h.Proppatch(w, r, res)
	case "LOCK":
		h.Lock(w, r, res)
	case "UNLOCK":
		h.Unlock(w, r, res)
	default:
		// Including COPY and MOVE, which reach this only when a mount routed
		// them here rather than through the two-endpoint call they need.
		h.methodNotAllowedFor(w, res)
	}
}

// methodNotAllowedFor refuses a method and names what would work instead.
//
// It takes the resolution rather than a fixed list, so the Allow header names
// what this resource accepts: a GET of a collection is refused, and the header
// beside the refusal has to be true of that collection.
func (h *Handler) methodNotAllowedFor(w http.ResponseWriter, res core.Resolved) {
	w.Header().Set("Allow", h.allowFor(res))
	w.WriteHeader(http.StatusMethodNotAllowed)
}
