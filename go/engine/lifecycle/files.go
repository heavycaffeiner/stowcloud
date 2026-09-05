//go:build linux

// The files family's read side.
//
// Every path arrives as a query parameter and is parsed by the one virtual
// path parser before it reaches anything. A handler that built a path from
// string pieces would be a second answer to the question the parser exists to
// answer once, and the two only have to disagree about one traversal.
package lifecycle

import (
	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// filesList answers one directory's entries.
func (e *Engine) filesList(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	// The virtual root is not a resolvable path: it names no share, and its
	// contents are the grants this account holds rather than a directory.
	raw := c.Query("path")
	if raw == "" || raw == "/" {
		return writeJSON(c, fiber.StatusOK, rootPage(e.Core.Roots(owner)))
	}

	r, err := e.resolve(owner, raw, acl.Read)
	if err != nil {
		return fail(c, err)
	}

	// The order and the window are the caller's, because the grid fetches the
	// rows it is about to draw rather than a fixed page. Core bounds the limit
	// and reads an unknown sort key as the default: a listing is a read, and
	// refusing one over a spelling takes the folder away instead of showing it
	// in an order nobody asked for.
	opt := core.ListOptions{
		Sort:  core.ParseSortKey(c.Query("sort")),
		Desc:  c.Query("order") == "desc",
		Limit: c.QueryInt("limit"),
	}
	page, err := e.Core.ListSorted(c.UserContext(), r, core.Cursor(c.Query("cursor")), opt)
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.PageOf(page, e.vpathOf(owner, r), e.refsOf(owner)))
}

// filesStat answers one entry.
func (e *Engine) filesStat(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	r, err := e.resolve(owner, c.Query("path"), acl.Read)
	if err != nil {
		return fail(c, err)
	}

	entry, err := e.Core.Stat(c.UserContext(), r)
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, e.entryView(owner, r, entry))
}

// vpath addresses one entry the way the caller has to ask for it: the share's
// label followed by the path inside it.
//
// Falling back to the share-relative path keeps a listing answerable when the
// label cannot be found, which happens only for a share the account cannot
// see. That row is unusable either way, and dropping the whole page over one
// entry is worse than one row whose path does not resolve.
func (e *Engine) vpath(owner core.UserID, r core.Resolved, entry core.Entry) string {
	vp, err := e.Core.VpathFor(owner, r.Share(), entry.Path)
	if err != nil {
		return entry.Path.String()
	}
	return vp.String()
}

// vpathOf binds vpath to one listing's share, for projecting a whole page.
func (e *Engine) vpathOf(owner core.UserID, r core.Resolved) func(core.Entry) string {
	return func(entry core.Entry) string { return e.vpath(owner, r, entry) }
}

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

// rootPage projects the virtual root: the shares this account can reach.
//
// A page rather than a bare list, so a client walks the root with the same
// code it walks a directory with. The root has no cursor because a grant list
// is bounded by how many shares one account holds.
func rootPage(roots []acl.RootEntry) handler.PageView {
	entries := make([]handler.EntryView, 0, len(roots))
	for _, root := range roots {
		entries = append(entries, handler.EntryView{
			Name:  root.Label,
			Path:  "/" + root.Label,
			IsDir: true,
		})
	}
	// No dir_perms: the root is a projection of grants rather than a
	// directory, so nothing may be created, renamed or deleted in it.
	return handler.PageView{Entries: entries}
}
