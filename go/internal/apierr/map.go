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
	CodeSetupTokenExpired Code = "setup.token_expired"
	CodeSetupInvalidToken Code = "setup.invalid_token"
	CodeSetupInvalidUser  Code = "setup.invalid_username"
	CodeSetupWeakPassword Code = "setup.weak_password"
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
