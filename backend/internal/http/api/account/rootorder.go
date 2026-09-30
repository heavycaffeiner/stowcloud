//go:build linux

// Package account owns account-scoped HTTP handlers.
package account

import (
	"github.com/gin-gonic/gin"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/backend/internal/db/state"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
)

// RootOrderDeps supplies the durable store for root ordering.
type RootOrderDeps struct {
	State *state.DB
}

// RootOrderHandler stores the caller's preferred order for account roots.
func RootOrderHandler(d RootOrderDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		owner, ok := middleware.UserOf(c)
		if !ok {
			middleware.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
			return
		}
		var req rootOrderRequest
		if err := middleware.DecodeJSON(c.Request.Body, &req); err != nil {
			middleware.Refuse(c, apierr.Classified{Class: apierr.Malformed})
			return
		}
		if len(req.Order) > rootOrderMaxEntries {
			middleware.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}
		seen := make(map[string]struct{}, len(req.Order))
		for _, label := range req.Order {
			if len(label) > rootOrderMaxLabelBytes {
				middleware.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
				return
			}
			if _, dup := seen[label]; dup {
				middleware.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
				return
			}
			seen[label] = struct{}{}
		}
		if err := d.State.SetRootOrder(c.Request.Context(), owner, req.Order); err != nil {
			middleware.Fail(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// rootOrderMaxEntries bounds one request's list. The body is client-supplied
// and every entry becomes a stored row, so an unbounded list is a way to make
// one request grow the table without limit.
const rootOrderMaxEntries = 256

// rootOrderMaxLabelBytes bounds one label. A label this long could not have
// come from a share name or a grant's own label field, both of which are
// bounded well below this at the point they are created.
const rootOrderMaxLabelBytes = 256

// rootOrderRequest names the roots' new sidebar order, most-preferred first.
type rootOrderRequest struct {
	Order []string `json:"order"`
}
