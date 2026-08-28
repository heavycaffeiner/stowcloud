package index

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
)

// base.idx, the immutable block-compressed trigram segment.
//
//	header (128 B)
//	block directory     block_count x { u64 offset, u32 comp_len, u32 raw_len }
//	trigram dictionary  trigram_count x { [3]byte trigram, u8 flags, u32 off, u32 len }, sorted
//	posting lists       delta and varint encoded block ids
//	blocks              zstd frames, block_size names each, in tree order
//
// Three things make this small and all three are load-bearing.
//
// Postings point at blocks rather than at documents, so grouping 32 names into
// one posting element makes every list 32 times shorter. Blocks are compressed
// together in tree order, so adjacent names share a prefix that nearly
// vanishes; that is why the builder sorts before chunking, and why random order
// costs multiples. There is no position information, because filename matching
// does not need offsets and the ranking is name-match based rather than BM25.
//
// The price is false positives: a block whose postings intersect may hold no
// actual match. They are filtered by decompressing the block and scanning its
// names, which is the trade plocate documents.

// The format's own constants.
const (
	HeaderLen     = 128
	BlockDirEntry = 16
	DictEntry     = 12

	// FlagPruned marks a dictionary entry whose posting list was dropped.
	FlagPruned = 1

	// MinBlocksForPrune is where high-df pruning starts to mean anything.
	// With three blocks, a trigram in two of them is at 67 percent and would
	// be dropped, leaving nothing to intersect. Pruning only starts once "60
	// percent of the blocks" is a statement about selectivity rather than an
	// artefact of a tiny corpus.
	MinBlocksForPrune = 16
)

// Magic identifies a base segment, and Version is the format revision this
// build reads and writes. Magic is a string rather than a byte array so it can
// be a constant: a package-level array would be mutable state.
const (
	Magic   = "SCNB"
	Version = 1
)

// Entry is one indexed name.
type Entry struct {
	Share uint32
	Path  string
}

// EncodeBlock encodes one block's payload: the names themselves, which is what
// makes the index self-contained and independent of any database row.
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

// DecodeBlock parses a block payload.
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

// WriteBase builds a base segment.
//
// entries must already be in tree order. That is not optional: block
// compression is the whole reason the index is small, and it only works when
// adjacent names share a prefix.
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
	// postings is keyed by trigram, and the keys are emitted in sorted order
	// below so the dictionary is binary-searchable and the bytes are
	// deterministic. A map iteration reaching a format-defined byte would make
	// two runs differ.
	postings := map[search.Trigram][]uint32{}
	var scratch []search.Trigram

	for start, bid := 0, uint32(0); start < len(entries); start, bid = start+bs, bid+1 {
		end := min(start+bs, len(entries))
		chunk := entries[start:end]

		// The trigrams of the whole block, folded and deduplicated, so the
		// index never stores a case or normalisation variant twice.
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
		// The directory records both lengths in 32 bits, so a block past that
		// is unrepresentable and is refused rather than truncated into an
		// entry pointing at the wrong bytes.
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
		// A trigram in more than the ratio of the blocks carries almost no
		// selectivity while owning the longest posting list, which is the worst
		// possible bytes per bit of information. The list is dropped and the
		// dictionary entry kept, so a query can tell pruned from absent.
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

// LookupKind is what a dictionary probe found.
type LookupKind int

const (
	// LookupMissing means no block holds the trigram, so the intersection is
	// empty and the query has no base hits.
	LookupMissing LookupKind = iota
	// LookupPruned means the list was dropped at build time for having no
	// selectivity. The query ignores it, and if every trigram lands here the
	// caller falls back to the walk.
	LookupPruned
	// LookupPostings means the entry carries a list.
	LookupPostings
)

// BaseSegment is the read side. Opening it parses only the header.
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

// OpenBase reads a segment out of a buffer, which is a mapping in the product
// and a fixture in the tests.
//
// Every offset in the header is validated against the buffer before anything
// is read through it. A segment is a file on disk that a crash or a bad disk
// can have rewritten, so nothing here trusts a length it did not check.
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
	// The offsets come off disk, so each one is narrowed rather than cast: a
	// header claiming a section past the address space is a corrupt segment,
	// not a panic.
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

// Lookup probes the dictionary for a trigram.
//
// The dictionary is sorted by the packed trigram, and the packing is
// big-endian, so this integer comparison is the same order as a comparison on
// the three raw bytes.
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

// Postings is the decoded block-id list for a trigram.
func (s *BaseSegment) Postings(t search.Trigram) ([]uint32, error) {
	kind, raw := s.Lookup(t)
	if kind != LookupPostings {
		return nil, nil
	}
	return search.Ascending(raw)
}

// Block decompresses one block and parses its names. This is where the false
// positives from the posting intersection are filtered out.
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

// blockFirst is the first entry of a block, which is the key the block-level
// binary search compares against.
//
// It costs one decompression, so the search below pays log(blocks) of them
// rather than reading the segment.
func (s *BaseSegment) blockFirst(id uint32) (Entry, bool) {
	entries, err := s.Block(id)
	if err != nil || len(entries) == 0 {
		return Entry{}, false
	}
	return entries[0], true
}

// entryLess orders two entries the way TreeOrder wrote them: by share, then by
// path in tree order.
func entryLess(aShare uint32, aPath string, bShare uint32, bPath string) bool {
	if aShare != bShare {
		return aShare < bShare
	}
	return TreeCompare(aPath, bPath) < 0
}

// EachUnder visits the live entries of one directory's subtree.
//
// It exists so keeping the index current costs the directory that changed
// rather than the corpus. The alternative is reading every entry to find the
// handful under one path, which is what makes an incremental update as
// expensive as a rebuild.
//
// The segment is in tree order and the order maps the separator below every
// other byte, so a directory's whole subtree is one contiguous run. That is
// what makes this a block-level binary search followed by a forward scan, and
// it is the same property block compression already depends on.
func (s *BaseSegment) EachUnder(share uint32, dir string, fn func(Entry) error) error {
	if s.BlockCount == 0 {
		return nil
	}

	// The lowest block that can hold the run: the last one whose first entry
	// does not sort past the directory. One before the first that does, because
	// the run can begin partway through a block.
	lo, hi := uint32(0), s.BlockCount
	for lo < hi {
		mid := lo + (hi-lo)/2
		first, ok := s.blockFirst(mid)
		if !ok {
			// An unreadable block cannot be compared, so the search cannot
			// narrow past it and the scan starts here instead. The index is a
			// cache: reading less of it is slower, never wrong.
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
				// Past this share, and shares are ordered.
				return nil
			}
			if under(e.Path, dir) {
				if ferr := fn(e); ferr != nil {
					return ferr
				}
				continue
			}
			if TreeCompare(e.Path, dir) > 0 {
				// Past the run: the subtree is contiguous, so an entry sorting
				// above the directory and not under it ends it.
				return nil
			}
		}
	}
	return nil
}

// under reports whether path is dir or lies beneath it. The share root, named
// by the empty string, holds everything.
func under(path, dir string) bool {
	if dir == "" {
		return true
	}
	if path == dir {
		return true
	}
	return len(path) > len(dir) && path[:len(dir)] == dir && path[len(dir)] == '/'
}

// EachEntry visits every live entry in block order, which the merge path
// needs. It streams rather than collecting: a base segment holds the whole
// corpus and materialising it would defeat the point of blocks.
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
