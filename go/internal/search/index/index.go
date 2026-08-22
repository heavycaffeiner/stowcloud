package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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
)

func (f FallbackReason) String() string {
	switch f {
	case FallbackQueryTooShort:
		return "QueryTooShort"
	case FallbackAllTrigramsPruned:
		return "AllTrigramsPruned"
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
	// tomb maps a share and path to the sequence at which it was deleted.
	tomb      map[tombKey]uint64
	tombBytes int64
	seq       uint64
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
	return ix, nil
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
	tris := search.DistinctTrigrams(folded)

	ix.mu.RLock()
	defer ix.mu.RUnlock()

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
func (ix *NameIndex) Merge(ctx context.Context, gate func() bool) error {
	if gate != nil && !gate() {
		return nil
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()

	var entries []Entry
	if ix.base != nil {
		if err := ix.base.EachEntry(func(e Entry) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if ix.tombstonedLocked(e.Share, e.Path, 0) {
				return nil
			}
			entries = append(entries, e)
			return nil
		}); err != nil {
			return err
		}
	}
	if gate != nil && !gate() {
		return nil
	}

	seen := map[tombKey]bool{}
	for _, e := range entries {
		seen[tombKey{share: e.Share, path: e.Path}] = true
	}
	for _, d := range ix.delta {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ix.tombstonedLocked(d.share, d.path, d.seq) {
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
	buf, err := WriteBase(entries, ix.cfg.BlockSize, ix.cfg.PruneDFRatio)
	if err != nil {
		return err
	}
	if gate != nil && !gate() {
		return nil
	}

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
	for _, f := range ix.deltaFiles {
		if rerr := os.Remove(f); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return fmt.Errorf("index: removing a merged delta: %w", rerr)
		}
	}
	tombPath := filepath.Join(ix.dir, "tomb.idx")
	if rerr := os.Remove(tombPath); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		return fmt.Errorf("index: removing the merged tombstones: %w", rerr)
	}

	seg, oerr := OpenBase(buf)
	if oerr != nil {
		return oerr
	}
	ix.base = seg
	ix.baseBytes = int64(len(buf))
	ix.delta = nil
	ix.deltaFiles = nil
	ix.deltaBytes = 0
	ix.tomb = map[tombKey]uint64{}
	ix.tombBytes = 0
	return nil
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
