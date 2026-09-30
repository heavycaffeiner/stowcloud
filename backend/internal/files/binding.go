//go:build linux

package files

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
)

// Owner reads the principal already selected by the middleware chain.
func Owner(c *gin.Context) (UserID, bool) {
	v, exists := c.Get(string(middleware.KeyCredential))
	principal, ok := v.(middleware.Principal)
	if !exists || !ok || principal.UserID == 0 {
		return 0, false
	}
	return UserID(principal.UserID), true
}

// OwnerFrom reads the signed-in account from a request context, refusing a
// request that carries none.
func OwnerFrom(ctx context.Context) (UserID, error) {
	id, err := middleware.UserFrom(ctx)
	return UserID(id), err
}
