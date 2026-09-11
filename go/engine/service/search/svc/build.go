// Builds only on Linux, where the types it names are openat2 handles beneath.
//go:build linux

package svc

import (
	"context"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/index"
)

// Populating the index from what exists on disk.
//
// A query uses the index when one exists and walks when none does, so this is
// what moves a deployment from the latter to the former. It runs once at an
// administrator's request rather than continuously: afterwards the watcher keeps
// the index current, and a rebuild addresses a corpus that changed beneath the
// server.
//
// This is the third walk and remains deliberately separate, since it streams
// into segment writes with its own batching, which neither the query walk nor
// the estimator's scan does.

// buildBatch sets how many entries are appended together.
//
// Batching exists because every append writes a record and acquires the index's
// lock, so appending per file would reduce the build to a stream of tiny writes
// with a query stalled behind each one. The bound exists because the batch is
// held in memory.
const buildBatch = 10_000

// buildReportEvery sets how many indexed names pass between progress reports.
// Progress used to travel only with a batch write, so a corpus smaller than
// one batch reported nothing at all until it was already finished, and the
// screen watching it had a spinner and no numbers for the whole run.
const buildReportEvery = 500

// ErrNoIndex is a build with no index open to build into.
var ErrNoIndex = errors.New("search: no index is open, so there is nothing to build into")

// BuildProgress reports on a build while it runs, so a lengthy one is observable
// rather than appearing as a request that has yet to answer.
type BuildProgress struct {
	// Files counts indexed names, folders among them, since a folder's name
	// is indexed like a file's. It is also what the entry ceiling is
	// measured against.
	Files uint64
	// Dirs counts directories read, which is a traversal cost rather than a
	// count of what was indexed.
	Dirs    uint64
	Partial bool
}

// builder carries one build's state, so the walk below is a method rather than
// a closure over six variables.
type builder struct {
	ix      *index.NameIndex
	gate    func() bool
	report  func(BuildProgress)
	ceiling uint64
	batch   []index.Entry
	// reported is the file count the last report carried, so the next one is
	// due a fixed number of names later rather than when a batch happens to
	// fill.
	reported uint64
	progress BuildProgress
}

// Build traverses every source and appends what it discovers.
//
// The gate is consulted between directories and a negative answer ends the
// build. That is how cancellation arrives here, and how a build defers to load:
// it traverses the entire corpus, and doing so while people are working is
// exactly what turns a search feature into something an administrator
// disables.
func (s *Service) Build(
	ctx context.Context, sources []search.Source, gate func() bool, report func(BuildProgress),
) (BuildProgress, error) {
	ix := s.index()
	if ix == nil {
		return BuildProgress{}, ErrNoIndex
	}

	// A rebuild never makes a partial index look complete. It becomes eligible
	// only after every source was traversed and the resulting segment published.
	// Keeping the flag set through all early exits also covers caller
	// cancellation, a refused gate, unreadable subtrees, and a failed merge.
	ix.SetIncomplete(true)

	b := &builder{
		ix:      ix,
		gate:    gate,
		report:  report,
		ceiling: limits.CorpusScanEntries,
		batch:   make([]index.Entry, 0, buildBatch),
	}

	for _, src := range sources {
		done, err := b.walkSource(ctx, src)
		if err != nil {
			return b.progress, err
		}
		if done {
			return b.progress, b.flush()
		}
	}

	if err := b.flush(); err != nil {
		return b.progress, err
	}

	// A build appends every entry as a single delta over whatever preceded it,
	// precisely the shape a merge exists to collapse. Merged unconditionally
	// rather than only past the ratio: the merge is what folds the previous
	// build's copy of a name into this one, so without it a rebuild leaves
	// two rows per file and the index reports twice the corpus it holds.
	if gate != nil && !gate() {
		b.progress.Partial = true
		return b.progress, nil
	}
	if err := ix.Merge(ctx, gate); err != nil {
		return b.progress, fmt.Errorf("merging the index after the build: %w", err)
	}
	if gate != nil && !gate() {
		b.progress.Partial = true
		return b.progress, nil
	}
	if !b.progress.Partial {
		ix.SetIncomplete(false)
	}
	return b.progress, nil
}

// walkSource indexes one share. It reports done when the build should stop,
// which is either the gate refusing or the entry ceiling being reached.
func (b *builder) walkSource(ctx context.Context, src search.Source) (bool, error) {
	if src.Root == nil {
		// A missing root is not an empty share. It is coverage we could not
		// inspect, so a build containing one cannot be published as complete.
		b.progress.Partial = true
		return false, nil
	}

	stack := []vfs.SafePath{src.Base}
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			b.progress.Partial = true
			return false, err
		}
		if b.gate != nil && !b.gate() {
			b.progress.Partial = true
			// Stopped rather than failed. Whatever was appended is genuine,
			// but the index does not cover the remainder of the corpus.
			return true, nil
		}

		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, rerr := src.Root.ReadDir(dir, vfs.HideReserved)
		if rerr != nil {
			// An unreadable directory is skipped rather than failing the
			// entire build, but it prevents claiming full coverage.
			b.progress.Partial = true
			continue
		}
		b.progress.Dirs++

		for _, e := range entries {
			child, jerr := dir.JoinExisting(e.Name)
			if jerr != nil {
				continue
			}
			if e.Kind.IsDir() {
				stack = append(stack, child)
			}
			if b.progress.Files >= b.ceiling {
				// The bound has been reached. What was indexed remains, and
				// the index is flagged as covering less than its corpus so
				// every query declines in favour of walking. Answering from
				// the portion of the tree that was reached would return a
				// result missing the rest, reporting success with nothing to
				// indicate otherwise.
				b.progress.Partial = true
				b.ix.SetIncomplete(true)
				return true, nil
			}

			// Folders are indexed too, and descended into. A name search
			// reports the folder someone named, so an index that held only
			// files answered the same question with a shorter list than the
			// walk did, and which one a client got depended on whether an
			// administrator had turned the index on.
			b.progress.Files++
			b.batch = append(b.batch, index.Entry{Share: src.Share, Path: child.String()})
			if len(b.batch) >= buildBatch {
				if err := b.flush(); err != nil {
					return false, err
				}
			}
			if b.progress.Files-b.reported >= buildReportEvery {
				b.tell()
			}
		}
	}
	return false, nil
}

// flush appends the batch and reports progress.
func (b *builder) flush() error {
	if len(b.batch) == 0 {
		return nil
	}
	if err := b.ix.Append(b.batch); err != nil {
		return fmt.Errorf("appending to the index: %w", err)
	}
	b.batch = b.batch[:0]
	b.tell()
	return nil
}

// tell hands the current counters to the caller watching the build.
func (b *builder) tell() {
	b.reported = b.progress.Files
	if b.report != nil {
		b.report(b.progress)
	}
}
