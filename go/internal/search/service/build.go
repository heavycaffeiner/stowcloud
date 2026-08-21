package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/search"
	"github.com/heavycaffeiner/stowcloud/go/internal/search/index"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Filling the index from what is on disk.
//
// A query answers from the index when there is one and walks when there is
// not, so this is what moves a deployment from the second to the first. It runs
// once on an administrator's request rather than continuously: the watcher
// keeps the index current after that, and a rebuild is for a corpus that
// changed underneath the server.

// buildBatch is how many entries are appended at a time.
//
// Batched because each append writes a record and takes the index's lock, so
// one per file would make the build a sequence of tiny writes with a query
// blocked behind each. Bounded because the batch is held in memory.
const buildBatch = 10_000

// BuildProgress reports a build as it runs, so a long one is visible rather
// than a request that has not answered yet.
type BuildProgress struct {
	Files   int64
	Dirs    int64
	Partial bool
}

// Build walks every source and appends what it finds.
//
// The gate is asked between batches and a false answer ends the build. It is
// how a cancellation reaches here, and how a build yields to load: this walks
// the whole corpus, and doing that while people are working is the thing that
// makes a search feature something an administrator turns off.
func (s *Service) Build(ctx context.Context, sources []search.Source, gate func() bool, report func(BuildProgress)) (BuildProgress, error) {
	ix := s.index()
	if ix == nil {
		return BuildProgress{}, errors.New("search: no index is open, so there is nothing to build into")
	}

	var progress BuildProgress
	batch := make([]index.Entry, 0, buildBatch)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := ix.Append(batch); err != nil {
			return fmt.Errorf("appending to the index: %w", err)
		}
		batch = batch[:0]
		if report != nil {
			report(progress)
		}
		return nil
	}

	for _, src := range sources {
		stack := []vfs.SafePath{src.Base}
		for len(stack) > 0 {
			if err := ctx.Err(); err != nil {
				return progress, err
			}
			if gate != nil && !gate() {
				// Stopped rather than failed. What was appended is real and
				// stays: the index is allowed to hold less than the corpus,
				// because a query that misses falls back to a walk.
				return progress, flush()
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
			progress.Dirs++

			for _, e := range entries {
				child, jerr := dir.Join(e.Name)
				if jerr != nil {
					continue
				}
				if e.Kind.IsDir() {
					stack = append(stack, child)
					continue
				}
				if progress.Files >= limits.CorpusScanEntries {
					// The bound is reached. What was indexed stays and the
					// caller is told it is partial, because a query for the
					// rest falls back to a walk rather than answering wrongly.
					progress.Partial = true
					return progress, flush()
				}

				progress.Files++
				batch = append(batch, index.Entry{Share: src.Share, Path: child.String()})
				if len(batch) >= buildBatch {
					if err := flush(); err != nil {
						return progress, err
					}
				}
			}
		}
	}

	if err := flush(); err != nil {
		return progress, err
	}

	// A build appends every entry as one delta on top of whatever was there,
	// which is exactly the shape a merge exists to collapse. Skipping it
	// leaves the index correct and every query paying for it.
	if ix.NeedsMerge() {
		if err := ix.Merge(ctx, gate); err != nil {
			return progress, fmt.Errorf("merging the index after the build: %w", err)
		}
	}
	return progress, nil
}
