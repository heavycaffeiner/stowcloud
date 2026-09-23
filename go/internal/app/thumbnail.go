//go:build linux

// Thumbnails: a re-encode produced by a jailed decoder.
//
// Addressed by path like every other read, not by a file id. An id is
// allocated lazily, so a plain listing carries none and a thumbnail could not
// be asked for until the file had been shared.
//
// Served from this origin rather than the content origin, unlike the file
// bytes. What travels here is a PNG this process wrote from pixels the
// decoder produced in its jail: none of the caller's bytes, and none of their
// metadata, reach the response.
package app

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/preview"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
)

// filesThumbnail answers with a thumbnail of the addressed file.
func (e *Engine) filesThumbnail(c *gin.Context) {
	lease, ok := e.previewLease()
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.NotFound})
		return
	}
	defer lease.Close()

	// Unreachable through the mounted chain, measured: the route requires a
	// credential and the middleware refuses first, so removing this changes
	// no answer. Kept because a handler that reads an owner it did not check
	// for would resolve as account zero if it were ever reached another way.
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	preset, perr := presetOf(c.Query("size"))
	if perr != nil {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	// The row's own sealed reference rather than a path. A grid composes one
	// of these per visible tile, and a path it joined itself is a path it can
	// join wrongly: the tile then shows another file's picture, which is the
	// least visible failure this interface has.
	//
	// The same two bits the file itself needs. A thumbnail is made out of the
	// bytes, so handing one to an account that cannot open the file hands it
	// a downscaled copy of what it was refused.
	claim, ok := e.openBoundClaim(c, handler.PurposeThumb, owner)
	if !ok {
		fail(c, core.ErrNotFound)
	}

	// Resolved again rather than trusted from the claim: a grant revoked
	// since the listing has to refuse the preview too.
	r, err := e.resolve(owner, claim.Path, acl.Read|acl.Download)
	if err != nil {
		fail(c, err)
	}

	// Checked before the preview service is touched at all, not inside it:
	// a decode attempt against ciphertext would fail the same way a
	// corrupt file does, and the preview service's negative cache would
	// then remember that failure past the point someone turns encryption
	// back off. The bytes behind an encrypted share are ciphertext this
	// server holds no key for, so there is nothing to decode in the first
	// place.
	if enc, eerr := e.Core.ShareEncrypted(c.Request.Context(), r.Share()); eerr != nil {
		fail(c, eerr)
	} else if enc {
		refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	thumb, terr := lease.Get(c.Request.Context(), r, preset)
	if terr != nil {
		// Every preview condition is already classified, including a file
		// nothing can decode and a worker that died. Re-deciding here would
		// be a second answer to the same question.
		//
		// Measured against a running server: a text file answers 501 "not
		// supported by this build", which describes the build rather than
		// the file. The status is defensible and the message is not, and the
		// message belongs to the classifier, which drops the sentinel's own
		// key for the class's generic one. Left alone because it is true of
		// the whole preview family rather than of this route.
		fail(c, terr)
	}

	e.sendThumb(c, thumb)
}

// sendThumb writes the thumbnail and releases it.
func (e *Engine) sendThumb(c *gin.Context, thumb preview.Thumb) {
	size, serr := thumbSize(thumb.File)
	if serr != nil {
		e.closeThumb(thumb)
		fail(c, serr)
		return
	}

	// PNG is what the encoder writes: lossless, keeps alpha, and carries no
	// metadata of its own, so stripping EXIF is a matter of never copying it
	// rather than of removing it afterwards.
	c.Header("Content-Type", "image/png")
	// Immutable because the cache key covers the identity, the mtime and the
	// size, so a changed file is a different thumbnail rather than a stale
	// hit. Private because the bytes are one account's to see.
	c.Header("Cache-Control", "private, max-age=86400, immutable")

	// No nosniff here: the chain sets it on every response, and a second
	// writer would be a second place for it to stop being set.

	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Status(http.StatusOK)
	defer e.closeThumb(thumb)
	if _, err := io.CopyN(c.Writer, &loggedThumb{inner: thumb, logger: e.logger}, size); err != nil && !errors.Is(err, io.EOF) {
		e.logger.Warn("a thumbnail ended early", "error", err)
	}
}

// loggedThumb reads a thumbnail and reports a read that ended early.
//
// A short body still carries a Content-Length promising more, so the client
// sees a truncated image. Without this the server would have no record of
// which file it happened on.
type loggedThumb struct {
	inner  preview.Thumb
	logger *slog.Logger
}

func (t *loggedThumb) Read(p []byte) (int, error) {
	n, err := t.inner.File.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		t.logger.Warn("a thumbnail ended early", "error", err)
	}
	return n, err
}

func (t *loggedThumb) Close() error { return t.inner.Close() }

// closeThumb releases a thumbnail whose bytes will not be sent.
func (e *Engine) closeThumb(thumb preview.Thumb) {
	if err := thumb.Close(); err != nil {
		e.logger.Warn("closing a thumbnail", "error", err)
	}
}

// thumbSize measures the thumbnail so its length can be declared.
func thumbSize(f *os.File) (int64, error) {
	if f == nil {
		return 0, errors.New("preview: the thumbnail carries no file")
	}
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	// Narrowed rather than converted: the length reaches an int, and a size
	// that does not fit would wrap into a short body served as though it were
	// whole.
	return num.Narrow[int64](st.Size())
}

// presetOf maps the query value onto a size.
//
// An absent value is the grid thumbnail, which is what a listing asks for.
// An unrecognised one is refused rather than rounded to the nearest, since a
// client asking for a size nobody defined has a bug the answer should show.
func presetOf(s string) (preview.Preset, error) {
	switch s {
	case "", "small":
		return preview.PresetSmall, nil
	case "medium":
		return preview.PresetMedium, nil
	case "large":
		return preview.PresetLarge, nil
	}
	return 0, errors.New("preview: unknown size")
}

// openPreview builds the decoder pool and its cache.
//
// A nil result is a deployment with no thumbnails, not a broken one. Every
// failure here is about the host rather than the request: no worker binary,
// no room for a cache. Refusing to boot over it would take down a server that
// can still serve every file it holds.
func openPreview(
	thumbsDir, worker string, c *core.Core, clk clock.Clock, log *slog.Logger,
) *preview.Service {
	opt := preview.PoolOptions{Clock: clk}
	if worker != "" {
		// Only the binary. The pool supplies its default argument either way,
		// and the shipped decoder reads no argv at all: its socket arrives on
		// a fixed descriptor, which is what leaves it no way to name a file.
		opt.Exe = worker
	}
	pool, perr := preview.NewPool(opt)
	if perr != nil {
		log.Warn("thumbnails are unavailable: the decoder pool did not open",
			"error", perr)
		return nil
	}

	cache, cerr := preview.NewCache(thumbsDir)
	if cerr != nil {
		log.Warn("thumbnails are unavailable: the cache directory did not open",
			"error", cerr)
		if clerr := pool.Close(); clerr != nil {
			log.Warn("closing the decoder pool", "error", clerr)
		}
		return nil
	}

	svc, serr := preview.NewService(preview.ServiceOptions{
		Core: c, Pool: pool, Cache: cache, Clock: clk,
	})
	if serr != nil {
		log.Warn("thumbnails are unavailable", "error", serr)
		if clerr := pool.Close(); clerr != nil {
			log.Warn("closing the decoder pool", "error", clerr)
		}
		return nil
	}
	return svc
}
