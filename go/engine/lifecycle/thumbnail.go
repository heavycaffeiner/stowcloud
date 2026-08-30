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
package lifecycle

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/preview"
)

// filesThumbnail answers with a thumbnail of the addressed file.
func (e *Engine) filesThumbnail(c *fiber.Ctx) error {
	if e.Preview == nil {
		// A deployment that runs no decoder. Absent rather than broken: the
		// listing says the same thing, so a client reading it never asks.
		return refuse(c, apierr.Classified{Class: apierr.NotFound})
	}

	// Unreachable through the mounted chain, measured: the route requires a
	// credential and the middleware refuses first, so removing this changes
	// no answer. Kept because a handler that reads an owner it did not check
	// for would resolve as account zero if it were ever reached another way.
	owner, ok := ownerOf(c)
	if !ok {
		return refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	preset, perr := presetOf(c.Query("size"))
	if perr != nil {
		return refuse(c, apierr.Classified{Class: apierr.Unprocessable})
	}

	// The same two bits the file itself needs. A thumbnail is made out of the
	// bytes, so handing one to an account that cannot open the file hands it
	// a downscaled copy of what it was refused.
	//
	// Redundant, measured: the service requires the same two bits and refuses
	// first, so weakening this to Read alone changes no answer. It is named
	// here so the refusal happens before a descriptor is opened, and because
	// what the bytes require is not this handler's to decide.
	r, err := e.resolve(owner, c.Query("path"), acl.Read|acl.Download)
	if err != nil {
		return fail(c, err)
	}

	thumb, terr := e.Preview.Get(c.UserContext(), r, preset)
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
		return fail(c, terr)
	}

	return e.sendThumb(c, thumb)
}

// sendThumb writes the thumbnail and releases it.
func (e *Engine) sendThumb(c *fiber.Ctx, thumb preview.Thumb) error {
	size, serr := thumbSize(thumb.File)
	if serr != nil {
		e.closeThumb(thumb)
		return fail(c, serr)
	}

	// PNG is what the encoder writes: lossless, keeps alpha, and carries no
	// metadata of its own, so stripping EXIF is a matter of never copying it
	// rather than of removing it afterwards.
	c.Set(fiber.HeaderContentType, "image/png")
	// Immutable because the cache key covers the identity, the mtime and the
	// size, so a changed file is a different thumbnail rather than a stale
	// hit. Private because the bytes are one account's to see.
	c.Set(fiber.HeaderCacheControl, "private, max-age=86400, immutable")

	// No nosniff here: the chain sets it on every response, and a second
	// writer would be a second place for it to stop being set.

	// The sized form for the same reason the file read uses it: a stream
	// writer forces chunked encoding and drops the Content-Length, leaving a
	// client nothing to preallocate against and no way to notice a transfer
	// that ended early.
	c.Status(fiber.StatusOK)
	// fasthttp closes the stream once it has read it, and a thumbnail holds an
	// open file, so the descriptor is released without a second close here.
	c.Context().SetBodyStream(&loggedThumb{
		inner:  thumb,
		logger: e.logger,
	}, int(size))
	return nil
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
	dataDir, worker string, workerArgs []string,
	c *core.Core, clk clock.Clock, log *slog.Logger,
) *preview.Service {
	opt := preview.PoolOptions{Clock: clk}
	if len(workerArgs) > 0 {
		// The caller named the argv, which is how a harness on a kernel that
		// refuses the confinement says so. Empty leaves the pool's default.
		opt.Args = workerArgs
	}
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

	cache, cerr := preview.NewCache(filepath.Join(dataDir, "thumbs"))
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
