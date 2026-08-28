// Linux only: it names types that are openat2 handles underneath.
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

// Filling the index from what is on disk.
//
// A query answers from the index when there is one and walks when there is
// not, so this is what moves a deployment from the second to the first. It runs
// once on an administrator's request rather than continuously: the watcher
// keeps the index current after that, and a rebuild is for a corpus that
// changed underneath the server.
//
// This is the third walk, and deliberately its own: it streams into segment
// writes with its own batching, which neither the query walk nor the
// estimator's scan does.

// buildBatch is how many entries are appended at a time.
//
// Batched because each append writes a record and takes the index's lock, so
// one per file would make the build a sequence of tiny writes with a query
// blocked behind each. Bounded because the batch is held in memory.
const buildBatch = 10_000

// ErrNoIndex is a build with no index open to build into.
var ErrNoIndex = errors.New("search: no index is open, so there is nothing to build into")

// BuildProgress reports a build as it runs, so a long one is visible rather
// than a request that has not answered yet.
type BuildProgress struct {
	Files   uint64
	Dirs    uint64
	Partial bool
}

// builder carries one build's state, so the walk below is a method rather than
// a closure over six variables.
type builder struct {
	ix       *index.NameIndex
	gate     func() bool
	report   func(BuildProgress)
	ceiling  uint64
	batch    []index.Entry
	progress BuildProgress
}

// Build walks every source and appends what it finds.
//
// The gate is asked between directories and a false answer ends the build. It
// is how a cancellation reaches here, and how a build yields to load: this
// walks the whole corpus, and doing that while people are working is the thing
// that makes a search feature something an administrator turns off.
func (s *Service) Build(
	ctx context.Context, sources []search.Source, gate func() bool, report func(BuildProgress),
) (BuildProgress, error) {
	ix := s.index()
	if ix == nil {
		return BuildProgress{}, ErrNoIndex
	}

	// A rebuild starts from whatever the previous one left. If that one
	// stopped at the ceiling and this one does not, the index is complete
	// again, and a flag nothing clears would make every query walk forever.
	ix.SetIncomplete(false)

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

	// A build appends every entry as one delta on top of whatever was there,
	// which is exactly the shape a merge exists to collapse. Skipping it
	// leaves the index correct and every query paying for it.
	if ix.NeedsMerge() {
		if err := ix.Merge(ctx, gate); err != nil {
			return b.progress, fmt.Errorf("merging the index after the build: %w", err)
		}
	}
	return b.progress, nil
}

// walkSource indexes one share. It reports done when the build should stop,
// which is either the gate refusing or the entry ceiling being reached.
func (b *builder) walkSource(ctx context.Context, src search.Source) (bool, error) {
	if src.Root == nil {
		return false, nil
	}
	stack := []vfs.SafePath{src.Base}
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if b.gate != nil && !b.gate() {
			// Stopped rather than failed. What was appended is real and
			// stays: the index is allowed to hold less than the corpus,
			// because a query that misses falls back to a walk.
			return true, nil
		}

		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		// The server's own control directories are not corpus. Indexing
		// them would return them from a query.
		entries, rerr := src.Root.ReadDir(dir, vfs.HideReserved)
		if rerr != nil {
			// A directory that cannot be read is skipped rather than
			// failing the build: the rest of the corpus is still worth
			// indexing, and a query for what was skipped falls back.
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
				continue
			}
			if b.progress.Files >= b.ceiling {
				// The bound is reached. What was indexed stays, and the
				// index is marked short of its corpus so every query
				// declines and walks instead: answering from the part of
				// the tree that was reached returns a result missing the
				// rest, with a success status and nothing saying so.
				b.progress.Partial = true
				b.ix.SetIncomplete(true)
				return true, nil
			}

			b.progress.Files++
			b.batch = append(b.batch, index.Entry{Share: src.Share, Path: child.String()})
			if len(b.batch) >= buildBatch {
				if err := b.flush(); err != nil {
					return false, err
				}
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
	if b.report != nil {
		b.report(b.progress)
	}
	return nil
}
