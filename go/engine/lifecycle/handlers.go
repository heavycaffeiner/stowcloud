//go:build linux

// Binding one handler family to the services behind it.
//
// A handler here does three things and no more: read what the chain already
// decided, call one service, and hand the result to a projection. It decides
// no policy, opens nothing, and never reaches past the service it was given.
package lifecycle

import (
	"bytes"
	"errors"
	"github.com/gofiber/fiber/v2"
	"io"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/upload"
)

// jobsList answers the caller's own operations.
func (e *Engine) jobsList(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	ops, err := e.Core.ListOperations(c.UserContext(), owner, jobsPageSize)
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.OperationsOf(ops))
}

// notFound is the one answer for every resource the caller may not have.
//
// An absent job, a job belonging to someone else and an id that is not a
// number all render byte for byte the same. The service's own refusal carries
// a catalogue key and a hand-built one would not, so this classifies the
// service's sentinel rather than naming the class directly: two refusals that
// differ by a field are two refusals a caller can tell apart, which is the
// existence rule broken by one JSON key.
func notFound(c *fiber.Ctx) error {
	return fail(c, core.ErrNotFound)
}

// jobsGet answers one operation.
func (e *Engine) jobsGet(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	id, ok := operationID(c)
	if !ok {
		return notFound(c)
	}

	op, err := e.Core.Operation(c.UserContext(), owner, id)
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.OperationOf(op))
}

// jobsCancel asks an operation to stop.
func (e *Engine) jobsCancel(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	id, ok := operationID(c)
	if !ok {
		return notFound(c)
	}

	if err := e.Core.CancelOperation(c.UserContext(), owner, id); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
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
func ownerOf(c *fiber.Ctx) (core.UserID, bool) {
	p, ok := c.Locals(middleware.KeyCredential).(middleware.Principal)
	if !ok || p.UserID == 0 {
		return 0, false
	}
	return core.UserID(p.UserID), true
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
func operationID(c *fiber.Ctx) (core.OperationID, bool) {
	n, err := strconv.ParseInt(c.Params("id"), 10, 64)
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
func fail(c *fiber.Ctx, err error) error {
	// A refusal that names how long to wait says so on the wire. The cache
	// carries a delay because what the caller waits for is a disk write
	// already under way, and a client guessing its own interval either hammers
	// the server or stalls longer than it needs to.
	var full *upload.CacheFullError
	if errors.As(err, &full) && full.RetryAfterSeconds > 0 {
		c.Set(fiber.HeaderRetryAfter, strconv.Itoa(full.RetryAfterSeconds))
	}
	// The classifier hides the message from the client on purpose. The access
	// log still needs it: a 500 whose cause is written nowhere leaves an
	// operator a status code and no next step.
	middleware.SetCause(c, err)
	return refuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}

// refuse writes a classified refusal.
func refuse(c *fiber.Ctx, class apierr.Classified) error {
	status, body := apierr.REST(class)
	return writeJSON(c, status, body)
}

// requestBodyReader streams the request body directly when available, falling
// back to buffered body bytes.
func requestBodyReader(c *fiber.Ctx) io.Reader {
	if stream := c.Context().RequestBodyStream(); stream != nil {
		return stream
	}
	return bytes.NewReader(c.Body())
}
