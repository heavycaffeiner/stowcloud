//go:build linux

package files

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	httpheader "github.com/heavycaffeiner/stowcloud/backend/internal/http/headers"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
)

// CloseStream closes a stream and records a close failure when a logger is supplied.
func CloseStream(stream *core.Stream, name string, logger *slog.Logger) {
	if stream == nil {
		return
	}
	if err := stream.Close(); err != nil && logger != nil {
		logger.Warn("closing a stream", "name", name, "error", err)
	}
}

// It closes the stream after copying, including when copying fails.
func SendStream(c *gin.Context, entry core.FidEntry, stream *core.Stream) error {
	defer CloseStream(stream, entry.Name, nil)
	length, err := num.Narrow[int64](stream.Remaining())
	if err != nil {
		return err
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", strconv.FormatInt(length, 10))
	c.Header("Content-Disposition", httpheader.Attachment(entry.Name))
	c.Status(http.StatusOK)
	_, err = io.CopyN(c.Writer, &sharedLoggedStream{inner: stream, name: entry.Name}, length)
	return err
}

// SendStreamRange writes an inline or attachment stream with range headers.
func SendStreamRange(c interface {
	Header(string, string)
	Status(int)
}, writer io.Writer, entry core.FidEntry, stream *core.Stream, ranged bool, rng handler.ByteRange, size int64, attachAs string, logger *slog.Logger) {
	length, err := num.Narrow[int64](stream.Remaining())
	if err != nil {
		CloseStream(stream, entry.Name, logger)
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(entry.Name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
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
	defer CloseStream(stream, entry.Name, logger)
	if _, err := io.CopyN(writer, &sharedLoggedStream{inner: stream, name: entry.Name, logger: logger}, length); err != nil && !errors.Is(err, io.EOF) && logger != nil {
		logger.Warn("copying a download ended early", "name", entry.Name, "error", err)
	}
}

type sharedLoggedStream struct {
	inner  *core.Stream
	name   string
	logger *slog.Logger
}

func (s *sharedLoggedStream) Read(p []byte) (int, error) {
	n, err := s.inner.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && s.logger != nil {
		s.logger.Warn("a download ended early", "name", s.name, "error", err)
	}
	return n, err
}

func (s *sharedLoggedStream) Close() error { return s.inner.Close() }
