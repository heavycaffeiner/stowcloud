//go:build linux

// Package uploads serves the Tus resumable upload protocol and its live
// administrator settings. Product state is supplied through narrow typed
// dependencies so this package owns only HTTP presentation and protocol
// parsing.
package uploads

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/uploads"
	"github.com/stowcloud/transfer"
)

const (
	tusChecksumAlgorithm = "Tus-Checksum-Algorithm"
	tusExtensions        = "creation,creation-defer-length,termination,checksum"
	tusMetadataMaxPairs  = 32
	tusChunkType         = "application/offset+octet-stream"
)

// Deps supplies the product services needed by the upload transport. Upload
// is nil when the spool subsystem was unavailable during startup.
type Deps struct {
	Upload  *uploads.Engine
	Core    *files.Core
	Resolve func(files.UserID, string, acl.Perms) (files.Resolved, error)
}

// NewHandlers builds the resumable upload handlers.
func NewHandlers(d Deps) *Handlers {
	return &Handlers{d: d}
}

type Handlers struct{ d Deps }

func (h *Handlers) setTusHeaders(c *gin.Context) {
	c.Header(uploads.TusResumable, uploads.TusProtocolVersion)
	c.Header(uploads.TusVersion, uploads.TusProtocolVersion)
	c.Header(uploads.TusExtension, tusExtensions)
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

func (h *Handlers) Discover(c *gin.Context) {
	h.setTusHeaders(c)
	c.Header("Allow", "OPTIONS, POST")
	c.Status(http.StatusNoContent)
}

func (h *Handlers) DiscoverOne(c *gin.Context) {
	h.setTusHeaders(c)
	c.Header("Allow", "OPTIONS, HEAD, PATCH, DELETE")
	c.Status(http.StatusNoContent)
}

func (h *Handlers) Create(c *gin.Context) {
	owner, ok := files.Owner(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := h.engine(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	h.setTusHeaders(c)
	if err := uploads.CheckResumable(c.GetHeader(uploads.TusResumable)); err != nil {
		h.refuseTus(c, err)
		return
	}
	length, err := uploads.ParseLength(c.GetHeader(uploads.UploadLength), c.GetHeader(uploads.UploadDefer))
	if err != nil {
		h.refuseTus(c, err)
		return
	}
	meta, err := uploads.ParseMetadata(c.GetHeader(uploads.UploadMetadata), tusMetadataMaxPairs)
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
		middleware.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if dest != "" {
		dest = strings.TrimSuffix(dest, "/") + "/" + leaf
	} else {
		dest = leaf
	}
	r, rerr := h.d.Resolve(owner, dest, acl.Write|acl.Create)
	if rerr != nil {
		middleware.Fail(c, rerr)
		return
	}
	spec := uploads.SessionSpec{IfMatch: c.GetHeader("If-Match"), Meta: uploadMetaOf(meta), RandomAccess: c.GetHeader(uploads.ScRandomAccess) == "1"}
	if !length.Deferred {
		total := length.Value
		spec.TotalLen = &total
		if qerr := h.d.Core.CheckQuota(c.Request.Context(), owner, length.Value); qerr != nil {
			middleware.Fail(c, qerr)
			return
		}
	}
	sess, cerr := engine.Create(c.Request.Context(), r, spec)
	if cerr != nil {
		middleware.Fail(c, cerr)
		return
	}
	c.Header("Location", "/api/v1/uploads/"+sess.ID.String())
	c.Header(uploads.UploadOffset, strconv.FormatUint(sess.Offset, 10))
	c.Status(http.StatusCreated)
}

func (h *Handlers) Status(c *gin.Context) {
	owner, ok := files.Owner(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := h.engine(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	h.setTusHeaders(c)
	if err := uploads.CheckResumable(c.GetHeader(uploads.TusResumable)); err != nil {
		h.refuseTus(c, err)
		return
	}
	id, ok := sessionIDOf(c)
	if !ok {
		middleware.Fail(c, files.ErrNotFound)
		return
	}
	sess, err := engine.Get(c.Request.Context(), id, owner)
	if err != nil {
		middleware.Fail(c, err)
		return
	}
	if terminal, _ := uploads.TerminalUploadState(sess.State.StateName()); terminal {
		middleware.Fail(c, files.ErrNotFound)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header(uploads.UploadOffset, strconv.FormatUint(sess.Offset, 10))
	if sess.TotalLen != nil {
		c.Header(uploads.UploadLength, strconv.FormatUint(*sess.TotalLen, 10))
	} else {
		c.Header(uploads.UploadDefer, "1")
	}
	c.Status(http.StatusOK)
}

func (h *Handlers) Patch(c *gin.Context) {
	owner, ok := files.Owner(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := h.engine(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	h.setTusHeaders(c)
	if err := uploads.CheckResumable(c.GetHeader(uploads.TusResumable)); err != nil {
		h.refuseTus(c, err)
		return
	}
	id, ok := sessionIDOf(c)
	if !ok {
		middleware.Fail(c, files.ErrNotFound)
		return
	}
	if c.GetHeader("Content-Type") != tusChunkType {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	offset, err := uploads.ParseOffset(c.GetHeader(uploads.UploadOffset))
	if err != nil {
		h.refuseTus(c, err)
		return
	}
	sum, err := chunkChecksum(c.GetHeader(uploads.UploadChecksum))
	if err != nil {
		h.refuseTus(c, err)
		return
	}
	sess, err := engine.Get(c.Request.Context(), id, owner)
	if err != nil {
		middleware.Fail(c, err)
		return
	}
	root, ok := h.d.Core.ShareRoot(sess.Share)
	if !ok {
		middleware.Fail(c, files.ErrNotFound)
		return
	}
	// Deferred-length sessions are quota-bounded by PatchAt while bytes arrive;
	// the handler must not wait for Upload-Length because it is intentionally absent.
	next, err := engine.PatchAt(c.Request.Context(), root, id, owner, offset, c.Request.Body, sum)
	if err != nil {
		h.failUpload(c, err)
		return
	}
	if sess.TotalLen != nil && next >= *sess.TotalLen {
		if !h.publish(c, engine, sess, id, owner) {
			return
		}
	}
	c.Header(uploads.UploadOffset, strconv.FormatUint(next, 10))
	c.Status(http.StatusNoContent)
}

func (h *Handlers) publish(c *gin.Context, engine *uploads.Engine, sess uploads.Session, id uploads.SessionID, owner files.UserID) bool {
	dest, err := h.d.Core.VpathFor(owner, sess.Share, sess.Dest.Share())
	if err != nil {
		middleware.Fail(c, files.ErrNotFound)
		return false
	}
	resolved, err := h.d.Resolve(owner, dest.String(), acl.Write|acl.Create)
	if err != nil {
		middleware.Fail(c, err)
		return false
	}
	if _, err := engine.Finalize(c.Request.Context(), resolved, id); err != nil {
		middleware.Fail(c, err)
		return false
	}
	return true
}

func (h *Handlers) Abort(c *gin.Context) {
	owner, ok := files.Owner(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := h.engine(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	h.setTusHeaders(c)
	if err := uploads.CheckResumable(c.GetHeader(uploads.TusResumable)); err != nil {
		h.refuseTus(c, err)
		return
	}
	id, ok := sessionIDOf(c)
	if !ok {
		middleware.Fail(c, files.ErrNotFound)
		return
	}
	if err := engine.Abort(c.Request.Context(), id, owner); err != nil {
		middleware.Fail(c, err)
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
func (h *Handlers) SettingsPatch(c *gin.Context) {
	engine, ok := h.engine(c)
	if !ok {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	var req SettingsRequest
	if err := middleware.DecodeJSON(c.Request.Body, &req); err != nil {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	var minBytes, defaultBytes *uint64
	if req.ChunkMin != nil {
		v, err := num.Narrow[uint64](*req.ChunkMin)
		if err != nil {
			middleware.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}
		minBytes = &v
	}
	if req.ChunkDefault != nil {
		v, err := num.Narrow[uint64](*req.ChunkDefault)
		if err != nil {
			middleware.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}
		defaultBytes = &v
	}
	if minBytes != nil || defaultBytes != nil {
		if err := engine.ApplySettings(c.Request.Context(), minBytes, defaultBytes); err != nil {
			middleware.Fail(c, err)
			return
		}
	}
	if req.CacheEnabled != nil && *req.CacheEnabled != engine.CacheEnabled() {
		if err := engine.SetCacheEnabled(c.Request.Context(), *req.CacheEnabled); err != nil {
			middleware.Fail(c, err)
			return
		}
	}
	storedMin, storedDefault := engine.Settings().Snapshot()
	viewMin, minErr := num.Narrow[int64](storedMin)
	viewDefault, defErr := num.Narrow[int64](storedDefault)
	if minErr != nil || defErr != nil {
		if minErr != nil {
			middleware.Fail(c, minErr)
		} else {
			middleware.Fail(c, defErr)
		}
		return
	}
	c.JSON(http.StatusOK, uploads.UploadSettingsView{ChunkMin: viewMin, ChunkDefault: viewDefault, CacheEnabled: engine.CacheEnabled(), CacheAvailable: engine.CacheAvailable()})
}

func (h *Handlers) engine(c *gin.Context) (*uploads.Engine, bool) {
	_ = c
	return h.d.Upload, h.d.Upload != nil
}

func sessionIDOf(c *gin.Context) (uploads.SessionID, bool) {
	id, err := transfer.ParseSessionID(c.Param("id"))
	if err != nil {
		return uploads.SessionID{}, false
	}
	return id, true
}

func (h *Handlers) refuseTus(c *gin.Context, err error) {
	if errors.Is(err, uploads.ErrTusVersion) {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Precondition})
		return
	}
	middleware.Refuse(c, apierr.Classified{Class: apierr.Malformed})
}

func (h *Handlers) failUpload(c *gin.Context, err error) {
	if errors.Is(err, uploads.ErrChecksum) {
		c.JSON(uploads.StatusChecksumMismatch, map[string]string{"error": "checksum_mismatch"})
		return
	}
	middleware.Fail(c, err)
}

func chunkChecksum(header string) (*uploads.Checksum, error) {
	if header == "" {
		return nil, nil
	}
	sum, err := uploads.ParseChecksum(header)
	if err != nil {
		return nil, err
	}
	return &sum, nil
}

func uploadMetaOf(meta map[string]string) uploads.Meta {
	out := uploads.Meta{Filename: meta["filename"], RelativePath: meta["relativePath"], Mime: meta["filetype"]}
	if raw := meta["mtime"]; raw != "" {
		if ns, err := strconv.ParseInt(raw, 10, 64); err == nil {
			out.MtimeNs = &ns
		}
	}
	return out
}
