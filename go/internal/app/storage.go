//go:build linux

// What the deployment is using, as an operator sees it.
//
// Reported from what this server already knows rather than by walking the
// tree: a walk of a twelve-terabyte array to draw a settings screen is a
// screen nobody opens twice.
package app

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
)

// adminStorage answers the storage accounting.
func (e *Engine) adminStorage(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}
	dbBytes, err := e.State.FileBytes()
	if err != nil {
		failKnown(c, err)
		return
	}
	shares := make([]handler.ShareUsage, 0, len(e.Core.Shares()))
	for _, sh := range e.Core.Shares() {
		usage := handler.ShareUsage{ID: sh.ID, Label: sh.Name}
		if root, ok := e.Core.ShareRoot(sh.ID); ok {
			if space, serr := root.Space(vfs.RootPath()); serr == nil {
				usage.Total, usage.Free = space.Total, space.Available
				usage.Measured = true
			}
		}
		shares = append(shares, usage)
	}
	writeJSON(c, http.StatusOK, handler.StorageOf(dbBytes, shares))
}
