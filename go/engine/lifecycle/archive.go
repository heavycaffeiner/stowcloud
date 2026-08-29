//go:build linux

// Archives: building one from a subtree, and reading what is inside one.
//
// The two are opposite operations that happen to share a word. Building
// streams a zip out of the tree; listing parses an existing zip's own
// directory and extracts nothing, so a bomb costs the directory parse.
package lifecycle

import (
	"bufio"
	"context"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/archive"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview"
)

// archiveRequest names what to put in the zip.
type archiveRequest struct {
	// Paths are the roots to include. More than one is the ordinary case: a
	// person selecting several files and asking for them together.
	Paths []string `json:"paths"`

	// Name is the download's filename. Absent takes a default, because a
	// browser saving a file called "archive" with no extension is a file the
	// person then cannot open by double-clicking.
	Name string `json:"name"`
}

// filesArchive streams a zip of the named subtrees.
//
// Everything that can refuse happens before the first byte. Once the response
// is committed there is no status left: the client has been told 200 and is
// already saving a file, so a failure can only end the stream and be
// recorded.
func (e *Engine) filesArchive(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	var req archiveRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	if len(req.Paths) == 0 {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	if len(req.Paths) > archiveMaxRoots {
		return refuse(c, apierr.Classified{Class: apierr.LimitExceeded})
	}

	// Every path is resolved before anything is written, so a selection
	// holding one unreadable entry is refused rather than delivered as a
	// partial archive the person believes is complete.
	roots := make([]core.Resolved, 0, len(req.Paths))
	for _, p := range req.Paths {
		r, err := e.resolve(owner, p, acl.Read|acl.Download)
		if err != nil {
			return fail(c, err)
		}
		roots = append(roots, r)
	}

	name, ok := archiveFilename(req.Name)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	c.Set(fiber.HeaderContentType, "application/zip")
	c.Set(fiber.HeaderContentDisposition, contentDisposition(name))
	// No length: a zip's size is not known until it is built, and a wrong
	// one is worse than none.
	c.Status(fiber.StatusOK)

	// The context is taken now, not inside the writer. Fiber releases the
	// request once the handler returns and the stream writer runs after that,
	// so reaching through c there is a nil dereference: measured, it panicked
	// on the first archive built.
	ctx := context.WithoutCancel(c.UserContext())
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		e.writeArchive(ctx, w, roots, name)
	})
	return nil
}

// archiveMaxRoots bounds one request's selection. The body is client-supplied
// and each root becomes a filesystem walk, so an unbounded list is work a
// caller can ask for with one request.
const archiveMaxRoots = 256

// writeArchive builds the zip into a committed response.
//
// It takes a context rather than the request, because by the time it runs the
// request is gone.
func (e *Engine) writeArchive(ctx context.Context, w *bufio.Writer, roots []core.Resolved, name string) {
	z := archive.NewWriter(w)

	for _, r := range roots {
		werr := e.Core.ArchiveWalk(ctx, r, func(entry core.WalkEntry, stream *core.Stream) error {
			switch {
			case entry.IsDir:
				// A zip has no directory concept beyond a zero-length member
				// whose name ends in a slash. Without one an empty directory
				// disappears on extraction.
				return z.AddDir(entry.RelPath, time.Unix(0, entry.MTimeNs))
			case !entry.Readable:
				// An entry that exists and could not be read is skipped, not
				// fatal: one unreadable file must not lose the rest of the
				// archive the person asked for.
				e.logger.Warn("skipped an unreadable entry", "path", entry.RelPath)
				return nil
			default:
				return z.AddFile(entry.RelPath, stream, time.Unix(0, entry.MTimeNs))
			}
		})
		if werr != nil {
			e.logger.Warn("an archive ended early", "name", name, "error", werr)
			break
		}
	}

	// Closed regardless, because a zip without its central directory is not
	// a zip: the bytes already sent are unreadable without it, and a client
	// that saved them has a file nothing will open.
	if cerr := z.Close(); cerr != nil {
		e.logger.Warn("closing an archive", "name", name, "error", cerr)
	}
	if ferr := w.Flush(); ferr != nil {
		e.logger.Warn("flushing an archive", "name", name, "error", ferr)
	}
}

// archiveFilename validates the requested download name.
//
// It reaches a Content-Disposition header, so a name carrying a quote or a
// newline could add header fields. Rejected rather than sanitised: a name
// that came back altered is one the person did not choose.
func archiveFilename(requested string) (string, bool) {
	if requested == "" {
		return "archive.zip", true
	}
	if len(requested) > archiveNameMax {
		return "", false
	}
	for i := 0; i < len(requested); i++ {
		c := requested[i]
		if c < 0x20 || c == 0x7f || c == '"' || c == '\\' || c == '/' {
			return "", false
		}
	}
	if !strings.HasSuffix(strings.ToLower(requested), ".zip") {
		requested += ".zip"
	}
	return requested, true
}

// archiveNameMax bounds the filename. Well under what any filesystem accepts,
// and short enough that the header stays a header.
const archiveNameMax = 200

// contentDisposition builds the header for a validated name.
//
// Both spellings: the plain one for clients that read it and the RFC 5987 one
// for anything non-ASCII, which the plain form cannot carry. The encoded half
// comes from the tree's one escaper rather than a second copy, since two
// answers to "how is this escaped" only have to differ once.
func contentDisposition(name string) string {
	encoded := strings.TrimPrefix(dav.EncodeHref([]string{name}, false), "/")
	return `attachment; filename="` + name + `"; filename*=UTF-8''` + encoded
}

// filesArchiveList answers what is inside an existing zip.
//
// Nothing is extracted: the archive's own central directory sits at the end
// of the file and is the only part read, so listing a bomb costs the parse.
func (e *Engine) filesArchiveList(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	r, err := e.resolve(owner, c.Query("path"), acl.Read|acl.Download)
	if err != nil {
		return fail(c, err)
	}

	entry, random, err := e.Core.OpenRandom(c.UserContext(), r)
	if err != nil {
		return fail(c, err)
	}
	defer func() {
		if cerr := random.Close(); cerr != nil {
			e.logger.Warn("closing an archive", "name", entry.Name, "error", cerr)
		}
	}()

	listing, lerr := preview.ListArchive(c.UserContext(), random, random.Size)
	if lerr != nil {
		// A file the caller cannot read and a file that is not a zip answer
		// the same way. Whether a file this account may not see happens to be
		// an archive is not something the answer should disclose.
		return notFound(c)
	}
	return writeJSON(c, fiber.StatusOK, handler.ArchiveListingOf(listing))
}
