//go:build linux

// The sidebar order an account keeps over its own roots.
package lifecycle

import (
	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
)

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

// accountRootsOrder stores the caller's own preferred order for the roots
// the sidebar lists.
//
// A label named here that the account does not currently hold a root under
// is accepted rather than refused: the account may be reordering before a
// grant lands or after one was revoked, and the order is applied by matching
// labels against whatever the listing actually holds, so a label with
// nothing behind it is silently inert instead of a reason to fail the whole
// request.
func (e *Engine) accountRootsOrder(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req rootOrderRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if len(req.Order) > rootOrderMaxEntries {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	seen := make(map[string]struct{}, len(req.Order))
	for _, label := range req.Order {
		if len(label) > rootOrderMaxLabelBytes {
			return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		}
		if _, dup := seen[label]; dup {
			return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		}
		seen[label] = struct{}{}
	}

	if err := e.State.SetRootOrder(c.UserContext(), int64(owner), req.Order); err != nil {
		return fail(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}
