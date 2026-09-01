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
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/archive"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
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

// filesArchive prepares a zip of the named subtrees and answers where to get
// it.
//
// Two steps rather than one streamed response. A stream has no length until
// its last entry is written, so it carries no Content-Length and no
// Accept-Ranges: the browser cannot show progress and a connection lost at
// 90% starts again from zero. Building it first gives both, at the cost of
// holding the bytes.
//
// Held in memory, never on disk: a temporary zip per download fills the data
// volume, and the volume filling takes the database with it. An archive over
// the memory bound is streamed instead, which is the old behaviour and the
// honest answer for something too big to hold.
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

	// Built before the response is committed, so a build that fails is still
	// a status the client can act on rather than a truncated download.
	held, token, err := e.holdArchive(c, owner, roots, name)
	if err == nil {
		return writeJSON(c, fiber.StatusOK, handler.ArchiveTicketView{
			Token: token,
			Name:  name,
			Size:  int64(len(held.Bytes)),
			URL:   server.Base + "/files/archive/fetch?ticket=" + url.QueryEscape(token),
		})
	}
	if !errors.Is(err, archive.ErrTooLarge) && !errors.Is(err, archive.ErrNoRoom) {
		return fail(c, err)
	}

	// Too big to hold, or no room to hold it. Streamed, which cannot be
	// resumed but delivers the archive the person asked for.
	return e.streamArchive(c, roots, name)
}

// holdArchive builds one archive into memory and stores it under a fresh
// ticket.
//
// Budget is reserved before the build, not after: with several people
// downloading at once, checking afterwards means every build allocates its
// bytes and only then discovers there was room for one of them, which is the
// moment the bound exists to prevent.
//
// The bound is also enforced as bytes arrive, so an archive that will not fit
// stops at the ceiling rather than being fully allocated and then refused.
func (e *Engine) holdArchive(c *fiber.Ctx, owner core.UserID, roots []core.Resolved, name string) (*archive.Held, string, error) {
	res, err := e.Archives.Reserve(int64(owner))
	if err != nil {
		return nil, "", err
	}
	// Returns the claim on every path that does not publish. After a
	// successful Put it is already spent and this costs nothing.
	defer res.Release()

	buf := archive.NewBuffer(res.Bound())
	if berr := e.buildArchive(c.UserContext(), buf, roots, name); berr != nil {
		return nil, "", berr
	}

	token, terr := archiveToken()
	if terr != nil {
		return nil, "", terr
	}
	held := &archive.Held{Name: name, Bytes: buf.Bytes(), Owner: int64(owner)}
	if perr := e.Archives.Put(token, held, res); perr != nil {
		return nil, "", perr
	}
	return held, token, nil
}

// streamArchive is the fallback for an archive too large to hold.
//
// Everything that can refuse has already happened. Once the response is
// committed there is no status left: the client has been told 200 and is
// already saving a file, so a failure can only end the stream and be
// recorded.
func (e *Engine) streamArchive(c *fiber.Ctx, roots []core.Resolved, name string) error {
	c.Set(fiber.HeaderContentType, "application/zip")
	c.Set(fiber.HeaderContentDisposition, contentDisposition(name))
	// No length: this archive's size is not known until it is built, and a
	// wrong one is worse than none.
	c.Status(fiber.StatusOK)

	// The context is taken now, not inside the writer. Fiber releases the
	// request once the handler returns and the stream writer runs after that,
	// so reaching through c there is a nil dereference: measured, it panicked
	// on the first archive built.
	ctx := context.WithoutCancel(c.UserContext())
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		if err := e.buildArchive(ctx, w, roots, name); err != nil {
			e.logger.Warn("an archive ended early", "name", name, "error", err)
		}
		if ferr := w.Flush(); ferr != nil {
			e.logger.Warn("flushing an archive", "name", name, "error", ferr)
		}
	})
	return nil
}

// archiveToken mints a fetch ticket.
//
// From crypto/rand because the ticket is a capability: a guessable one would
// let somebody fetch an archive built for another account.
func archiveToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// archiveMaxRoots bounds one request's selection. The body is client-supplied
// and each root becomes a filesystem walk, so an unbounded list is work a
// caller can ask for with one request.
const archiveMaxRoots = 256

// buildArchive writes the zip for one selection.
//
// It reports failure rather than only logging it. A stream cannot act on the
// error, its status having been sent already, but a held archive must not be
// published half-built: that would hand somebody a Content-Length and a
// truncated file, which is worse than a download that failed outright.
//
// It takes a context rather than the request, because for a stream the
// request is gone by the time this runs.
func (e *Engine) buildArchive(ctx context.Context, w io.Writer, roots []core.Resolved, name string) error {
	z := archive.NewWriter(w)

	var failure error
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
			failure = werr
			break
		}
	}

	// Closed regardless, because a zip without its central directory is not
	// a zip: the bytes already written are unreadable without it, and a
	// client that saved them has a file nothing will open.
	cerr := z.Close()
	if failure != nil {
		return failure
	}
	if cerr != nil {
		return fmt.Errorf("closing the archive %q: %w", name, cerr)
	}
	return nil
}

// filesArchiveFetch delivers a prepared archive.
//
// This is the half that makes a folder download behave like a file download:
// the length is known, so the browser shows progress, and ranges are
// answered, so a lost connection resumes where it stopped rather than
// starting again.
func (e *Engine) filesArchiveFetch(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	held, ok := e.Archives.Get(c.Query("ticket"), int64(owner))
	if !ok {
		// Expired, already collected, or never existed. All the same answer:
		// distinguishing them would confirm that a guessed ticket names a
		// real archive.
		return notFound(c)
	}

	c.Set(fiber.HeaderContentType, "application/zip")
	c.Set(fiber.HeaderContentDisposition, contentDisposition(held.Name))
	// Announced before the range is read: a client learns ranges are
	// available from the first response, which is the one it may have to
	// resume.
	c.Set(fiber.HeaderAcceptRanges, "bytes")

	total := int64(len(held.Bytes))
	start, end, status := archiveRange(c.Get(fiber.HeaderRange), total)
	switch status {
	case fiber.StatusRequestedRangeNotSatisfiable:
		c.Set(fiber.HeaderContentRange, fmt.Sprintf("bytes */%d", total))
		return c.SendStatus(fiber.StatusRequestedRangeNotSatisfiable)
	case fiber.StatusPartialContent:
		c.Set(fiber.HeaderContentRange, fmt.Sprintf("bytes %d-%d/%d", start, end, total))
	}
	c.Status(status)

	// Not dropped here. A resume is a second request for an archive whose
	// first delivery did not finish, and this handler cannot tell a completed
	// transfer from one the client abandoned partway: the write is buffered
	// and a disconnect looks the same. Releasing on the first response made
	// every resume answer not-found, which is the case the hold exists for.
	//
	// The TTL reclaims instead. It is short, and the bytes are bounded per
	// archive, per account and in total, so the cost of holding them for a
	// few minutes is one the store already accounts for.
	return c.Send(held.Bytes[start : end+1])
}

// archiveRange reads one byte range against a known length.
//
// Only the single-range forms a browser resume actually sends. A multipart
// range would need a multipart body, which nothing here asks for, so it is
// answered whole rather than half-implemented.
func archiveRange(header string, total int64) (start, end int64, status int) {
	if header == "" || total == 0 {
		return 0, total - 1, fiber.StatusOK
	}
	spec, found := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, total - 1, fiber.StatusOK
	}

	first, last, ok := strings.Cut(spec, "-")
	if !ok {
		return 0, total - 1, fiber.StatusOK
	}
	if first == "" {
		// A suffix range: the last n bytes.
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, fiber.StatusRequestedRangeNotSatisfiable
		}
		if n > total {
			n = total
		}
		return total - n, total - 1, fiber.StatusPartialContent
	}

	from, err := strconv.ParseInt(first, 10, 64)
	if err != nil || from < 0 || from >= total {
		return 0, 0, fiber.StatusRequestedRangeNotSatisfiable
	}
	to := total - 1
	if last != "" {
		parsed, perr := strconv.ParseInt(last, 10, 64)
		if perr != nil || parsed < from {
			return 0, 0, fiber.StatusRequestedRangeNotSatisfiable
		}
		if parsed < to {
			to = parsed
		}
	}
	return from, to, fiber.StatusPartialContent
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
