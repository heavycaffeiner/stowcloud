package index

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
)

// base.idx holds the immutable block-compressed trigram segment.
//
//	header            128 bytes, fixed
//	block directory   per block: u64 offset, u32 comp_len, u32 raw_len
//	trigram dict      sorted: [3]byte trigram, u8 flags, u32 off, u32 len
//	posting lists     block ids, delta then varint encoded
//	blocks            zstd frames holding block_size names each, tree ordered
//
// Three properties keep this small, and each one carries weight.
//
// Postings reference blocks rather than documents, so packing 32 names into a
// single posting element shortens every list by a factor of 32. Blocks are
// compressed together in tree order, letting adjacent names share a prefix that
// all but disappears; hence the builder sorts before chunking, and hence random
// order costs several times as much. No position information is stored, since
// filename matching needs no offsets and ranking is based on name matches rather
// than BM25.
//
// The cost is false positives: a block whose postings intersect may contain no
// actual match. Filtering them means decompressing the block and scanning its
// names, the same trade plocate documents.

// The format's own constants.
const (
	HeaderLen     = 128
	BlockDirEntry = 16
	DictEntry     = 12

	// FlagPruned identifies a dictionary entry whose posting list was
	// discarded.
	FlagPruned = 1

	// MinBlocksForPrune marks where high-df pruning begins to carry meaning.
	// Across three blocks a trigram in two reaches 67 percent and would be
	// discarded, leaving nothing to intersect. Pruning engages only once a
	// threshold expressed as a fraction of blocks describes selectivity rather
	// than an artefact of a tiny corpus.
	MinBlocksForPrune = 16
)

// Magic identifies a base segment and Version gives the format revision this
// build reads and writes. Magic is a string rather than a byte array so it can be
// a constant, since a package-level array would be mutable state.
const (
	Magic   = "SCNB"
	Version = 1
)

// Entry is one indexed name.
type Entry struct {
	Share uint32
	Path  string
}

// EncodeBlock serializes a block's payload, meaning the names themselves, which
// is what leaves the index self-contained and independent of any database row.
func EncodeBlock(entries []Entry) []byte {
	size := 0
	for _, e := range entries {
		size += len(e.Path) + 4
	}
	out := make([]byte, 0, size)
	for _, e := range entries {
		out = search.PutVarint(out, uint64(e.Share))
		out = search.PutVarint(out, uint64(len(e.Path)))
		out = append(out, e.Path...)
	}
	return out
}

// DecodeBlock parses a block's payload.
func DecodeBlock(payload []byte) ([]Entry, error) {
	var out []Entry
	pos := 0
	for pos < len(payload) {
		share, next, err := search.Varint(payload, pos)
		if err != nil {
			return nil, fmt.Errorf("%w: a truncated share id in a block", ErrCorrupt)
		}
		pos = next
		length, next, err := search.Varint(payload, pos)
		if err != nil {
			return nil, fmt.Errorf("%w: a truncated name length in a block", ErrCorrupt)
		}
		pos = next
		n, nerr := num.Narrow[int](length)
		if nerr != nil || n > len(payload)-pos {
			return nil, fmt.Errorf("%w: a name runs past the end of its block", ErrCorrupt)
		}
		id, serr := num.Narrow[uint32](share)
		if serr != nil {
			return nil, fmt.Errorf("%w: a share id that cannot be one", ErrCorrupt)
		}
		end := pos + n
		out = append(out, Entry{Share: id, Path: string(payload[pos:end])})
		pos = end
	}
	return out, nil
}

// WriteBase constructs a base segment.
//
// entries must arrive already in tree order. This is mandatory: block
// compression is the entire reason the index stays small, and it depends on
// adjacent names sharing a prefix.
func WriteBase(entries []Entry, blockSize uint32, pruneDFRatio float32) ([]byte, error) {
	if blockSize == 0 {
		blockSize = 1
	}
	bs := int(blockSize)
	blockCount := (len(entries) + bs - 1) / bs
	// The block id is a u32 by format, so a corpus needing more blocks than
	// that is refused here rather than truncated into ids pointing at the
	// wrong bytes.
	blockCount32, err := num.Narrow[uint32](blockCount)
	if err != nil {
		return nil, fmt.Errorf("index: %d blocks does not fit a u32 block id", blockCount)
	}

	var blocksBuf, blockDir []byte
	// postings is keyed by trigram, and the keys are written in sorted order
	// below so the dictionary supports binary search and the bytes stay
	// deterministic. Letting map iteration order reach a format-defined byte
	// would make two runs differ.
	postings := map[search.Trigram][]uint32{}
	var scratch []search.Trigram

	for start, bid := 0, uint32(0); start < len(entries); start, bid = start+bs, bid+1 {
		end := min(start+bs, len(entries))
		chunk := entries[start:end]

		// The whole block's trigrams, folded and deduplicated, so the index
		// never records a case or normalisation variant more than once.
		scratch = scratch[:0]
		for _, e := range chunk {
			scratch = search.AppendTrigrams(scratch, search.FoldString(e.Path))
		}
		search.SortTrigrams(scratch)
		scratch = search.DedupTrigrams(scratch)
		for _, t := range scratch {
			postings[t] = append(postings[t], bid)
		}

		raw := EncodeBlock(chunk)
		comp, cerr := Compress(raw)
		if cerr != nil {
			return nil, cerr
		}
		// The directory stores both lengths in 32 bits, so a block exceeding
		// that is unrepresentable and gets rejected rather than truncated into
		// an entry pointing at the wrong bytes.
		compLen, cerr := num.Narrow[uint32](len(comp))
		rawLen, rerr := num.Narrow[uint32](len(raw))
		if cerr != nil || rerr != nil {
			return nil, fmt.Errorf("index: a block of %d bytes does not fit the directory", len(raw))
		}
		blockDir = binary.LittleEndian.AppendUint64(blockDir, uint64(len(blocksBuf)))
		blocksBuf = append(blocksBuf, comp...)
		blockDir = binary.LittleEndian.AppendUint32(blockDir, compLen)
		blockDir = binary.LittleEndian.AppendUint32(blockDir, rawLen)
	}

	keys := make([]search.Trigram, 0, len(postings))
	for t := range postings {
		keys = append(keys, t)
	}
	search.SortTrigrams(keys)

	pruneAbove := float64(pruneDFRatio) * float64(blockCount)
	canPrune := blockCount >= MinBlocksForPrune

	dict := make([]byte, 0, len(keys)*DictEntry)
	var postBuf []byte
	var prunedCount uint32

	for _, t := range keys {
		ids := postings[t]
		// A trigram present in more than the configured fraction of blocks
		// offers almost no selectivity while holding the longest posting list,
		// the worst possible ratio of bytes to information. The list is dropped
		// and the dictionary entry retained, so a query can distinguish pruned
		// from absent.
		pruned := canPrune && float64(len(ids)) > pruneAbove
		var off, length uint32
		if pruned {
			prunedCount++
		} else {
			start, serr := num.Narrow[uint32](len(postBuf))
			if serr != nil {
				return nil, fmt.Errorf("index: the posting region does not fit a u32 offset")
			}
			postBuf = search.PutAscending(postBuf, ids)
			end, eerr := num.Narrow[uint32](len(postBuf))
			if eerr != nil {
				return nil, fmt.Errorf("index: the posting region does not fit a u32 offset")
			}
			off, length = start, end-start
		}
		b := t.Bytes()
		dict = append(dict, b[0], b[1], b[2])
		if pruned {
			dict = append(dict, FlagPruned)
		} else {
			dict = append(dict, 0)
		}
		dict = binary.LittleEndian.AppendUint32(dict, off)
		dict = binary.LittleEndian.AppendUint32(dict, length)
	}

	blockDirOff := uint64(HeaderLen)
	blockDirLen := uint64(len(blockDir))
	dictOff := blockDirOff + blockDirLen
	dictLen := uint64(len(dict))
	postOff := dictOff + dictLen
	postLen := uint64(len(postBuf))
	blocksOff := postOff + postLen
	blocksLen := uint64(len(blocksBuf))

	hdr := make([]byte, HeaderLen)
	copy(hdr[0:4], Magic)
	binary.LittleEndian.PutUint32(hdr[4:], Version)
	binary.LittleEndian.PutUint32(hdr[8:], blockSize)
	binary.LittleEndian.PutUint32(hdr[12:], blockCount32)
	binary.LittleEndian.PutUint64(hdr[16:], uint64(len(entries)))
	dictCount, kerr := num.Narrow[uint32](len(keys))
	if kerr != nil {
		return nil, fmt.Errorf("index: %d trigrams does not fit the dictionary count", len(keys))
	}
	binary.LittleEndian.PutUint32(hdr[24:], dictCount)
	binary.LittleEndian.PutUint32(hdr[28:], prunedCount)
	binary.LittleEndian.PutUint32(hdr[32:], math.Float32bits(pruneDFRatio))
	binary.LittleEndian.PutUint64(hdr[40:], blockDirOff)
	binary.LittleEndian.PutUint64(hdr[48:], blockDirLen)
	binary.LittleEndian.PutUint64(hdr[56:], dictOff)
	binary.LittleEndian.PutUint64(hdr[64:], dictLen)
	binary.LittleEndian.PutUint64(hdr[72:], postOff)
	binary.LittleEndian.PutUint64(hdr[80:], postLen)
	binary.LittleEndian.PutUint64(hdr[88:], blocksOff)
	binary.LittleEndian.PutUint64(hdr[96:], blocksLen)

	total, terr := num.Narrow[int](blocksOff + blocksLen)
	if terr != nil {
		return nil, fmt.Errorf("index: the segment does not fit in memory: %w", terr)
	}
	out := make([]byte, 0, total)
	out = append(out, hdr...)
	out = append(out, blockDir...)
	out = append(out, dict...)
	out = append(out, postBuf...)
	out = append(out, blocksBuf...)
	return out, nil
}

// LookupKind describes what a dictionary probe found.
type LookupKind int

const (
	// LookupMissing means no block contains the trigram, leaving the
	// intersection empty and the query without base hits.
	LookupMissing LookupKind = iota
	// LookupPruned means the list was discarded at build time for lacking
	// selectivity. The query skips it, and when every trigram lands here the
	// caller falls back to walking.
	LookupPruned
	// LookupPostings means the entry holds a list.
	LookupPostings
)

// BaseSegment provides the read side. Opening one parses only the header.
type BaseSegment struct {
	buf []byte

	BlockSize    uint32
	BlockCount   uint32
	EntryCount   uint64
	TrigramCount uint32
	PrunedCount  uint32
	PruneDFRatio float32
	FileBytes    uint64

	blockDirOff int
	dictOff     int
	postOff     int
	postLen     int
	blocksOff   int
	blocksLen   int
}

// OpenBase reads a segment from a buffer, which is a mapping in the product and
// a fixture in the tests.
//
// Every header offset is validated against the buffer before anything is read
// through it. A segment is a file on disk that a crash or failing hardware may
// have rewritten, so nothing here trusts an unchecked length.
func OpenBase(buf []byte) (*BaseSegment, error) {
	if len(buf) < HeaderLen {
		return nil, fmt.Errorf("%w: shorter than a header", ErrCorrupt)
	}
	if string(buf[0:4]) != Magic {
		return nil, fmt.Errorf("%w: bad magic", ErrCorrupt)
	}
	if v := binary.LittleEndian.Uint32(buf[4:]); v != Version {
		return nil, fmt.Errorf("%w: unsupported index version %d", ErrCorrupt, v)
	}

	s := &BaseSegment{
		buf:          buf,
		BlockSize:    binary.LittleEndian.Uint32(buf[8:]),
		BlockCount:   binary.LittleEndian.Uint32(buf[12:]),
		EntryCount:   binary.LittleEndian.Uint64(buf[16:]),
		TrigramCount: binary.LittleEndian.Uint32(buf[24:]),
		PrunedCount:  binary.LittleEndian.Uint32(buf[28:]),
		PruneDFRatio: math.Float32frombits(binary.LittleEndian.Uint32(buf[32:])),
		FileBytes:    uint64(len(buf)),
	}
	// The offsets originate on disk, so each is narrowed rather than cast. A
	// header describing a section beyond the address space indicates a corrupt
	// segment rather than justifying a panic.
	for _, f := range []struct {
		at   int
		into *int
	}{
		{40, &s.blockDirOff}, {56, &s.dictOff},
		{72, &s.postOff}, {80, &s.postLen},
		{88, &s.blocksOff}, {96, &s.blocksLen},
	} {
		v, err := num.Narrow[int](binary.LittleEndian.Uint64(buf[f.at:]))
		if err != nil {
			return nil, fmt.Errorf("%w: a section offset that cannot be addressed", ErrCorrupt)
		}
		*f.into = v
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *BaseSegment) validate() error {
	fits := func(off, length int) bool {
		if off < 0 || length < 0 {
			return false
		}
		end := off + length
		return end >= off && end <= len(s.buf)
	}
	if !fits(s.blockDirOff, int(s.BlockCount)*BlockDirEntry) ||
		!fits(s.dictOff, int(s.TrigramCount)*DictEntry) ||
		!fits(s.postOff, s.postLen) ||
		!fits(s.blocksOff, s.blocksLen) {
		return fmt.Errorf("%w: section offsets run past the end of the segment", ErrCorrupt)
	}
	if s.BlockSize == 0 {
		return fmt.Errorf("%w: a zero block size", ErrCorrupt)
	}
	return nil
}

func (s *BaseSegment) dictAt(i int) (t search.Trigram, flags byte, off, length uint32) {
	e := s.buf[s.dictOff+i*DictEntry:]
	return search.TrigramOf(e[0], e[1], e[2]), e[3],
		binary.LittleEndian.Uint32(e[4:]), binary.LittleEndian.Uint32(e[8:])
}

// Lookup searches the dictionary for a trigram.
//
// The dictionary is sorted by packed trigram using big-endian packing, so this
// integer comparison yields the same ordering as comparing the three raw
// bytes.
func (s *BaseSegment) Lookup(t search.Trigram) (LookupKind, []byte) {
	lo, hi := 0, int(s.TrigramCount)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		got, flags, off, length := s.dictAt(mid)
		switch {
		case got < t:
			lo = mid + 1
		case got > t:
			hi = mid
		default:
			if flags&FlagPruned != 0 {
				return LookupPruned, nil
			}
			start := s.postOff + int(off)
			end := start + int(length)
			if start < s.postOff || end > s.postOff+s.postLen || end < start {
				return LookupMissing, nil
			}
			return LookupPostings, s.buf[start:end]
		}
	}
	return LookupMissing, nil
}

// Postings returns the decoded block-id list for a trigram.
func (s *BaseSegment) Postings(t search.Trigram) ([]uint32, error) {
	kind, raw := s.Lookup(t)
	if kind != LookupPostings {
		return nil, nil
	}
	return search.Ascending(raw)
}

// Block decompresses a block and parses its names. This is where false positives
// from the posting intersection get filtered away.
func (s *BaseSegment) Block(id uint32) ([]Entry, error) {
	if id >= s.BlockCount {
		return nil, fmt.Errorf("%w: block %d of %d", ErrCorrupt, id, s.BlockCount)
	}
	e := s.buf[s.blockDirOff+int(id)*BlockDirEntry:]
	off := binary.LittleEndian.Uint64(e[0:])
	compLen := binary.LittleEndian.Uint32(e[8:])
	rawLen := binary.LittleEndian.Uint32(e[12:])

	start, oerr := num.Narrow[int](off)
	if oerr != nil || start > s.blocksLen {
		return nil, fmt.Errorf("%w: block %d starts past the block region", ErrCorrupt, id)
	}
	start += s.blocksOff
	end := start + int(compLen)
	if end < start || end > s.blocksOff+s.blocksLen {
		return nil, fmt.Errorf("%w: block %d runs past the block region", ErrCorrupt, id)
	}

	raw, err := DecompressHint(s.buf[start:end], int(rawLen))
	if err != nil {
		return nil, err
	}
	return DecodeBlock(raw)
}

// blockFirst returns a block's first entry, the key the block-level binary
// search compares against.
//
// Each call costs one decompression, so the search below performs log(blocks) of
// them instead of reading the whole segment.
func (s *BaseSegment) blockFirst(id uint32) (Entry, bool) {
	entries, err := s.Block(id)
	if err != nil || len(entries) == 0 {
		return Entry{}, false
	}
	return entries[0], true
}

// entryLess orders two entries as TreeOrder wrote them: by share first, then by
// path in tree order.
func entryLess(aShare uint32, aPath string, bShare uint32, bPath string) bool {
	if aShare != bShare {
		return aShare < bShare
	}
	return TreeCompare(aPath, bPath) < 0
}

// EachUnder visits the live entries within one directory's subtree.
//
// It exists so that keeping the index current costs the changed directory rather
// than the whole corpus. The alternative reads every entry to locate the handful
// beneath one path, which is what would make an incremental update as expensive
// as a rebuild.
//
// The segment sits in tree order, and that order places the separator below
// every other byte, so a directory's entire subtree forms one contiguous run.
// That is what reduces this to a block-level binary search followed by a forward
// scan, and it is the same property block compression already relies on.
func (s *BaseSegment) EachUnder(share uint32, dir string, fn func(Entry) error) error {
	if s.BlockCount == 0 {
		return nil
	}

	// The earliest block that could contain the run: the last whose first entry
	// does not sort beyond the directory. That is one before the first which
	// does, because a run may begin partway through a block.
	lo, hi := uint32(0), s.BlockCount
	for lo < hi {
		mid := lo + (hi-lo)/2
		first, ok := s.blockFirst(mid)
		if !ok {
			// An unreadable block admits no comparison, so the search cannot
			// narrow beyond it and the scan begins here instead. The index is a
			// cache, so reading less of it costs speed and never correctness.
			hi = mid
			break
		}
		if entryLess(first.Share, first.Path, share, dir) {
			lo = mid + 1
			continue
		}
		hi = mid
	}
	start := hi
	if start > 0 {
		start--
	}

	for id := start; id < s.BlockCount; id++ {
		entries, err := s.Block(id)
		if err != nil {
			return err
		}
		for _, e := range entries {
			switch {
			case e.Share < share:
				continue
			case e.Share > share:
				// Beyond this share, and shares are ordered.
				return nil
			}
			if under(e.Path, dir) {
				if ferr := fn(e); ferr != nil {
					return ferr
				}
				continue
			}
			if TreeCompare(e.Path, dir) > 0 {
				// Beyond the run. The subtree is contiguous, so an entry
				// sorting above the directory without lying beneath it ends
				// it.
				return nil
			}
		}
	}
	return nil
}

// under reports whether path is dir itself or sits below it. The share root,
// denoted by the empty string, contains everything.
func under(path, dir string) bool {
	if dir == "" {
		return true
	}
	if path == dir {
		return true
	}
	return len(path) > len(dir) && path[:len(dir)] == dir && path[len(dir)] == '/'
}

// EachEntry visits every live entry in block order, as the merge path requires.
// It streams rather than accumulating, since a base segment spans the entire
// corpus and materialising it would negate the purpose of blocks.
func (s *BaseSegment) EachEntry(fn func(Entry) error) error {
	for id := uint32(0); id < s.BlockCount; id++ {
		entries, err := s.Block(id)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := fn(e); err != nil {
				return err
			}
		}
	}
	return nil
}
