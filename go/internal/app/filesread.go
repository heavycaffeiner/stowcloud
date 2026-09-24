//go:build linux

// Serving bytes, and moving them between places.
//
// The read path decides every status before it sets a body. Once the response
// is committed there is no status left to report with: the client has been
// told 200 and how many bytes to expect, so the only honest end to a failure
// is to stop and record it.
package app

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	httpheader "github.com/heavycaffeiner/stowcloud/go/internal/platform/http/headers"
	num "github.com/heavycaffeiner/stowcloud/go/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/objstore"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/archive"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
)

// filesRead streams one file inline, for the surfaces that render bytes: the
// editor, the previewer, an <img> or a <video> src.
//
// It takes the row's own sealed reference rather than a path. A client that
// composes a content URL out of a path is a client that can compose it
// wrongly, and one did: an account granted a folder inside a share joined its
// own label onto a path that already carried it. The listing hands each row
// the reference for its own bytes, so there is nothing left to join.
//
// It never sets a disposition either. A download is the ticket pair below, so
// no query parameter decides whether a response is a download.
func (e *Engine) filesRead(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	claim, ok := e.openBoundClaim(c, handler.PurposeContent, owner)
	if !ok {
		fail(c, core.ErrNotFound)
		return
	}

	// Resolved again rather than trusted from the claim: a grant revoked
	// since the listing must refuse the bytes, not serve them on the strength
	// of a seal minted while it still held.
	//
	// Download rather than Read: a drop-style grant that hands out bytes
	// without letting the holder list the tree is a real configuration.
	r, err := e.resolve(owner, claim.Path, acl.Download)
	if err != nil {
		fail(c, err)
		return
	}
	e.streamFile(c, r, "")
}

// streamFile serves one resolved file, as an attachment when attachAs names
// one and inline when it is empty.
//
// Everything that can refuse happens before the first byte, which is why the
// range is parsed against the size of the file already open rather than a
// separate stat: a size that can go stale between the two is a range checked
// against a file that is no longer the one being served.
func (e *Engine) streamFile(c *gin.Context, r core.Resolved, attachAs string) {
	entry, stream, err := e.Core.OpenStream(c.Request.Context(), r, nil)
	if err != nil {
		fail(c, err)
		return
	}

	size, nerr := num.Narrow[int64](entry.Size)
	if nerr != nil {
		e.closeStream(stream, entry.Name)
		fail(c, core.ErrNotFound)
		return
	}

	rng, ranged, rerr := handler.ParseRange(c.GetHeader("Range"), size)
	if rerr != nil {
		e.closeStream(stream, entry.Name)
		if errors.Is(rerr, handler.ErrRangeUnsatisfiable) {
			c.Header("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
			refuse(c, apierr.Classified{Class: apierr.RangeNotSatisfiable})
			return
		}
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}

	if ranged {
		// Reopened for the range rather than seeking the open stream: the
		// core clamps, and seeking here would be a second answer to that.
		e.closeStream(stream, entry.Name)
		start, serr := num.Narrow[uint64](rng.Start)
		last, lerr := num.Narrow[uint64](rng.End - 1)
		if serr != nil || lerr != nil {
			fail(c, core.ErrNotFound)
			return
		}
		bounds := [2]uint64{start, last}
		entry, stream, err = e.Core.OpenStream(c.Request.Context(), r, &bounds)
		if err != nil {
			fail(c, err)
			return
		}
	}

	e.sendStream(c, entry, stream, ranged, rng, size, attachAs)
}

// filesDownload validates a path and answers where to fetch it.
//
// Two steps, for the reason the archive pair has two: the fetch has to be a
// plain navigation the browser owns, so the bytes land in its own download
// list and never pass through the tab. A POST cannot be that navigation.
//
// The path goes in the body and comes back as an opaque token, which is the
// other half of the point. A download URL built from a path is a URL a client
// composes, and a client composing one composed it wrong: an account granted
// a folder inside a share saw its own label twice and downloaded nothing. The
// server resolves the path here and the browser never sees one.
func (e *Engine) filesDownload(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	r, err := e.resolve(owner, req.Path, acl.Read|acl.Download)
	if err != nil {
		fail(c, err)
		return
	}
	st, err := r.Root().Stat(r.Path())
	if err != nil {
		fail(c, core.ErrNotFound)
		return
	}
	if st.Kind.IsDir() {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}

	token, err := archiveToken()
	if err != nil {
		fail(c, err)
		return
	}
	name := r.Path().Name()
	if !e.Archives.Put(token, &archive.Ticket{
		Kind: archive.KindFile, Name: name, Paths: []string{req.Path}, Owner: int64(owner),
	}) {
		refuse(c, apierr.Classified{Class: apierr.LimitExceeded})
		return
	}

	writeJSON(c, http.StatusOK, handler.TicketView{
		Token: token,
		Name:  name,
		URL:   "/api/v1/files/download/fetch?token=" + url.QueryEscape(token),
	})
}

// filesDownloadFetch streams the file a ticket names.
//
// Re-resolved rather than trusting the mint: a grant revoked in between must
// refuse the download rather than serve it on a check that passed a minute
// ago. Ranges work, because this is the same stream the inline read serves.
func (e *Engine) filesDownloadFetch(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	ticket, ok := e.Archives.Get(c.Query("token"), int64(owner), archive.KindFile)
	if !ok || len(ticket.Paths) != 1 {
		refuse(c, apierr.Classified{Class: apierr.NotFound})
		return
	}
	resolved, err := e.resolve(owner, ticket.Paths[0], acl.Read|acl.Download)
	if err != nil {
		fail(c, err)
		return
	}

	if c.GetHeader("Range") == "" {
		if provider, ok := resolved.Root().(objstore.DirectTransferProvider); ok && provider.DirectTransfer() {
			keyer, keyOK := resolved.Root().(interface{ ObjectKey(vfs.SafePath) string })
			if keyOK {
				key := keyer.ObjectKey(resolved.Path())
				if signed, signErr := provider.PresignGet(c.Request.Context(), key, 5*time.Minute); signErr == nil {
					c.Header("Content-Disposition", httpheader.Attachment(ticket.Name))
					c.Redirect(http.StatusFound, signed)
					return
				} else if !errors.Is(signErr, objstore.ErrDirectTransferUnsupported) {
					fail(c, signErr)
					return
				}
			}
		}
	}
	e.streamFile(c, resolved, ticket.Name)
}

// sendStream commits the response and copies the bytes.
func (e *Engine) sendStream(
	c *gin.Context, entry core.FidEntry, stream *core.Stream,
	ranged bool, rng handler.ByteRange, size int64, attachAs string,
) {
	length, lerr := num.Narrow[int64](stream.Remaining())
	if lerr != nil {
		e.closeStream(stream, entry.Name)
		return
	}

	// Every header before the first byte. The status is written with the
	// first write, so anything set afterwards lands on a response the client
	contentType := mime.TypeByExtension(filepath.Ext(entry.Name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)
	c.Header("Accept-Ranges", "bytes")
	if entry.ETag != "" {
		c.Header("ETag", etagHeader(entry))
	}
	c.Header("Content-Length", strconv.FormatInt(length, 10))

	// A name means the response is a download: the browser saves it under that
	// name rather than rendering it or inventing one from the URL, which for a
	// ticket fetch would be "fetch".
	//
	// The name is quoted and escaped by the helper, and carried in the RFC 5987
	// form as well: a header built by pasting a filename in is one a filename
	// can break out of.
	//
	// When serving inline without an attachment, active content types that can
	// execute script in a browser are forced to attachment under the file's own
	// name to prevent stored cross-site scripting on the application origin.
	// All inline responses additionally carry a sandboxing CSP.
	if attachAs == "" {
		if httpheader.IsExecutableMIME(contentType) {
			attachAs = entry.Name
			c.Header("Content-Disposition", httpheader.Attachment(attachAs))
		} else {
			c.Header("Content-Security-Policy", httpheader.SafeInlineCSP)
		}
	} else {
		c.Header("Content-Disposition", httpheader.Attachment(attachAs))
	}
	c.Header("X-Content-Type-Options", "nosniff")

	status := http.StatusOK
	if ranged {
		status = http.StatusPartialContent
		c.Header("Content-Range", rng.ContentRange(size))
	}
	c.Status(status)

	// The sized form, not the writer form. A stream writer forces chunked
	// encoding, which drops the Content-Length: measured, the bytes still
	// arrive, and a client that preallocates or shows progress has nothing to
	// work from. The size is also what makes a truncated transfer detectable
	// at the other end, since the connection ends before the promised length.
	//
	defer func() {
		if err := stream.Close(); err != nil {
			e.logger.Warn("closing a stream", "name", entry.Name, "error", err)
		}
	}()
	if _, err := io.CopyN(c.Writer, &loggedStream{inner: stream, name: entry.Name, logger: e.logger}, length); err != nil && !errors.Is(err, io.EOF) {
		e.logger.Warn("copying a download ended early", "name", entry.Name, "error", err)
	}
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
func (e *Engine) filesWrite(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	r, err := e.resolve(owner, c.Query("path"), acl.Write|acl.Create)
	if err != nil {
		fail(c, err)
		return
	}

	if lerr := e.guardDavLock(c.Request.Context(), uint32(r.Share()), r.Path().String(), int64(owner)); lerr != nil {
		refuse(c, apierr.Classified{Class: apierr.Locked, Key: "dav.locked"})
		return
	}

	// The declared body length, checked before the bytes are taken. A
	// chunked request declares none, and that write is settled by the ledger
	// after it lands rather than refused here on a size nobody stated.
	if declared := c.Request.ContentLength; declared > 0 {
		if qerr := e.Core.CheckQuota(c.Request.Context(), core.UserID(owner), uint64(declared)); qerr != nil {
			fail(c, qerr)
			return
		}
	}
	opts := vfs.DurableOpts{Mode: r.Root().Policy().ModeFile}
	reader := requestBodyReader(c)
	entry, err := e.Core.CreateFile(c.Request.Context(), r, opts, ifMatchOf(c),
		func(f *vfs.File) error {
			var off int64
			buf := make([]byte, 256<<10)
			for {
				n, rerr := reader.Read(buf)
				if n > 0 {
					if _, werr := f.WriteAt(buf[:n], off); werr != nil {
						return werr
					}
					off += int64(n)
				}
				if errors.Is(rerr, io.EOF) {
					return f.Truncate(off)
				}
				if rerr != nil {
					return rerr
				}
			}
		})
	if err != nil {
		fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, e.entryView(owner, r, entry))
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
func ifMatchOf(c *gin.Context) *core.Token {
	raw := strings.TrimSpace(c.GetHeader("If-Match"))
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
func (e *Engine) filesMove(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	req, from, to, proceed := e.transferEnds(c, owner, acl.Read|acl.Move)
	if !proceed {
		return
	}
	policy, known := req.policy()
	if !known {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}

	result, err := e.Core.Move(c.Request.Context(), from, to, core.MoveOpts{OnConflict: policy})
	if err != nil {
		fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.MoveOf(result))
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
func (e *Engine) filesCopy(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}

	req, from, to, proceed := e.transferEnds(c, owner, acl.Read|acl.Download)
	if !proceed {
		return
	}
	policy, known := req.policy()
	if !known {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}

	start, err := e.Core.StartCopy(c.Request.Context(), owner, from, to, policy)
	if err != nil {
		fail(c, err)
		return
	}
	if start.Skipped {
		writeJSON(c, http.StatusOK, handler.CopyStartOf(start))
		return
	}
	writeJSON(c, http.StatusAccepted, handler.CopyStartOf(start))
}

// transferEnds decodes the body and resolves both ends.
//
// The destination is resolved as its parent plus the name being created,
// never as a whole path. A copy or a move names where the entry is going,
// which by definition does not exist yet: resolving it directly answered
// not-found, and the existence rule rendered that as 404. Every copy to a new
// name failed that way, which is every duplicate and every transfer into a
// folder, while a destination that happened to exist worked.
//
// The bool says whether the caller may proceed. On false, the helper has
// already written the response.
func (e *Engine) transferEnds(
	c *gin.Context, owner core.UserID, sourceNeed acl.Perms,
) (req transferRequest, from, to core.Resolved, ok bool) {
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return req, from, to, false
	}
	from, err := e.resolve(owner, req.From, sourceNeed)
	if err != nil {
		fail(c, err)
		return req, core.Resolved{}, core.Resolved{}, false
	}
	to, err = e.resolveDest(owner, req.To)
	if err != nil {
		fail(c, err)
		return req, core.Resolved{}, core.Resolved{}, false
	}
	return req, from, to, true
}

// resolveDest resolves a path that is about to be created.
//
// The parent has to exist and take a write; the leaf is the name being
// minted, so it is validated as creatable rather than looked up. A caller
// naming an existing directory as the destination gets it resolved directly,
// which is what makes "copy into this folder" work alongside "copy to this
// new name".
func (e *Engine) resolveDest(owner core.UserID, raw string) (core.Resolved, error) {
	if r, err := e.resolve(owner, raw, acl.Write|acl.Create); err == nil {
		return r, nil
	}

	parent, name, ok := splitDest(raw)
	if !ok {
		return core.Resolved{}, core.ErrNotFound
	}
	pr, err := e.resolve(owner, parent, acl.Write|acl.Create)
	if err != nil {
		return core.Resolved{}, err
	}
	leaf, jerr := pr.Path().Join(name)
	if jerr != nil {
		// A name this tree will not create: refused as the malformed input it
		// is rather than reported as an absent parent.
		return core.Resolved{}, core.ErrNotFound
	}
	return e.Core.ResolveUnder(pr, leaf, acl.Write|acl.Create)
}

// splitDest cuts a destination into the parent to resolve and the name to
// create. A path with no separator names something directly under a share
// root, whose parent is that root.
func splitDest(raw string) (parent, name string, ok bool) {
	trimmed := strings.TrimSuffix(raw, "/")
	i := strings.LastIndex(trimmed, "/")
	if i < 0 || i == len(trimmed)-1 {
		return "", "", false
	}
	return trimmed[:i], trimmed[i+1:], true
}

// filesSize answers a subtree's recursive rollup.
func (e *Engine) filesSize(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	r, err := e.resolve(owner, c.Query("path"), acl.Read)
	if err != nil {
		fail(c, err)
	}

	agg, err := e.Core.Aggregate(c.Request.Context(), r.Share(), r.Path())
	if err != nil {
		fail(c, err)
	}
	writeJSON(c, http.StatusOK, handler.AggregateOf(agg))
}

// filesRecent answers what this account wrote lately.
func (e *Engine) filesRecent(c *gin.Context) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}
	if e.Journal == nil {
		// The journal is what this reads, and its absence is a degradation
		// rather than a fault. An empty list is honest: nothing is known,
		// which is not the same as claiming a failure.
		writeJSON(c, http.StatusOK, []handler.RecentView{})
	}

	// A nanosecond instant rather than a day count, because a day count has to
	// be resolved against somebody's clock and the two ends of this wire are
	// frequently in different zones: the same request would mean two different
	// windows depending on which side did the arithmetic. Unparseable reads as
	// no window, which the shared policy resolves to the default one.
	since, perr := strconv.ParseInt(c.Query("since"), 10, 64)
	if perr != nil || since < 0 {
		since = 0
	}
	limit, lerr := strconv.Atoi(c.Query("limit"))
	if lerr != nil {
		limit = 0
	}
	hits, err := e.Core.Recent(c.Request.Context(), owner, core.RecentQuery{
		SinceNs: core.RecentSinceOf(since, e.clock.Now()),
		Limit:   core.RecentLimitOf(limit),
		Scope:   c.Query("path"),
	})
	if err != nil {
		fail(c, err)
	}
	writeJSON(c, http.StatusOK, handler.RecentListOf(hits))
}
