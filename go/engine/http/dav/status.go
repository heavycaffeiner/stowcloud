//go:build linux

package dav

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
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
		errors.Is(err, ErrChunkRange), errors.Is(err, ErrNoDestination),
		errors.Is(err, ErrChunkOnCollection), errors.Is(err, ErrNoUploadLength),
		errors.Is(err, ErrBadUploadLength), errors.Is(err, ErrBadUploadMTime),
		errors.Is(err, ErrNoBody), errors.Is(err, ErrNoLockToken),
		errors.Is(err, ErrBadLockDepth):
		return http.StatusBadRequest, xml.Name{}

	// A body that parsed and was too big to accept. Still the request's fault,
	// so still 400: the bound is on what a client may send, not on storage.
	case errors.Is(err, ErrBodyTooLarge), errors.Is(err, ErrTooDeep),
		errors.Is(err, ErrTooManyElements), errors.Is(err, ErrNameTooLong),
		errors.Is(err, ErrTooMuchText), errors.Is(err, ErrTooManyProperties),
		errors.Is(err, ErrIfTooLarge):
		return http.StatusBadRequest, xml.Name{}

	// A refusal an adapter marked as the client's own mistake. The interface
	// rather than a named type: the adapters marking these cannot be imported
	// here, and a sentinel each would mean a new one to remember adding.
	case badRequest(err):
		return http.StatusBadRequest, xml.Name{}

	// XML the decoder itself rejected: a truncated document, a mismatched end
	// tag, an illegal character. The sentinels above cover what this package
	// refuses on purpose, and this covers what the parser refuses on its own.
	// Without it a client's malformed body is reported as a server fault.
	case isSyntaxError(err):
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

	// 409 rather than 501: the request is one this server implements, and the
	// conflict is with how this deployment is configured. A client that reads
	// 501 stops offering the feature; one that reads 409 reports the failure
	// against the resource it tried.
	case errors.Is(err, ErrNoPropertyStore):
		return http.StatusConflict, xml.Name{}

	// A deployment that records no locks does not implement LOCK, and Allow
	// leaves the method out. 405 rather than 501 for the same reason as above:
	// a client reading 501 stops offering locking entirely, where 405 is about
	// this resource on this server.
	case errors.Is(err, ErrNoLockTable):
		return http.StatusMethodNotAllowed, xml.Name{}

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

// badRequester is what an adapter marks a client's own mistake with.
//
// The adapters live below this package and cannot be imported, so the mark
// travels as a capability rather than a sentinel: one interface, checked with
// errors.As, and no new sentinel to remember adding to the table.
type badRequester interface{ BadRequest() bool }

// badRequest reports whether an error was marked as the client's fault by an
// adapter that cannot be imported here.
func badRequest(err error) bool {
	var m badRequester
	return errors.As(err, &m)
}

// isSyntaxError reports whether the decoder rejected the document itself.
//
// Both forms are checked. A syntax error is what a malformed document
// produces, and an unexpected EOF is what a truncated one produces, which is
// the more common of the two: a client whose connection dropped mid-body.

// A syntax error is a malformed document's product.
//
// Both forms are checked. A syntax error is what a malformed document
// produces, and an unexpected EOF is what a truncated one produces, which is
// the more common of the two: a client whose connection dropped mid-body.
func isSyntaxError(err error) bool {
	var syntax *xml.SyntaxError
	return errors.As(err, &syntax) || errors.Is(err, io.ErrUnexpectedEOF)
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
	// ErrNoPropertyStore reports a deployment with nowhere to keep dead
	// properties, on a request that asked to store one.
	//
	// Reported rather than answered 200. A client told its properties were
	// stored has no reason to expect the next PROPFIND to lose them.
	ErrNoPropertyStore = errors.New("this deployment stores no dead properties")
)

// WriteError writes an error response, with the precondition element when the
// status carries one.
//
// A 207 never comes through here: a multistatus reports per-resource failures
// inside its own body, and a request that failed as a whole is what this
// answers.
//
// It reports whether the body reached the client. The status line is already
// sent by then, so a failure has no second response to become; a caller that
// logs is the only thing left, and one that does not may ignore it.
func WriteError(w http.ResponseWriter, err error) error {
	status, cond := StatusOf(err)

	if cond.Local == "" {
		http.Error(w, http.StatusText(status), status)
		return nil
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
	if _, werr := w.Write([]byte(body)); werr != nil {
		return fmt.Errorf("writing the error document: %w", werr)
	}
	return nil
}
