//go:build linux

// Package account owns account-scoped HTTP handlers.
package account

import (
	"github.com/gin-gonic/gin"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

// RootOrderDeps supplies the authorization and durable operation for root ordering.
type RootOrderDeps struct {
	State  *state.DB
	Owner  func(*gin.Context) (int64, bool)
	Fail   func(*gin.Context, error)
	Refuse func(*gin.Context, apierr.Classified)
	Decode func(*gin.Context, any) error
}

// RootOrderHandler stores the caller's preferred order for account roots.
func RootOrderHandler(d RootOrderDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		owner, ok := d.Owner(c)
		if !ok {
			d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
			return
		}
		var req rootOrderRequest
		if err := d.Decode(c, &req); err != nil {
			d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
			return
		}
		if len(req.Order) > rootOrderMaxEntries {
			d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}
		seen := make(map[string]struct{}, len(req.Order))
		for _, label := range req.Order {
			if len(label) > rootOrderMaxLabelBytes {
				d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
				return
			}
			if _, dup := seen[label]; dup {
				d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
				return
			}
			seen[label] = struct{}{}
		}
		if err := d.State.SetRootOrder(c.Request.Context(), owner, req.Order); err != nil {
			d.Fail(c, err)
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
