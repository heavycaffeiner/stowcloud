//go:build linux

package search

import (
	"context"
	"sort"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
)

// The parallel walk, which is the query tier.
//
// A bounded worker pool operating over the share's own directory handles. The
// walker is written here instead of taken from a library, and that is a security
// decision ahead of a performance one: filepath.WalkDir and everything built on
// it resolve a path per entry, reintroducing symlink escape and TOCTOU by
// bypassing the openat2 invariant. Walking through directory handles also skips
// the whole-path re-resolution a path-based walker pays for each entry, so the
// safe choice is also the quicker one.
//
// This is one of three walks and remains distinct. The estimator's ScanCorpus
// exists to be cheap and must not start a worker pool merely to report a
// corpus's size, while the ingest walk streams into segment writes with its own
// batching. They share the leaf vocabulary below and nothing more.

// Hit is one match.
type Hit struct {
	Share uint32
	Path  string
	Name  string
	IsDir bool
	// Size and MTimeNs stay nil unless the stat phase executed. A name-only query
	// never stats, so they remain nil and the ranking's recency term is zero.
	Size    *uint64
	MTimeNs *int64
	Score   float32
}

// WalkOptions bounds one walk.
type WalkOptions struct {
	// Needle is the folded query.
	Needle []byte
	// Limit bounds the result set. A truncated result declares itself rather than
	// appearing complete.
	Limit int
	// Scope names the directory the caller is searching from, feeding the
	// ranking's in-scope term.
	Scope string
	// Threads is the worker count. Zero takes one.
	Threads int
	// WithMetadata enables the stat phase. A name-only query leaves it disabled,
	// since published measurement places metadata at roughly half a walk's cost
	// and statting for information nobody requested doubles the price.
	WithMetadata bool
	// NowNs feeds the recency term.
	NowNs int64
}

// WalkResult holds what a walk produced.
type WalkResult struct {
	Hits []Hit
	// Truncated indicates the limit shortened the result, letting a caller
	// disclose that instead of presenting a partial answer as complete.
	Truncated bool
	// DirsVisited and EntriesSeen record what it cost.
	DirsVisited int64
	EntriesSeen int64
}

// pending holds a matched entry awaiting the stat phase.
type pending struct {
	src    int
	dev    uint64
	ino    uint64
	hasIno bool
	dirSeq uint64
	entSeq uint32
	path   vfs.SafePath
	name   string
	isDir  bool

	// Populated by the stat phase, and nil when that did not run.
	statSize  *uint64
	statMTime *int64
}

// job is a single unit of work, meaning one directory. Parallelism occurs at
// directory boundaries and nowhere else.
type job struct {
	src   int
	path  vfs.SafePath
	depth int
}

// Walk searches every source.
func Walk(ctx context.Context, sources []Source, opt WalkOptions) (WalkResult, error) {
	if opt.Threads <= 0 {
		opt.Threads = 1
	}
	if opt.Limit <= 0 {
		opt.Limit = limits.SearchResults
	}

	w := &walker{
		sources: sources,
		opt:     opt,
		queue:   make([]job, 0, 64),
	}
	w.idle = sync.NewCond(&w.mu)
	for i, s := range sources {
		if s.Root == nil {
			// A broken share. The adapter drops these, and a caller building
			// sources by hand gets the same treatment rather than a panic.
			continue
		}
		w.queue = append(w.queue, job{src: i, path: s.Base})
	}

	w.run(ctx)
	if err := ctx.Err(); err != nil {
		return WalkResult{}, err
	}

	if opt.WithMetadata {
		w.stat()
	}
	return w.finish(), nil
}

type walker struct {
	sources []Source
	opt     WalkOptions

	mu   sync.Mutex
	idle *sync.Cond
	// busy counts workers currently inside a directory, which is what tells an
	// idle worker whether further work may still materialise.
	busy    int
	stopped bool
	queue   []job
	pending []pending
	dirs    int64
	entries int64
	dirSeq  uint64
}

// run empties the queue using a bounded pool.
//
// A worker encountering an empty queue stops, so the pool finishes when the tree
// does. Idle workers are counted, because one stopping while another was still
// pushing children would end the walk prematurely.
func (w *walker) run(ctx context.Context) {
	var wg sync.WaitGroup
	for range w.opt.Threads {
		wg.Add(1)
		task.Go(ctx, "search: walk worker", func() {
			defer wg.Done()
			for {
				j, ok := w.take()
				if !ok {
					return
				}
				// Cancellation is polled per directory rather than per entry. A
				// search the client abandoned must stop traversing a huge tree
				// without consulting a context a million times.
				if ctx.Err() != nil {
					w.done()
					w.drain()
					return
				}
				w.visit(j)
				w.done()
			}
		})
	}
	wg.Wait()
}

// drain clears the queue and wakes every waiter, so the remaining workers stop
// as well instead of each noticing the cancellation one directory at a time.
func (w *walker) drain() {
	w.mu.Lock()
	w.queue = w.queue[:0]
	w.stopped = true
	w.idle.Broadcast()
	w.mu.Unlock()
}

// take yields the next directory or reports the walk finished.
//
// An empty queue alone does not signal the end, since another worker may be
// inside a directory about to push its children. A worker therefore waits while
// any other remains busy, and the walk concludes only once the queue is empty
// and nobody is working.
func (w *walker) take() (job, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		if len(w.queue) > 0 {
			j := w.queue[len(w.queue)-1]
			w.queue = w.queue[:len(w.queue)-1]
			w.busy++
			return j, true
		}
		if w.busy == 0 || w.stopped {
			// Nothing queued and nothing running that could produce more.
			w.idle.Broadcast()
			return job{}, false
		}
		w.idle.Wait()
	}
}

// done marks a directory complete and wakes anyone awaiting further work.
func (w *walker) done() {
	w.mu.Lock()
	w.busy--
	w.idle.Broadcast()
	w.mu.Unlock()
}

func (w *walker) visit(j job) {
	src := w.sources[j.src]

	w.mu.Lock()
	w.dirs++
	dirSeq := w.dirSeq
	w.dirSeq++
	w.mu.Unlock()

	if j.depth > limits.SearchWalkDepth {
		return
	}

	var (
		children []job
		matched  []pending
		seen     int64
		entSeq   uint32
	)

	// Reserved names belonging to this server are skipped, since a part file
	// mid-upload is not a document anyone searched for.
	err := src.Root.ReadDirFunc(j.path, vfs.HideReserved, func(e vfs.DirEntry) bool {
		seen++
		p, jerr := j.path.JoinExisting(e.Name)
		if jerr != nil {
			return true
		}
		isDir := e.Kind.IsDir()

		// The permission check precedes scoring the entry. Search traverses the
		// entire tree, making it the widest opening through which an existence
		// leak could appear.
		if src.Allow != nil && !src.Allow(p, isDir) {
			return true
		}

		if isDir {
			children = append(children, job{src: j.src, path: p, depth: j.depth + 1})
		}
		if matchesName(e.Name, w.opt.Needle) {
			matched = append(matched, pending{
				src: j.src, ino: e.Ino, hasIno: e.Ino != 0,
				dirSeq: dirSeq, entSeq: entSeq,
				path: p, name: e.Name, isDir: isDir,
			})
			entSeq++
		}
		return true
	})
	if err != nil {
		// An unreadable directory is skipped rather than failing the entire
		// search, since one inaccessible subtree must not discard every other
		// hit.
		return
	}

	w.mu.Lock()
	w.entries += seen
	w.queue = append(w.queue, children...)
	w.pending = append(w.pending, matched...)
	w.mu.Unlock()
}

// stat resolves size and time for entries that survived filtering.
//
// The batch is first sorted by device and inode. Filesystems allocate inodes in
// increasing order, so requesting them that way keeps the disk seeking forward
// and improves the odds that several arrive from a single block.
func (w *walker) stat() {
	sortForStat(w.pending)
	for i := range w.pending {
		p := &w.pending[i]
		st, err := w.sources[p.src].Root.Stat(p.path)
		if err != nil {
			continue
		}
		size := st.Size
		mtime := st.MtimeNs
		p.dev = st.Dev
		p.statSize = &size
		p.statMTime = &mtime
	}
}

func (w *walker) finish() WalkResult {
	out := WalkResult{DirsVisited: w.dirs, EntriesSeen: w.entries}
	hits := make([]Hit, 0, len(w.pending))
	for _, p := range w.pending {
		src := w.sources[p.src]
		path := src.Prefix + p.path.String()
		hits = append(hits, Hit{
			Share:   src.Share,
			Path:    path,
			Name:    p.name,
			IsDir:   p.isDir,
			Size:    p.statSize,
			MTimeNs: p.statMTime,
			Score: Score(RankInput{
				NameFolded: FoldString(p.name),
				Needle:     w.opt.Needle,
				Path:       path,
				MTimeNs:    p.statMTime,
				NowNs:      w.opt.NowNs,
				Scope:      w.opt.Scope,
			}),
		})
	}

	SortHits(hits)
	if len(hits) > w.opt.Limit {
		hits = hits[:w.opt.Limit]
		out.Truncated = true
	}
	out.Hits = hits
	return out
}

// SortHits orders a result set: score first, then path, so a run with equal
// scores is stable and reproducible rather than dependent on which worker
// reached a directory first.
func SortHits(hits []Hit) {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Path < hits[j].Path
	})
}

// sortForStat arranges matched entries by device and inode, falling back to the
// order the directory read produced.
//
// Where a filesystem supplies no inode number, the sort degrades to grouping by
// directory while preserving readdir order. That is the best available proxy for
// locality and is not equivalent to a true inode sort.
func sortForStat(p []pending) {
	sort.Slice(p, func(i, j int) bool {
		a, b := p[i], p[j]
		if a.dev != b.dev {
			return a.dev < b.dev
		}
		ai, bi := a.ino, b.ino
		if !a.hasIno {
			ai = ^uint64(0)
		}
		if !b.hasIno {
			bi = ^uint64(0)
		}
		if ai != bi {
			return ai < bi
		}
		if a.dirSeq != b.dirSeq {
			return a.dirSeq < b.dirSeq
		}
		return a.entSeq < b.entSeq
	})
}

// matchesName performs the name test. An empty needle matches everything, which
// is how a scoped listing is expressed.
func matchesName(name string, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	if IsFoldedASCII(needle) {
		// The common case, a Latin query against a Latin filename, allocating
		// nothing.
		return ContainsASCIIFold([]byte(name), needle)
	}
	return Contains(FoldString(name), needle)
}
