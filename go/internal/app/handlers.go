//go:build linux

// Binding one handler family to the services behind it.
//
// A handler here does three things and no more: read what the chain already
// decided, call one service, and hand the result to a projection. It decides
// no policy, opens nothing, and never reaches past the service it was given.
package app

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/uploads"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
)

// jobsList answers the caller's own operations.
func (e *Engine) jobsList(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	ops, err := e.Core.ListOperations(c.Request.Context(), owner, jobsPageSize)
	if err != nil {
		fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.OperationsOf(ops))
}

// notFound is the one answer for every resource the caller may not have.
//
// An absent job, a job belonging to someone else and an id that is not a
// number all render byte for byte the same. The service's own refusal carries
// a catalogue key and a hand-built one would not, so this classifies the
// service's sentinel rather than naming the class directly: two refusals that
// differ by a field are two refusals a caller can tell apart, which is the
// existence rule broken by one JSON key.
func notFound(c *gin.Context) {
	fail(c, core.ErrNotFound)
}

// jobsGet answers one operation.
func (e *Engine) jobsGet(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id, ok := operationID(c)
	if !ok {
		notFound(c)
		return
	}
	op, err := e.Core.Operation(c.Request.Context(), owner, id)
	if err != nil {
		fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.OperationOf(op))
}

// jobsCancel asks an operation to stop.
func (e *Engine) jobsCancel(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id, ok := operationID(c)
	if !ok {
		notFound(c)
		return
	}
	if err := e.Core.CancelOperation(c.Request.Context(), owner, id); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (e *Engine) jobsRetry(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id, ok := operationID(c)
	if !ok {
		notFound(c)
		return
	}
	op, err := e.Core.Operation(c.Request.Context(), owner, id)
	if err != nil {
		fail(c, err)
		return
	}
	if op.State != state.OpFailed && op.State != state.OpInterrupted {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if err := e.State.ResumeOp(c.Request.Context(), int64(id), e.clk().Nanos()); err != nil {
		fail(c, err)
		return
	}
	e.Core.StartJobs()
	c.Status(http.StatusNoContent)
}

func (e *Engine) jobsPause(c *gin.Context)  { e.jobPauseResume(c, true) }
func (e *Engine) jobsResume(c *gin.Context) { e.jobPauseResume(c, false) }
func (e *Engine) jobPauseResume(c *gin.Context, pause bool) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	id, ok := operationID(c)
	if !ok {
		notFound(c)
		return
	}
	if _, err := e.Core.Operation(c.Request.Context(), owner, id); err != nil {
		fail(c, err)
		return
	}
	var err error
	if pause {
		err = e.State.PauseOp(c.Request.Context(), int64(id))
	} else {
		err = e.State.ResumeOp(c.Request.Context(), int64(id), e.clk().Nanos())
	}
	if err != nil {
		fail(c, err)
		return
	}
	e.Core.StartJobs()
	c.Status(http.StatusNoContent)
}

// jobsPageSize bounds a listing. One page, because a client watching its own
// jobs wants the recent ones and an unbounded listing is a query whose cost
// grows with how long an account has been used.
const jobsPageSize = 100

// ownerOf reads the account the chain authenticated.
//
// The chain has already decided this: a handler that re-derived an identity
// from the request would be a second answer to the question the chain exists
// to answer once.
func ownerOf(c *gin.Context) (core.UserID, bool) {
	p, ok := c.Get(string(middleware.KeyCredential))
	principal, okp := p.(middleware.Principal)
	if !ok || !okp || principal.UserID == 0 {
		return 0, false
	}
	return core.UserID(principal.UserID), true
}

// operationID reads the path's job id.
//
// Decimal on the wire because a JavaScript number loses exactness past 2^53,
// so an id a client round-trips would come back as a different id.
//
// The guard is defensive rather than load-bearing: the service refuses an id
// it does not own with the same not-found this produces, so removing this
// check changes no answer. It stays because a malformed id should not become
// a database query, and because the service's guarantee is the service's to
// change.
func operationID(c *gin.Context) (core.OperationID, bool) {
	n, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return core.OperationID(n), true
}

// fail renders a service error through the one classifier.
//
// Known visibility: an ACL permission denial (core.ErrDenied) is reported as
// 403 Forbidden rather than disguised as not-found, while missing paths and
// foreign shares without grants remain 404 Not Found.
func fail(c *gin.Context, err error) {
	var full *upload.CacheFullError
	if errors.As(err, &full) && full.RetryAfterSeconds > 0 {
		c.Header("Retry-After", strconv.Itoa(full.RetryAfterSeconds))
	}
	middleware.SetCause(c, err)
	refuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}

// refuse writes a classified refusal.
func refuse(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	writeJSON(c, status, body)
}

// requestBodyReader streams the request body directly when available, falling
// back to buffered body bytes.
func requestBodyReader(c *gin.Context) io.Reader {
	if c.Request != nil && c.Request.Body != nil {
		return c.Request.Body
	}
	return bytes.NewReader(nil)
}
