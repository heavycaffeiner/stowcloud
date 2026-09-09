//go:build linux && compat_nc

package nc

import (
	"net/http"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The DAV dispatch: what a method means for each of the trees, and the two
// answers that come before any of them.
//
// Discovery is answered without a credential, because a client establishes
// that the server speaks the protocol before it will authenticate, and a
// refusal there is reported to a person as an address that does not work.
// Everything else needs a caller, and the challenge is what makes a client
// send one: without the header it reports a failure instead of prompting.

// The methods this surface answers, as one list, because two of the clients
// read it and change what they do. One of them refuses to issue a search
// unless the list names it, so a discovery answer that leaves it out disables
// the whole of that client's favourites, gallery and recent screens.
const davAllow = "OPTIONS, GET, HEAD, POST, DELETE, TRACE, PROPFIND, PROPPATCH, " +
	"COPY, MOVE, LOCK, UNLOCK, REPORT, SEARCH, MKCOL, PUT"

// davCompliance is the compliance class list. Class 2 claims locking, which
// one client probes for before it will open a file for editing.
const davCompliance = "1, 2, 3"

// serveDav answers one DAV request.
func (s *Server) serveDav(w http.ResponseWriter, r *http.Request) {
	target, ok := ParseTarget(r.URL.EscapedPath())
	if !ok {
		s.failDav(w, r, core.ErrNotFound, apierr.VisibilityHidden)
		return
	}

	if r.Method == http.MethodOptions {
		s.davOptions(w)
		return
	}

	p, authed := principalOfRequest(r)
	if !authed {
		// A WebDAV client does not send a credential until it is asked, and
		// this is what asks. Without the challenge it reports a failure
		// instead of prompting, which reads as a broken server to whoever is
		// holding it.
		w.Header().Set("WWW-Authenticate", `Basic realm="WebDAV", charset="UTF-8"`)
		WriteDAVError(w, http.StatusUnauthorized,
			"Sabre\\DAV\\Exception\\NotAuthenticated", "No public access to this resource")
		return
	}

	switch target.Kind {
	case KindFiles:
		s.davFiles(w, r, p, target)
	case KindUploads:
		s.davUpload(w, r, p, target)
	case KindTrash:
		s.davTrash(w, r, p, target)
	case KindRoot:
		s.davMountRoot(w, r, p, target)
	case KindPrincipals:
		s.davPrincipals(w, r, p, target)
	default:
		// A tree this deployment has no store for. Answered as absent rather
		// than as a refusal: every client treats absence here as "the server
		// does not have this feature" and stops asking, while a refusal is
		// shown to a person as an error.
		s.failDav(w, r, core.ErrNotFound, apierr.VisibilityHidden)
	}
}

// davOptions answers discovery.
//
// The header set is the same for every tree, including the ones that hold
// nothing: a client asks about the mount root before it knows what is under
// it, and the answer is about the protocol rather than about a resource.
func (s *Server) davOptions(w http.ResponseWriter) {
	w.Header().Set("DAV", davCompliance)
	w.Header().Set("Allow", davAllow)
	w.Header().Set("MS-Author-Via", "DAV")
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

// davFiles dispatches one request against the files tree.
func (s *Server) davFiles(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	if t.IsRoot() {
		// The account's own root holds the shares it may reach. Nothing on
		// disk corresponds to it, so the listing is projected rather than
		// stat'd, and the other methods have nothing to act on.
		switch r.Method {
		case "PROPFIND":
			s.davRootPropfind(w, r, p, t)
		case "REPORT":
			s.davReport(w, r, p, t)
		case "SEARCH":
			s.davSearch(w, r, p, t)
		case http.MethodGet, http.MethodHead:
			w.Header().Set("DAV", davCompliance)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
		default:
			s.davMethodNotAllowed(w)
		}
		return
	}

	switch r.Method {
	case "PROPFIND":
		s.davPropfind(w, r, p, t)
	case "PROPPATCH":
		s.davProppatch(w, r, p, t)
	case "REPORT":
		s.davReport(w, r, p, t)
	case "SEARCH":
		s.davSearch(w, r, p, t)
	case http.MethodGet:
		s.davGet(w, r, p, t, true)
	case http.MethodHead:
		s.davGet(w, r, p, t, false)
	case http.MethodPut:
		s.davPut(w, r, p, t)
	case "MKCOL":
		s.davMkcol(w, r, p, t)
	case http.MethodDelete:
		s.davDelete(w, r, p, t)
	case "MOVE", "COPY":
		s.davTransfer(w, r, p, t)
	case "LOCK":
		s.davLock(w, r, p, t)
	case "UNLOCK":
		s.davUnlock(w, r, p, t)
	default:
		s.davMethodNotAllowed(w)
	}
}

// davMountRoot answers the mount itself, above every tree.
//
// A client asks about it to learn that the protocol is here and, in one case,
// to run a search across everything it can reach. Neither needs a resource
// behind the path.
func (s *Server) davMountRoot(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	switch r.Method {
	case "PROPFIND":
		s.davRootPropfind(w, r, p, t)
	case "REPORT":
		s.davReport(w, r, p, t)
	case "SEARCH":
		s.davSearch(w, r, p, t)
	case http.MethodGet, http.MethodHead:
		w.Header().Set("DAV", davCompliance)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
	default:
		s.davMethodNotAllowed(w)
	}
}

// davPrincipals answers the principal collection.
//
// One client asks for it during account setup and treats a refusal as a
// server it cannot use. The answer names the caller and nothing else: this
// deployment has no principal tree, and a listing of accounts would be a
// directory a device credential has no business reading.
func (s *Server) davPrincipals(w http.ResponseWriter, r *http.Request, p Principal, t Target) {
	if r.Method != "PROPFIND" {
		s.davMethodNotAllowed(w)
		return
	}

	login := s.loginNameOf(r.Context(), p)
	href := t.Prefix + "/users/" + escapeSegment(login)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	m := NewMulti(w)
	m.Open()
	m.Response(href+"/", []Prop{
		TextProp(PropDisplayName(), login),
		{Name: dav("resourcetype"), Children: []Node{{Name: dav("principal")}, {Name: dav("collection")}}},
		{Name: dav("current-user-principal"), Children: []Node{{Name: dav("href"), Text: href + "/"}}},
		{Name: dav("principal-URL"), Children: []Node{{Name: dav("href"), Text: href + "/"}}},
	}, nil)
	if err := m.Close(); err != nil {
		s.log.Warn("a principal listing was not delivered", "error", err)
	}
}

// davMethodNotAllowed refuses a method and names what the resource takes.
//
// The header matters: one client does not map the status for a write and shows
// whoever is holding it the reason phrase, so without the list neither they
// nor a proxy is told what would have worked.
func (s *Server) davMethodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", davAllow)
	WriteDAVError(w, http.StatusMethodNotAllowed,
		"Sabre\\DAV\\Exception\\MethodNotAllowed", "The method is not allowed on this resource")
}

// depthOf reads the Depth header.
//
// Absent means infinity by the standard, and that is what one client relies on
// for its own root listing. The value is clamped by the caller, because what a
// listing may walk is a policy of this surface rather than of the header.
func depthOf(r *http.Request) (depth int, infinite bool) {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Depth"))) {
	case "0":
		return 0, false
	case "1":
		return 1, false
	case "infinity", "":
		return 0, true
	default:
		return 0, false
	}
}
