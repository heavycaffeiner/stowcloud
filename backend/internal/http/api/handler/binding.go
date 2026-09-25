//go:build linux

package handler

import (
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/uploads"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
)

// Owner reads the principal already selected by the middleware chain.
func Owner(c *gin.Context) (core.UserID, bool) {
	v, exists := c.Get(string(middleware.KeyCredential))
	principal, ok := v.(middleware.Principal)
	if !exists || !ok || principal.UserID == 0 {
		return 0, false
	}
	return core.UserID(principal.UserID), true
}

// Fail sends a classified service error and preserves retry advice.
func Fail(c *gin.Context, err error) {
	var full *upload.CacheFullError
	if errors.As(err, &full) && full.RetryAfterSeconds > 0 {
		c.Header("Retry-After", strconv.Itoa(full.RetryAfterSeconds))
	}
	middleware.SetCause(c, err)
	Refuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}

func NotFound(c *gin.Context) { Fail(c, core.ErrNotFound) }

func Refuse(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	c.JSON(status, body)
}

func Body(c *gin.Context) io.Reader {
	if c.Request != nil && c.Request.Body != nil {
		return c.Request.Body
	}
	return strings.NewReader("")
}
