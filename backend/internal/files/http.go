//go:build linux

// Package files contains the HTTP protocol adapters for the authenticated file
// surface. Resolution and claims are supplied as narrow callbacks: this
// package does not receive the application engine or any app-shaped bundle.
package files

import (
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

	"github.com/heavycaffeiner/stowcloud/backend/internal/fs/objstore"
	"github.com/heavycaffeiner/stowcloud/backend/internal/fs/vfs"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/httpx"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

// Deps are the capabilities needed by the authenticated file routes.
// Callbacks keep path parsing and claims outside this transport package.
type Deps struct {
	Core           *Core
	Refs           func(UserID) func(Entry, string) EntryRefs
	Archives       *Tickets
	Gate           *ArchiveGate
	Resolve        func(UserID, string, acl.Perms) (Resolved, error)
	OpenClaim      func(*gin.Context, ClaimPurpose, UserID) (Claim, bool)
	EntryView      func(UserID, Resolved, Entry) EntryView
	Vpath          func(UserID, Resolved, Entry) string
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
	return &Handler{d: d}
}

func (h *Handler) resolve(owner UserID, raw string, need acl.Perms) (Resolved, error) {
	if h.d.Resolve == nil {
		return Resolved{}, ErrNotFound
	}
	return h.d.Resolve(owner, raw, need)
}

type listPageInput struct {
	Path   string `query:"path"`
	Cursor string `query:"cursor"`
	Sort   string `query:"sort"`
	Order  string `query:"order"`
	Limit  string `query:"limit"`
}
type pathQueryInput struct {
	Path string `query:"path"`
}
type pageOutput struct{ Body PageView }
type entryOutput struct {
	Status int
	Body   EntryView
}

// List serves the virtual grant root or a permission-checked directory page.
func (h *Handler) List(ctx context.Context, in *listPageInput) (*pageOutput, error) {
	owner, err := OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	if in.Path == "" || in.Path == "/" {
		roots := h.d.Core.Roots(owner)
		entries := make([]EntryView, 0, len(roots))
		for _, root := range roots {
			entries = append(entries, EntryView{Name: root.Label, Path: "/" + root.Label, IsDir: true})
		}
		return &pageOutput{Body: PageView{Entries: entries}}, nil
	}
	r, err := h.resolve(owner, in.Path, acl.Read)
	if err != nil {
		return nil, err
	}
	limit, perr := strconv.Atoi(in.Limit)
	if perr != nil {
		limit = 0
	}
	page, err := h.d.Core.ListSorted(ctx, r, Cursor(in.Cursor), ListOptions{Sort: ParseSortKey(in.Sort), Desc: in.Order == "desc", Limit: limit})
	if err != nil {
		return nil, err
	}
	return &pageOutput{Body: PageOf(page, func(entry Entry) string { return h.d.Vpath(owner, r, entry) }, h.d.Refs(owner))}, nil
}

// Stat serves one permission-checked entry and its content references.
func (h *Handler) Stat(ctx context.Context, in *pathQueryInput) (*entryOutput, error) {
	owner, err := OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.resolve(owner, in.Path, acl.Read)
	if err != nil {
		return nil, err
	}
	entry, err := h.d.Core.Stat(ctx, r)
	if err != nil {
		return nil, err
	}
	return &entryOutput{Status: http.StatusOK, Body: h.d.EntryView(owner, r, entry)}, nil
}

// Read serves a sealed content claim with inline disposition rules.
func (h *Handler) Read(c *gin.Context) {
	owner, ok := Owner(c)
	if !ok {
		httpx.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	if h.d.OpenClaim == nil {
		httpx.Fail(c, ErrNotFound)
		return
	}
	claim, ok := h.d.OpenClaim(c, PurposeContent, owner)
	if !ok {
		httpx.Fail(c, ErrNotFound)
		return
	}
	r, err := h.resolve(owner, claim.Path, acl.Download)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	h.StreamFile(c, r, "")
}

// StreamFile serves one resolved file. It is exported for public-link transport
// adapters, which use the same range, ETag, CSP, and partial-stream semantics.
func (h *Handler) StreamFile(c *gin.Context, r Resolved, attachAs string) {
	entry, stream, err := h.d.Core.OpenStream(c.Request.Context(), r, nil)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	size, nerr := num.Narrow[int64](entry.Size)
	if nerr != nil {
		h.closeStream(stream, entry.Name)
		httpx.Fail(c, ErrNotFound)
		return
	}
	rng, ranged, rerr := httpx.ParseRange(c.GetHeader("Range"), size)
	if rerr != nil {
		h.closeStream(stream, entry.Name)
		if errors.Is(rerr, httpx.ErrRangeUnsatisfiable) {
			c.Header("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
			httpx.Refuse(c, apierr.Classified{Class: apierr.RangeNotSatisfiable})
			return
		}
		httpx.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if ranged {
		h.closeStream(stream, entry.Name)
		start, serr := num.Narrow[uint64](rng.Start)
		last, lerr := num.Narrow[uint64](rng.End - 1)
		if serr != nil || lerr != nil {
			httpx.Fail(c, ErrNotFound)
			return
		}
		entry, stream, err = h.d.Core.OpenStream(c.Request.Context(), r, &[2]uint64{start, last})
		if err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	h.SendStream(c, entry, stream, ranged, rng, size, attachAs)
}

// SendStream writes an already-open stream with all byte-serving headers.
// The stream is always closed and read failures after commitment are logged.
func (h *Handler) SendStream(c *gin.Context, entry FidEntry, stream *Stream, ranged bool, rng httpx.ByteRange, size int64, attachAs string) {
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
		if httpx.IsExecutableMIME(contentType) {
			attachAs = entry.Name
			c.Header("Content-Disposition", httpx.Attachment(attachAs))
		} else {
			c.Header("Content-Security-Policy", httpx.SafeInlineCSP)
		}
	} else {
		c.Header("Content-Disposition", httpx.Attachment(attachAs))
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
func ETagHeader(entry FidEntry) string {
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
	inner  *Stream
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

func (h *Handler) closeStream(stream *Stream, name string) {
	if stream == nil {
		return
	}
	if err := stream.Close(); err != nil && h.d.Logger != nil {
		h.d.Logger.Warn("closing a stream", "name", name, "error", err)
	}
}

type pathBodyInput struct{ Body pathRequest }
type ticketOutput struct{ Body TicketView }

// Download mints a browser-navigation ticket for one file.
func (h *Handler) Download(ctx context.Context, in *pathBodyInput) (*ticketOutput, error) {
	owner, err := OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.resolve(owner, in.Body.Path, acl.Read|acl.Download)
	if err != nil {
		return nil, err
	}
	st, err := r.Root().Stat(r.Path())
	if err != nil {
		return nil, ErrNotFound
	}
	if st.Kind.IsDir() {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	token, err := archiveToken()
	if err != nil {
		return nil, err
	}
	if h.d.Archives == nil || !h.d.Archives.Put(token, &Ticket{Kind: TicketFile, Name: r.Path().Name(), Paths: []string{in.Body.Path}, Owner: int64(owner)}) {
		return nil, apierr.AsClassified(apierr.LimitExceeded, "")
	}
	return &ticketOutput{Body: TicketView{Token: token, Name: r.Path().Name(), URL: "/api/v1/files/download/fetch?token=" + url.QueryEscape(token)}}, nil
}

// DownloadFetch resolves and streams a previously minted file ticket.
func (h *Handler) DownloadFetch(c *gin.Context) {
	owner, ok := Owner(c)
	if !ok {
		httpx.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	if h.d.Archives == nil {
		httpx.Refuse(c, apierr.Classified{Class: apierr.NotFound})
		return
	}
	ticket, ok := h.d.Archives.Get(c.Query("token"), int64(owner), TicketFile)
	if !ok || len(ticket.Paths) != 1 {
		httpx.Refuse(c, apierr.Classified{Class: apierr.NotFound})
		return
	}
	resolved, err := h.resolve(owner, ticket.Paths[0], acl.Read|acl.Download)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if c.GetHeader("Range") == "" {
		if provider, ok := resolved.Root().(objstore.DirectTransferProvider); ok && provider.DirectTransfer() {
			if keyer, keyOK := resolved.Root().(interface{ ObjectKey(vfs.SafePath) string }); keyOK {
				key := keyer.ObjectKey(resolved.Path())
				if signed, signErr := provider.PresignGet(c.Request.Context(), key, 5*time.Minute); signErr == nil {
					c.Header("Content-Disposition", httpx.Attachment(ticket.Name))
					c.Redirect(http.StatusFound, signed)
					return
				} else if !errors.Is(signErr, objstore.ErrDirectTransferUnsupported) {
					httpx.Fail(c, signErr)
					return
				}
			}
		}
	}
	h.StreamFile(c, resolved, ticket.Name)
}

type archiveRequest struct {
	Paths []string `json:"paths"`
	Name  string   `json:"name"`
}
type archiveInput struct{ Body archiveRequest }

// Archive mints a ticket for a validated selection.
func (h *Handler) Archive(ctx context.Context, in *archiveInput) (*ticketOutput, error) {
	owner, err := OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	req := in.Body
	if len(req.Paths) == 0 {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	if len(req.Paths) > archiveMaxRoots {
		return nil, apierr.AsClassified(apierr.LimitExceeded, "")
	}
	for _, p := range req.Paths {
		r, rerr := h.resolve(owner, p, acl.Read|acl.Download)
		if rerr != nil {
			return nil, rerr
		}
		encrypted, eerr := h.d.Core.ShareEncrypted(ctx, r.Share())
		if eerr != nil {
			return nil, eerr
		}
		if encrypted {
			return nil, apierr.AsClassified(apierr.Unprocessable, "")
		}
	}
	name, ok := archiveFilename(req.Name)
	if !ok {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	token, err := archiveToken()
	if err != nil {
		return nil, err
	}
	if h.d.Archives == nil || !h.d.Archives.Put(token, &Ticket{Kind: TicketArchive, Name: name, Paths: req.Paths, Owner: int64(owner)}) {
		return nil, apierr.AsClassified(apierr.LimitExceeded, "")
	}
	return &ticketOutput{Body: TicketView{Token: token, Name: name, URL: "/api/v1/files/archive/fetch?token=" + url.QueryEscape(token)}}, nil
}

// ArchiveFetch streams a validated archive ticket.
func (h *Handler) ArchiveFetch(c *gin.Context) {
	owner, ok := Owner(c)
	if !ok {
		httpx.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	if h.d.Archives == nil {
		httpx.Refuse(c, apierr.Classified{Class: apierr.NotFound})
		return
	}
	ticket, ok := h.d.Archives.Get(c.Query("token"), int64(owner), TicketArchive)
	if !ok {
		httpx.Refuse(c, apierr.Classified{Class: apierr.NotFound})
		return
	}
	roots := make([]Resolved, 0, len(ticket.Paths))
	for _, path := range ticket.Paths {
		resolved, err := h.resolve(owner, path, acl.Read|acl.Download)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		encrypted, err := h.d.Core.ShareEncrypted(c.Request.Context(), resolved.Share())
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		if encrypted {
			httpx.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}
		roots = append(roots, resolved)
	}
	release, acquired := h.d.AcquireArchive()
	if !acquired {
		httpx.Refuse(c, apierr.Classified{Class: apierr.ResourceExhausted, Key: "archive.busy"})
		return
	}
	defer release()
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", httpx.Attachment(ticket.Name))
	c.Status(http.StatusOK)
	c.Writer.Flush()
	if err := BuildArchive(c.Request.Context(), c.Writer, ticket.Name, func(ctx context.Context, visit ArchiveVisit) error {
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

// Mkdir creates one directory.
func (h *Handler) Mkdir(ctx context.Context, in *pathBodyInput) (*entryOutput, error) {
	owner, err := OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.resolve(owner, in.Body.Path, acl.Create)
	if err != nil {
		return nil, err
	}
	entry, err := h.d.Core.Mkdir(ctx, r)
	if err != nil {
		return nil, err
	}
	return &entryOutput{Status: http.StatusCreated, Body: h.d.EntryView(owner, r, entry)}, nil
}

// Delete removes one entry, respecting DAV locks and share trash policy.
func (h *Handler) Delete(ctx context.Context, in *pathBodyInput) (*noContentOutput, error) {
	owner, err := OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.resolve(owner, in.Body.Path, acl.Delete)
	if err != nil {
		return nil, err
	}
	if err = h.guardLock(ctx, owner, r); err != nil {
		return nil, err
	}
	if err = h.d.Core.Delete(ctx, r, false); err != nil {
		return nil, err
	}
	return &noContentOutput{Status: http.StatusNoContent}, nil
}

type pathRequest struct {
	Path string `json:"path"`
}
type renameRequest struct {
	Path string `json:"path"`
	Name string `json:"new_name"`
}
type renameInput struct{ Body renameRequest }

// Rename changes one entry name in place.
func (h *Handler) Rename(ctx context.Context, in *renameInput) (*entryOutput, error) {
	owner, err := OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.resolve(owner, in.Body.Path, acl.Rename)
	if err != nil {
		return nil, err
	}
	if err = h.guardLock(ctx, owner, r); err != nil {
		return nil, err
	}
	entry, err := h.d.Core.Rename(ctx, r, in.Body.Name, nil)
	if err != nil {
		return nil, err
	}
	return &entryOutput{Status: http.StatusOK, Body: h.d.EntryView(owner, r, entry)}, nil
}

// guardLock refuses a change under a DAV lock another client holds.
func (h *Handler) guardLock(ctx context.Context, owner UserID, r Resolved) error {
	if h.d.GuardLock == nil {
		return nil
	}
	if err := h.d.GuardLock(ctx, uint32(r.Share()), r.Path().String(), int64(owner)); err != nil {
		return apierr.AsClassified(apierr.Locked, "dav.locked")
	}
	return nil
}

// Write durably replaces one file from the request body.
func (h *Handler) Write(c *gin.Context) {
	owner, ok := Owner(c)
	if !ok {
		httpx.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	r, err := h.resolve(owner, c.Query("path"), acl.Write|acl.Create)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if h.d.GuardLock != nil {
		if lerr := h.d.GuardLock(c.Request.Context(), uint32(r.Share()), r.Path().String(), int64(owner)); lerr != nil {
			httpx.Refuse(c, apierr.Classified{Class: apierr.Locked, Key: "dav.locked"})
			return
		}
	}
	if declared := c.Request.ContentLength; declared > 0 {
		if qerr := h.d.Core.CheckQuota(c.Request.Context(), owner, uint64(declared)); qerr != nil {
			httpx.Fail(c, qerr)
			return
		}
	}
	entry, err := h.d.Core.WriteStream(c.Request.Context(), r, c.Request.Body, ifMatchOf(c))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusOK, h.d.EntryView(owner, r, entry))
}

func ifMatchOf(c *gin.Context) *Token {
	raw := strings.TrimSpace(c.GetHeader("If-Match"))
	if raw == "" {
		return nil
	}
	raw = strings.TrimPrefix(raw, "W/")
	raw = strings.Trim(raw, `"`)
	if raw == "" {
		return nil
	}
	token := Token(raw)
	return &token
}

type transferRequest struct {
	From       string `json:"from"`
	To         string `json:"to"`
	OnConflict string `json:"on_conflict"`
}

func (t transferRequest) policy() (OnConflict, bool) {
	if t.OnConflict == "" {
		return ConflictFail, true
	}
	return ParseOnConflict(t.OnConflict)
}

type transferInput struct{ Body transferRequest }
type moveOutput struct{ Body MoveView }
type copyStartOutput struct {
	Status int
	Body   CopyStartView
}

// Move relocates an entry.
func (h *Handler) Move(ctx context.Context, in *transferInput) (*moveOutput, error) {
	owner, err := OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	from, to, err := h.transferEnds(owner, in.Body, acl.Read|acl.Move)
	if err != nil {
		return nil, err
	}
	policy, known := in.Body.policy()
	if !known {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	result, err := h.d.Core.Move(ctx, from, to, MoveOpts{OnConflict: policy})
	if err != nil {
		return nil, err
	}
	return &moveOutput{Body: MoveOf(result)}, nil
}

// Copy starts a detached copy operation.
func (h *Handler) Copy(ctx context.Context, in *transferInput) (*copyStartOutput, error) {
	owner, err := OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	from, to, err := h.transferEnds(owner, in.Body, acl.Read|acl.Download)
	if err != nil {
		return nil, err
	}
	policy, known := in.Body.policy()
	if !known {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	start, err := h.d.Core.StartCopy(ctx, owner, from, to, policy)
	if err != nil {
		return nil, err
	}
	status := http.StatusAccepted
	if start.Skipped {
		status = http.StatusOK
	}
	return &copyStartOutput{Status: status, Body: CopyStartOf(start)}, nil
}

func (h *Handler) transferEnds(owner UserID, req transferRequest, sourceNeed acl.Perms) (from, to Resolved, err error) {
	from, err = h.resolve(owner, req.From, sourceNeed)
	if err != nil {
		return Resolved{}, Resolved{}, err
	}
	to, err = h.resolveDest(owner, req.To)
	if err != nil {
		return Resolved{}, Resolved{}, err
	}
	return from, to, nil
}

func (h *Handler) resolveDest(owner UserID, raw string) (Resolved, error) {
	if r, err := h.resolve(owner, raw, acl.Write|acl.Create); err == nil {
		return r, nil
	}
	parent, name, ok := splitDest(raw)
	if !ok {
		return Resolved{}, ErrNotFound
	}
	pr, err := h.resolve(owner, parent, acl.Write|acl.Create)
	if err != nil {
		return Resolved{}, err
	}
	leaf, err := pr.Path().Join(name)
	if err != nil {
		return Resolved{}, ErrNotFound
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

type aggregateOutput struct{ Body AggregateView }

// Size returns a recursive subtree aggregate.
func (h *Handler) Size(ctx context.Context, in *pathQueryInput) (*aggregateOutput, error) {
	owner, err := OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.resolve(owner, in.Path, acl.Read)
	if err != nil {
		return nil, err
	}
	agg, err := h.d.Core.Aggregate(ctx, r.Share(), r.Path())
	if err != nil {
		return nil, err
	}
	return &aggregateOutput{Body: AggregateOf(agg)}, nil
}

type recentInput struct {
	Path  string `query:"path"`
	Since string `query:"since"`
	Limit string `query:"limit"`
}
type recentOutput struct{ Body []RecentView }

// Recent returns the account's recent writes. A nil journal is an empty listing.
func (h *Handler) Recent(ctx context.Context, in *recentInput) (*recentOutput, error) {
	owner, err := OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	if !h.d.Journal {
		return &recentOutput{Body: []RecentView{}}, nil
	}
	since, perr := strconv.ParseInt(in.Since, 10, 64)
	if perr != nil || since < 0 {
		since = 0
	}
	limit, perr := strconv.Atoi(in.Limit)
	if perr != nil {
		limit = 0
	}
	hits, err := h.d.Core.Recent(ctx, owner, RecentQuery{SinceNs: RecentSinceOf(since, h.d.Now()), Limit: RecentLimitOf(limit), Scope: in.Path})
	if err != nil {
		return nil, err
	}
	return &recentOutput{Body: RecentListOf(hits)}, nil
}
