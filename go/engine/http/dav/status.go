//go:build linux

package dav

import (
	"encoding/xml"
	"errors"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// Turning an error into the status and precondition element a WebDAV client
// reads.
//
// The mapping is here rather than in apierr because the two vocabularies
// disagree on purpose. A REST client is told a resource does not exist; a
// WebDAV client is told 423 with a lock-token-submitted element, which is what
// its retry logic branches on. Sharing one table would make one of the two
// wrong.

// StatusOf reports the status a failure answers with, and the precondition
// element naming it when RFC 4918 defines one.
//
// The zero Name means no element: most failures have a status and nothing
// more to say, and inventing an element for them would have a client parse
// for a condition the specification does not define.
func StatusOf(err error) (int, xml.Name) {
	switch {
	case err == nil:
		return http.StatusOK, xml.Name{}

	// A body this package refused to parse. Every one of these is the client's
	// request rather than the server's state.
	case errors.Is(err, ErrDirective), errors.Is(err, ErrProcInst),
		errors.Is(err, ErrUndeclaredPrefix), errors.Is(err, ErrBadPropertyName),
		errors.Is(err, ErrBadDepth), errors.Is(err, ErrBadIf),
		errors.Is(err, ErrBadEscape), errors.Is(err, ErrEncodedSeparator),
		errors.Is(err, ErrNUL), errors.Is(err, ErrDotSegment),
		errors.Is(err, ErrChunkNotDecimal), errors.Is(err, ErrChunkLeadingZero),
		errors.Is(err, ErrChunkRange), errors.Is(err, ErrNoDestination):
		return http.StatusBadRequest, xml.Name{}

	// A body that parsed and was too big to accept. Still the request's fault,
	// so still 400: the bound is on what a client may send, not on storage.
	case errors.Is(err, ErrBodyTooLarge), errors.Is(err, ErrTooDeep),
		errors.Is(err, ErrTooManyElements), errors.Is(err, ErrNameTooLong),
		errors.Is(err, ErrTooMuchText), errors.Is(err, ErrTooManyProperties),
		errors.Is(err, ErrIfTooLarge):
		return http.StatusBadRequest, xml.Name{}

	// A Destination on another host. 502 rather than 400, because the request
	// is well formed and this server simply is not the one that can serve it.
	case errors.Is(err, ErrForeignDestination):
		return http.StatusBadGateway, xml.Name{}

	// The one refusal that carries an element. A client reads it to learn that
	// resubmitting with the lock token is what would work.
	case errors.Is(err, ErrLocked):
		return http.StatusLocked, davName("lock-token-submitted")

	case errors.Is(err, ErrPreconditionFailed), errors.Is(err, core.ErrPrecondition):
		return http.StatusPreconditionFailed, xml.Name{}

	// Absence and denial answer the same, which is what stops a stranger
	// mapping a tree they cannot read by watching which paths answer 403.
	case errors.Is(err, core.ErrNotFound):
		return http.StatusNotFound, xml.Name{}

	case errors.Is(err, core.ErrDenied):
		return http.StatusForbidden, xml.Name{}

	// 405 rather than 409: the target exists and the method cannot apply to
	// what is there, which is what a PUT onto a collection is.
	case errors.Is(err, core.ErrExists):
		return http.StatusMethodNotAllowed, xml.Name{}

	case errors.Is(err, ErrUnsupportedMedia):
		return http.StatusUnsupportedMediaType, xml.Name{}

	// A parent that is missing, or a collection that still has members. Both
	// are the request meeting a tree it did not expect.
	case errors.Is(err, core.ErrNotEmpty), errors.Is(err, core.ErrConflict),
		errors.Is(err, core.ErrCrossShare):
		return http.StatusConflict, xml.Name{}

	// Out of room, either the volume's or a configured bound. A client retries
	// these differently from a refusal, so they do not fold into 409.
	case errors.Is(err, core.ErrNoSpace), errors.Is(err, core.ErrQuotaExceeded),
		errors.Is(err, limits.ErrTooLarge):
		return http.StatusInsufficientStorage, xml.Name{}

	// A share whose backing went away. 503 rather than 404: the resource is
	// not gone, this server cannot reach it, and a sync client that reads 404
	// deletes its local copy.
	case errors.Is(err, core.ErrShareBroken):
		return http.StatusServiceUnavailable, xml.Name{}

	default:
		return http.StatusInternalServerError, xml.Name{}
	}
}

// The refusals this layer makes that have no home in another package.
var (
	// ErrPreconditionFailed reports a condition the request stated and the
	// server's state did not satisfy.
	ErrPreconditionFailed = errors.New("a precondition failed")
	// ErrLocked reports a write turned away because the caller never presented
	// the token covering the target.
	//
	// Separate from the lock service's sentinel by design. That one states a
	// fact about the domain; this one is how the protocol answers it, and the
	// mount translates between them.
	ErrLocked = errors.New("the resource is locked")
	// ErrUnsupportedMedia is a body on a method that defines none.
	ErrUnsupportedMedia = errors.New("unsupported media type")
)

// WriteError writes an error response, with the precondition element when the
// status carries one.
//
// A 207 never comes through here: a multistatus reports per-resource failures
// inside its own body, and a request that failed as a whole is what this
// answers.
func WriteError(w http.ResponseWriter, err error) {
	status, cond := StatusOf(err)

	if cond.Local == "" {
		http.Error(w, http.StatusText(status), status)
		return
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)

	// Written directly rather than through the multistatus writer: this is a
	// single error document, and the element name is this package's own
	// constant rather than anything a request supplied.
	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<` + davPrefix + `:error xmlns:` + davPrefix + `="` + davNS + `">` +
		`<` + davPrefix + `:` + cond.Local + `/>` +
		`</` + davPrefix + `:error>`
	//nolint:errcheck // the status is already sent and a failed body write has nowhere to go.
	_, _ = w.Write([]byte(body))
}
