//go:build linux

// Shared credential confirmation for app-owned settings routes.
package app

import (
	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	secret "github.com/heavycaffeiner/stowcloud/go/internal/platform/security/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
)

func (e *Engine) reconfirm(c *gin.Context, owner int64, password string) bool {
	if password == "" {
		refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return false
	}
	ok, err := e.Auth.VerifyAccountPassword(c.Request.Context(), owner, secret.New([]byte(password)))
	if err != nil {
		failKnown(c, err)
		return false
	}
	if !ok {
		refuse(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return false
	}
	return true
}
