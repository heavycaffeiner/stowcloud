//go:build linux

// Package humabridge preserves the native API's authentication context and
// error envelope while typed Huma operations share the Gin middleware chain.
package humabridge

import (
	"context"
	"errors"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humagin"
	"github.com/gin-gonic/gin"
	upload "github.com/heavycaffeiner/stowcloud/backend/internal/feature/uploads"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
	"strconv"
)

type ginKey struct{}

// Install makes the Gin context selected by the product middleware available
// to typed operations, without storing request state in a global variable.
func Install(api huma.API) {
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		next(huma.WithValue(ctx, ginKey{}, humagin.Unwrap(ctx)))
	})
}

func Gin(ctx context.Context) *gin.Context {
	c, ok := ctx.Value(ginKey{}).(*gin.Context)
	if !ok {
		panic("Huma operation requires the Gin transport middleware")
	}
	return c
}

type responseError struct {
	status int
	body   *apierr.Error
}

func (e responseError) Error() string                { return e.body.Error() }
func (e responseError) GetStatus() int               { return e.status }
func (e responseError) MarshalJSON() ([]byte, error) { return e.body.MarshalJSON() }

// Refusal retains the native error envelope instead of leaking Huma's default
// error format into existing clients.
func Refusal(class apierr.Classified) error {
	status, body := apierr.REST(class)
	return responseError{status: status, body: body}
}

func Failure(ctx context.Context, err error) error {
	c := Gin(ctx)
	var full *upload.CacheFullError
	if errors.As(err, &full) && full.RetryAfterSeconds > 0 {
		c.Header("Retry-After", strconv.Itoa(full.RetryAfterSeconds))
	}
	middleware.SetCause(c, err)
	return Refusal(apierr.Classify(err, apierr.VisibilityKnown))
}
