//go:build linux

// Thumbnail responses: protocol framing around the preview service.
package preview

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	featurepreview "github.com/heavycaffeiner/stowcloud/backend/internal/feature/preview"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
)

// ThumbnailDeps supplies the narrow application capabilities needed by the
// thumbnail protocol. Resolution and claim validation remain application
// policy; framing and preview service use belong to this transport package.
type ThumbnailDeps struct {
	Core         *core.Core
	Owner        func(*gin.Context) (core.UserID, bool)
	Resolve      func(core.UserID, string, acl.Perms) (core.Resolved, error)
	OpenClaim    func(*gin.Context, handler.ClaimPurpose, core.UserID) (handler.Claim, bool)
	PreviewLease func() (*featurepreview.Lease, bool)
	Fail         func(*gin.Context, error)
	Refuse       func(*gin.Context, apierr.Classified)
	Logger       *slog.Logger
}

// ThumbnailHandler answers with a thumbnail of the addressed file.
func ThumbnailHandler(d ThumbnailDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		lease, ok := d.PreviewLease()
		if !ok {
			d.Refuse(c, apierr.Classified{Class: apierr.NotFound})
			return
		}
		defer lease.Close()

		owner, ok := d.Owner(c)
		if !ok {
			d.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
			return
		}

		preset, err := presetOf(c.Query("size"))
		if err != nil {
			d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}

		claim, ok := d.OpenClaim(c, handler.PurposeThumb, owner)
		if !ok {
			d.Fail(c, core.ErrNotFound)
			return
		}
		r, err := d.Resolve(owner, claim.Path, acl.Read|acl.Download)
		if err != nil {
			d.Fail(c, err)
			return
		}
		if enc, eerr := d.Core.ShareEncrypted(c.Request.Context(), r.Share()); eerr != nil {
			d.Fail(c, eerr)
			return
		} else if enc {
			d.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}

		thumb, err := lease.Get(c.Request.Context(), r, preset)
		if err != nil {
			d.Fail(c, err)
			return
		}
		sendThumb(c, thumb, d.Logger, d.Fail)
	}
}

func sendThumb(c *gin.Context, thumb featurepreview.Thumb, logger *slog.Logger, fail func(*gin.Context, error)) {
	size, err := thumbSize(thumb.File)
	if err != nil {
		closeThumb(thumb, logger)
		fail(c, err)
		return
	}

	c.Header("Content-Type", "image/png")
	c.Header("Cache-Control", "private, max-age=86400, immutable")
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Status(http.StatusOK)
	defer closeThumb(thumb, logger)
	if _, err := io.CopyN(c.Writer, &loggedThumb{inner: thumb, logger: logger}, size); err != nil && !errors.Is(err, io.EOF) && logger != nil {
		logger.Warn("a thumbnail ended early", "error", err)
	}
}

type loggedThumb struct {
	inner  featurepreview.Thumb
	logger *slog.Logger
}

func (t *loggedThumb) Read(p []byte) (int, error) {
	n, err := t.inner.File.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && t.logger != nil {
		t.logger.Warn("a thumbnail ended early", "error", err)
	}
	return n, err
}

func (t *loggedThumb) Close() error { return t.inner.Close() }

func closeThumb(thumb featurepreview.Thumb, logger *slog.Logger) {
	if err := thumb.Close(); err != nil && logger != nil {
		logger.Warn("closing a thumbnail", "error", err)
	}
}

func thumbSize(f *os.File) (int64, error) {
	if f == nil {
		return 0, errors.New("preview: the thumbnail carries no file")
	}
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return num.Narrow[int64](st.Size())
}

func presetOf(s string) (featurepreview.Preset, error) {
	switch s {
	case "", "small":
		return featurepreview.PresetSmall, nil
	case "medium":
		return featurepreview.PresetMedium, nil
	case "large":
		return featurepreview.PresetLarge, nil
	default:
		return 0, errors.New("preview: unknown size")
	}
}
