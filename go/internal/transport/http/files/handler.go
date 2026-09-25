//go:build linux

// Package files contains the HTTP protocol adapters for the authenticated file
// surface. Resolution and identity are supplied as narrow callbacks: this
// package does not receive the application engine or any app-shaped bundle.
package files

import (
	"bytes"
	"context"
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

	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	featurepreview "github.com/heavycaffeiner/stowcloud/go/internal/feature/preview"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	httpheader "github.com/heavycaffeiner/stowcloud/go/internal/platform/http/headers"
	num "github.com/heavycaffeiner/stowcloud/go/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/objstore"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/archive"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
)

// Deps are the capabilities needed by the authenticated file routes.
// Callbacks keep application policy such as identity, path parsing, claims,
// and error projection outside this transport package.
type Deps struct {
	Core           *core.Core
	Refs           func(core.UserID) func(core.Entry, string) handler.EntryRefs
	Archives       *archive.Tickets
	Gate           *ArchiveGate
	Owner          func(*gin.Context) (core.UserID, bool)
	Resolve        func(core.UserID, string, acl.Perms) (core.Resolved, error)
	OpenClaim      func(*gin.Context, handler.ClaimPurpose, core.UserID) (handler.Claim, bool)
	EntryView      func(core.UserID, core.Resolved, core.Entry) handler.EntryView
	Vpath          func(core.UserID, core.Resolved, core.Entry) string
	Fail           func(*gin.Context, error)
	Refuse         func(*gin.Context, apierr.Classified)
	NotFound       func(*gin.Context)
	Decode         func(*gin.Context, any) error
	Body           func(*gin.Context) io.Reader
	GuardLock      func(context.Context, uint32, string, int64) error
	AcquireArchive func() (release func(), ok bool)
	Now            func() time.Time
	Journal        bool
	Logger         *slog.Logger
}

// Handler implements the authenticated file HTTP routes.
type Handler struct{ d Deps }

// Handler implements the authenticated file HTTP routes.
// NewHandler constructs a file transport handler with explicit dependencies.
func NewHandler(d Deps) *Handler {
	if d.Gate == nil {
		d.Gate = NewArchiveGate()
	}
	if d.AcquireArchive == nil {
		d.AcquireArchive = func() (func(), bool) {
			if !d.Gate.TryAcquire() {
				return nil, false
			}
			return d.Gate.Release, true
		}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Body == nil {
		d.Body = requestBodyReader
	}
	return &Handler{d: d}
}

func (h *Handler) owner(c *gin.Context) (core.UserID, bool) {
	if h.d.Owner == nil {
		return 0, false
	}
	return h.d.Owner(c)
}

func (h *Handler) fail(c *gin.Context, err error) {
	if h.d.Fail != nil {
		h.d.Fail(c, err)
		return
	}
	fail(c, err)
}

func (h *Handler) refuse(c *gin.Context, class apierr.Classified) {
	if h.d.Refuse != nil {
		h.d.Refuse(c, class)
		return
	}
	refuse(c, class)
}

func (h *Handler) notFound(c *gin.Context) {
	if h.d.NotFound != nil {
		h.d.NotFound(c)
		return
	}
	h.refuse(c, apierr.Classified{Class: apierr.NotFound})
}

func (h *Handler) resolve(owner core.UserID, raw string, need acl.Perms) (core.Resolved, error) {
	if h.d.Resolve == nil {
		return core.Resolved{}, core.ErrNotFound
	}
	return h.d.Resolve(owner, raw, need)
}

func (h *Handler) decode(c *gin.Context, into any) error {
	if h.d.Decode != nil {
		return h.d.Decode(c, into)
	}
	return middleware.DecodeJSON(middleware.LimitBody(c.Request.Body, route.BodyJSON), into)
}

// List serves the virtual grant root or a permission-checked directory page.
func (h *Handler) List(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	raw := c.Query("path")
	if raw == "" || raw == "/" {
		roots := h.d.Core.Roots(owner)
		entries := make([]handler.EntryView, 0, len(roots))
		for _, root := range roots {
			entries = append(entries, handler.EntryView{Name: root.Label, Path: "/" + root.Label, IsDir: true})
		}
		writeJSON(c, http.StatusOK, handler.PageView{Entries: entries})
		return
	}
	r, err := h.resolve(owner, raw, acl.Read)
	if err != nil {
		h.fail(c, err)
		return
	}
	limit, parseErr := strconv.Atoi(c.Query("limit"))
	if parseErr != nil {
		limit = 0
	}
	page, err := h.d.Core.ListSorted(c.Request.Context(), r, core.Cursor(c.Query("cursor")), core.ListOptions{Sort: core.ParseSortKey(c.Query("sort")), Desc: c.Query("order") == "desc", Limit: limit})
	if err != nil {
		h.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.PageOf(page, func(entry core.Entry) string { return h.d.Vpath(owner, r, entry) }, h.d.Refs(owner)))
}

// Stat serves one permission-checked entry and its content references.
func (h *Handler) Stat(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	r, err := h.resolve(owner, c.Query("path"), acl.Read)
	if err != nil {
		h.fail(c, err)
		return
	}
	entry, err := h.d.Core.Stat(c.Request.Context(), r)
	if err != nil {
		h.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, h.d.EntryView(owner, r, entry))
}

// Read serves a sealed content claim with inline disposition rules.
func (h *Handler) Read(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	if h.d.OpenClaim == nil {
		h.notFound(c)
		return
	}
	claim, ok := h.d.OpenClaim(c, handler.PurposeContent, owner)
	if !ok {
		h.notFound(c)
		return
	}
	r, err := h.resolve(owner, claim.Path, acl.Download)
	if err != nil {
		h.fail(c, err)
		return
	}
	h.StreamFile(c, r, "")
}

// StreamFile serves one resolved file. It is exported for public-link transport
// adapters, which use the same range, ETag, CSP, and partial-stream semantics.
func (h *Handler) StreamFile(c *gin.Context, r core.Resolved, attachAs string) {
	entry, stream, err := h.d.Core.OpenStream(c.Request.Context(), r, nil)
	if err != nil {
		h.fail(c, err)
		return
	}
	size, nerr := num.Narrow[int64](entry.Size)
	if nerr != nil {
		h.closeStream(stream, entry.Name)
		h.fail(c, core.ErrNotFound)
		return
	}
	rng, ranged, rerr := handler.ParseRange(c.GetHeader("Range"), size)
	if rerr != nil {
		h.closeStream(stream, entry.Name)
		if errors.Is(rerr, handler.ErrRangeUnsatisfiable) {
			c.Header("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
			h.refuse(c, apierr.Classified{Class: apierr.RangeNotSatisfiable})
			return
		}
		h.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if ranged {
		h.closeStream(stream, entry.Name)
		start, serr := num.Narrow[uint64](rng.Start)
		last, lerr := num.Narrow[uint64](rng.End - 1)
		if serr != nil || lerr != nil {
			h.fail(c, core.ErrNotFound)
			return
		}
		entry, stream, err = h.d.Core.OpenStream(c.Request.Context(), r, &[2]uint64{start, last})
		if err != nil {
			h.fail(c, err)
			return
		}
	}
	h.SendStream(c, entry, stream, ranged, rng, size, attachAs)
}

// SendStream writes an already-open stream with all byte-serving headers.
// The stream is always closed and read failures after commitment are logged.
func (h *Handler) SendStream(c *gin.Context, entry core.FidEntry, stream *core.Stream, ranged bool, rng handler.ByteRange, size int64, attachAs string) {
	length, lerr := num.Narrow[int64](stream.Remaining())
	if lerr != nil {
		h.closeStream(stream, entry.Name)
		return
	}
	contentType := mimeType(entry.Name)
	c.Header("Content-Type", contentType)
	c.Header("Accept-Ranges", "bytes")
	if entry.ETag != "" {
		c.Header("ETag", ETagHeader(entry))
	}
	c.Header("Content-Length", strconv.FormatInt(length, 10))
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
	defer h.closeStream(stream, entry.Name)
	if _, err := io.CopyN(c.Writer, &loggedStream{inner: stream, name: entry.Name, logger: h.d.Logger}, length); err != nil && !errors.Is(err, io.EOF) && h.d.Logger != nil {
		h.d.Logger.Warn("copying a download ended early", "name", entry.Name, "error", err)
	}
}

// ETagHeader formats a file validator with its weak marker.
func ETagHeader(entry core.FidEntry) string {
	if entry.ETagWeak {
		return `W/"` + entry.ETag + `"`
	}
	return `"` + entry.ETag + `"`
}

func mimeType(name string) string {
	contentType := mime.TypeByExtension(filepath.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return contentType
}

type loggedStream struct {
	inner  *core.Stream
	name   string
	logger *slog.Logger
}

func (s *loggedStream) Read(p []byte) (int, error) {
	n, err := s.inner.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && s.logger != nil {
		s.logger.Warn("a download ended early", "name", s.name, "error", err)
	}
	return n, err
}
func (s *loggedStream) Close() error { return s.inner.Close() }

func (h *Handler) closeStream(stream *core.Stream, name string) {
	if stream == nil {
		return
	}
	if err := stream.Close(); err != nil && h.d.Logger != nil {
		h.d.Logger.Warn("closing a stream", "name", name, "error", err)
	}
}

// Download mints a browser-navigation ticket for one file.
func (h *Handler) Download(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := h.decode(c, &req); err != nil {
		h.refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	r, err := h.resolve(owner, req.Path, acl.Read|acl.Download)
	if err != nil {
		h.fail(c, err)
		return
	}
	st, err := r.Root().Stat(r.Path())
	if err != nil {
		h.fail(c, core.ErrNotFound)
		return
	}
	if st.Kind.IsDir() {
		h.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	token, err := archiveToken()
	if err != nil {
		h.fail(c, err)
		return
	}
	if h.d.Archives == nil || !h.d.Archives.Put(token, &archive.Ticket{Kind: archive.KindFile, Name: r.Path().Name(), Paths: []string{req.Path}, Owner: int64(owner)}) {
		h.refuse(c, apierr.Classified{Class: apierr.LimitExceeded})
		return
	}
	writeJSON(c, http.StatusOK, handler.TicketView{Token: token, Name: r.Path().Name(), URL: "/api/v1/files/download/fetch?token=" + url.QueryEscape(token)})
}

// DownloadFetch resolves and streams a previously minted file ticket.
func (h *Handler) DownloadFetch(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	if h.d.Archives == nil {
		h.refuse(c, apierr.Classified{Class: apierr.NotFound})
		return
	}
	ticket, ok := h.d.Archives.Get(c.Query("token"), int64(owner), archive.KindFile)
	if !ok || len(ticket.Paths) != 1 {
		h.refuse(c, apierr.Classified{Class: apierr.NotFound})
		return
	}
	resolved, err := h.resolve(owner, ticket.Paths[0], acl.Read|acl.Download)
	if err != nil {
		h.fail(c, err)
		return
	}
	if c.GetHeader("Range") == "" {
		if provider, ok := resolved.Root().(objstore.DirectTransferProvider); ok && provider.DirectTransfer() {
			if keyer, keyOK := resolved.Root().(interface{ ObjectKey(vfs.SafePath) string }); keyOK {
				key := keyer.ObjectKey(resolved.Path())
				if signed, signErr := provider.PresignGet(c.Request.Context(), key, 5*time.Minute); signErr == nil {
					c.Header("Content-Disposition", httpheader.Attachment(ticket.Name))
					c.Redirect(http.StatusFound, signed)
					return
				} else if !errors.Is(signErr, objstore.ErrDirectTransferUnsupported) {
					h.fail(c, signErr)
					return
				}
			}
		}
	}
	h.StreamFile(c, resolved, ticket.Name)
}

// Archive mints a ticket for a validated selection.
func (h *Handler) Archive(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req struct {
		Paths []string `json:"paths"`
		Name  string   `json:"name"`
	}
	if err := h.decode(c, &req); err != nil {
		h.refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	if len(req.Paths) == 0 {
		h.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if len(req.Paths) > archiveMaxRoots {
		h.refuse(c, apierr.Classified{Class: apierr.LimitExceeded})
		return
	}
	for _, p := range req.Paths {
		r, err := h.resolve(owner, p, acl.Read|acl.Download)
		if err != nil {
			h.fail(c, err)
			return
		}
		encrypted, err := h.d.Core.ShareEncrypted(c.Request.Context(), r.Share())
		if err != nil {
			h.fail(c, err)
			return
		}
		if encrypted {
			h.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}
	}
	name, ok := archiveFilename(req.Name)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	token, err := archiveToken()
	if err != nil {
		h.fail(c, err)
		return
	}
	if h.d.Archives == nil || !h.d.Archives.Put(token, &archive.Ticket{Kind: archive.KindArchive, Name: name, Paths: req.Paths, Owner: int64(owner)}) {
		h.refuse(c, apierr.Classified{Class: apierr.LimitExceeded})
		return
	}
	writeJSON(c, http.StatusOK, handler.TicketView{Token: token, Name: name, URL: "/api/v1/files/archive/fetch?token=" + url.QueryEscape(token)})
}

// ArchiveFetch streams a validated archive ticket.
func (h *Handler) ArchiveFetch(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	if h.d.Archives == nil {
		h.refuse(c, apierr.Classified{Class: apierr.NotFound})
		return
	}
	ticket, ok := h.d.Archives.Get(c.Query("token"), int64(owner), archive.KindArchive)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.NotFound})
		return
	}
	roots := make([]core.Resolved, 0, len(ticket.Paths))
	for _, path := range ticket.Paths {
		resolved, err := h.resolve(owner, path, acl.Read|acl.Download)
		if err != nil {
			h.fail(c, err)
			return
		}
		encrypted, err := h.d.Core.ShareEncrypted(c.Request.Context(), resolved.Share())
		if err != nil {
			h.fail(c, err)
			return
		}
		if encrypted {
			h.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}
		roots = append(roots, resolved)
	}
	release, acquired := h.d.AcquireArchive()
	if !acquired {
		h.refuse(c, apierr.Classified{Class: apierr.ResourceExhausted, Key: "archive.busy"})
		return
	}
	defer release()
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", httpheader.Attachment(ticket.Name))
	c.Status(http.StatusOK)
	c.Writer.Flush()
	if err := BuildArchive(context.WithoutCancel(c.Request.Context()), c.Writer, ticket.Name, func(ctx context.Context, visit ArchiveVisit) error {
		for _, root := range roots {
			if err := h.d.Core.ArchiveWalk(ctx, root, visit); err != nil {
				return err
			}
		}
		return nil
	}, h.d.Logger); err != nil && h.d.Logger != nil {
		h.d.Logger.Warn("an archive ended early", "name", ticket.Name, "error", err)
	}
}

// ArchiveList reads an existing archive's central directory.
func (h *Handler) ArchiveList(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	resolved, err := h.resolve(owner, c.Query("path"), acl.Read|acl.Download)
	if err != nil {
		h.fail(c, err)
		return
	}
	encrypted, err := h.d.Core.ShareEncrypted(c.Request.Context(), resolved.Share())
	if err != nil {
		h.fail(c, err)
		return
	}
	if encrypted {
		h.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	release, acquired := h.d.AcquireArchive()
	if !acquired {
		h.refuse(c, apierr.Classified{Class: apierr.ResourceExhausted, Key: "archive.busy"})
		return
	}
	defer release()
	entry, random, err := h.d.Core.OpenRandom(c.Request.Context(), resolved)
	if err != nil {
		h.fail(c, err)
		return
	}
	defer func() {
		if closeErr := random.Close(); closeErr != nil && h.d.Logger != nil {
			h.d.Logger.Warn("closing an archive", "name", entry.Name, "error", closeErr)
		}
	}()
	listing, err := featurepreview.ListArchive(c.Request.Context(), random, random.Size)
	if err != nil {
		h.notFound(c)
		return
	}
	writeJSON(c, http.StatusOK, handler.ArchiveListingOf(listing))
}

// Mkdir creates one directory.
func (h *Handler) Mkdir(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req pathRequest
	if err := h.decode(c, &req); err != nil {
		h.refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	r, err := h.resolve(owner, req.Path, acl.Create)
	if err != nil {
		h.fail(c, err)
		return
	}
	entry, err := h.d.Core.Mkdir(c.Request.Context(), r)
	if err != nil {
		h.fail(c, err)
		return
	}
	writeJSON(c, http.StatusCreated, h.d.EntryView(owner, r, entry))
}

// Delete removes one entry, respecting DAV locks and share trash policy.
func (h *Handler) Delete(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req pathRequest
	if err := h.decode(c, &req); err != nil {
		h.refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	r, err := h.resolve(owner, req.Path, acl.Delete)
	if err != nil {
		h.fail(c, err)
		return
	}
	if h.d.GuardLock != nil {
		if lerr := h.d.GuardLock(c.Request.Context(), uint32(r.Share()), r.Path().String(), int64(owner)); lerr != nil {
			h.refuse(c, apierr.Classified{Class: apierr.Locked, Key: "dav.locked"})
			return
		}
	}
	if err := h.d.Core.Delete(c.Request.Context(), r, false); err != nil {
		h.fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type pathRequest struct {
	Path string `json:"path"`
}
type renameRequest struct {
	Path string `json:"path"`
	Name string `json:"new_name"`
}

// Rename changes one entry name in place.
func (h *Handler) Rename(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	var req renameRequest
	if err := h.decode(c, &req); err != nil {
		h.refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	r, err := h.resolve(owner, req.Path, acl.Rename)
	if err != nil {
		h.fail(c, err)
		return
	}
	if h.d.GuardLock != nil {
		if lerr := h.d.GuardLock(c.Request.Context(), uint32(r.Share()), r.Path().String(), int64(owner)); lerr != nil {
			h.refuse(c, apierr.Classified{Class: apierr.Locked, Key: "dav.locked"})
			return
		}
	}
	entry, err := h.d.Core.Rename(c.Request.Context(), r, req.Name, nil)
	if err != nil {
		h.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, h.d.EntryView(owner, r, entry))
}

// Write durably replaces one file from the request body.
func (h *Handler) Write(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	r, err := h.resolve(owner, c.Query("path"), acl.Write|acl.Create)
	if err != nil {
		h.fail(c, err)
		return
	}
	if h.d.GuardLock != nil {
		if lerr := h.d.GuardLock(c.Request.Context(), uint32(r.Share()), r.Path().String(), int64(owner)); lerr != nil {
			h.refuse(c, apierr.Classified{Class: apierr.Locked, Key: "dav.locked"})
			return
		}
	}
	if declared := c.Request.ContentLength; declared > 0 {
		if qerr := h.d.Core.CheckQuota(c.Request.Context(), owner, uint64(declared)); qerr != nil {
			h.fail(c, qerr)
			return
		}
	}
	reader := h.d.Body(c)
	entry, err := h.d.Core.CreateFile(c.Request.Context(), r, vfs.DurableOpts{Mode: r.Root().Policy().ModeFile}, ifMatchOf(c), func(f *vfs.File) error {
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
		h.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, h.d.EntryView(owner, r, entry))
}

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

type transferRequest struct {
	From       string `json:"from"`
	To         string `json:"to"`
	OnConflict string `json:"on_conflict"`
}

func (t transferRequest) policy() (core.OnConflict, bool) {
	if t.OnConflict == "" {
		return core.ConflictFail, true
	}
	return core.ParseOnConflict(t.OnConflict)
}

// Move relocates an entry.
func (h *Handler) Move(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	req, from, to, proceed := h.transferEnds(c, owner, acl.Read|acl.Move)
	if !proceed {
		return
	}
	policy, known := req.policy()
	if !known {
		h.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	result, err := h.d.Core.Move(c.Request.Context(), from, to, core.MoveOpts{OnConflict: policy})
	if err != nil {
		h.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.MoveOf(result))
}

// Copy starts a detached copy operation.
func (h *Handler) Copy(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	req, from, to, proceed := h.transferEnds(c, owner, acl.Read|acl.Download)
	if !proceed {
		return
	}
	policy, known := req.policy()
	if !known {
		h.refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	start, err := h.d.Core.StartCopy(c.Request.Context(), owner, from, to, policy)
	if err != nil {
		h.fail(c, err)
		return
	}
	if start.Skipped {
		writeJSON(c, http.StatusOK, handler.CopyStartOf(start))
		return
	}
	writeJSON(c, http.StatusAccepted, handler.CopyStartOf(start))
}

func (h *Handler) transferEnds(c *gin.Context, owner core.UserID, sourceNeed acl.Perms) (req transferRequest, from, to core.Resolved, ok bool) {
	if err := h.decode(c, &req); err != nil {
		h.refuse(c, apierr.Classified{Class: apierr.Malformed})
		return req, from, to, false
	}
	from, err := h.resolve(owner, req.From, sourceNeed)
	if err != nil {
		h.fail(c, err)
		return req, core.Resolved{}, core.Resolved{}, false
	}
	to, err = h.resolveDest(owner, req.To)
	if err != nil {
		h.fail(c, err)
		return req, core.Resolved{}, core.Resolved{}, false
	}
	return req, from, to, true
}
func (h *Handler) resolveDest(owner core.UserID, raw string) (core.Resolved, error) {
	if r, err := h.resolve(owner, raw, acl.Write|acl.Create); err == nil {
		return r, nil
	}
	parent, name, ok := splitDest(raw)
	if !ok {
		return core.Resolved{}, core.ErrNotFound
	}
	pr, err := h.resolve(owner, parent, acl.Write|acl.Create)
	if err != nil {
		return core.Resolved{}, err
	}
	leaf, err := pr.Path().Join(name)
	if err != nil {
		return core.Resolved{}, core.ErrNotFound
	}
	return h.d.Core.ResolveUnder(pr, leaf, acl.Write|acl.Create)
}
func splitDest(raw string) (parent, name string, ok bool) {
	trimmed := strings.TrimSuffix(raw, "/")
	i := strings.LastIndex(trimmed, "/")
	if i < 0 || i == len(trimmed)-1 {
		return "", "", false
	}
	return trimmed[:i], trimmed[i+1:], true
}

// Size returns a recursive subtree aggregate.
func (h *Handler) Size(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	r, err := h.resolve(owner, c.Query("path"), acl.Read)
	if err != nil {
		h.fail(c, err)
		return
	}
	agg, err := h.d.Core.Aggregate(c.Request.Context(), r.Share(), r.Path())
	if err != nil {
		h.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.AggregateOf(agg))
}

// Recent returns the account's recent writes. A nil journal is an empty listing.
func (h *Handler) Recent(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		h.refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	if !h.d.Journal {
		writeJSON(c, http.StatusOK, []handler.RecentView{})
		return
	}
	since, err := strconv.ParseInt(c.Query("since"), 10, 64)
	if err != nil || since < 0 {
		since = 0
	}
	limit, err := strconv.Atoi(c.Query("limit"))
	if err != nil {
		limit = 0
	}
	hits, err := h.d.Core.Recent(c.Request.Context(), owner, core.RecentQuery{SinceNs: core.RecentSinceOf(since, h.d.Now()), Limit: core.RecentLimitOf(limit), Scope: c.Query("path")})
	if err != nil {
		h.fail(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.RecentListOf(hits))
}

func requestBodyReader(c *gin.Context) io.Reader {
	if c.Request != nil && c.Request.Body != nil {
		return c.Request.Body
	}
	return bytes.NewReader(nil)
}
func writeJSON(c *gin.Context, status int, value any) { c.JSON(status, value) }
func refuse(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	writeJSON(c, status, body)
}
func fail(c *gin.Context, err error) {
	middleware.SetCause(c, err)
	refuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}

// Method-shaped aliases make route binding explicit without app forwarding methods.
func (h *Handler) ReadHandler(c *gin.Context)          { h.Read(c) }
func (h *Handler) WriteHandler(c *gin.Context)         { h.Write(c) }
func (h *Handler) MkdirHandler(c *gin.Context)         { h.Mkdir(c) }
func (h *Handler) DeleteHandler(c *gin.Context)        { h.Delete(c) }
func (h *Handler) RenameHandler(c *gin.Context)        { h.Rename(c) }
func (h *Handler) MoveHandler(c *gin.Context)          { h.Move(c) }
func (h *Handler) CopyHandler(c *gin.Context)          { h.Copy(c) }
func (h *Handler) SizeHandler(c *gin.Context)          { h.Size(c) }
func (h *Handler) RecentHandler(c *gin.Context)        { h.Recent(c) }
func (h *Handler) ArchiveHandler(c *gin.Context)       { h.Archive(c) }
func (h *Handler) ArchiveFetchHandler(c *gin.Context)  { h.ArchiveFetch(c) }
func (h *Handler) ArchiveListHandler(c *gin.Context)   { h.ArchiveList(c) }
func (h *Handler) DownloadHandler(c *gin.Context)      { h.Download(c) }
func (h *Handler) DownloadFetchHandler(c *gin.Context) { h.DownloadFetch(c) }
