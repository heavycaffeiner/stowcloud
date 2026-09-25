//go:build linux

// Resolving a product path before handing it to a transport adapter.
package app

import (
	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
)

// resolve turns a query parameter into a permission-checked location.
//
// One function for every read route, so the parse, the permission and the
// refusal are decided in one place. A route that resolved inline would be the
// route where a permission was passed differently.
//
// An unparseable path is the same refusal as one that does not exist: the
// difference between them says whether a path is well-formed, which is a
// question about what exists.
//
// The parse check is defence in depth rather than the only guard. Measured: a
// failed parse leaves the zero path, which is the virtual root, and Resolve
// refuses the root because it names no share. Dropping the check changes no
// answer today. It stays because relying on the zero value of a failed parse
// is relying on two unrelated decisions happening to line up.
//
// The permission argument cannot be weakened by passing nothing: measured, the
// evaluator refuses an empty want, so a zero here refuses every path rather
// than admitting one. What it can be is wrong in the other direction, asking
// for less than the route needs, which is why each route names its own.
func (e *Engine) resolve(owner core.UserID, raw string, need acl.Perms) (core.Resolved, error) {
	p, err := vfs.ParseVpath(raw)
	if err != nil {
		return core.Resolved{}, core.ErrNotFound
	}
	return e.Core.Resolve(owner, p, need)
}

func (e *Engine) vpath(owner core.UserID, r core.Resolved, entry core.Entry) string {
	vp, err := e.Core.VpathFor(owner, r.Share(), entry.Path)
	if err != nil {
		return entry.Path.String()
	}
	return vp.String()
}

func decodeBody(c *gin.Context, into any) error {
	return middleware.DecodeJSON(middleware.LimitBody(c.Request.Body, route.BodyJSON), into)
}
