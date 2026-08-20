package index

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/search"
)

// Milestone 8c's whole claim: reading the Rust-built segment, and re-writing it
// from the same corpus, produce identical structural bytes and identical
// decoded payloads.
//
// The block payloads are compared through what they decompress to rather than
// byte for byte. zstd's format constrains what a decoder accepts, not what an
// encoder emits, so two independent encoders agreeing frame for frame would be
// a coincidence rather than a property. Everything else here is the format's
// own bytes and is compared exactly.

func goldenPath(name string) string {
	return filepath.Join("..", "..", "..", "testdata", "golden", "search", name)
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(goldenPath(name))
	if err != nil {
		t.Fatalf("reading the fixture %s: %v", name, err)
	}
	return b
}

// readCorpus parses corpus.txt: share id, a tab, the path.
func readCorpus(t *testing.T) []Entry {
	t.Helper()
	var out []Entry
	sc := bufio.NewScanner(strings.NewReader(string(readGolden(t, "corpus.txt"))))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		share, path, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("a corpus line has no tab: %q", line)
		}
		id, err := strconv.ParseUint(share, 10, 32)
		if err != nil {
			t.Fatalf("a corpus share id %q is not a number: %v", share, err)
		}
		out = append(out, Entry{Share: uint32(id), Path: path})
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanning the corpus: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("the corpus is empty, so this test checks nothing")
	}
	return out
}

func openGoldenBase(t *testing.T) *BaseSegment {
	t.Helper()
	s, err := OpenBase(readGolden(t, "base.idx"))
	if err != nil {
		t.Fatalf("opening the golden base segment: %v", err)
	}
	return s
}

func TestTheGoldenBaseSegmentOpens(t *testing.T) {
	s := openGoldenBase(t)
	corpus := readCorpus(t)

	if s.EntryCount != uint64(len(corpus)) {
		t.Fatalf("the segment holds %d entries, the corpus has %d", s.EntryCount, len(corpus))
	}
	if s.BlockSize == 0 || s.BlockCount == 0 || s.TrigramCount == 0 {
		t.Fatalf("the header is degenerate: %+v", s)
	}
	// The corpus is deliberately over 512 names so it fills more than the
	// sixteen blocks below which pruning refuses to run.
	if s.BlockCount < MinBlocksForPrune {
		t.Fatalf("the corpus produced %d blocks, too few for pruning to run", s.BlockCount)
	}
	if s.PrunedCount == 0 {
		t.Fatal("nothing was pruned, so the pruning path is unexercised")
	}
}

// Every name in the corpus comes back out of the blocks, in the same order the
// writer put it in.
func TestTheGoldenBlocksDecodeToTheCorpus(t *testing.T) {
	s := openGoldenBase(t)

	want := readCorpus(t)
	TreeOrder(want)

	var got []Entry
	if err := s.EachEntry(func(e Entry) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("walking the segment: %v", err)
	}

	if len(got) != len(want) {
		t.Fatalf("the segment decoded to %d names, the corpus has %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d is %+v, want %+v\n(tree order disagrees, "+
				"which would scatter siblings and cost compression)", i, got[i], want[i])
		}
	}
}

// The claim 8c exists for. Re-writing the segment from the same corpus
// reproduces the header, the block directory's structural fields, the trigram
// dictionary and the posting lists exactly, and every block decodes to the
// same bytes of the same recorded length.
func TestRewritingTheSegmentReproducesItsStructuralBytes(t *testing.T) {
	goldenBuf := readGolden(t, "base.idx")
	golden := openGoldenBase(t)

	entries := readCorpus(t)
	TreeOrder(entries)
	mineBuf, err := WriteBase(entries, golden.BlockSize, golden.PruneDFRatio)
	if err != nil {
		t.Fatalf("WriteBase: %v", err)
	}
	mine, err := OpenBase(mineBuf)
	if err != nil {
		t.Fatalf("opening what was just written: %v", err)
	}

	// The header, minus the four section offsets that follow the block region,
	// whose values depend on the compressed sizes.
	for _, f := range []struct {
		name      string
		got, want uint64
	}{
		{"block size", uint64(mine.BlockSize), uint64(golden.BlockSize)},
		{"block count", uint64(mine.BlockCount), uint64(golden.BlockCount)},
		{"entry count", mine.EntryCount, golden.EntryCount},
		{"trigram count", uint64(mine.TrigramCount), uint64(golden.TrigramCount)},
		{"pruned count", uint64(mine.PrunedCount), uint64(golden.PrunedCount)},
	} {
		if f.got != f.want {
			t.Errorf("the %s is %d, the fixture says %d", f.name, f.got, f.want)
		}
	}
	if mine.PruneDFRatio != golden.PruneDFRatio {
		t.Errorf("the prune ratio is %v, the fixture says %v", mine.PruneDFRatio, golden.PruneDFRatio)
	}

	// The trigram dictionary, byte for byte. It carries the trigram, the
	// pruned flag and the posting offset and length, so comparing it exactly
	// is comparing the pruning decision and the posting layout together.
	gDict := section(t, goldenBuf, 56, 64)
	mDict := section(t, mineBuf, 56, 64)
	if !bytes.Equal(gDict, mDict) {
		t.Fatalf("the trigram dictionary differs: %d bytes against %d",
			len(mDict), len(gDict))
	}

	// The posting lists, byte for byte.
	gPost := section(t, goldenBuf, 72, 80)
	mPost := section(t, mineBuf, 72, 80)
	if !bytes.Equal(gPost, mPost) {
		t.Fatalf("the posting lists differ: %d bytes against %d", len(mPost), len(gPost))
	}

	// The block directory's raw lengths, which are the format's own bytes. The
	// offsets and compressed lengths are not compared: they are a consequence
	// of the encoder's framing.
	if mine.BlockCount != golden.BlockCount {
		t.Fatalf("block counts differ, so the directories cannot be compared")
	}
	for id := uint32(0); id < golden.BlockCount; id++ {
		gRaw := blockRawLen(goldenBuf, golden, id)
		mRaw := blockRawLen(mineBuf, mine, id)
		if gRaw != mRaw {
			t.Fatalf("block %d records %d uncompressed bytes, the fixture says %d",
				id, mRaw, gRaw)
		}
		gBlock, gerr := golden.Block(id)
		mBlock, merr := mine.Block(id)
		if gerr != nil || merr != nil {
			t.Fatalf("block %d: %v / %v", id, gerr, merr)
		}
		if len(gBlock) != len(mBlock) {
			t.Fatalf("block %d holds %d names, the fixture holds %d", id, len(mBlock), len(gBlock))
		}
		for i := range gBlock {
			if gBlock[i] != mBlock[i] {
				t.Fatalf("block %d name %d is %+v, the fixture says %+v",
					id, i, mBlock[i], gBlock[i])
			}
		}
	}
}

// Cross-decoding is the property that survives when byte-identity does not:
// this build's decoder reads the Rust encoder's frames, and its own frames
// decode to the same bytes of the recorded length.
func TestThisBuildDecodesTheRustEncodersFrames(t *testing.T) {
	golden := openGoldenBase(t)
	if golden.BlockCount == 0 {
		t.Fatal("the fixture has no blocks")
	}
	for id := uint32(0); id < golden.BlockCount; id++ {
		entries, err := golden.Block(id)
		if err != nil {
			t.Fatalf("block %d did not decompress: %v", id, err)
		}
		if len(entries) == 0 {
			t.Fatalf("block %d decoded to nothing", id)
		}
		// Re-encoding the same names and decoding again is a round trip
		// through this build's encoder over the Rust writer's payload.
		raw := EncodeBlock(entries)
		comp, cerr := Compress(raw)
		if cerr != nil {
			t.Fatalf("compressing block %d: %v", id, cerr)
		}
		back, derr := DecompressHint(comp, len(raw))
		if derr != nil {
			t.Fatalf("decompressing block %d: %v", id, derr)
		}
		if !bytes.Equal(back, raw) {
			t.Fatalf("block %d did not survive a round trip", id)
		}
	}
}

// The posting lists have to select the blocks that actually hold the trigram,
// or the intersection returns hits the block scan then has to throw away, or
// worse, misses one.
func TestEveryPostingListSelectsBlocksThatHoldTheTrigram(t *testing.T) {
	s := openGoldenBase(t)

	// A handful of trigrams from real names in the corpus.
	corpus := readCorpus(t)
	seen := map[search.Trigram]bool{}
	var probes []search.Trigram
	for _, e := range corpus {
		for _, tg := range search.DistinctTrigrams(search.FoldString(e.Path)) {
			if !seen[tg] {
				seen[tg] = true
				probes = append(probes, tg)
			}
		}
		if len(probes) > 40 {
			break
		}
	}

	checked := 0
	for _, tg := range probes {
		kind, _ := s.Lookup(tg)
		if kind != LookupPostings {
			continue
		}
		ids, err := s.Postings(tg)
		if err != nil {
			t.Fatalf("decoding the postings for %x: %v", tg, err)
		}
		for _, id := range ids {
			block, berr := s.Block(id)
			if berr != nil {
				t.Fatalf("block %d: %v", id, berr)
			}
			found := false
			for _, e := range block {
				if bytes.Contains(search.FoldString(e.Path), tg[:]) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("the postings for %x name block %d, which does not hold it", tg, id)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no posting list was checked")
	}
}

// A trigram nothing holds is Missing, and a pruned one is Pruned. Conflating
// them is how a fallback becomes a wrong empty result.
func TestMissingAndPrunedAreDifferentAnswers(t *testing.T) {
	s := openGoldenBase(t)

	// "data/" is in every name by construction, so it is the pruned case.
	kind, _ := s.Lookup(search.Trigram{'d', 'a', 't'})
	if kind != LookupPruned {
		t.Fatalf("the ubiquitous trigram looked up as %v, want pruned", kind)
	}

	// Three bytes no filename in the corpus contains.
	kind, _ = s.Lookup(search.Trigram{0x00, 0x01, 0x02})
	if kind != LookupMissing {
		t.Fatalf("an absent trigram looked up as %v, want missing", kind)
	}
}

func section(t *testing.T, buf []byte, offAt, lenAt int) []byte {
	t.Helper()
	off := binary.LittleEndian.Uint64(buf[offAt:])
	length := binary.LittleEndian.Uint64(buf[lenAt:])
	if off+length > uint64(len(buf)) {
		t.Fatalf("a section at %d runs past the buffer", offAt)
	}
	return buf[off : off+length]
}

func blockRawLen(buf []byte, s *BaseSegment, id uint32) uint32 {
	at := s.blockDirOff + int(id)*BlockDirEntry
	return binary.LittleEndian.Uint32(buf[at+12:])
}
