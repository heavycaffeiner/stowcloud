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

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/search"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The trigram name index, off by default.
//
// It is an escalation taken when measurement says the walk is not fast enough,
// and it is a cache: delete the directory and search keeps working.
//
//	<store>/names/
//	  base.idx        immutable, block-compressed
//	  delta.NNN.idx   append-only, lightly compressed, linearly scanned
//	  tomb.idx        deletions
//
// A query is base plus the deltas minus the tombstones.
//
// The split is a necessity rather than an optimisation: an immutable block
// index cannot be upserted. A name cannot be inserted into the middle of a
// compressed 32-name block, and a block id cannot be inserted into a
// delta-encoded posting list. plocate sidesteps this by rebuilding nightly,
// which this cannot do because changes have to be visible immediately. So
// writes go to a delta segment in constant time and the rebuild happens under
// a gate, at idle.

// MinTrigramQuery is the shortest query that can produce a trigram.
const MinTrigramQuery = 3

// FallbackReason is why the index declined to answer, so the caller runs a
// walk instead.
type FallbackReason int

const (
	// FallbackNone means the index answered.
	FallbackNone FallbackReason = iota
	// FallbackQueryTooShort is a query under three bytes, which has no
	// trigram to look up.
	FallbackQueryTooShort
	// FallbackAllTrigramsPruned means every trigram was dropped by high-df
	// pruning, so the intersection would be over nothing.
	FallbackAllTrigramsPruned
	// FallbackIncomplete means the index is knowingly short of the corpus,
	// because it reached its entry ceiling. A name it does not hold may still
	// exist, so the caller has to walk.
	//
	// Without it an incomplete index answers every query from what it has, and
	// a file past the ceiling is absent from a result carrying a success
	// status. That is the same silent shortness the incremental update path
	// exists to prevent, arriving by another route.
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

// Hit is one index match. The index stores names only, so a caller resolves
// size and time with a stat performed after its own ACL check, which doubles
// as the staleness check.
type Hit struct {
	Share uint32
	Path  string
	Name  string
	Score float32
}

// Result is what a query produced.
//
// The fallback is a field rather than an empty slice because "the index looked
// and found nothing" and "the index declined to look" are different answers,
// and conflating them turns a fallback into a wrong empty result.
type Result struct {
	Hits     []Hit
	Fallback FallbackReason

	// CandidateBlocks is what the posting intersection produced, and
	// FalsePositiveBlocks how many held no match after all. The second is the
	// documented cost of block-level postings and the number that says whether
	// the block size suits this corpus.
	CandidateBlocks     int
	ScannedEntries      int
	FalsePositiveBlocks int
}

// MustFallBack reports that the caller has to run a walk.
func (r Result) MustFallBack() bool { return r.Fallback != FallbackNone }

// Config is the index's tuning.
type Config struct {
	// BlockSize is plocate's default and this one's: 32. Larger compresses
	// better and shortens posting lists, but makes the postings less precise
	// so more work goes into scanning blocks that hold no match.
	BlockSize uint32
	// PruneDFRatio drops a trigram present in more than this fraction of the
	// blocks.
	PruneDFRatio float32
	// MergeRatio is when the deltas have grown enough to rebuild.
	MergeRatio float32
}

// DefaultConfig is the tuning the product ships.
func DefaultConfig() Config {
	return Config{BlockSize: 32, PruneDFRatio: 0.6, MergeRatio: 0.15}
}

// live is one entry from a delta segment.
type live struct {
	seq   uint64
	share uint32
	path  string
}

// NameIndex is the union of the segments.
type NameIndex struct {
	dir string
	cfg Config

	mu         sync.RWMutex
	base       *BaseSegment
	baseBytes  int64
	delta      []live
	deltaFiles []string
	deltaBytes int64
	// merging is set while a merge builds a base outside the lock. It is what
	// stops a second one starting from a snapshot the first has not published
	// yet, which would publish a base missing the first's writes.
	merging bool
	// tomb maps a share and path to the sequence at which it was deleted.
	tomb      map[tombKey]uint64
	tombBytes int64
	seq       uint64
	// incomplete marks an index that stopped short of its corpus. Every query
	// then declines rather than answering from a part of the tree, because the
	// index cannot tell "no such name" from "a name past where I stopped".
	incomplete bool
}

type tombKey struct {
	share uint32
	path  string
}

// Open reads an index directory.
//
// A torn tail on a delta or the tombstone file is cut rather than refused: it
// is the expected state after a crash, and a segment that failed to parse
// would otherwise disable the index every time the machine lost power.
func Open(dir string, cfg Config) (*NameIndex, error) {
	if cfg.BlockSize == 0 {
		cfg.BlockSize = DefaultConfig().BlockSize
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("index: creating %s: %w", dir, err)
	}

	ix := &NameIndex{dir: dir, cfg: cfg, tomb: map[tombKey]uint64{}}

	// The path is built from the caller's index directory and a fixed name, so
	// there is no component here a request could have chosen.
	basePath := filepath.Join(dir, "base.idx")
	if buf, err := os.ReadFile(basePath); err == nil { //nolint:gosec // G304: a fixed name under the operator's own index directory.
		seg, oerr := OpenBase(buf)
		if oerr != nil {
			// A corrupt base is reported, not fatal. The index is a cache, so
			// the caller disables it and search continues on the walk: a broken
			// cache costs speed, never answers.
			return nil, fmt.Errorf("%w: %s: %w", ErrIndexCorrupt, basePath, oerr)
		}
		ix.base = seg
		ix.baseBytes = int64(len(buf))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("index: reading %s: %w", basePath, err)
	}

	names, err := deltaNames(dir)
	if err != nil {
		return nil, err
	}
	var maxSeq uint64
	for _, name := range names {
		path := filepath.Join(dir, name)
		rec, rerr := ReadRecords(path)
		if rerr != nil {
			return nil, rerr
		}
		if rec.Torn {
			if terr := TruncateTo(path, rec.GoodLen); terr != nil {
				return nil, terr
			}
		}
		ix.deltaFiles = append(ix.deltaFiles, path)
		ix.deltaBytes += rec.GoodLen
		for _, payload := range rec.Records {
			seq, entries, derr := DecodePayload(payload)
			if derr != nil {
				return nil, derr
			}
			if seq > maxSeq {
				maxSeq = seq
			}
			for _, e := range entries {
				ix.delta = append(ix.delta, live{seq: seq, share: e.Share, path: e.Path})
			}
		}
	}

	tombPath := filepath.Join(dir, "tomb.idx")
	rec, err := ReadRecords(tombPath)
	if err != nil {
		return nil, err
	}
	if rec.Torn {
		if terr := TruncateTo(tombPath, rec.GoodLen); terr != nil {
			return nil, terr
		}
	}
	ix.tombBytes = rec.GoodLen
	for _, payload := range rec.Records {
		seq, entries, derr := DecodePayload(payload)
		if derr != nil {
			return nil, derr
		}
		if seq > maxSeq {
			maxSeq = seq
		}
		for _, e := range entries {
			k := tombKey{share: e.Share, path: e.Path}
			if seq > ix.tomb[k] {
				ix.tomb[k] = seq
			}
		}
	}

	ix.seq = maxSeq + 1

	// An index that reached its ceiling is short of its corpus, and that has to
	// survive a restart: the flag is in memory, so without deriving it here a
	// reopened index would answer every query from a part of the tree with a
	// success status. Derived rather than stored, because the count is already
	// on disk and a second copy is a second thing to keep true.
	if ix.entryCount() >= limits.CorpusScanEntries {
		ix.incomplete = true
	}
	return ix, nil
}

// entryCount is the live entry count without taking the lock, for use during
// Open before the index is shared.
func (ix *NameIndex) entryCount() uint64 {
	var base uint64
	if ix.base != nil {
		base = ix.base.EntryCount
	}
	return base + uint64(len(ix.delta))
}

// Config is the tuning this index was opened with.
func (ix *NameIndex) Config() Config { return ix.cfg }

// Dir is the index directory.
func (ix *NameIndex) Dir() string { return ix.dir }

// Query intersects the posting lists into candidate blocks, decompresses them,
// scans the names inside, and applies the overlay.
func (ix *NameIndex) Query(needle []byte, limit int) (Result, error) {
	folded := search.Fold(needle)
	if len(folded) < MinTrigramQuery {
		return Result{Fallback: FallbackQueryTooShort}, nil
	}
	if ix.Incomplete() {
		// Answering from a part of the corpus is the one thing an index must
		// never do: the caller cannot tell a short result from a complete one,
		// and the status says success either way.
		return Result{Fallback: FallbackIncomplete}, nil
	}
	tris := search.DistinctTrigrams(folded)

	ix.mu.RLock()
	defer ix.mu.RUnlock()

	// An index holding nothing covers none of its corpus, so answering from it
	// is answering "no such file" about every file that exists.
	//
	// Reachable rather than theoretical: enabling the index and building it are
	// separate actions, and the admin route stores the switch and applies it
	// with no build behind it. In that window the index opens cleanly, sits
	// nowhere near its entry ceiling, and holds nothing, so no other check here
	// marks it unusable. Every query returned zero hits and reported success.
	//
	// Zero entries is the ceiling's claim from the other end: the index knows
	// it does not cover the corpus, so the caller has to walk.
	if ix.entryCount() == 0 {
		return Result{Fallback: FallbackIncomplete}, nil
	}

	var out Result
	seen := map[tombKey]bool{}
	var hits []Hit

	if ix.base != nil {
		lists := make([][]uint32, 0, len(tris))
		pruned := 0
		missing := false
		for _, t := range tris {
			kind, raw := ix.base.Lookup(t)
			switch kind {
			case LookupPruned:
				pruned++
			case LookupMissing:
				missing = true
			case LookupPostings:
				ids, err := search.Ascending(raw)
				if err != nil {
					missing = true
					break
				}
				lists = append(lists, ids)
			}
			if missing {
				break
			}
		}

		// Every trigram was high-df, so the index cannot narrow this query at
		// all. Saying so is what stops the caller treating an empty result as
		// "nothing matched".
		if !missing && len(lists) == 0 && pruned == len(tris) && ix.base.BlockCount > 0 {
			return Result{Fallback: FallbackAllTrigramsPruned}, nil
		}

		var candidates []uint32
		if !missing {
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
				if ix.tombstoned(e.Share, e.Path, 0) {
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

	// The deltas are bounded by the merge gate, so a linear scan is the right
	// answer: they can never grow past a fixed fraction of the base.
	for _, d := range ix.delta {
		out.ScannedEntries++
		if !matchesFolded(d.path, folded) {
			continue
		}
		if ix.tombstoned(d.share, d.path, d.seq) {
			continue
		}
		k := tombKey{share: d.share, path: d.path}
		if !seen[k] {
			seen[k] = true
			hits = append(hits, makeHit(d.share, d.path, folded))
		}
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Path < hits[j].Path
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out.Hits = hits
	return out, nil
}

// Append adds names to the current delta segment.
//
// Constant time in the size of the index: one framed record, one write. This
// is why the segment split exists.
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
	if !containsString(ix.deltaFiles, path) {
		ix.deltaFiles = append(ix.deltaFiles, path)
	}
	ix.deltaBytes += written
	for _, e := range entries {
		ix.delta = append(ix.delta, live{seq: seq, share: e.Share, path: e.Path})
	}
	return nil
}

// ChildrenOf is the paths the index holds directly inside one directory.
//
// Direct children only, not the subtree. It is what an incremental update
// needs: a change notification names a directory, and what has to be compared
// is that directory's own listing. A subtree answer would make one file
// appearing at the top of a share cost the whole share.
//
// The overlay is applied, so an entry appended since the last merge is present
// and a tombstoned one is absent, which is what makes two updates in a row
// agree with each other.
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

// isChildOf reports whether path names an entry directly inside dir.
func isChildOf(path, dir string) bool {
	if dir == "" {
		return path != "" && !strings.Contains(path, "/")
	}
	if len(path) <= len(dir)+1 || path[:len(dir)] != dir || path[len(dir)] != '/' {
		return false
	}
	return !strings.Contains(path[len(dir)+1:], "/")
}

// Tombstone records deletions. The base segment is never touched.
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
	written, err := AppendRecord(filepath.Join(ix.dir, "tomb.idx"), payload)
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

// Incomplete reports whether this index stopped short of its corpus.
func (ix *NameIndex) Incomplete() bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.incomplete
}

// SetIncomplete records that the index holds less than the tree it covers,
// which is what reaching the entry ceiling means.
//
// It is the index's own flag rather than the caller's bookkeeping, because
// every query has to see it and only the index is on that path.
func (ix *NameIndex) SetIncomplete(v bool) {
	ix.mu.Lock()
	ix.incomplete = v
	ix.mu.Unlock()
}

// NeedsMerge reports whether the overlay has outgrown its share of the base.
//
// Bounding this ratio is what bounds read cost: the linear delta scan on every
// query can never grow past a fixed fraction of the base.
func (ix *NameIndex) NeedsMerge() bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	extra := ix.deltaBytes + ix.tombBytes
	if ix.baseBytes == 0 {
		return extra > 0
	}
	return float64(extra) > float64(ix.cfg.MergeRatio)*float64(ix.baseBytes)
}

// Merge rebuilds the base from base plus the deltas minus the tombstones and
// drops the segments.
//
// This is the only heavy operation in the design, so it runs under a gate and
// never on a request path. The gate is polled as it goes, and a refusal aborts
// cleanly: a merge that was told to stop must never be able to damage the
// index, so nothing is replaced until the new segment is complete.
//
// The rebuild runs without the lock. It reads every entry in the base, sorts
// them and compresses the result, which measured 4.4 seconds on a million
// entries against 87 microseconds for a query: holding the write lock across it
// stops fifty thousand queries' worth of work, on a timer nobody asked for.
//
// What makes that safe is that a base segment is immutable once written and the
// overlay is only ever appended to. So the merge takes a snapshot, builds
// against it with nothing held, and takes the lock again only to swap. Writes
// that land in between are not lost and not merged: they stay in the overlay
// and the next merge takes them.
func (ix *NameIndex) Merge(ctx context.Context, gate func() bool) error {
	if gate != nil && !gate() {
		return nil
	}

	snap, ok := ix.sealForMerge()
	if !ok {
		// Another merge is in flight. Two would each build a base from a
		// snapshot taken before the other's, and whichever finished second
		// would publish one that never held the first's writes.
		return nil
	}
	defer ix.releaseMerge()

	buf, err := snap.build(ctx, gate, ix.cfg)
	if err != nil || buf == nil {
		// A gate refusal returns no buffer and no error. Nothing has been
		// replaced, and the sealed segment is picked up by the next merge.
		return err
	}

	return ix.publish(snap, buf)
}

// mergeSnapshot is the state one merge builds against.
//
// Every field is either immutable or a copy, so the build reads it with no
// lock held while the index goes on serving queries and taking writes.
type mergeSnapshot struct {
	base *BaseSegment
	// delta is the overlay as it stood. The backing array is never written
	// again past this length: an append either fits and writes past it, or
	// reallocates and leaves this one alone.
	delta []live
	tomb  map[tombKey]uint64
	// files are the delta segments this merge absorbs. Appends after the seal
	// go to a new file, so these can be removed without losing a write that
	// landed during the build.
	files []string
	// seq is the sequence the seal happened at. A tombstone above it is newer
	// than this base and has to survive the swap.
	seq uint64
}

// sealForMerge takes the snapshot and starts a fresh delta segment.
//
// The fresh segment is what makes the old ones safe to delete. Without it an
// append during the build would land in a file this merge is about to remove,
// and the entry would be in no segment and in no base.
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

	// Appends from here go to a segment this merge does not absorb.
	ix.deltaFiles = append(ix.deltaFiles, nextDeltaPath(ix.dir, ix.deltaFiles))
	return snap, true
}

func (ix *NameIndex) releaseMerge() {
	ix.mu.Lock()
	ix.merging = false
	ix.mu.Unlock()
}

// build produces the new base segment. No lock is held for any of it.
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

// publish swaps the new base in and drops what it absorbed.
//
// This is the only part that holds the write lock. It is a rename, a handful of
// removes and one small rewrite, all of them bounded by what arrived during the
// build rather than by the corpus.
func (ix *NameIndex) publish(snap mergeSnapshot, buf []byte) error {
	seg, oerr := OpenBase(buf)
	if oerr != nil {
		return oerr
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()

	// The new base is staged and published by an atomic rename with the parent
	// directory synced, so a crash here leaves the old segment intact rather
	// than a half-written index. The rename lives in the VFS because that is
	// the one place allowed to do one.
	basePath := filepath.Join(ix.dir, "base.idx")
	if werr := vfs.ReplaceFileDurable(basePath, 0o600, func(f *os.File) error {
		_, err := f.Write(buf)
		return err
	}); werr != nil {
		return fmt.Errorf("index: publishing the new base: %w", werr)
	}

	// The segments the new base absorbed are only removed after it is in
	// place, so a crash between the two leaves duplicates rather than a hole.
	// Only the sealed ones: anything appended during the build is in a segment
	// this merge never read.
	absorbed := make(map[string]bool, len(snap.files))
	for _, f := range snap.files {
		absorbed[f] = true
		if rerr := os.Remove(f); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return fmt.Errorf("index: removing a merged delta: %w", rerr)
		}
	}

	// The tombstones this base already applied are gone; any recorded during
	// the build are newer than it and are kept. The file is rewritten rather
	// than removed, because removing it would drop those.
	//
	// The comparison is inclusive because the seal records the next sequence to
	// be issued, so the first write after it carries exactly that number. An
	// exclusive one drops that tombstone while the base it was checked against
	// still holds the entry, which resurrects a deleted file.
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

	// What the overlay keeps: the entries appended after the seal. They are
	// the tail of the slice, because appends only ever add to the end.
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

// rewriteTombstones replaces tomb.idx with the ones that outlived a merge, and
// reports the file's new size. An empty set removes the file.
func rewriteTombstones(dir string, survivors map[tombKey]uint64) (int64, error) {
	tombPath := filepath.Join(dir, "tomb.idx")
	if len(survivors) == 0 {
		if rerr := os.Remove(tombPath); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return 0, fmt.Errorf("index: removing the merged tombstones: %w", rerr)
		}
		return 0, nil
	}

	// One record per sequence, because the sequence is what orders a tombstone
	// against an append and a record carries exactly one.
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

	if werr := vfs.ReplaceFileDurable(tombPath, 0o600, func(f *os.File) error {
		_, err := f.Write(body)
		return err
	}); werr != nil {
		return 0, fmt.Errorf("index: rewriting the tombstones: %w", werr)
	}
	return int64(len(body)), nil
}

// nextDeltaPath names a segment after every one that exists, so the ordering
// their names encode keeps holding.
func nextDeltaPath(dir string, existing []string) string {
	next := 0
	for _, f := range existing {
		var n int
		if _, err := fmt.Sscanf(filepath.Base(f), "delta.%03d.idx", &n); err == nil && n >= next {
			next = n + 1
		}
	}
	return filepath.Join(dir, fmt.Sprintf("delta.%03d.idx", next))
}

// Stats reports what the index holds.
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

// Stats is the index's accounting.
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

// tombstoned reports whether a path was deleted after the sequence it was
// written at. The caller holds the read lock.
func (ix *NameIndex) tombstoned(share uint32, path string, seq uint64) bool {
	return ix.tombstonedLocked(share, path, seq)
}

func (ix *NameIndex) tombstonedLocked(share uint32, path string, seq uint64) bool {
	at, ok := ix.tomb[tombKey{share: share, path: path}]
	// A tombstone only hides a write that came before it, which is what makes
	// a delete-then-recreate end up live rather than hidden forever.
	return ok && at >= seq
}

func (ix *NameIndex) currentDelta() string {
	if len(ix.deltaFiles) > 0 {
		return ix.deltaFiles[len(ix.deltaFiles)-1]
	}
	return filepath.Join(ix.dir, "delta.000.idx")
}

// deltaNames lists the delta segments in creation order, which their names
// encode.
func deltaNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("index: listing %s: %w", dir, err)
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "delta.") && strings.HasSuffix(n, ".idx") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// intersect is the posting-list intersection, smallest list first so the
// result shrinks as fast as it can.
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

func containsString(set []string, s string) bool {
	for _, have := range set {
		if have == s {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
