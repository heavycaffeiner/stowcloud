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
	"strconv"

	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/uploads"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
)

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

func notFound(c *gin.Context) { fail(c, core.ErrNotFound) }

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
