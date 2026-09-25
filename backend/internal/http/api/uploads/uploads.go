//go:build linux

// Package uploads serves the Tus resumable upload protocol and its live
// administrator settings. Product state is supplied through narrow typed
// dependencies so this package owns only HTTP presentation and protocol
// parsing.
package uploads

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	upload "github.com/heavycaffeiner/stowcloud/backend/internal/feature/uploads"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/server"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/stowcloud/transfer"
)

const (
	tusChecksumAlgorithm = "Tus-Checksum-Algorithm"
	tusExtensions        = "creation,creation-defer-length,termination,checksum"
	tusMetadataMaxPairs  = 32
	tusChunkType         = "application/offset+octet-stream"
)

// Deps supplies the product services and common response/authentication seams
// needed by the upload transport. Upload may be nil when the spool subsystem
// was unavailable during startup.
type Deps struct {
	Upload    *upload.Engine
	Core      *core.Core
	Resolve   func(core.UserID, string, acl.Perms) (core.Resolved, error)
	Owner     func(*gin.Context) (core.UserID, bool)
	Admin     func(*gin.Context) (int64, bool)
	Fail      func(*gin.Context, error)
	Refuse    func(*gin.Context, apierr.Classified)
	Decode    func(*gin.Context, any) error
	WriteJSON func(*gin.Context, int, any)
}

// NewHandlers constructs the native route handlers. The returned names are
// consumed by the app composition layer and intentionally match the route
// table's upload names.
func NewHandlers(d Deps) map[string]gin.HandlerFunc {
	h := &handlers{d: d}
	return map[string]gin.HandlerFunc{
		"uploads.discover":      h.Discover,
		"uploads.discover.one":  h.DiscoverOne,
		"uploads.create":        h.Create,
		"uploads.status":        h.Status,
		"uploads.patch":         h.Patch,
		"uploads.abort":         h.Abort,
		"admin.settings.upload": h.SettingsPatch,
	}
}

type handlers struct{ d Deps }

func (h *handlers) setTusHeaders(c *gin.Context) {
	c.Header(handler.TusResumable, handler.TusProtocolVersion)
	c.Header(handler.TusVersion, handler.TusProtocolVersion)
	c.Header(handler.TusExtension, tusExtensions)
	c.Header(tusChecksumAlgorithm, checksumAlgorithms())
}

func checksumAlgorithms() string {
	algos := transfer.Algorithms()
	names := make([]string, 0, len(algos))
	for _, a := range algos {
		names = append(names, a.String())
	}
	return strings.Join(names, ",")
}

func (h *handlers) Discover(c *gin.Context) {
	h.setTusHeaders(c)
	c.Header("Allow", "OPTIONS, POST")
	c.Status(http.StatusNoContent)
}

func (h *handlers) DiscoverOne(c *gin.Context) {
	h.setTusHeaders(c)
	c.Header("Allow", "OPTIONS, HEAD, PATCH, DELETE")
	c.Status(http.StatusNoContent)
}

func (h *handlers) Create(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := h.engine(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	h.setTusHeaders(c)
	if err := handler.CheckResumable(c.GetHeader(handler.TusResumable)); err != nil {
		h.refuseTus(c, err)
		return
	}
	length, err := handler.ParseLength(c.GetHeader(handler.UploadLength), c.GetHeader(handler.UploadDefer))
	if err != nil {
		h.refuseTus(c, err)
		return
	}
	meta, err := handler.ParseMetadata(c.GetHeader(handler.UploadMetadata), tusMetadataMaxPairs)
	if err != nil {
		h.refuseTus(c, err)
		return
	}
	dest := meta["dest"]
	leaf := meta["relativePath"]
	if leaf == "" {
		leaf = meta["filename"]
	}
	if leaf == "" {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if dest != "" {
		dest = strings.TrimSuffix(dest, "/") + "/" + leaf
	} else {
		dest = leaf
	}
	r, rerr := h.d.Resolve(owner, dest, acl.Write|acl.Create)
	if rerr != nil {
		h.d.Fail(c, rerr)
		return
	}
	spec := upload.SessionSpec{IfMatch: c.GetHeader("If-Match"), Meta: uploadMetaOf(meta), RandomAccess: c.GetHeader(handler.ScRandomAccess) == "1"}
	if !length.Deferred {
		total := length.Value
		spec.TotalLen = &total
		if qerr := h.d.Core.CheckQuota(c.Request.Context(), owner, length.Value); qerr != nil {
			h.d.Fail(c, qerr)
			return
		}
	}
	sess, cerr := engine.Create(c.Request.Context(), r, spec)
	if cerr != nil {
		h.d.Fail(c, cerr)
		return
	}
	c.Header("Location", server.Base+"/uploads/"+sess.ID.String())
	c.Header(handler.UploadOffset, strconv.FormatUint(sess.Offset, 10))
	c.Status(http.StatusCreated)
}

func (h *handlers) Status(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := h.engine(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	h.setTusHeaders(c)
	if err := handler.CheckResumable(c.GetHeader(handler.TusResumable)); err != nil {
		h.refuseTus(c, err)
		return
	}
	id, ok := sessionIDOf(c)
	if !ok {
		h.notFound(c)
		return
	}
	sess, err := engine.Get(c.Request.Context(), id, owner)
	if err != nil {
		h.d.Fail(c, err)
		return
	}
	if terminal, _ := handler.TerminalUploadState(sess.State.StateName()); terminal {
		h.notFound(c)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header(handler.UploadOffset, strconv.FormatUint(sess.Offset, 10))
	if sess.TotalLen != nil {
		c.Header(handler.UploadLength, strconv.FormatUint(*sess.TotalLen, 10))
	} else {
		c.Header(handler.UploadDefer, "1")
	}
	c.Status(http.StatusOK)
}

func (h *handlers) Patch(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := h.engine(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	h.setTusHeaders(c)
	if err := handler.CheckResumable(c.GetHeader(handler.TusResumable)); err != nil {
		h.refuseTus(c, err)
		return
	}
	id, ok := sessionIDOf(c)
	if !ok {
		h.notFound(c)
		return
	}
	if c.GetHeader("Content-Type") != tusChunkType {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	offset, err := handler.ParseOffset(c.GetHeader(handler.UploadOffset))
	if err != nil {
		h.refuseTus(c, err)
		return
	}
	sum, err := chunkChecksum(c.GetHeader(handler.UploadChecksum))
	if err != nil {
		h.refuseTus(c, err)
		return
	}
	sess, err := engine.Get(c.Request.Context(), id, owner)
	if err != nil {
		h.d.Fail(c, err)
		return
	}
	root, ok := h.d.Core.ShareRoot(sess.Share)
	if !ok {
		h.d.Fail(c, core.ErrNotFound)
		return
	}
	next, err := engine.PatchAt(c.Request.Context(), root, id, owner, offset, requestBodyReader(c), sum)
	if err != nil {
		h.failUpload(c, err)
		return
	}
	if sess.TotalLen != nil && next >= *sess.TotalLen {
		if !h.publish(c, engine, sess, id, owner) {
			return
		}
	}
	c.Header(handler.UploadOffset, strconv.FormatUint(next, 10))
	c.Status(http.StatusNoContent)
}

func (h *handlers) publish(c *gin.Context, engine *upload.Engine, sess upload.Session, id upload.SessionID, owner core.UserID) bool {
	dest, err := h.d.Core.VpathFor(owner, sess.Share, sess.Dest.Share())
	if err != nil {
		h.d.Fail(c, core.ErrNotFound)
		return false
	}
	resolved, err := h.d.Resolve(owner, dest.String(), acl.Write|acl.Create)
	if err != nil {
		h.d.Fail(c, err)
		return false
	}
	if _, err := engine.Finalize(c.Request.Context(), resolved, id); err != nil {
		h.d.Fail(c, err)
		return false
	}
	return true
}

func (h *handlers) Abort(c *gin.Context) {
	owner, ok := h.d.Owner(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := h.engine(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	h.setTusHeaders(c)
	if err := handler.CheckResumable(c.GetHeader(handler.TusResumable)); err != nil {
		h.refuseTus(c, err)
		return
	}
	id, ok := sessionIDOf(c)
	if !ok {
		h.notFound(c)
		return
	}
	if err := engine.Abort(c.Request.Context(), id, owner); err != nil {
		h.d.Fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type SettingsRequest struct {
	ChunkMin     *int64 `json:"chunk_min"`
	ChunkDefault *int64 `json:"chunk_default"`
	CacheEnabled *bool  `json:"cache_enabled"`
}

// SettingsPatch handles the upload section of the administrator settings
// route. It is exposed separately because the settings transport owns section
// dispatch while this package owns the upload-specific state and validation.
func (h *handlers) SettingsPatch(c *gin.Context) {
	if _, ok := h.d.Admin(c); !ok {
		return
	}
	engine, ok := h.engine(c)
	if !ok {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	var req SettingsRequest
	if err := h.d.Decode(c, &req); err != nil {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	var minBytes, defaultBytes *uint64
	if req.ChunkMin != nil {
		v, err := num.Narrow[uint64](*req.ChunkMin)
		if err != nil {
			h.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}
		minBytes = &v
	}
	if req.ChunkDefault != nil {
		v, err := num.Narrow[uint64](*req.ChunkDefault)
		if err != nil {
			h.d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}
		defaultBytes = &v
	}
	if minBytes != nil || defaultBytes != nil {
		if err := engine.ApplySettings(c.Request.Context(), minBytes, defaultBytes); err != nil {
			h.d.Fail(c, err)
			return
		}
	}
	if req.CacheEnabled != nil && *req.CacheEnabled != engine.CacheEnabled() {
		if err := engine.SetCacheEnabled(c.Request.Context(), *req.CacheEnabled); err != nil {
			h.d.Fail(c, err)
			return
		}
	}
	storedMin, storedDefault := engine.Settings().Snapshot()
	viewMin, minErr := num.Narrow[int64](storedMin)
	viewDefault, defErr := num.Narrow[int64](storedDefault)
	if minErr != nil || defErr != nil {
		if minErr != nil {
			h.d.Fail(c, minErr)
		} else {
			h.d.Fail(c, defErr)
		}
		return
	}
	h.d.WriteJSON(c, http.StatusOK, handler.UploadSettingsView{ChunkMin: viewMin, ChunkDefault: viewDefault, CacheEnabled: engine.CacheEnabled(), CacheAvailable: engine.CacheAvailable()})
}

func (h *handlers) engine(c *gin.Context) (*upload.Engine, bool) {
	_ = c
	return h.d.Upload, h.d.Upload != nil
}

func sessionIDOf(c *gin.Context) (upload.SessionID, bool) {
	id, err := transfer.ParseSessionID(c.Param("id"))
	if err != nil {
		return upload.SessionID{}, false
	}
	return id, true
}

func (h *handlers) refuseTus(c *gin.Context, err error) {
	if errors.Is(err, handler.ErrTusVersion) {
		h.d.Refuse(c, apierr.Classified{Class: apierr.Precondition})
		return
	}
	h.d.Refuse(c, apierr.Classified{Class: apierr.Malformed})
}

func (h *handlers) failUpload(c *gin.Context, err error) {
	if errors.Is(err, upload.ErrChecksum) {
		h.d.WriteJSON(c, handler.StatusChecksumMismatch, map[string]string{"error": "checksum_mismatch"})
		return
	}
	h.d.Fail(c, err)
}

func chunkChecksum(header string) (*upload.Checksum, error) {
	if header == "" {
		return nil, nil
	}
	sum, err := upload.ParseChecksum(header)
	if err != nil {
		return nil, err
	}
	return &sum, nil
}

func uploadMetaOf(meta map[string]string) upload.Meta {
	out := upload.Meta{Filename: meta["filename"], RelativePath: meta["relativePath"], Mime: meta["filetype"]}
	if raw := meta["mtime"]; raw != "" {
		if ns, err := strconv.ParseInt(raw, 10, 64); err == nil {
			out.MtimeNs = &ns
		}
	}
	return out
}

func (h *handlers) notFound(c *gin.Context) { h.d.Fail(c, core.ErrNotFound) }

func requestBodyReader(c *gin.Context) io.Reader {
	if c.Request != nil && c.Request.Body != nil {
		return c.Request.Body
	}
	return strings.NewReader("")
}
