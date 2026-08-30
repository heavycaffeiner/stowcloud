//go:build linux

// Serving bytes, and moving them between places.
//
// The read path decides every status before it sets a body. Once the response
// is committed there is no status left to report with: the client has been
// told 200 and how many bytes to expect, so the only honest end to a failure
// is to stop and record it.
package lifecycle

import (
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// filesRead streams one file.
func (e *Engine) filesRead(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	// Download rather than Read: a drop-style grant that hands out bytes
	// without letting the holder list the tree is a real configuration.
	//
	// Redundant, measured: OpenStream requires the same bit and refuses
	// first, so weakening this to Read changes no answer. It is named here so
	// the refusal happens before a descriptor is opened, and because the
	// core's requirement is the core's to change.
	r, err := e.resolve(owner, c.Query("path"), acl.Download)
	if err != nil {
		return fail(c, err)
	}

	// Opened before the range is parsed, because the size a range is checked
	// against has to be the size of the file about to be served. Statting
	// separately would range against a size that can already be stale.
	entry, stream, err := e.Core.OpenStream(c.UserContext(), r, nil)
	if err != nil {
		return fail(c, err)
	}

	size, nerr := num.Narrow[int64](entry.Size)
	if nerr != nil {
		e.closeStream(stream, entry.Name)
		return fail(c, core.ErrNotFound)
	}

	rng, ranged, rerr := handler.ParseRange(c.Get(fiber.HeaderRange), size)
	if rerr != nil {
		e.closeStream(stream, entry.Name)
		if errors.Is(rerr, handler.ErrRangeUnsatisfiable) {
			// The one refusal that reports the real size, because that is
			// what a client needs in order to ask again correctly.
			c.Set(fiber.HeaderContentRange, "bytes */"+strconv.FormatInt(size, 10))
			return refuse(c, apierr.Classified{Class: apierr.RangeNotSatisfiable})
		}
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	if ranged {
		// Reopened for the range rather than seeking the open stream: the
		// core clamps, and seeking here would be a second answer to that.
		e.closeStream(stream, entry.Name)
		// Narrowed rather than converted: ParseRange has already checked
		// both ends against the file size, so a negative here would mean the
		// parser broke rather than the client lied, and a wrapped value
		// would read as a range near the top of the address space.
		start, serr := num.Narrow[uint64](rng.Start)
		last, lerr := num.Narrow[uint64](rng.End - 1)
		if serr != nil || lerr != nil {
			return fail(c, core.ErrNotFound)
		}
		bounds := [2]uint64{start, last}
		entry, stream, err = e.Core.OpenStream(c.UserContext(), r, &bounds)
		if err != nil {
			return fail(c, err)
		}
	}

	return e.sendStream(c, entry, stream, ranged, rng, size)
}

// sendStream commits the response and copies the bytes.
func (e *Engine) sendStream(
	c *fiber.Ctx, entry core.FidEntry, stream *core.Stream,
	ranged bool, rng handler.ByteRange, size int64,
) error {
	length, lerr := num.Narrow[int64](stream.Remaining())
	if lerr != nil {
		e.closeStream(stream, entry.Name)
		return fail(c, core.ErrNotFound)
	}

	// Every header before the first byte. The status is written with the
	// first write, so anything set afterwards lands on a response the client
	// has already started reading.
	c.Set(fiber.HeaderContentType, fiber.MIMEOctetStream)
	c.Set(fiber.HeaderAcceptRanges, "bytes")
	if entry.ETag != "" {
		c.Set(fiber.HeaderETag, etagHeader(entry))
	}
	c.Set(fiber.HeaderContentLength, strconv.FormatInt(length, 10))

	// ?download=1 asks for the file as a download rather than as something to
	// render. Without the header the browser navigates to the bytes and shows
	// them, or offers a name taken from the URL, which here is "read".
	//
	// The name is quoted and escaped by the helper, and carried in the RFC 5987
	// form as well: a header built by pasting a filename in is one a filename
	// can break out of.
	if c.Query("download") == "1" {
		c.Set(fiber.HeaderContentDisposition, handler.ContentDisposition(entry.Name))
	}

	status := fiber.StatusOK
	if ranged {
		status = fiber.StatusPartialContent
		c.Set(fiber.HeaderContentRange, rng.ContentRange(size))
	}
	c.Status(status)

	// The sized form, not the writer form. A stream writer forces chunked
	// encoding, which drops the Content-Length: measured, the bytes still
	// arrive, and a client that preallocates or shows progress has nothing to
	// work from. The size is also what makes a truncated transfer detectable
	// at the other end, since the connection ends before the promised length.
	//
	// fasthttp closes the stream once it has read it, and core.Stream is an
	// io.Closer, so the descriptor is released without a second close here.
	c.Context().SetBodyStream(&loggedStream{
		inner:  stream,
		name:   entry.Name,
		logger: e.logger,
	}, int(length))
	return nil
}

// loggedStream reports a read that failed after the response was committed.
//
// By then the status is spent: the client has been told 200 and a length, so
// the failure cannot be answered, only recorded. Without this the error is
// swallowed by the server loop and a truncated download leaves no trace.
type loggedStream struct {
	inner  *core.Stream
	name   string
	logger *slog.Logger
}

func (s *loggedStream) Read(p []byte) (int, error) {
	n, err := s.inner.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		s.logger.Warn("a download ended early", "name", s.name, "error", err)
	}
	return n, err
}

func (s *loggedStream) Close() error { return s.inner.Close() }

// etagHeader renders the validator with the weakness marker the core reports.
//
// Every token this system mints is weak, and saying so is not decoration: a
// client that reads a weak validator as strong will use it for a byte-range
// precondition, which is the one thing weakness forbids.
func etagHeader(entry core.FidEntry) string {
	if entry.ETagWeak {
		return `W/"` + entry.ETag + `"`
	}
	return `"` + entry.ETag + `"`
}

// closeStream shuts a stream and records a failure rather than dropping it.
func (e *Engine) closeStream(stream *core.Stream, name string) {
	if stream == nil {
		return
	}
	if err := stream.Close(); err != nil {
		e.logger.Warn("closing a stream", "name", name, "error", err)
	}
}

// filesWrite replaces one file's contents.
//
// The path is a query parameter because the body is the file itself. A JSON
// envelope carrying both would hold the whole upload in memory twice, and the
// resumable route is what a large transfer should be using anyway.
func (e *Engine) filesWrite(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	r, err := e.resolve(owner, c.Query("path"), acl.Write|acl.Create)
	if err != nil {
		return fail(c, err)
	}

	body := c.Body()
	// The mode comes from the share's own policy rather than a constant here:
	// a file this route creates has to be reachable on the same terms as one
	// created by any other, and a second answer would differ by which route
	// happened to make it.
	opts := vfs.DurableOpts{Mode: r.Root().Policy().ModeFile}
	entry, err := e.Core.CreateFile(c.UserContext(), r, opts, ifMatchOf(c),
		func(f *vfs.File) error {
			// WriteAt rather than a copy: vfs.File is positional, so a write
			// does not depend on a shared offset.
			_, werr := f.WriteAt(body, 0)
			return werr
		})
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.EntryOf(entry, e.vpath(owner, r, entry)))
}

// ifMatchOf reads the change token a write is conditioned on.
//
// Absent means unconditional, which is right for a new file: there is no
// version to condition on. Present, the write is refused when the file has
// moved underneath, which is the whole defence against two people editing the
// same file and the second save erasing the first.
//
// The header's quoting and weak marker are stripped here, because the token
// the store compares is the value inside them and a caller echoing back what
// it was given should not have to know that.
func ifMatchOf(c *fiber.Ctx) *core.Token {
	raw := strings.TrimSpace(c.Get(fiber.HeaderIfMatch))
	if raw == "" {
		return nil
	}
	raw = strings.TrimPrefix(raw, "W/")
	raw = strings.Trim(raw, `"`)
	if raw == "" {
		return nil
	}
	token := core.Token(raw)
	return &token
}

// transferRequest names both ends of a copy or a move.
type transferRequest struct {
	From string `json:"from"`
	To   string `json:"to"`

	// OnConflict is the policy when the destination is taken. Absent means
	// refuse: a transfer that silently replaced a file would destroy data the
	// caller never named.
	OnConflict string `json:"on_conflict"`
}

// policy reads the requested conflict handling.
//
// The false return is passed through rather than folded into the default: a
// client asking for a policy this build does not have is refused, never
// quietly given a different one, because the two differ by whether a file
// survives.
func (t transferRequest) policy() (core.OnConflict, bool) {
	if t.OnConflict == "" {
		return core.ConflictFail, true
	}
	return core.ParseOnConflict(t.OnConflict)
}

// filesMove relocates an entry.
//
// Move at the source and Create at the destination, which is what the core
// requires. Naming them here means the refusal happens before anything is
// attempted and says the right thing.
func (e *Engine) filesMove(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	req, from, to, proceed, written := e.transferEnds(c, owner, acl.Read|acl.Move)
	if !proceed {
		return written
	}
	policy, known := req.policy()
	if !known {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	result, err := e.Core.Move(c.UserContext(), from, to, core.MoveOpts{OnConflict: policy})
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.MoveOf(result))
}

// filesCopy duplicates an entry.
//
// Read and Download at the source rather than Move: nothing leaves it, and
// demanding Move would refuse a copy out of a read-only share, which is the
// ordinary reason to make one.
//
// The work is detached and the response is the job. A recursive copy can run
// for minutes, and holding the request open makes a client disconnect look
// like a cancelled copy.
func (e *Engine) filesCopy(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	req, from, to, proceed, written := e.transferEnds(c, owner, acl.Read|acl.Download)
	if !proceed {
		return written
	}
	policy, known := req.policy()
	if !known {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	start, err := e.Core.StartCopy(c.UserContext(), owner, from, to, policy)
	if err != nil {
		return fail(c, err)
	}

	// A skip finished without a job, so there is no id to poll and 202 would
	// leave a client waiting on a row that will never exist.
	if start.Skipped {
		return writeJSON(c, fiber.StatusOK, handler.CopyStartOf(start))
	}
	return writeJSON(c, fiber.StatusAccepted, handler.CopyStartOf(start))
}

// transferEnds decodes the body and resolves both ends.
//
// The bool says whether the caller may proceed and the error is the written
// response, in that order. An error alone cannot work: refuse and fail both
// write the refusal and return nil, so a caller testing the error for nil
// reads a refusal as a success and carries on with two zero Resolved values.
// That is not a wrong answer but a nil dereference, which is how it was
// found: the panic took down the connection rather than returning 403.
func (e *Engine) transferEnds(
	c *fiber.Ctx, owner core.UserID, sourceNeed acl.Perms,
) (req transferRequest, from, to core.Resolved, ok bool, written error) {
	if err := decodeBody(c, &req); err != nil {
		return req, from, to, false, refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	from, err := e.resolve(owner, req.From, sourceNeed)
	if err != nil {
		return req, core.Resolved{}, core.Resolved{}, false, fail(c, err)
	}
	to, err = e.resolve(owner, req.To, acl.Write|acl.Create)
	if err != nil {
		return req, core.Resolved{}, core.Resolved{}, false, fail(c, err)
	}
	return req, from, to, true, nil
}

// filesSize answers a subtree's recursive rollup.
func (e *Engine) filesSize(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	r, err := e.resolve(owner, c.Query("path"), acl.Read)
	if err != nil {
		return fail(c, err)
	}

	agg, err := e.Core.Aggregate(c.UserContext(), r.Share(), r.Path())
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.AggregateOf(agg))
}

// filesRecent answers what this account wrote lately.
func (e *Engine) filesRecent(c *fiber.Ctx) error {
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	if e.Journal == nil {
		// The journal is what this reads, and its absence is a degradation
		// rather than a fault. An empty list is honest: nothing is known,
		// which is not the same as claiming a failure.
		return writeJSON(c, fiber.StatusOK, []handler.RecentView{})
	}

	// A nanosecond instant rather than a day count, because a day count has to
	// be resolved against somebody's clock and the two ends of this wire are
	// frequently in different zones: the same request would mean two different
	// windows depending on which side did the arithmetic. Unparseable reads as
	// no window, which is what an absent one means.
	since, perr := strconv.ParseInt(c.Query("since"), 10, 64)
	if perr != nil || since < 0 {
		since = 0
	}
	hits, err := e.Core.Recent(c.UserContext(), owner, core.RecentQuery{
		SinceNs: since,
		Limit:   recentLimit(c.Query("limit")),
		Scope:   c.Query("path"),
	})
	if err != nil {
		return fail(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.RecentListOf(hits))
}

// recentLimit bounds the window a client may ask for.
//
// An unbounded limit is a journal scan whose cost grows with how long the
// account has been used, and the screen this feeds shows one page.
func recentLimit(raw string) int {
	const fallback, ceiling = 50, 500

	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return min(n, ceiling)
}
