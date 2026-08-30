//go:build linux

// The surface a stranger reaches with nothing but a URL.
//
// No account is involved anywhere below. The token names the link, the link
// carries its own permission set, and that set is the entire answer to what
// the caller may do. Where a link is locked, the password is answered once and
// the proof rides in a cookie that goes nowhere else.
//
// Unversioned, unlike the rest of the API: these addresses get pasted into
// messages and typed by hand, and a version segment in one is noise to
// everybody who ever reads it aloud.
package lifecycle

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/archive"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// PublicLinkPrefix is where the public link surface is mounted.
const PublicLinkPrefix = "/s"

// errArchiveBounded stops the walk once a zip has reached its ceiling. Not a
// failure: the archive closes carrying what it packed.
var errArchiveBounded = errors.New("archive bounds reached")

// mountPublicLinks binds the five routes a link's holder reaches.
//
// Registered directly rather than through the route table, because the table
// carries the API's version prefix and these paths are the product's own.
func (e *Engine) mountPublicLinks(app *fiber.App) {
	app.Get(PublicLinkPrefix+"/:token", e.linkLanding)
	app.Post(PublicLinkPrefix+"/:token/auth", e.linkUnlock)
	app.Get(PublicLinkPrefix+"/:token/download", e.linkDownload)
	app.Get(PublicLinkPrefix+"/:token/zip", e.linkZip)
	app.Post(PublicLinkPrefix+"/:token/drop", e.linkDrop)
}

// linkFor turns a token into a link, or writes the refusal and reports false.
//
// Every route that touches content goes through here so the lock is checked in
// exactly one place. Four separate copies of the check is three chances to
// leave one out, and the one left out serves a locked link to anybody.
func (e *Engine) linkFor(c *fiber.Ctx) (core.Link, error) {
	link, _, err := e.Core.LinkPublic(c.UserContext(), c.Params("token"))
	if err != nil {
		return core.Link{}, fail(c, err)
	}
	if link.HasPassword && !e.linkUnlocked(c, link) {
		return core.Link{}, refuse(c, linkPasswordRefusal())
	}
	return link, nil
}

// linkPasswordRefusal is the single answer a locked link gives, whether the
// cookie was missing, stale, or simply wrong. One shape, so the response says
// only that a password is wanted.
func linkPasswordRefusal() apierr.Classified {
	return apierr.Classified{Class: apierr.Unprocessable, Key: "fs.link_password"}
}

// linkCookie names the cookie that remembers one link's password.
//
// Per link, and scoped to that link's own path. Two links shared with two
// different people are two separate secrets, and a cookie broad enough to
// cover both would make answering one password enough to open the other.
func linkCookie(id int64) string {
	return "sc_link_" + strconv.FormatInt(id, 10)
}

// linkUnlocked re-checks the password the visitor answered earlier.
//
// The cookie carries the password itself and is verified on every request
// rather than trusted as a ticket. That keeps revocation immediate: changing
// a link's password locks out the cookies already issued for it, with no
// server-side list of open sessions to hunt through and expire.
func (e *Engine) linkUnlocked(c *fiber.Ctx, link core.Link) bool {
	raw := c.Cookies(linkCookie(link.ID))
	if raw == "" {
		return false
	}
	plain, derr := base64.RawURLEncoding.DecodeString(raw)
	if derr != nil {
		return false
	}
	ok, cerr := e.Core.LinkCheckPassword(c.UserContext(), link, string(plain))
	return cerr == nil && ok
}

// This surface answers data and nothing else. Pasting one of these addresses
// into a browser shows JSON today, because the page that would render it is
// served by the command this engine has not replaced yet: the split between a
// document request and the page's own fetch belongs with whoever serves both,
// and putting half of it here would be a decision made in the wrong place.

// linkLanding answers GET /s/{token}: what the page draws itself from.
func (e *Engine) linkLanding(c *fiber.Ctx) error {
	link, _, err := e.Core.LinkPublic(c.UserContext(), c.Params("token"))
	if err != nil {
		return fail(c, err)
	}

	// A locked link answers with nothing but the fact that it is locked. The
	// name and the listing are behind the password too: a link whose contents
	// are readable without it is one where the password only guards the bytes.
	if link.HasPassword && !e.linkUnlocked(c, link) {
		return writeJSON(c, fiber.StatusOK, fiber.Map{"protected": true})
	}

	listing, lerr := e.Core.LinkBrowse(c.UserContext(), link, c.Query("path"))
	if lerr != nil {
		return fail(c, lerr)
	}

	out := fiber.Map{
		"protected": false,
		"id":        strconv.FormatInt(link.ID, 10),
		"name":      listing.Name,
		"is_dir":    listing.IsDir,
		"size":      listing.Size,
		"label":     link.Label,
		"note":      link.Note,
		"path":      listing.Path,
		// The page decides which controls to draw from these two, so they are
		// answered even where they are false.
		"can_download": link.Perms.Has(acl.Download),
		"drop":         link.Perms.Has(acl.Create) && !link.Perms.Has(acl.Read),
		"has_password": link.HasPassword,
		// The ceiling a drop hits, so the page can refuse an oversized file
		// before it streams one and learns at the end.
		"max_upload_bytes": limits.RequestBody,
	}

	// Withheld unless the link grants reading. A collection box is the point
	// of a drop link, and a box whose contents are visible to everyone who can
	// post into it is not one.
	if listing.IsDir && link.Perms.Has(acl.Read) {
		entries := make([]fiber.Map, 0, len(listing.Entries))
		for _, entry := range listing.Entries {
			kind := "file"
			if entry.IsDir {
				kind = "dir"
			}
			entries = append(entries, fiber.Map{
				"name": entry.Name, "kind": kind, "size": entry.Size,
			})
		}
		out["entries"] = entries
	}
	return writeJSON(c, fiber.StatusOK, out)
}

// linkUnlock answers POST /s/{token}/auth, which is how a visitor answers the
// password.
//
// A wrong password answers the same way whether the link is locked or not, so
// the endpoint does not report which links have passwords.
func (e *Engine) linkUnlock(c *fiber.Ctx) error {
	link, _, err := e.Core.LinkPublic(c.UserContext(), c.Params("token"))
	if err != nil {
		return fail(c, err)
	}

	var req struct {
		Password string `json:"password"`
	}
	if derr := decodeBody(c, &req); derr != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	ok, cerr := e.Core.LinkCheckPassword(c.UserContext(), link, req.Password)
	if cerr != nil {
		return fail(c, cerr)
	}
	if !ok {
		return refuse(c, linkPasswordRefusal())
	}

	c.Cookie(&fiber.Cookie{
		Name:  linkCookie(link.ID),
		Value: base64.RawURLEncoding.EncodeToString([]byte(req.Password)),
		// Scoped to this link, so unlocking one sends the proof nowhere else.
		Path:     PublicLinkPrefix + "/" + c.Params("token"),
		HTTPOnly: true,
		Secure:   true,
		SameSite: "Lax",
	})
	return c.SendStatus(fiber.StatusNoContent)
}

// linkDownload answers GET /s/{token}/download, serving the bytes.
//
// Reached by navigation, which is why the password is a cookie and not a
// header: a browser following a download URL sends neither a body nor a
// header anybody here chose.
func (e *Engine) linkDownload(c *fiber.Ctx) error {
	link, lerr := e.linkFor(c)
	if lerr != nil {
		// linkFor wrote the refusal; this is its result, not a second one.
		return lerr
	}
	if !link.Perms.Has(acl.Download) {
		return refuse(c, apierr.Classified{Class: apierr.Denied, Key: "fs.link_no_download"})
	}

	// The subpath, so a file inside a shared folder downloads through the same
	// address as a file the link points at directly.
	entry, stream, serr := e.Core.LinkStreamAt(c.UserContext(), link, c.Query("path"), nil)
	if serr != nil {
		return fail(c, serr)
	}
	if nerr := e.Core.NoteLinkDownload(c.UserContext(), link); nerr != nil {
		e.closeStream(stream, entry.Name)
		return fail(c, nerr)
	}

	length, lerr := num.Narrow[int64](stream.Remaining())
	if lerr != nil {
		e.closeStream(stream, entry.Name)
		return fail(c, core.ErrNotFound)
	}

	c.Set(fiber.HeaderContentType, fiber.MIMEOctetStream)
	c.Set(fiber.HeaderContentLength, strconv.FormatInt(length, 10))
	c.Set(fiber.HeaderContentDisposition, handler.ContentDisposition(entry.Name))
	c.Status(fiber.StatusOK)
	c.Context().SetBodyStream(&loggedStream{
		inner:  stream,
		name:   entry.Name,
		logger: e.logger,
	}, int(length))
	return nil
}

// linkZip answers GET /s/{token}/zip, packing a shared folder.
//
// The response commits before the walk finishes, so hitting a ceiling halfway
// cannot turn into an error status. The archive is finished off as-is instead:
// a short zip that opens beats a truncated stream that does not.
func (e *Engine) linkZip(c *fiber.Ctx) error {
	link, lerr := e.linkFor(c)
	if lerr != nil {
		// linkFor wrote the refusal; this is its result, not a second one.
		return lerr
	}
	if !link.Perms.Has(acl.Download) {
		return refuse(c, apierr.Classified{Class: apierr.Denied, Key: "fs.link_no_download"})
	}

	sub := c.Query("path")
	listing, lerr := e.Core.LinkBrowse(c.UserContext(), link, sub)
	if lerr != nil {
		return fail(c, lerr)
	}
	if !listing.IsDir {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "fs.link_not_a_folder"})
	}
	if nerr := e.Core.NoteLinkDownload(c.UserContext(), link); nerr != nil {
		return fail(c, nerr)
	}

	c.Set(fiber.HeaderContentType, "application/zip")
	c.Set(fiber.HeaderContentDisposition, handler.ContentDisposition(listing.Name+".zip"))
	// No length: a zip's size is not known until it is built, and a wrong one
	// is worse than none.
	c.Status(fiber.StatusOK)

	// The context is taken now rather than inside the writer. Fiber releases
	// the request once the handler returns and the stream writer runs after
	// that, so reaching through c there is a nil dereference.
	ctx := context.WithoutCancel(c.UserContext())
	name := listing.Name
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		e.writeLinkArchive(ctx, w, link, sub, name)
	})
	return nil
}

// writeLinkArchive builds a link's zip into a committed response.
//
// Bounded by the same ceilings the authenticated archive uses: a link is
// reachable by anyone holding the address, so an unbounded walk is work a
// stranger can ask for with one request.
func (e *Engine) writeLinkArchive(
	ctx context.Context, w *bufio.Writer, link core.Link, sub, name string,
) {
	z := archive.NewWriter(w)

	var entries int64
	var packed uint64
	werr := e.Core.LinkArchiveWalk(ctx, link, sub,
		func(entry core.WalkEntry, stream *core.Stream) error {
			if entries >= limits.ArchivePackedEntries || packed >= limits.ArchivePackedBytes {
				return errArchiveBounded
			}
			entries++
			switch {
			case entry.IsDir:
				// A zip has no directory concept beyond a zero-length member
				// whose name ends in a slash. Without one an empty directory
				// disappears on extraction.
				return z.AddDir(entry.RelPath, time.Unix(0, entry.MTimeNs))
			case !entry.Readable:
				// Skipped rather than fatal: one unreadable file must not lose
				// the rest of the archive.
				return nil
			default:
				if aerr := z.AddFile(entry.RelPath, stream, time.Unix(0, entry.MTimeNs)); aerr != nil {
					return aerr
				}
				packed += entry.Size
				return nil
			}
		})
	if werr != nil && !errors.Is(werr, errArchiveBounded) {
		e.logger.Warn("a link archive ended early", "name", name, "error", werr)
	}

	// Closed regardless, because a zip without its central directory is not a
	// zip: the bytes already sent are unreadable without it.
	if cerr := z.Close(); cerr != nil {
		e.logger.Warn("closing a link archive", "name", name, "error", cerr)
	}
	if ferr := w.Flush(); ferr != nil {
		e.logger.Warn("flushing a link archive", "name", name, "error", ferr)
	}
}

// linkDrop answers POST /s/{token}/drop, the upload half of a link.
//
// Create without Read is what makes a link a collection box. The check below
// is not defending the route from misuse; it is the thing that distinguishes
// this kind of link from the ordinary kind.
func (e *Engine) linkDrop(c *fiber.Ctx) error {
	link, lerr := e.linkFor(c)
	if lerr != nil {
		// linkFor wrote the refusal; this is its result, not a second one.
		return lerr
	}
	if !link.Perms.Has(acl.Create) {
		return refuse(c, apierr.Classified{Class: apierr.Denied, Key: "fs.link_no_upload"})
	}
	name := c.Query("name")
	if name == "" {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable, Key: "fs.link_no_name"})
	}

	entry, werr := e.Core.LinkDropFile(c.UserContext(), link, name, bytesReader(c.Body()))
	if werr != nil {
		return fail(c, werr)
	}
	return writeJSON(c, fiber.StatusCreated, fiber.Map{
		"name": entry.Name,
		// Decimal, like every other size on this API: a file past 2^53 bytes
		// is not exact as a JavaScript number.
		"size": strconv.FormatUint(entry.Size, 10),
	})
}

// bytesReader adapts a request body for the core's streaming writer.
func bytesReader(b []byte) io.Reader { return &byteSliceReader{b: b} }

type byteSliceReader struct {
	b   []byte
	off int
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.off >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.off:])
	r.off += n
	return n, nil
}
