// Builds only on Linux, like the walk it shares a Source with, because a source
// names a *vfs.ShareRoot, an openat2 handle that exists on no other platform.
//go:build linux

package search

import (
	"context"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// Measuring a corpus so the index can be sized before it is built.
//
// Distinct from Walk, which serves a query and halts at a limit. This visits
// everything and scores nothing, because the two deciding terms are the total
// name bytes and how many distinct trigrams those names contain, neither of
// which a partial view can reveal. It is also the cheapest of the three walks:
// it must never start the query walk's worker pool merely to report a corpus's
// size.
//
// The trigram count comes from the sketch rather than a set, which is precisely
// what makes this affordable. A set holding every distinct trigram in a large
// corpus is itself large, and this runs to tell an administrator whether they
// have the disk for the index.

// ScanOptions limits a measurement.
type ScanOptions struct {
	// MaxEntries halts the scan, and zero selects the compiled-in bound.
	//
	// A scan that ran out remains useful: it measured a genuine sample, and the
	// result declares itself partial rather than presenting the fraction it saw
	// as the whole.
	MaxEntries int64
	// SketchPrecision sizes the distinct-trigram sketch, and zero selects the
	// default the estimator was calibrated against.
	SketchPrecision uint8
}

// ScanResult describes a measured corpus.
type ScanResult struct {
	Stats CorpusStats
	// Partial indicates the bound stopped the scan, so the statistics describe a
	// sample rather than the corpus. The caller discloses that instead of
	// presenting a fraction as if it were the whole.
	Partial     bool
	DirsVisited int64
}

// ScanCorpus measures every name the sources can reach.
//
// Directories count as entries while their names go unindexed, matching what the
// index holds, since a query searches for files.
func ScanCorpus(ctx context.Context, sources []Source, opt ScanOptions) (ScanResult, error) {
	limit := opt.MaxEntries
	if limit <= 0 {
		limit = limits.CorpusScanEntries
	}
	// Narrowed once here rather than converted at the comparison inside the
	// loop, so the bound crosses into the counter's own type in one checked
	// place.
	maxFiles, err := num.Narrow[uint64](limit)
	if err != nil {
		return ScanResult{}, fmt.Errorf("search: a scan bound of %d: %w", limit, err)
	}
	precision := opt.SketchPrecision
	if precision == 0 {
		precision = HLLDefaultPrecision
	}

	sketch := NewHLL(precision)

	var out ScanResult
	// Reused for every name, so a large corpus costs a single buffer instead of
	// one allocation per entry.
	var folded []byte

	for _, src := range sources {
		if src.Root == nil {
			continue
		}
		// Deliberately single-threaded. This is not the query path: it runs when
		// an administrator asks what an index would cost, and that answer
		// matters less than the service it would otherwise slow.
		stack := []vfs.SafePath{src.Base}
		for len(stack) > 0 {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			dir := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			// The server's own control directories fall outside the corpus,
			// since indexing them would size the index against files no query
			// can return.
			entries, rerr := src.Root.ReadDir(dir, vfs.HideReserved)
			if rerr != nil {
				// An unreadable directory is skipped rather than failing the
				// measurement, since a permission the scan lacks is no reason
				// to withhold an estimate covering everything else.
				continue
			}
			out.DirsVisited++

			for _, e := range entries {
				if out.Stats.Files >= maxFiles {
					out.Partial = true
					return finishScan(out, sketch), nil
				}
				child, jerr := dir.JoinExisting(e.Name)
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
				Trigrams(folded, func(t Trigram) {
					b := t.Bytes()
					sketch.AddHash(Hash64(b[:]))
				})
			}
		}
	}
	return finishScan(out, sketch), nil
}

func finishScan(out ScanResult, sketch *HLL) ScanResult {
	out.Stats.DistinctTrigramsEst = sketch.EstimateUint()
	// Nothing measured compression, so the estimator is told as much rather than
	// handed a ratio nobody observed. It falls back to its analytic model and
	// labels the answer modelled.
	out.Stats.SampleCompressRatio = 1
	return out
}
