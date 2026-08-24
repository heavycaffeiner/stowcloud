// Linux only, like the walk it shares a Source with: a source names a
// *vfs.ShareRoot, which is an openat2 handle and exists on no other platform.
//go:build linux

package search

import (
	"context"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Measuring a corpus, so the index can be sized before one is built.
//
// Separate from Walk, which answers a query and stops at a limit. This visits
// everything and scores nothing, because the two terms that decide the answer
// are the total name bytes and how many distinct trigrams those names hold, and
// neither is knowable from a partial view.
//
// The trigram count comes from the sketch rather than a set, which is the whole
// reason this is affordable: a set of every distinct trigram in a large corpus
// is itself large, and this runs to tell an administrator whether they have the
// disk for the index.

// ScanOptions bounds a measurement.
type ScanOptions struct {
	// MaxEntries stops the scan. Zero takes the compiled-in bound.
	//
	// A scan that ran out is still useful: it measured a real sample, and the
	// result says it was partial rather than reporting the fraction it saw as
	// the whole.
	MaxEntries int64
	// SketchPrecision sizes the distinct-trigram sketch. Zero takes the
	// default, which is what the estimator was calibrated against.
	SketchPrecision uint8
}

// defaultSketchPrecision costs sixteen kilobytes and estimates within about
// one percent, which is finer than the estimate it feeds.
const defaultSketchPrecision = 14

// ScanResult is a measured corpus.
type ScanResult struct {
	Stats CorpusStats
	// Partial reports that the bound stopped the scan, so the statistics
	// describe a sample rather than the corpus. The caller says so rather than
	// presenting a fraction as the whole.
	Partial     bool
	DirsVisited int64
}

// ScanCorpus measures every name reachable from the sources.
//
// Directories are counted as entries but their names are not indexed, matching
// what the index holds: a query looks for files.
func ScanCorpus(ctx context.Context, sources []Source, opt ScanOptions) (ScanResult, error) {
	limit := opt.MaxEntries
	if limit <= 0 {
		limit = limits.CorpusScanEntries
	}
	precision := opt.SketchPrecision
	if precision == 0 {
		precision = defaultSketchPrecision
	}

	sketch := NewHLL(precision)

	var out ScanResult
	// Reused across every name, so a large corpus costs one buffer rather than
	// one allocation per entry.
	var folded []byte

	for _, src := range sources {
		// Single-threaded on purpose. This is not the query path: it runs when
		// an administrator asks what an index would cost, and the answer is
		// worth less than the service it would slow down.
		stack := []vfs.SafePath{src.Base}
		for len(stack) > 0 {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			dir := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			// The server's own control directories are not corpus: indexing
			// them would size the index against files no query can return.
			entries, rerr := src.Root.ReadDir(dir, vfs.HideReserved)
			if rerr != nil {
				// A directory that cannot be read is skipped rather than
				// failing the measurement: a permission the scan lacks is not
				// a reason to refuse an estimate for everything else.
				continue
			}
			out.DirsVisited++

			for _, e := range entries {
				if out.Stats.Files >= uint64(limit) { //nolint:gosec // the bound is a positive constant, checked above.
					out.Partial = true
					return finish(out, sketch), nil
				}
				child, jerr := dir.Join(e.Name)
				if jerr != nil {
					continue
				}
				if src.Allow != nil && !src.Allow(child, e.Kind.IsDir()) {
					continue
				}
				if e.Kind.IsDir() {
					stack = append(stack, child)
					continue
				}

				out.Stats.Files++
				folded = Fold([]byte(e.Name))
				out.Stats.NameBytesTotal += uint64(len(folded))
				Trigrams(folded, func(t Trigram) { sketch.AddHash(Hash64(t[:])) })
			}
		}
	}
	return finish(out, sketch), nil
}

func finish(out ScanResult, sketch *HLL) ScanResult {
	out.Stats.DistinctTrigramsEst = sketch.EstimateUint()
	// No compression measured, so the estimator is told so rather than being
	// handed a ratio nothing observed. It falls back to its analytic model and
	// reports the answer as modelled.
	out.Stats.SampleCompressRatio = 1
	return out
}
