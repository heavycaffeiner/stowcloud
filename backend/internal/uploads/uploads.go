//go:build linux

// Package uploads serves the Tus resumable upload protocol and its live
// administrator settings. Product state is supplied through narrow typed
// dependencies so this package owns only HTTP presentation and protocol
// parsing.
package uploads

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/httpx"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
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
	Upload  *Engine
	Core    *files.Core
	Resolve func(files.UserID, string, acl.Perms) (files.Resolved, error)
}

// NewHandlers builds the resumable upload handlers.
func NewHandlers(d Deps) *Handlers {
	return &Handlers{d: d}
}

type Handlers struct{ d Deps }

func (h *Handlers) setTusHeaders(c *gin.Context) {
	c.Header(TusResumable, TusProtocolVersion)
	c.Header(TusVersion, TusProtocolVersion)
	c.Header(TusExtension, tusExtensions)
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
		httpx.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := h.engine(c)
	if !ok {
		httpx.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	h.setTusHeaders(c)
	if err := CheckResumable(c.GetHeader(TusResumable)); err != nil {
		h.refuseTus(c, err)
		return
	}
	length, err := ParseLength(c.GetHeader(UploadLength), c.GetHeader(UploadDefer))
	if err != nil {
		h.refuseTus(c, err)
		return
	}
	meta, err := ParseMetadata(c.GetHeader(UploadMetadata), tusMetadataMaxPairs)
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
		httpx.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	if dest != "" {
		dest = strings.TrimSuffix(dest, "/") + "/" + leaf
	} else {
		dest = leaf
	}
	r, rerr := h.d.Resolve(owner, dest, acl.Write|acl.Create)
	if rerr != nil {
		httpx.Fail(c, rerr)
		return
	}
	spec := SessionSpec{IfMatch: c.GetHeader("If-Match"), Meta: uploadMetaOf(meta), RandomAccess: c.GetHeader(ScRandomAccess) == "1"}
	if !length.Deferred {
		total := length.Value
		spec.TotalLen = &total
		if qerr := h.d.Core.CheckQuota(c.Request.Context(), owner, length.Value); qerr != nil {
			httpx.Fail(c, qerr)
			return
		}
	}
	sess, cerr := engine.Create(c.Request.Context(), r, spec)
	if cerr != nil {
		httpx.Fail(c, cerr)
		return
	}
	c.Header("Location", "/api/v1/uploads/"+sess.ID.String())
	c.Header(UploadOffset, strconv.FormatUint(sess.Offset, 10))
	c.Status(http.StatusCreated)
}

func (h *Handlers) Status(c *gin.Context) {
	owner, ok := files.Owner(c)
	if !ok {
		httpx.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := h.engine(c)
	if !ok {
		httpx.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	h.setTusHeaders(c)
	if err := CheckResumable(c.GetHeader(TusResumable)); err != nil {
		h.refuseTus(c, err)
		return
	}
	id, ok := sessionIDOf(c)
	if !ok {
		httpx.Fail(c, files.ErrNotFound)
		return
	}
	sess, err := engine.Get(c.Request.Context(), id, owner)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if terminal, _ := TerminalUploadState(sess.State.StateName()); terminal {
		httpx.Fail(c, files.ErrNotFound)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header(UploadOffset, strconv.FormatUint(sess.Offset, 10))
	if sess.TotalLen != nil {
		c.Header(UploadLength, strconv.FormatUint(*sess.TotalLen, 10))
	} else {
		c.Header(UploadDefer, "1")
	}
	c.Status(http.StatusOK)
}

func (h *Handlers) Patch(c *gin.Context) {
	owner, ok := files.Owner(c)
	if !ok {
		httpx.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := h.engine(c)
	if !ok {
		httpx.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	h.setTusHeaders(c)
	if err := CheckResumable(c.GetHeader(TusResumable)); err != nil {
		h.refuseTus(c, err)
		return
	}
	id, ok := sessionIDOf(c)
	if !ok {
		httpx.Fail(c, files.ErrNotFound)
		return
	}
	if c.GetHeader("Content-Type") != tusChunkType {
		httpx.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
		return
	}
	offset, err := ParseOffset(c.GetHeader(UploadOffset))
	if err != nil {
		h.refuseTus(c, err)
		return
	}
	sum, err := chunkChecksum(c.GetHeader(UploadChecksum))
	if err != nil {
		h.refuseTus(c, err)
		return
	}
	sess, err := engine.Get(c.Request.Context(), id, owner)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	root, ok := h.d.Core.ShareRoot(sess.Share)
	if !ok {
		httpx.Fail(c, files.ErrNotFound)
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
	c.Header(UploadOffset, strconv.FormatUint(next, 10))
	c.Status(http.StatusNoContent)
}

func (h *Handlers) publish(c *gin.Context, engine *Engine, sess Session, id SessionID, owner files.UserID) bool {
	dest, err := h.d.Core.VpathFor(owner, sess.Share, sess.Dest.Share())
	if err != nil {
		httpx.Fail(c, files.ErrNotFound)
		return false
	}
	resolved, err := h.d.Resolve(owner, dest.String(), acl.Write|acl.Create)
	if err != nil {
		httpx.Fail(c, err)
		return false
	}
	if _, err := engine.Finalize(c.Request.Context(), resolved, id); err != nil {
		httpx.Fail(c, err)
		return false
	}
	return true
}

func (h *Handlers) Abort(c *gin.Context) {
	owner, ok := files.Owner(c)
	if !ok {
		httpx.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return
	}
	engine, ok := h.engine(c)
	if !ok {
		httpx.Refuse(c, apierr.Classified{Class: apierr.SubsystemUnavailable})
		return
	}
	h.setTusHeaders(c)
	if err := CheckResumable(c.GetHeader(TusResumable)); err != nil {
		h.refuseTus(c, err)
		return
	}
	id, ok := sessionIDOf(c)
	if !ok {
		httpx.Fail(c, files.ErrNotFound)
		return
	}
	if err := engine.Abort(c.Request.Context(), id, owner); err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type SettingsRequest struct {
	ChunkMin     *int64 `json:"chunk_min"`
	ChunkDefault *int64 `json:"chunk_default"`
	CacheEnabled *bool  `json:"cache_enabled"`
}

type settingsPatchInput struct{ Body SettingsRequest }

type settingsOutput struct{ Body UploadSettingsView }

// SettingsPatch handles the upload section of the administrator settings. It
// is served here because this package owns the upload state and validation.
func (h *Handlers) SettingsPatch(ctx context.Context, in *settingsPatchInput) (*settingsOutput, error) {
	engine := h.d.Upload
	if engine == nil {
		return nil, apierr.AsClassified(apierr.Unprocessable, "")
	}
	req := in.Body
	var minBytes, defaultBytes *uint64
	if req.ChunkMin != nil {
		v, err := num.Narrow[uint64](*req.ChunkMin)
		if err != nil {
			return nil, apierr.AsClassified(apierr.Unprocessable, "")
		}
		minBytes = &v
	}
	if req.ChunkDefault != nil {
		v, err := num.Narrow[uint64](*req.ChunkDefault)
		if err != nil {
			return nil, apierr.AsClassified(apierr.Unprocessable, "")
		}
		defaultBytes = &v
	}
	if minBytes != nil || defaultBytes != nil {
		if err := engine.ApplySettings(ctx, minBytes, defaultBytes); err != nil {
			return nil, err
		}
	}
	if req.CacheEnabled != nil && *req.CacheEnabled != engine.CacheEnabled() {
		if err := engine.SetCacheEnabled(ctx, *req.CacheEnabled); err != nil {
			return nil, err
		}
	}
	storedMin, storedDefault := engine.Settings().Snapshot()
	viewMin, err := num.Narrow[int64](storedMin)
	if err != nil {
		return nil, err
	}
	viewDefault, err := num.Narrow[int64](storedDefault)
	if err != nil {
		return nil, err
	}
	return &settingsOutput{Body: UploadSettingsView{ChunkMin: viewMin, ChunkDefault: viewDefault, CacheEnabled: engine.CacheEnabled(), CacheAvailable: engine.CacheAvailable()}}, nil
}

func (h *Handlers) engine(c *gin.Context) (*Engine, bool) {
	_ = c
	return h.d.Upload, h.d.Upload != nil
}

func sessionIDOf(c *gin.Context) (SessionID, bool) {
	id, err := transfer.ParseSessionID(c.Param("id"))
	if err != nil {
		return SessionID{}, false
	}
	return id, true
}

func (h *Handlers) refuseTus(c *gin.Context, err error) {
	if errors.Is(err, ErrTusVersion) {
		httpx.Refuse(c, apierr.Classified{Class: apierr.Precondition})
		return
	}
	httpx.Refuse(c, apierr.Classified{Class: apierr.Malformed})
}

func (h *Handlers) failUpload(c *gin.Context, err error) {
	if errors.Is(err, ErrChecksum) {
		c.JSON(StatusChecksumMismatch, map[string]string{"error": "checksum_mismatch"})
		return
	}
	httpx.Fail(c, err)
}

func chunkChecksum(header string) (*Checksum, error) {
	if header == "" {
		return nil, nil
	}
	sum, err := ParseChecksum(header)
	if err != nil {
		return nil, err
	}
	return &sum, nil
}

func uploadMetaOf(meta map[string]string) Meta {
	out := Meta{Filename: meta["filename"], RelativePath: meta["relativePath"], Mime: meta["filetype"]}
	if raw := meta["mtime"]; raw != "" {
		if ns, err := strconv.ParseInt(raw, 10, 64); err == nil {
			out.MtimeNs = &ns
		}
	}
	return out
}
