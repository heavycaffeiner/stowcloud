//go:build linux && compat_nc

package nc

import (
	"errors"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
)

// The status ladder.
//
// One classifier, consulted once, then two adapters over its answer: a DAV
// status with the exception name a client's error parser reads, and an OCS
// code for the envelope. Deriving either from raw sentinel chains a second
// time is how the same refusal ends up as 403 on one surface and 500 on the
// other.

// ErrBadPath is a request path that names something other than a resource
// below the mount.
var ErrBadPath = errors.New("nc: malformed path")

// ErrFlowPending is a device login whose person has not approved it yet. The
// client polls against it, so it is not a failure.
var ErrFlowPending = errors.New("nc: login flow pending")

// DavStatus is the answer one refusal gets on the DAV surface.
type DavStatus struct {
	Code int
	// Exception is the name a client's error parser matches on. The vocabulary
	// spells these as PHP class names, and a client branches on two of them
	// (a virus report and a terms-of-service gate) while showing the rest.
	Exception string
	Message   string
}

// davStatusOf reduces an error to a DAV answer.
//
// Visibility is the caller's decision, not this function's: a path reached
// through a listing the caller may read can be told it was denied, and a path
// the caller guessed may not learn the difference between denied and absent.
func davStatusOf(err error, vis apierr.Visibility) DavStatus {
	c := apierr.Classify(err, vis)
	switch c.Class {
	case apierr.Hidden, apierr.NotFound:
		return DavStatus{http.StatusNotFound, "Sabre\\DAV\\Exception\\NotFound", "File not found"}
	case apierr.Denied:
		return DavStatus{http.StatusForbidden, "Sabre\\DAV\\Exception\\Forbidden", "Access denied"}
	case apierr.Gone:
		return DavStatus{http.StatusNotFound, "Sabre\\DAV\\Exception\\NotFound", "File not found"}
	case apierr.AuthRequired, apierr.AuthInvalid, apierr.AccountDisabled:
		return DavStatus{http.StatusUnauthorized, "Sabre\\DAV\\Exception\\NotAuthenticated", "No public access to this resource"}
	case apierr.Conflict:
		return DavStatus{http.StatusConflict, "Sabre\\DAV\\Exception\\Conflict", "The destination is in conflict"}
	case apierr.NotEmpty:
		return DavStatus{http.StatusConflict, "Sabre\\DAV\\Exception\\Conflict", "The collection is not empty"}
	case apierr.Exists:
		// A create that collided. The vocabulary answers this with the status
		// a collection uses for a name already taken, which one client maps
		// straight onto "the folder is already there" rather than an error.
		return DavStatus{http.StatusMethodNotAllowed, "Sabre\\DAV\\Exception\\MethodNotAllowed", "The resource already exists"}
	case apierr.Precondition:
		return DavStatus{http.StatusPreconditionFailed, "Sabre\\DAV\\Exception\\PreconditionFailed", "The precondition failed"}
	case apierr.Locked:
		return DavStatus{http.StatusLocked, "Sabre\\DAV\\Exception\\Locked", "The resource is locked"}
	case apierr.RangeNotSatisfiable:
		return DavStatus{http.StatusRequestedRangeNotSatisfiable, "Sabre\\DAV\\Exception\\RequestedRangeNotSatisfiable", "The range is not satisfiable"}
	case apierr.NoSpace:
		// The status one client maps to a quota message of its own. A generic
		// 500 there reads as a broken server and the upload is retried until
		// the account is out of space in a different way.
		return DavStatus{http.StatusInsufficientStorage, "Sabre\\DAV\\Exception\\InsufficientStorage", "Insufficient storage"}
	case apierr.Malformed, apierr.Unprocessable:
		return DavStatus{http.StatusBadRequest, "Sabre\\DAV\\Exception\\BadRequest", "The request is malformed"}
	case apierr.BodyTooLarge, apierr.LimitExceeded:
		return DavStatus{http.StatusRequestEntityTooLarge, "Sabre\\DAV\\Exception\\BadRequest", "The request is too large"}
	case apierr.RateLimited, apierr.ResourceExhausted:
		return DavStatus{http.StatusServiceUnavailable, "Sabre\\DAV\\Exception\\ServiceUnavailable", "Try again later"}
	case apierr.ShareUnavailable, apierr.SubsystemUnavailable:
		return DavStatus{http.StatusServiceUnavailable, "Sabre\\DAV\\Exception\\ServiceUnavailable", "The storage is unavailable"}
	case apierr.NotImplemented:
		return DavStatus{http.StatusNotImplemented, "Sabre\\DAV\\Exception\\NotImplemented", "Not implemented"}
	default:
		return DavStatus{http.StatusInternalServerError, "Sabre\\DAV\\Exception", "Server error"}
	}
}

// failDav writes the refusal for one DAV request.
//
// A refused write is logged, because a client shows whoever is holding it the
// reason phrase and nothing else: without a line here an operator answering
// "the upload says forbidden" cannot see which request that was. Reads stay
// quiet, since a sync client probes for absent paths constantly and logging
// those buries the line that matters.
func (s *Server) failDav(w http.ResponseWriter, r *http.Request, err error, vis apierr.Visibility) {
	st := davStatusOf(err, vis)
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, "PROPFIND", "REPORT", "SEARCH":
	default:
		if st.Code >= http.StatusInternalServerError || st.Code == http.StatusForbidden {
			s.log.Warn("a request was refused",
				"method", r.Method, "path", r.URL.Path, "status", st.Code, "error", err)
		}
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(st.Code)
		return
	}
	WriteDAVError(w, st.Code, st.Exception, st.Message)
}

// ocsErrorOf reduces an error to an envelope refusal.
//
// The codes are the four a client branches on. Everything else is a failure,
// because a code a client does not know is shown to a person as whatever text
// came with it, and a wrong one sends them to the wrong place.
func ocsErrorOf(err error, vis apierr.Visibility) *Error {
	c := apierr.Classify(err, vis)
	switch c.Class {
	case apierr.Hidden, apierr.NotFound, apierr.Gone:
		return NotFound("The requested resource could not be found")
	case apierr.Denied:
		return Forbidden("Not allowed")
	case apierr.AuthRequired, apierr.AuthInvalid, apierr.AccountDisabled:
		return Unauthorized("Unauthorised")
	case apierr.Malformed, apierr.Unprocessable, apierr.Conflict, apierr.Exists,
		apierr.NotEmpty, apierr.Precondition, apierr.WeakPassword, apierr.NameTaken:
		return BadRequest("Invalid request")
	case apierr.NoSpace:
		return Failure("Insufficient storage")
	default:
		return Failure("Server error")
	}
}
