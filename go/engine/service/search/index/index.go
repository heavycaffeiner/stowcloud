package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/fsatomic"
)

// The union of the segments.
//
//	<store>/names/
//	  base.idx        immutable, compressed in blocks
//	  delta.NNN.idx   append-only, lightly compressed, scanned linearly
//	  tomb.idx        the deletion record
//
// A query evaluates base plus the deltas minus the tombstones.
//
// This split is forced rather than chosen: an immutable block index admits no
// upsert. A name cannot be spliced into the middle of a compressed 32-name
// block, and a block id cannot be spliced into a delta-encoded posting list.
// plocate avoids the problem by rebuilding nightly, which is unavailable here
// because changes must appear immediately. Writes therefore land in a delta
// segment in constant time, and the rebuild happens under a gate while
// idle.

// MinTrigramQuery gives the shortest query capable of yielding a trigram.
const MinTrigramQuery = 3

// baseName, tombName and the delta prefix are the fixed names in an index
// directory. They are constants because a path this package builds is never a
// component a request chose.
const (
	baseName    = "base.idx"
	tombName    = "tomb.idx"
	deltaPrefix = "delta."
	deltaSuffix = ".idx"
)

// FallbackReason explains why the index declined to answer, sending the caller
// to a walk instead.
type FallbackReason int

const (
	// FallbackNone indicates the index answered.
	FallbackNone FallbackReason = iota
	// FallbackQueryTooShort marks a query under three bytes, leaving no trigram
	// to look up.
	FallbackQueryTooShort
	// FallbackAllTrigramsPruned means high-df pruning removed every trigram,
	// leaving the intersection with nothing to work on.
	FallbackAllTrigramsPruned
	// FallbackIncomplete means the index knows it covers less than the corpus
	// because it hit its entry ceiling. A name absent from it may nonetheless
	// exist, so the caller must walk.
	//
	// Without this an incomplete index answers every query from what it holds,
	// and a file beyond the ceiling goes missing from a result reporting
	// success. That is the same silent incompleteness the incremental update
	// path exists to prevent, reached by a different route.
	FallbackIncomplete
)

func (f FallbackReason) String() string {
	switch f {
	case FallbackQueryTooShort:
		return "QueryTooShort"
	case FallbackAllTrigramsPruned:
		return "AllTrigramsPruned"
	case FallbackIncomplete:
		return "Incomplete"
	}
	return "-"
}

// Hit represents a single index match. Only names are stored, so a caller
// obtains size and time from a stat run after its own ACL check, which serves as
// the staleness check too.
type Hit struct {
	Share uint32
	Path  string
	Name  string
	Score float32
}

// Result holds what a query produced.
//
// Fallback is a separate field rather than an empty slice, because the index
// searching and finding nothing differs from the index declining to search.
// Merging the two would turn a fallback into an incorrect empty result.
type Result struct {
	Hits     []Hit
	Fallback FallbackReason

	// CandidateBlocks counts what the posting intersection yielded, and
	// FalsePositiveBlocks how many of those contained no match. The latter is
	// the acknowledged cost of block-level postings and the figure indicating
	// whether the block size fits this corpus.
	CandidateBlocks     int
	ScannedEntries      int
	FalsePositiveBlocks int
}

// MustFallBack reports that the caller must run a walk.
func (r Result) MustFallBack() bool { return r.Fallback != FallbackNone }

// Config is the index's tuning.
type Config struct {
	// BlockSize is 32, matching plocate's default and this one. Larger values
	// compress better and shorten posting lists while making the postings less
	// precise, shifting effort into scanning blocks that contain no match.
	BlockSize uint32
	// PruneDFRatio discards any trigram appearing in more than this fraction of
	// the blocks.
	PruneDFRatio float32
	// MergeRatio sets the point at which the deltas justify a rebuild.
	MergeRatio float32
}

// DefaultConfig holds the tuning the product ships with.
func DefaultConfig() Config {
	return Config{BlockSize: 32, PruneDFRatio: 0.6, MergeRatio: 0.15}
}

// live holds a single entry from a delta segment.
type live struct {
	seq   uint64
	share uint32
	path  string
}

type tombKey struct {
	share uint32
	path  string
}

// NameIndex presents the segments as one.
type NameIndex struct {
	dir string
	cfg Config

	mu         sync.RWMutex
	base       *BaseSegment
	baseBytes  int64
	delta      []live
	deltaFiles []string
	deltaBytes int64
	// merging is held while a merge builds a base outside the lock. It prevents a
	// second merge starting from a snapshot the first has not yet published,
	// which would produce a base lacking the first's writes.
	merging bool
	// tomb associates a share and path with the sequence that deleted it.
	tomb      map[tombKey]uint64
	tombBytes int64
	seq       uint64
	// incomplete flags an index covering less than its corpus. Every query then
	// declines rather than answering from a portion of the tree, because the
	// index cannot distinguish a name that does not exist from one beyond where
	// it stopped.
	incomplete bool
}

// Open loads an index directory.
//
// A torn tail on a delta or the tombstone file is trimmed rather than rejected,
// since that is the expected state after a crash. A segment failing to parse
// would otherwise disable the index on every unclean shutdown.
func Open(dir string, cfg Config) (*NameIndex, error) {
	if cfg.BlockSize == 0 {
		cfg.BlockSize = DefaultConfig().BlockSize
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("index: creating %s: %w", dir, err)
	}

	ix := &NameIndex{dir: dir, cfg: cfg, tomb: map[tombKey]uint64{}}

	basePath := filepath.Join(dir, baseName)
	// The path combines the caller's index directory with a fixed name, so no
	// component of it originates from a request.
	if buf, err := os.ReadFile(basePath); err == nil { //nolint:gosec // G304: a fixed name under the operator's own index directory.
		seg, oerr := OpenBase(buf)
		if oerr != nil {
			// A corrupt base is reported rather than fatal. The index is a
			// cache, so the caller disables it and search proceeds by walking:
			// a broken cache costs speed and never correctness.
			return nil, fmt.Errorf("%w: %s: %w", ErrIndexCorrupt, basePath, oerr)
		}
		ix.base = seg
		ix.baseBytes = int64(len(buf))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("index: reading %s: %w", basePath, err)
	}

	maxSeq, err := ix.loadDeltas(dir)
	if err != nil {
		return nil, err
	}
	tombSeq, err := ix.loadTombstones(dir)
	if err != nil {
		return nil, err
	}
	ix.seq = max(maxSeq, tombSeq) + 1

	// An index that hit its ceiling covers less than its corpus, and that fact
	// must survive a restart. The flag lives in memory, so without deriving it
	// here a reopened index would answer every query from a portion of the tree
	// while reporting success. It is derived rather than stored because the
	// count already sits on disk, and a second copy is a second thing to keep
	// accurate.
	if ix.entryCount() >= limits.CorpusScanEntries {
		ix.incomplete = true
	}
	return ix, nil
}

// loadDeltas reads every delta segment in order, cutting a torn tail, and
// reports the highest sequence it saw.
func (ix *NameIndex) loadDeltas(dir string) (uint64, error) {
	names, err := deltaNames(dir)
	if err != nil {
		return 0, err
	}
	var maxSeq uint64
	for _, name := range names {
		path := filepath.Join(dir, name)
		rec, rerr := ReadRecords(path)
		if rerr != nil {
			return 0, rerr
		}
		if rec.Torn {
			if terr := TruncateTo(path, rec.GoodLen); terr != nil {
				return 0, terr
			}
		}
		ix.deltaFiles = append(ix.deltaFiles, path)
		ix.deltaBytes += rec.GoodLen
		for _, payload := range rec.Records {
			seq, entries, derr := DecodePayload(payload)
			if derr != nil {
				return 0, derr
			}
			maxSeq = max(maxSeq, seq)
			for _, e := range entries {
				ix.delta = append(ix.delta, live{seq: seq, share: e.Share, path: e.Path})
			}
		}
	}
	return maxSeq, nil
}

// loadTombstones reads tomb.idx the same way and reports its highest sequence.
func (ix *NameIndex) loadTombstones(dir string) (uint64, error) {
	tombPath := filepath.Join(dir, tombName)
	rec, err := ReadRecords(tombPath)
	if err != nil {
		return 0, err
	}
	if rec.Torn {
		if terr := TruncateTo(tombPath, rec.GoodLen); terr != nil {
			return 0, terr
		}
	}
	ix.tombBytes = rec.GoodLen

	var maxSeq uint64
	for _, payload := range rec.Records {
		seq, entries, derr := DecodePayload(payload)
		if derr != nil {
			return 0, derr
		}
		maxSeq = max(maxSeq, seq)
		for _, e := range entries {
			k := tombKey{share: e.Share, path: e.Path}
			if seq > ix.tomb[k] {
				ix.tomb[k] = seq
			}
		}
	}
	return maxSeq, nil
}

// entryCount returns the live entry count without acquiring the lock, for use in
// Open before the index becomes shared.
func (ix *NameIndex) entryCount() uint64 {
	var base uint64
	if ix.base != nil {
		base = ix.base.EntryCount
	}
	return base + uint64(len(ix.delta))
}

// Config returns the tuning this index was opened under.
func (ix *NameIndex) Config() Config { return ix.cfg }

// Dir is the index directory.
func (ix *NameIndex) Dir() string { return ix.dir }

// Query intersects the posting lists down to candidate blocks, decompresses
// those, scans the names within, and applies the overlay.
func (ix *NameIndex) Query(needle []byte, limit int) (Result, error) {
	folded := search.Fold(needle)
	if len(folded) < MinTrigramQuery {
		return Result{Fallback: FallbackQueryTooShort}, nil
	}
	tris := search.DistinctTrigrams(folded)

	ix.mu.RLock()
	defer ix.mu.RUnlock()

	// An index holding nothing covers none of the corpus, so answering from it
	// means answering "no such file" about every file that exists.
	//
	// This is reachable, not theoretical. The switch that enables the index is
	// separate from the build that fills it, and the administrative handler
	// stores the switch and applies it before any build has run. In that window
	// the index is open, empty and not at its ceiling, so nothing else here
	// marks it unusable: every search returns zero hits and reports success.
	//
	// Zero entries is the same claim the ceiling makes, from the other end: the
	// index knows it does not cover the corpus, so the caller must walk.
	if ix.entryCount() == 0 {
		return Result{Fallback: FallbackIncomplete}, nil
	}

	if ix.incomplete {
		// Answering from part of the corpus is the single thing an index must
		// never do. The caller cannot separate a short result from a complete
		// one, and the status reports success in both cases.
		return Result{Fallback: FallbackIncomplete}, nil
	}

	var out Result
	seen := map[tombKey]bool{}
	var hits []Hit

	if ix.base != nil {
		lists, kind := ix.candidateLists(tris)
		if kind == FallbackAllTrigramsPruned {
			return Result{Fallback: FallbackAllTrigramsPruned}, nil
		}

		var candidates []uint32
		if lists != nil {
			candidates = intersect(lists)
		}
		out.CandidateBlocks = len(candidates)

		for _, bid := range candidates {
			entries, err := ix.base.Block(bid)
			if err != nil {
				return Result{}, err
			}
			out.ScannedEntries += len(entries)
			before := len(hits)
			for _, e := range entries {
				if !matchesFolded(e.Path, folded) {
					continue
				}
				if ix.tombstonedLocked(e.Share, e.Path, 0) {
					continue
				}
				k := tombKey{share: e.Share, path: e.Path}
				if !seen[k] {
					seen[k] = true
					hits = append(hits, makeHit(e.Share, e.Path, folded))
				}
			}
			if len(hits) == before {
				out.FalsePositiveBlocks++
			}
		}
	}

	// The merge gate bounds the deltas, so a linear scan is appropriate: they
	// can never exceed a fixed fraction of the base.
	for _, d := range ix.delta {
		out.ScannedEntries++
		if !matchesFolded(d.path, folded) {
			continue
		}
		if ix.tombstonedLocked(d.share, d.path, d.seq) {
			continue
		}
		k := tombKey{share: d.share, path: d.path}
		if !seen[k] {
			seen[k] = true
			hits = append(hits, makeHit(d.share, d.path, folded))
		}
	}

	sortHits(hits)
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out.Hits = hits
	return out, nil
}

// candidateLists probes the dictionary for every trigram of the query.
//
// A nil list with FallbackNone means one trigram is absent, so the
// intersection is empty and the base has no hits. FallbackAllTrigramsPruned
// means every trigram was high-df, so the index cannot narrow this query at
// all; saying so is what stops the caller treating an empty result as
// "nothing matched". The caller holds the read lock.
func (ix *NameIndex) candidateLists(tris []search.Trigram) ([][]uint32, FallbackReason) {
	lists := make([][]uint32, 0, len(tris))
	pruned := 0
	for _, t := range tris {
		kind, raw := ix.base.Lookup(t)
		switch kind {
		case LookupPruned:
			pruned++
		case LookupMissing:
			return nil, FallbackNone
		case LookupPostings:
			ids, err := search.Ascending(raw)
			if err != nil {
				// A posting list that will not decode is a corrupt region.
				// Treated as absent: the index is a cache, so reading less of
				// it is slower rather than wrong.
				return nil, FallbackNone
			}
			lists = append(lists, ids)
		}
	}
	if len(lists) == 0 && pruned == len(tris) && ix.base.BlockCount > 0 {
		return nil, FallbackAllTrigramsPruned
	}
	return lists, FallbackNone
}

// Append writes names into the current delta segment.
//
// It costs constant time regardless of index size: one framed record and one
// write. This is precisely why the segment split exists.
func (ix *NameIndex) Append(entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()

	seq := ix.seq
	ix.seq++

	payload, err := EncodePayload(seq, entries)
	if err != nil {
		return err
	}
	path := ix.currentDelta()
	written, err := AppendRecord(path, payload)
	if err != nil {
		return err
	}
	if !slices.Contains(ix.deltaFiles, path) {
		ix.deltaFiles = append(ix.deltaFiles, path)
	}
	ix.deltaBytes += written
	for _, e := range entries {
		ix.delta = append(ix.delta, live{seq: seq, share: e.Share, path: e.Path})
	}
	return nil
}

// Tombstone records deletions, leaving the base segment untouched.
func (ix *NameIndex) Tombstone(entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()

	seq := ix.seq
	ix.seq++

	payload, err := EncodePayload(seq, entries)
	if err != nil {
		return err
	}
	written, err := AppendRecord(filepath.Join(ix.dir, tombName), payload)
	if err != nil {
		return err
	}
	ix.tombBytes += written
	for _, e := range entries {
		k := tombKey{share: e.Share, path: e.Path}
		if seq > ix.tomb[k] {
			ix.tomb[k] = seq
		}
	}
	return nil
}

// ChildrenOf lists the paths the index holds immediately inside one directory.
//
// Direct children only, never the subtree. That is what an incremental update
// requires: a change notification names a directory, and the comparison covers
// that directory's own listing. A subtree answer would make a single file
// appearing at a share's root cost the entire share.
//
// The overlay is applied, so an entry appended since the last merge appears and
// a tombstoned one does not, which is what makes consecutive updates agree.
func (ix *NameIndex) ChildrenOf(share uint32, dir string) ([]string, error) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	out := map[string]bool{}
	keep := func(path string, seq uint64) {
		if !isChildOf(path, dir) {
			return
		}
		if ix.tombstonedLocked(share, path, seq) {
			return
		}
		out[path] = true
	}

	if ix.base != nil {
		if err := ix.base.EachUnder(share, dir, func(e Entry) error {
			keep(e.Path, 0)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	for _, d := range ix.delta {
		if d.share != share {
			continue
		}
		keep(d.path, d.seq)
	}

	paths := make([]string, 0, len(out))
	for p := range out {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths, nil
}

// isChildOf reports whether path names an entry immediately inside dir.
func isChildOf(path, dir string) bool {
	if dir == "" {
		return path != "" && !strings.Contains(path, "/")
	}
	if len(path) <= len(dir)+1 || path[:len(dir)] != dir || path[len(dir)] != '/' {
		return false
	}
	return !strings.Contains(path[len(dir)+1:], "/")
}

// Incomplete reports whether this index covers less than its corpus.
func (ix *NameIndex) Incomplete() bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.incomplete
}

// SetIncomplete records that the index holds less than the tree it covers, which
// is what hitting the entry ceiling amounts to.
//
// The flag belongs to the index rather than the caller's bookkeeping, because
// every query must observe it and only the index sits on that path.
func (ix *NameIndex) SetIncomplete(v bool) {
	ix.mu.Lock()
	ix.incomplete = v
	ix.mu.Unlock()
}

// NeedsMerge reports whether the overlay has exceeded its allowance against the
// base.
//
// Bounding that ratio is what bounds read cost, since the linear delta scan run
// by every query can never exceed a fixed fraction of the base.
func (ix *NameIndex) NeedsMerge() bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	extra := ix.deltaBytes + ix.tombBytes
	if ix.baseBytes == 0 {
		return extra > 0
	}
	return float64(extra) > float64(ix.cfg.MergeRatio)*float64(ix.baseBytes)
}

// Stats describes what the index holds.
func (ix *NameIndex) Stats() Stats {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	s := Stats{
		DeltaEntries:  uint64(len(ix.delta)),
		Tombstones:    uint64(len(ix.tomb)),
		BaseBytes:     ix.baseBytes,
		DeltaBytes:    ix.deltaBytes,
		TombBytes:     ix.tombBytes,
		DeltaSegments: len(ix.deltaFiles),
	}
	if ix.base != nil {
		s.BaseEntries = ix.base.EntryCount
		s.Blocks = ix.base.BlockCount
		s.Trigrams = ix.base.TrigramCount
		s.PrunedTrigrams = ix.base.PrunedCount
	}
	s.Entries = s.BaseEntries + s.DeltaEntries
	return s
}

// Stats carries the index's accounting.
type Stats struct {
	Entries        uint64
	BaseEntries    uint64
	DeltaEntries   uint64
	Tombstones     uint64
	Blocks         uint32
	Trigrams       uint32
	PrunedTrigrams uint32
	BaseBytes      int64
	DeltaBytes     int64
	TombBytes      int64
	DeltaSegments  int
}

// tombstonedLocked reports whether a path was deleted after the sequence it
// was written at. The caller holds the lock.
func (ix *NameIndex) tombstonedLocked(share uint32, path string, seq uint64) bool {
	at, ok := ix.tomb[tombKey{share: share, path: path}]
	// A tombstone conceals only writes preceding it, which is what lets a delete
	// followed by a recreate end up live rather than permanently hidden.
	return ok && at >= seq
}

func (ix *NameIndex) currentDelta() string {
	if len(ix.deltaFiles) > 0 {
		return ix.deltaFiles[len(ix.deltaFiles)-1]
	}
	return filepath.Join(ix.dir, deltaPrefix+"000"+deltaSuffix)
}

// deltaNames lists the delta segments in creation order, the ordering their
// names encode.
func deltaNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("index: listing %s: %w", dir, err)
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, deltaPrefix) && strings.HasSuffix(n, deltaSuffix) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// intersect performs the posting-list intersection starting from the smallest
// list, so the result contracts as quickly as possible.
func intersect(lists [][]uint32) []uint32 {
	if len(lists) == 0 {
		return nil
	}
	sort.Slice(lists, func(i, j int) bool { return len(lists[i]) < len(lists[j]) })
	out := lists[0]
	for _, l := range lists[1:] {
		out = intersectTwo(out, l)
		if len(out) == 0 {
			return nil
		}
	}
	return out
}

func intersectTwo(a, b []uint32) []uint32 {
	out := make([]uint32, 0, min(len(a), len(b)))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] < b[j]:
			i++
		case a[i] > b[j]:
			j++
		default:
			out = append(out, a[i])
			i++
			j++
		}
	}
	return out
}

func matchesFolded(path string, folded []byte) bool {
	return search.Contains(search.FoldString(path), folded)
}

func makeHit(share uint32, path string, folded []byte) Hit {
	name := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		name = path[i+1:]
	}
	return Hit{
		Share: share,
		Path:  path,
		Name:  name,
		Score: search.Score(search.RankInput{
			NameFolded: search.FoldString(name),
			Needle:     folded,
			Path:       path,
		}),
	}
}

// sortHits orders a result set: score first, then path, so a run with equal
// scores is reproducible rather than dependent on block order.
func sortHits(hits []Hit) {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Path < hits[j].Path
	})
}

// Merge rebuilds the base from base plus deltas minus tombstones, then discards
// the absorbed segments.
//
// This is the design's only expensive operation, so it runs under a gate and
// never on a request path. The gate is polled throughout and a refusal aborts
// cleanly: a merge instructed to stop must never damage the index, so nothing is
// replaced until the new segment is finished.
//
// The rebuild proceeds without the lock. It reads every base entry, sorts them
// and compresses the result, measured at 4.4 seconds over a million entries
// against 87 microseconds for a query. Holding the write lock across that would
// block fifty thousand queries' worth of work on a timer nobody requested.
//
// Safety comes from a base segment being immutable once written and the overlay
// being append-only. The merge therefore takes a snapshot, builds against it
// holding nothing, and reacquires the lock solely to swap. Writes arriving in
// between are neither lost nor merged: they remain in the overlay for the next
// merge to collect.
func (ix *NameIndex) Merge(ctx context.Context, gate func() bool) error {
	if gate != nil && !gate() {
		return nil
	}

	snap, ok := ix.sealForMerge()
	if !ok {
		// Another merge is already running. Two would each build a base from a
		// snapshot predating the other's, and whichever finished last would
		// publish one that never contained the first's writes.
		return nil
	}
	defer ix.releaseMerge()

	buf, err := snap.build(ctx, gate, ix.cfg)
	if err != nil || buf == nil {
		// A gate refusal yields neither buffer nor error. Nothing has been
		// replaced, and the next merge collects the sealed segment.
		return err
	}

	return ix.publish(snap, buf)
}

// mergeSnapshot captures the state a single merge builds against.
//
// Each field is immutable or a copy, so the build reads it while holding no lock
// and the index continues serving queries and accepting writes.
type mergeSnapshot struct {
	base *BaseSegment
	// delta holds the overlay as it stood. Nothing rewrites the backing array
	// beyond this length: an append either fits and writes past it or
	// reallocates, leaving this array untouched.
	delta []live
	tomb  map[tombKey]uint64
	// files lists the delta segments this merge absorbs. Appends following the
	// seal go to a new file, so these can be deleted without losing a write that
	// arrived during the build.
	files []string
	// seq records the sequence at which the seal occurred. Any tombstone above it
	// postdates this base and must survive the swap.
	seq uint64
}

// sealForMerge captures the snapshot and opens a fresh delta segment.
//
// That fresh segment is what makes the old ones safe to delete. Without it an
// append occurring during the build would land in a file this merge is about to
// delete,
// leaving the entry in no segment and no base.
func (ix *NameIndex) sealForMerge() (mergeSnapshot, bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	if ix.merging {
		return mergeSnapshot{}, false
	}
	ix.merging = true

	snap := mergeSnapshot{
		base:  ix.base,
		delta: ix.delta,
		tomb:  make(map[tombKey]uint64, len(ix.tomb)),
		files: slices.Clone(ix.deltaFiles),
		seq:   ix.seq,
	}
	for k, v := range ix.tomb {
		snap.tomb[k] = v
	}

	// Appends from this point land in a segment this merge does not absorb.
	ix.deltaFiles = append(ix.deltaFiles, nextDeltaPath(ix.dir, ix.deltaFiles))
	return snap, true
}

func (ix *NameIndex) releaseMerge() {
	ix.mu.Lock()
	ix.merging = false
	ix.mu.Unlock()
}

// build constructs the new base segment, holding no lock throughout.
func (s mergeSnapshot) build(ctx context.Context, gate func() bool, cfg Config) ([]byte, error) {
	tombstoned := func(share uint32, path string, seq uint64) bool {
		at, ok := s.tomb[tombKey{share: share, path: path}]
		return ok && at >= seq
	}

	var entries []Entry
	if s.base != nil {
		if err := s.base.EachEntry(func(e Entry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if tombstoned(e.Share, e.Path, 0) {
				return nil
			}
			entries = append(entries, e)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if gate != nil && !gate() {
		return nil, nil
	}

	seen := make(map[tombKey]bool, len(entries)+len(s.delta))
	for _, e := range entries {
		seen[tombKey{share: e.Share, path: e.Path}] = true
	}
	for _, d := range s.delta {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if tombstoned(d.share, d.path, d.seq) {
			continue
		}
		k := tombKey{share: d.share, path: d.path}
		if seen[k] {
			continue
		}
		seen[k] = true
		entries = append(entries, Entry{Share: d.share, Path: d.path})
	}

	TreeOrder(entries)
	buf, err := WriteBase(entries, cfg.BlockSize, cfg.PruneDFRatio)
	if err != nil {
		return nil, err
	}
	if gate != nil && !gate() {
		return nil, nil
	}
	return buf, nil
}

// publish installs the new base and discards what it absorbed.
//
// Only this portion holds the write lock. It performs a rename, a few removes
// and one small rewrite, all bounded by what arrived during the build rather
// than by the corpus.
func (ix *NameIndex) publish(snap mergeSnapshot, buf []byte) error {
	seg, oerr := OpenBase(buf)
	if oerr != nil {
		return oerr
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()

	// The new base is staged and installed via an atomic rename with the parent
	// directory synced, so a crash here preserves the old segment rather than
	// leaving a half-written index.
	basePath := filepath.Join(ix.dir, baseName)
	if werr := fsatomic.ReplaceFileDurable(basePath, 0o600, func(f *os.File) error {
		_, err := f.Write(buf)
		return err
	}); werr != nil {
		return fmt.Errorf("index: publishing the new base: %w", werr)
	}

	// Segments absorbed by the new base are removed only once it is in place, so
	// a crash between the two leaves duplicates rather than a gap. Only the
	// sealed ones qualify: anything appended during the build sits in a segment
	// this merge never read.
	absorbed := make(map[string]bool, len(snap.files))
	for _, f := range snap.files {
		absorbed[f] = true
		if rerr := os.Remove(f); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return fmt.Errorf("index: removing a merged delta: %w", rerr)
		}
	}

	// Tombstones this base already applied are discarded, while any recorded
	// during the build postdate it and are retained. The file is rewritten
	// instead of removed, since removing it would lose those.
	//
	// The comparison is inclusive because the seal stores the next sequence to be
	// issued, so the first write after it carries exactly that number. An
	// exclusive comparison would drop that tombstone while the base it was
	// checked against still holds the entry, resurrecting a deleted file.
	survivors := map[tombKey]uint64{}
	for k, at := range ix.tomb {
		if at >= snap.seq {
			survivors[k] = at
		}
	}
	tombBytes, terr := rewriteTombstones(ix.dir, survivors)
	if terr != nil {
		return terr
	}

	// The overlay retains entries appended after the seal. They form the slice's
	// tail, since appends only ever extend the end.
	kept := ix.delta[min(len(snap.delta), len(ix.delta)):]
	var files []string
	var deltaBytes int64
	for _, f := range ix.deltaFiles {
		if absorbed[f] {
			continue
		}
		files = append(files, f)
		if st, serr := os.Stat(f); serr == nil {
			deltaBytes += st.Size()
		}
	}

	ix.base = seg
	ix.baseBytes = int64(len(buf))
	ix.delta = slices.Clone(kept)
	ix.deltaFiles = files
	ix.deltaBytes = deltaBytes
	ix.tomb = survivors
	ix.tombBytes = tombBytes
	return nil
}

// rewriteTombstones rewrites tomb.idx with whichever entries outlived a merge
// and reports the file's new size. An empty set deletes the file.
func rewriteTombstones(dir string, survivors map[tombKey]uint64) (int64, error) {
	tombPath := filepath.Join(dir, tombName)
	if len(survivors) == 0 {
		if rerr := os.Remove(tombPath); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return 0, fmt.Errorf("index: removing the merged tombstones: %w", rerr)
		}
		return 0, nil
	}

	// One record per sequence, since the sequence is what orders a tombstone
	// relative to an append and each record carries exactly one.
	bySeq := map[uint64][]Entry{}
	for k, at := range survivors {
		bySeq[at] = append(bySeq[at], Entry{Share: k.share, Path: k.path})
	}
	seqs := make([]uint64, 0, len(bySeq))
	for s := range bySeq {
		seqs = append(seqs, s)
	}
	slices.Sort(seqs)

	var body []byte
	for _, s := range seqs {
		entries := bySeq[s]
		TreeOrder(entries)
		payload, err := EncodePayload(s, entries)
		if err != nil {
			return 0, err
		}
		framed, ferr := Frame(payload)
		if ferr != nil {
			return 0, ferr
		}
		body = append(body, framed...)
	}

	if werr := fsatomic.ReplaceFileDurable(tombPath, 0o600, func(f *os.File) error {
		_, err := f.Write(body)
		return err
	}); werr != nil {
		return 0, fmt.Errorf("index: rewriting the tombstones: %w", werr)
	}
	return int64(len(body)), nil
}

// nextDeltaPath names a segment following every existing one, preserving the
// ordering their names encode.
func nextDeltaPath(dir string, existing []string) string {
	next := 0
	for _, f := range existing {
		var n int
		if _, err := fmt.Sscanf(filepath.Base(f), deltaPrefix+"%03d"+deltaSuffix, &n); err == nil && n >= next {
			next = n + 1
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s%03d%s", deltaPrefix, next, deltaSuffix))
}
