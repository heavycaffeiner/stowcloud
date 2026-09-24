//go:build linux

package app

import (
	"github.com/gin-gonic/gin"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
)

// admin authorizes app-owned routes that still depend on the product engine.
func (e *Engine) admin(c *gin.Context) (int64, bool) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return 0, false
	}
	isAdmin, err := e.Auth.IsAdmin(c.Request.Context(), int64(owner))
	if err != nil {
		failKnown(c, err)
		return 0, false
	}
	if !isAdmin {
		refuse(c, apierr.Classified{Class: apierr.Denied})
		return 0, false
	}
	return int64(owner), true
}
