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
	"log/slog"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/preview"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/clock"
)

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
