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

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/httpx"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

// ThumbnailDeps supplies the narrow application capabilities needed by the
// thumbnail protocol. Resolution and claim validation remain application
// policy; framing and preview service use belong to this transport package.
type ThumbnailDeps struct {
	Core         *files.Core
	Resolve      func(files.UserID, string, acl.Perms) (files.Resolved, error)
	OpenClaim    func(*gin.Context, files.ClaimPurpose, files.UserID) (files.Claim, bool)
	PreviewLease func() (*Lease, bool)
	Logger       *slog.Logger
}

// ThumbnailHandler answers with a thumbnail of the addressed file.
func ThumbnailHandler(d ThumbnailDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		lease, ok := d.PreviewLease()
		if !ok {
			httpx.Refuse(c, apierr.Classified{Class: apierr.NotFound})
			return
		}
		defer lease.Close()

		owner, ok := files.Owner(c)
		if !ok {
			httpx.Refuse(c, apierr.Classified{Class: apierr.AuthRequired})
			return
		}

		preset, err := presetOf(c.Query("size"))
		if err != nil {
			httpx.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}

		claim, ok := d.OpenClaim(c, files.PurposeThumb, owner)
		if !ok {
			httpx.Fail(c, files.ErrNotFound)
			return
		}
		r, err := d.Resolve(owner, claim.Path, acl.Read|acl.Download)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		if enc, eerr := d.Core.ShareEncrypted(c.Request.Context(), r.Share()); eerr != nil {
			httpx.Fail(c, eerr)
			return
		} else if enc {
			httpx.Refuse(c, apierr.Classified{Class: apierr.Unprocessable})
			return
		}

		thumb, err := lease.Get(c.Request.Context(), r, preset)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		sendThumb(c, thumb, d.Logger)
	}
}

func sendThumb(c *gin.Context, thumb Thumb, logger *slog.Logger) {
	size, err := thumbSize(thumb.File)
	if err != nil {
		closeThumb(thumb, logger)
		httpx.Fail(c, err)
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
	inner  Thumb
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

func closeThumb(thumb Thumb, logger *slog.Logger) {
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

func presetOf(s string) (Preset, error) {
	switch s {
	case "", "small":
		return PresetSmall, nil
	case "medium":
		return PresetMedium, nil
	case "large":
		return PresetLarge, nil
	default:
		return 0, errors.New("preview: unknown size")
	}
}
