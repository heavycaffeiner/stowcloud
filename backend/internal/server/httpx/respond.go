//go:build linux

package httpx

import (
	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
)

// Fail records err as the request's outcome and stops the chain. The chain's
// error handler renders it once the handler returns.
func Fail(c *gin.Context, err error) {
	c.Errors = append(c.Errors, &gin.Error{Err: err, Type: gin.ErrorTypePrivate})
	c.Abort()
}

// Refuse answers a classified refusal in the native envelope.
func Refuse(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	c.JSON(status, body)
}
