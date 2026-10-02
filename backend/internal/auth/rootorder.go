//go:build linux

package auth

import (
	"context"

	"github.com/heavycaffeiner/stowcloud/backend/internal/db/state"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
)

// RootOrderHandler stores the caller's preferred order for account roots.
type RootOrderHandler struct {
	State *state.DB
}

type rootOrderInput struct{ Body rootOrderRequest }

// Set replaces the caller's root order.
func (h *RootOrderHandler) Set(ctx context.Context, in *rootOrderInput) (*noContent, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	order := in.Body.Order
	if len(order) > rootOrderMaxEntries {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	seen := make(map[string]struct{}, len(order))
	for _, label := range order {
		if len(label) > rootOrderMaxLabelBytes {
			return nil, apierr.AsClassified(apierr.Unprocessable, "")
		}
		if _, dup := seen[label]; dup {
			return nil, apierr.AsClassified(apierr.Unprocessable, "")
		}
		seen[label] = struct{}{}
	}
	if err := h.State.SetRootOrder(ctx, owner, order); err != nil {
		return nil, err
	}
	return &noContent{}, nil
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
