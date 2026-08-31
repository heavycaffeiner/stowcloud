//go:build linux

// What the deployment is using, as an operator sees it.
//
// Reported from what this server already knows rather than by walking the
// tree: a walk of a twelve-terabyte array to draw a settings screen is a
// screen nobody opens twice.
package lifecycle

import (
	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// adminStorage answers the storage accounting.
func (e *Engine) adminStorage(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	dbBytes, err := e.State.FileBytes()
	if err != nil {
		return failKnown(c, err)
	}

	shares := make([]handler.ShareUsage, 0, len(e.Core.Shares()))
	for _, sh := range e.Core.Shares() {
		usage := handler.ShareUsage{ID: sh.ID, Label: sh.Name}

		root, ok := e.Core.ShareRoot(sh.ID)
		if ok {
			// The error is checked rather than ignored, though measured it
			// does not currently fire: a share whose backing did not open has
			// no root at all, so the lookup above already refused it and this
			// call runs only against a root that is open. It stays because a
			// filesystem can stop answering after it was opened, and reporting
			// that as zero would show a full disk on a device that is fine.
			if space, serr := root.Space(vfs.RootPath()); serr == nil {
				usage.Total, usage.Free = space.Total, space.Available
				usage.Measured = true
			}
		}
		// A share whose filesystem cannot be measured is still listed, with
		// its figures marked absent rather than sent as zero. Dropping the row
		// reads as a share that does not exist, and zero reads as a full disk;
		// both are answers an operator would act on.
		shares = append(shares, usage)
	}

	return writeJSON(c, fiber.StatusOK, handler.StorageOf(dbBytes, shares))
}
