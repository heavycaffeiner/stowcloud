//go:build linux

// The optional name index, and what building one would cost.
//
// The index is an escalation, not the default. Search works by walking, and
// an index is what an operator adds once measurement shows the walk is too
// slow for their corpus. Sizing it before building it is the point of the
// estimate: the build traverses everything, and an operator deciding whether
// to spend that wants the number first.
package lifecycle

import (
	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
)

// adminIndexEstimate measures the corpus and reports what an index would cost.
//
// Every share is measured, not the caller's own view. The index covers the
// whole deployment, so a figure taken from one account's shares would be
// smaller than the index that gets built.
func (e *Engine) adminIndexEstimate(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	sources := indexSourcesOf(e.Core.ScanSources())
	result, err := search.ScanCorpus(c.UserContext(), sources, search.ScanOptions{})
	if err != nil {
		return failKnown(c, err)
	}

	// A measurement from this deployment's own disk and corpus beats the
	// compiled-in guess; an unreadable rate falls back to it the same way an
	// unset one does.
	rate, rerr := e.State.IndexBuildRate(c.UserContext())
	if rerr != nil {
		rate = 0
	}
	estimate := search.EstimateNameIndex(result.Stats, indexBlockSize, rate)
	return writeJSON(c, fiber.StatusOK, handler.IndexEstimateOf(result, estimate))
}

// indexBlockSize is how many entries share a block. It is the value the
// estimator was calibrated against, so passing anything else would report a
// size for an index this build does not produce.
const indexBlockSize = 1024

// indexSourcesOf adapts the core's scan sources with no permission filter.
//
// Nil Allow is the administrator-scoped form, and the walker skips the call
// entirely rather than treating nil as a closure returning true. The prefix
// is empty because nothing here reports a path: the scan counts names and
// measures bytes.
func indexSourcesOf(scan []core.ScanSource) []search.Source {
	out := make([]search.Source, 0, len(scan))
	for _, s := range scan {
		if s.Root == nil {
			// A share whose backing did not open. Counted as absent rather
			// than as an empty share, since the walker would skip it anyway
			// and an unopenable root is not zero files.
			continue
		}
		out = append(out, search.Source{
			Share: uint32(s.Share),
			Root:  s.Root,
			Base:  s.Base,
		})
	}
	return out
}
