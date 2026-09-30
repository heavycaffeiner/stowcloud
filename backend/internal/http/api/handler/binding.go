//go:build linux

package handler

import (
	"io"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
)

// Owner reads the principal already selected by the middleware chain.
func Owner(c *gin.Context) (files.UserID, bool) {
	v, exists := c.Get(string(middleware.KeyCredential))
	principal, ok := v.(middleware.Principal)
	if !exists || !ok || principal.UserID == 0 {
		return 0, false
	}
	return files.UserID(principal.UserID), true
}

// Fail records a service error for the chain to classify and render.
func Fail(c *gin.Context, err error) { middleware.Fail(c, err) }

func NotFound(c *gin.Context) { Fail(c, files.ErrNotFound) }

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
