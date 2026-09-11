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
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/archive"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/httpheader"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
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

// filesArchive validates a selection and answers where to fetch it.
//
// Two steps, and neither holds an archive. The POST cannot be the download:
// its body is the only place bytes could go, and reading a POST body means
// collecting the whole archive in the tab before any of it reaches the disk,
// which is what a folder download has to avoid. The GET that follows is a
// navigation the browser owns, so the bytes land as they arrive.
//
// Everything that can refuse happens here, before any token exists: an
// unreadable path, too many roots, a name that is not one. The fetch
// re-resolves anyway, so a grant revoked between the two requests is caught
// there too.
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

	// Every path is resolved before a token is minted, so a selection holding
	// one unreadable entry is refused now rather than delivered as a partial
	// archive the person believes is complete.
	for _, p := range req.Paths {
		r, err := e.resolve(owner, p, acl.Read|acl.Download)
		if err != nil {
			return fail(c, err)
		}
		// A server-built zip has to read every file's bytes to pack them.
		// An encrypted share holds only ciphertext this server has no key
		// for, so refuse before minting a ticket for a download that could
		// never be assembled.
		if enc, eerr := e.Core.ShareEncrypted(c.UserContext(), r.Share()); eerr != nil {
			return fail(c, eerr)
		} else if enc {
			return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		}
	}

	name, ok := archiveFilename(req.Name)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	token, terr := archiveToken()
	if terr != nil {
		return fail(c, terr)
	}
	if !e.Archives.Put(token, &archive.Ticket{
		Kind: archive.KindArchive, Name: name, Paths: req.Paths, Owner: int64(owner),
	}) {
		return refuse(c, apierr.Classified{Class: apierr.LimitExceeded})
	}

	return writeJSON(c, fiber.StatusOK, handler.TicketView{
		Token: token,
		Name:  name,
		URL:   "/api/v1/files/archive/fetch?token=" + url.QueryEscape(token),
	})
}

// filesArchiveFetch streams the archive a ticket names.
//
// The walk runs into the response as it goes, so a selection of any size costs
// the server nothing to serve. The price is a download with no declared length
// and no resume, which is the trade for being able to download a folder at
// all.
//
// Everything that can refuse happens before the first byte. Once the response
// is committed there is no status left: the client has been told 200 and is
// already saving a file, so a failure can only end the stream and be recorded.
func (e *Engine) filesArchiveFetch(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	t, ok := e.Archives.Get(c.Query("token"), int64(owner), archive.KindArchive)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.NotFound})
	}

	// Re-resolved rather than resolved once at mint: a grant revoked in
	// between must refuse the download, not serve it on the strength of a
	// check that passed a minute ago.
	roots := make([]core.Resolved, 0, len(t.Paths))
	for _, p := range t.Paths {
		r, err := e.resolve(owner, p, acl.Read|acl.Download)
		if err != nil {
			return fail(c, err)
		}
		// Re-checked here too: encryption can be turned on for a share
		// between the mint and the fetch, and the walk below is the point
		// that actually reads the bytes it would have to pack.
		if enc, eerr := e.Core.ShareEncrypted(c.UserContext(), r.Share()); eerr != nil {
			return fail(c, eerr)
		} else if enc {
			return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		}
		roots = append(roots, r)
	}

	return e.streamArchive(c, roots, t.Name)
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

// streamArchive writes the zip into a committed response.
//
// It takes the context rather than the request, because fiber releases the
// request once the handler returns and the stream writer runs after that:
// reaching through c there is a nil dereference, measured on the first
// archive built.
func (e *Engine) streamArchive(c *fiber.Ctx, roots []core.Resolved, name string) error {
	release, ok := e.tryAcquireArchive()
	if !ok {
		return refuse(c, archiveBusy())
	}

	c.Set(fiber.HeaderContentType, "application/zip")
	c.Set(fiber.HeaderContentDisposition, httpheader.Attachment(name))
	// No length: a zip's size is not known until it is built, and a wrong one
	// is worse than none.
	c.Status(fiber.StatusOK)

	ctx := context.WithoutCancel(c.UserContext())
	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer release()
		if err := e.buildArchive(ctx, w, roots, name); err != nil {
			e.logger.Warn("an archive ended early", "name", name, "error", err)
		}
		if ferr := w.Flush(); ferr != nil {
			e.logger.Warn("flushing an archive", "name", name, "error", ferr)
		}
	})
	return nil
}

// archiveMaxRoots bounds one request's selection. The body is client-supplied
// and each root becomes a filesystem walk, so an unbounded list is work a
// caller can ask for with one request.
const archiveMaxRoots = 256

// archiveConcurrencyGate is a fail-fast bound on archive work. It tracks
// active streams rather than handing out a channel, so a settings change takes
// effect on the next request even while existing streams finish.
type archiveConcurrencyGate struct {
	mu     sync.Mutex
	active int
	limit  int
}

func (g *archiveConcurrencyGate) SetLimit(limit int) {
	if limit <= 0 {
		limit = limits.ConcurrentArchives
	}
	g.mu.Lock()
	g.limit = limit
	g.mu.Unlock()
}

func (g *archiveConcurrencyGate) TryAcquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	limit := g.limit
	if limit <= 0 {
		limit = limits.ConcurrentArchives
	}
	if g.active >= limit {
		return false
	}
	g.active++
	return true
}

func (g *archiveConcurrencyGate) Release() {
	g.mu.Lock()
	if g.active > 0 {
		g.active--
	}
	g.mu.Unlock()
}

// tryAcquireArchive reserves one archive slot until the returned release
// function runs. The value gate's zero limit selects its compiled-in default,
// so manually assembled engines remain bounded too.
func (e *Engine) tryAcquireArchive() (release func(), ok bool) {
	if !e.archiveGate.TryAcquire() {
		return nil, false
	}
	return e.archiveGate.Release, true
}

func archiveBusy() apierr.Classified {
	return apierr.Classified{Class: apierr.ResourceExhausted, Key: "archive.busy"}
}

// archiveIncompleteName is a visible member name, not a log-only signal:
// streaming has already committed the response by the time a walk can hit a
// bound, so a saved archive needs a durable indication that it is partial.
const archiveIncompleteName = "__stowcloud_incomplete__.txt"

const archiveIncompleteBody = "This archive is incomplete. One or more requested entries were omitted because an archive bound was reached or an entry changed while it was being read.\n"
const (
	archiveContentEntries = limits.ArchivePackedEntries
	archiveContentBytes   = limits.ArchivePackedBytes
)

// archiveBuilder applies one set of limits to both authenticated and public
// archive streams.
type archiveBuilder struct {
	z          *archive.Writer
	entries    int64
	packed     uint64
	incomplete bool
	names      map[string]struct{}
}

// archiveEntryReader keeps a member within its admitted size while it streams.
// It turns a short or overlong source into EOF and records the mismatch so the
// finished archive carries the incomplete marker instead of silently claiming
// a complete member.
type archiveEntryReader struct {
	source     io.Reader
	remaining  uint64
	read       uint64
	incomplete bool
}

func (r *archiveEntryReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		var extra [1]byte
		n, err := r.source.Read(extra[:])
		if n > 0 || (err != nil && !errors.Is(err, io.EOF)) {
			r.incomplete = true
		}
		return 0, io.EOF
	}
	if uint64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.source.Read(p)
	if n > 0 {
		r.remaining -= uint64(n)
		r.read += uint64(n)
	}
	if err != nil {
		if !errors.Is(err, io.EOF) || r.remaining > 0 {
			r.incomplete = true
		}
		return n, io.EOF
	}
	return n, nil
}

func (b *archiveBuilder) add(entry core.WalkEntry, stream *core.Stream) error {
	if b.entries >= archiveContentEntries {
		b.incomplete = true
		return errArchiveBounded
	}
	b.entries++

	if !entry.Readable {
		b.incomplete = true
		return nil
	}
	if entry.IsDir {
		if err := b.z.AddDir(entry.RelPath, time.Unix(0, entry.MTimeNs)); err != nil {
			return err
		}
		b.rememberName(entry.RelPath)
		return nil
	}
	if b.packed >= archiveContentBytes ||
		entry.Size > archiveContentBytes-b.packed {
		b.incomplete = true
		return errArchiveBounded
	}
	if stream == nil {
		b.incomplete = true
		return nil
	}

	reader := &archiveEntryReader{source: stream, remaining: entry.Size}
	if err := b.z.AddFile(entry.RelPath, reader, time.Unix(0, entry.MTimeNs)); err != nil {
		return err
	}
	b.rememberName(entry.RelPath)
	b.packed += reader.read
	if reader.incomplete {
		b.incomplete = true
	}
	return nil
}

func (b *archiveBuilder) rememberName(name string) {
	if b.names == nil {
		b.names = make(map[string]struct{})
	}
	b.names[name] = struct{}{}
}

func (b *archiveBuilder) addMarker() error {
	if !b.incomplete {
		return nil
	}
	name := archiveIncompleteName
	for suffix := 2; ; suffix++ {
		if _, exists := b.names[name]; !exists {
			break
		}
		name = fmt.Sprintf("__stowcloud_incomplete__%d.txt", suffix)
	}
	return b.z.AddBytes(name, []byte(archiveIncompleteBody), time.Unix(0, 0))
}

// buildArchive writes the zip for one selection.
//
// The response is committed before the walk finishes. If a content ceiling or
// a changing source stops it, the archive is still closed but carries a
// visible marker, and the sentinel is returned for logging.
//
// It takes a context rather than the request, because for a stream the
// request is gone by the time this runs.
func (e *Engine) buildArchive(ctx context.Context, w io.Writer, roots []core.Resolved, name string) error {
	z := archive.NewWriter(w)
	builder := archiveBuilder{z: z}

	var walkErr error
	for _, r := range roots {
		werr := e.Core.ArchiveWalk(ctx, r, func(entry core.WalkEntry, stream *core.Stream) error {
			if !entry.IsDir && !entry.Readable {
				// An entry that exists and could not be read is skipped, not
				// fatal: one unreadable file must not lose the rest of the
				// archive the person asked for.
				e.logger.Warn("skipped an unreadable entry", "path", entry.RelPath)
			}
			return builder.add(entry, stream)
		})
		if werr != nil {
			walkErr = werr
			builder.incomplete = true
			break
		}
	}
	if merr := builder.addMarker(); merr != nil && walkErr == nil {
		walkErr = merr
	}

	// Closed regardless, because a zip without its central directory is not a
	// zip: the bytes already written are unreadable without it.
	cerr := z.Close()
	if walkErr != nil {
		return walkErr
	}
	if cerr != nil {
		return fmt.Errorf("closing the archive %q: %w", name, cerr)
	}
	return nil
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

	// An encrypted share's central directory is ciphertext too: parsing it
	// as a zip would only fail differently than the "not an archive" case
	// below, and would do it after opening the file for random access.
	// Refused first because there is nothing here worth opening at all.
	if enc, eerr := e.Core.ShareEncrypted(c.UserContext(), r.Share()); eerr != nil {
		return fail(c, eerr)
	} else if enc {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}
	release, ok := e.tryAcquireArchive()
	if !ok {
		return refuse(c, archiveBusy())
	}
	defer release()

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
