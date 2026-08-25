package apierr

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The wire codes the native REST surface branches on. Each is a constant
// because a client ships a switch over these strings, and a literal that
// drifts between the mapper and the catalogue is a client bug nobody sees.
const (
	CodeAuthRequired      Code = "auth.required"
	CodeAuthInvalid       Code = "auth.invalid_credentials"
	CodeACLDenied         Code = "acl.denied"
	CodeFsNotFound        Code = "fs.not_found"
	CodeFsConflict        Code = "fs.conflict"
	CodeFsPrecondition    Code = "fs.precondition"
	CodeFsInvalidName     Code = "fs.invalid_name"
	CodeFsGone            Code = "fs.gone"
	CodeQuotaExceeded     Code = "quota.exceeded"
	CodeRateLimited       Code = "rate.limited"
	CodeLimitExceeded     Code = "fs.limit_exceeded"
	CodeSetupCompleted    Code = "setup.completed"
	CodeSetupTokenExpired Code = "setup.token_expired" //nolint:gosec // G101 reads the identifier: a code value, not a credential.
	CodeSetupInvalidToken Code = "setup.invalid_token"
	CodeSetupInvalidUser  Code = "setup.invalid_username"
	CodeSetupWeakPassword Code = "setup.weak_password"
	CodeLastAdmin         Code = "admin.last_admin"
	CodeInvalidQuota      Code = "admin.invalid_quota"
	CodeWeakPassword      Code = "auth.weak_password" //nolint:gosec // G101 reads the identifier: a code value, not a credential.
	CodeNotImplemented    Code = "internal.not_implemented"
	CodeSubsystemUnavail  Code = "internal.subsystem_unavailable"
	CodeInvalidRequest    Code = "fs.invalid_request"
)

// The generic fallback sentences. They are stable and generic by design: the
// reader's language comes from the catalogue key in detail, and a sentence
// that changes between builds is a translation churn for no gain.
const (
	msgAuthRequired     Message = "authentication required"
	msgAuthInvalid      Message = "invalid credentials"
	msgACLDenied        Message = "permission denied"
	msgNotFound         Message = "not found"
	msgConflict         Message = "state conflict"
	msgPrecondition     Message = "precondition failed"
	msgInvalidName      Message = "invalid name"
	msgGone             Message = "the resource is permanently gone"
	msgQuotaExceeded    Message = "quota exceeded"
	msgRateLimited      Message = "rate limited"
	msgLimitExceeded    Message = "a configured limit was exceeded"
	msgLastAdmin        Message = "refusing to remove the last administrator"
	msgInvalidQuota     Message = "quota must be greater than zero, or absent for unlimited"
	msgWeakPassword     Message = "password is too short"
	msgNotImplemented   Message = "not implemented in this build"
	msgSubsystemUnavail Message = "a named subsystem is unavailable"
	msgInvalidRequest   Message = "malformed request"
)

// Map turns a domain error into a status and a wire error. It is the only
// function on the native REST surface that names an HTTP status, and the only
// one that decides whether a refusal is visible as 403 or hidden as 404. TUS,
// WebDAV and the compat mounts have their own mappers, because their status
// vocabularies are set outside this repository.
//
// The existence rule lives here and nowhere else: ErrNotFound and the denied
// subset a caller may not know about both produce the same 404 body.
func Map(err error) (int, *Error) {
	if err == nil {
		return http.StatusOK, nil
	}

	// A refusal this surface produced itself, for a malformed or invalid
	// request. It names its own status, code and catalogue key, and it flows
	// through this function like every other error so that a status is still
	// chosen in one place.
	// The body limiter refuses by wrapping the reader, so its refusal arrives
	// as a read error rather than as one of this package's own. Unmapped it
	// became a server error: the client was told the server broke when it was
	// the client that sent too much.
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return http.StatusRequestEntityTooLarge,
			NewError(CodeLimitExceeded, msgLimitExceeded, "http.body_too_large")
	}

	var req *RequestError
	if errors.As(err, &req) {
		return req.Status, NewError(req.Code, req.Message, req.Key, req.Args...)
	}

	switch {
	case errors.Is(err, core.ErrNotFound), errors.Is(err, vfs.ErrNotFound), errors.Is(err, vfs.ErrSymlinkDenied):
		return http.StatusNotFound, notFound()
	case errors.Is(err, core.ErrDenied), errors.Is(err, vfs.ErrDenied):
		return http.StatusForbidden, NewError(CodeACLDenied, msgACLDenied, "")
	case errors.Is(err, core.ErrPrecondition):
		var pre *core.PreconditionError
		if errors.As(err, &pre) {
			return http.StatusPreconditionFailed,
				NewError(CodeFsPrecondition, msgPrecondition, "",
					Arg{Name: "current_etag", Value: pre.Current})
		}
		return http.StatusPreconditionFailed, NewError(CodeFsPrecondition, msgPrecondition, "")
	case errors.Is(err, core.ErrConflict), errors.Is(err, vfs.ErrCrossDevice):
		return http.StatusConflict, NewError(CodeFsConflict, msgConflict, "")
	case errors.Is(err, auth.ErrNameTaken):
		// A name somebody else holds is a conflict the person who typed it can
		// act on, not a server error.
		return http.StatusConflict, NewError(CodeFsConflict, "that name is already taken", "admin.name_taken")
	case errors.Is(err, core.ErrExists), errors.Is(err, vfs.ErrExists):
		return http.StatusConflict, NewError(CodeFsConflict, msgConflict, "")
	case errors.Is(err, core.ErrNotEmpty), errors.Is(err, vfs.ErrNotEmpty):
		return http.StatusConflict, NewError(CodeFsConflict, msgConflict, "")
	case errors.Is(err, core.ErrCrossShare):
		return http.StatusConflict, NewError(CodeFsConflict, msgConflict, "")
	case errors.Is(err, vfs.ErrNotADirectory):
		return http.StatusConflict, NewError(CodeFsConflict, msgConflict, "")
	case errors.Is(err, core.ErrTrashDisabled):
		return http.StatusConflict, NewError(CodeFsConflict, msgConflict, "")
	case errors.Is(err, core.ErrLinkExpired):
		return http.StatusGone, NewError(CodeFsGone, msgGone, "")
	case errors.Is(err, core.ErrNoSpace), errors.Is(err, vfs.ErrNoSpace):
		return http.StatusInsufficientStorage, NewError(CodeQuotaExceeded, msgQuotaExceeded, "")
	case errors.Is(err, core.ErrQuotaExceeded):
		return http.StatusInsufficientStorage, NewError(CodeQuotaExceeded, msgQuotaExceeded, "")
	case errors.Is(err, limits.ErrTooLarge):
		var ex *limits.Exceeded
		if errors.As(err, &ex) {
			return http.StatusRequestEntityTooLarge, NewError(CodeLimitExceeded, msgLimitExceeded,
				"fs.limit_exceeded",
				Arg{Name: "limit", Value: ex.Limit},
				Arg{Name: "bound", Value: fmt.Sprint(ex.Bound)},
				Arg{Name: "got", Value: fmt.Sprint(ex.Got)})
		}
		return http.StatusRequestEntityTooLarge, NewError(CodeLimitExceeded, msgLimitExceeded, "")
	case errors.Is(err, vfs.ErrInvalidName), errors.Is(err, vfs.ErrReservedName):
		var n *vfs.NameError
		if errors.As(err, &n) {
			return http.StatusUnprocessableEntity, NewError(CodeFsInvalidName, msgInvalidName,
				"fs.invalid_name",
				Arg{Name: "component", Value: n.Component})
		}
		return http.StatusUnprocessableEntity, NewError(CodeFsInvalidName, msgInvalidName, "")
	case errors.Is(err, auth.ErrLastAdmin):
		// A conflict, not a permission failure: the caller is allowed to do
		// this, the deployment's state is what refuses it.
		return http.StatusConflict, NewError(CodeLastAdmin, msgLastAdmin, "admin.last_admin")
	case errors.Is(err, auth.ErrInvalidQuota):
		return http.StatusUnprocessableEntity, NewError(CodeInvalidQuota, msgInvalidQuota, "admin.invalid_quota")
	case errors.Is(err, auth.ErrWeakPassword):
		return http.StatusUnprocessableEntity, NewError(CodeWeakPassword, msgWeakPassword, "auth.weak_password",
			Arg{Name: "min_length", Value: fmt.Sprint(auth.MinPasswordLen)})
	case errors.Is(err, auth.ErrCredentials):
		return http.StatusUnauthorized, NewError(CodeAuthInvalid, msgAuthInvalid, "")
	case errors.Is(err, auth.ErrAccountDisabled):
		return http.StatusForbidden, NewError(CodeACLDenied, msgACLDenied, "")
	case errors.Is(err, auth.ErrRateLimited):
		return http.StatusTooManyRequests, NewError(CodeRateLimited, msgRateLimited, "")
	default:
		return http.StatusInternalServerError, Internal()
	}
}

// notFound is the 404 body, and it is one shape on purpose: the existence
// rule's byte-identical guarantee is a property of this single construction,
// so the test for it has a single function to hold.
func notFound() *Error {
	return NewError(CodeFsNotFound, msgNotFound, "")
}

// MapNotFound is the explicit form of the existence rule for the callers that
// know before the core runs that the caller may not learn the truth: a path
// outside every grant and a path that does not exist are the same answer.
func MapNotFound() *Error { return notFound() }

// RequestError is a refusal the surface itself produces: a malformed request,
// or syntactically valid input that fails a named field or domain constraint.
// It carries its own status, code and catalogue key, and Map renders it like
// any other error, so the rule that one function names a status holds for the
// surface's own refusals too.
type RequestError struct {
	Status  int
	Code    Code
	Message Message
	Key     MessageKey
	Args    []Arg
}

func (e *RequestError) Error() string { return string(e.Code) + ": " + string(e.Message) }

// BadRequest builds a 400 for a malformed request. field names the part of
// the request that was wrong; the offending value is never echoed.
func BadRequest(key MessageKey, field string) *RequestError {
	return &RequestError{
		Status: http.StatusBadRequest, Code: CodeInvalidRequest,
		Message: msgInvalidRequest, Key: key,
		Args: []Arg{{Name: "field", Value: field}},
	}
}

// BadGateway builds a 502 for a request naming a resource on another server.
//
// WebDAV's COPY and MOVE take a Destination that may be an absolute URL, and
// RFC 4918 9.8.3 makes one this server cannot write a 502 rather than a
// refusal about the request's own syntax: the request is well formed and names
// somewhere this server does not speak for.
func BadGateway(key MessageKey, field string) *RequestError {
	return &RequestError{
		Status: http.StatusBadGateway, Code: CodeInvalidRequest,
		Message: msgInvalidRequest, Key: key,
		Args: []Arg{{Name: "field", Value: field}},
	}
}

// Unprocessable builds a 422 for input that parses but fails a named field or
// domain constraint.
func Unprocessable(key MessageKey, field string) *RequestError {
	return &RequestError{
		Status: http.StatusUnprocessableEntity, Code: CodeInvalidRequest,
		Message: msgInvalidRequest, Key: key,
		Args: []Arg{{Name: "field", Value: field}},
	}
}
