package index

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
)

// entries builds a tree-ordered corpus of n names under a few directories.
func entries(n int) []Entry {
	out := make([]Entry, 0, n)
	for i := range n {
		out = append(out, Entry{
			Share: 1,
			Path:  fmt.Sprintf("dir%02d/file-%04d.txt", i%8, i),
		})
	}
	TreeOrder(out)
	return out
}

func mustWriteBase(t *testing.T, e []Entry, blockSize uint32, prune float32) *BaseSegment {
	t.Helper()
	buf, err := WriteBase(e, blockSize, prune)
	if err != nil {
		t.Fatalf("WriteBase: %v", err)
	}
	seg, err := OpenBase(buf)
	if err != nil {
		t.Fatalf("OpenBase: %v", err)
	}
	return seg
}

// Build a base from a corpus, read it back, and get the corpus back: the
// round trip the whole format exists for.
func TestBaseRoundTripsACorpus(t *testing.T) {
	want := entries(200)
	seg := mustWriteBase(t, want, 32, 0.6)

	if seg.EntryCount != uint64(len(want)) {
		t.Errorf("header says %d entries, want %d", seg.EntryCount, len(want))
	}
	if seg.BlockSize != 32 {
		t.Errorf("header says block size %d, want 32", seg.BlockSize)
	}
	if want := (len(want) + 31) / 32; int(seg.BlockCount) != want {
		t.Errorf("header says %d blocks, want %d", seg.BlockCount, want)
	}

	var got []Entry
	if err := seg.EachEntry(func(e Entry) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("EachEntry: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("the corpus did not survive the round trip: got %d entries, want %d", len(got), len(want))
	}
}

// The header's fixed layout, checked field by field: it is a format two
// implementations have to agree on, so the offsets are pinned rather than
// assumed.
func TestBaseHeaderGoldenLayout(t *testing.T) {
	buf, err := WriteBase(entries(64), 32, 0.6)
	if err != nil {
		t.Fatalf("WriteBase: %v", err)
	}
	if len(buf) < HeaderLen {
		t.Fatalf("segment is shorter than a header")
	}
	if string(buf[0:4]) != Magic {
		t.Errorf("magic is %q, want %q", buf[0:4], Magic)
	}
	if v := binary.LittleEndian.Uint32(buf[4:]); v != Version {
		t.Errorf("version is %d, want %d", v, Version)
	}
	if bs := binary.LittleEndian.Uint32(buf[8:]); bs != 32 {
		t.Errorf("block size is %d, want 32", bs)
	}
	if n := binary.LittleEndian.Uint32(buf[12:]); n != 2 {
		t.Errorf("block count is %d, want 2", n)
	}
	if n := binary.LittleEndian.Uint64(buf[16:]); n != 64 {
		t.Errorf("entry count is %d, want 64", n)
	}
	// The block directory begins immediately after the header.
	if off := binary.LittleEndian.Uint64(buf[40:]); off != HeaderLen {
		t.Errorf("block directory starts at %d, want %d", off, HeaderLen)
	}
}

// The block payload encoding: a varint share, a varint length, then the name.
func TestBlockPayloadGoldenEncoding(t *testing.T) {
	in := []Entry{{Share: 1, Path: "a.txt"}, {Share: 300, Path: "b"}}
	raw := EncodeBlock(in)

	var want []byte
	want = search.PutVarint(want, 1)
	want = search.PutVarint(want, 5)
	want = append(want, "a.txt"...)
	want = search.PutVarint(want, 300)
	want = search.PutVarint(want, 1)
	want = append(want, 'b')

	if !bytes.Equal(raw, want) {
		t.Errorf("block payload is %x, want %x", raw, want)
	}
	got, err := DecodeBlock(raw)
	if err != nil {
		t.Fatalf("DecodeBlock: %v", err)
	}
	if !slices.Equal(got, in) {
		t.Errorf("decoded %v, want %v", got, in)
	}
}

func TestQueryFindsANameAndTheDictionaryIsSearchable(t *testing.T) {
	corpus := entries(200)
	seg := mustWriteBase(t, corpus, 32, 0.6)

	tris := search.DistinctTrigrams(search.FoldString("file-0007"))
	if len(tris) == 0 {
		t.Fatal("the probe query produced no trigram")
	}
	for _, tr := range tris {
		kind, _ := seg.Lookup(tr)
		if kind == LookupMissing {
			b := tr.Bytes()
			t.Errorf("trigram %q is absent from a corpus that contains it", b[:])
		}
	}
}

// A block id past the count is refused rather than read out of the directory
// at an offset that belongs to something else.
func TestBlockRefusesAnIdPastTheCount(t *testing.T) {
	seg := mustWriteBase(t, entries(64), 32, 0.6)
	if _, err := seg.Block(seg.BlockCount); !errors.Is(err, ErrCorrupt) {
		t.Errorf("block past the count = %v, want ErrCorrupt", err)
	}
	if _, err := seg.Block(^uint32(0)); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a huge block id = %v, want ErrCorrupt", err)
	}
}

// A trigram in more than the ratio of the blocks is dropped and its dictionary
// entry kept, so a query can tell pruned from absent.
func TestHighDFPruningDropsTheListAndKeepsTheEntry(t *testing.T) {
	// Every name shares the "common" run, so its trigrams are in every block.
	var corpus []Entry
	for i := range 32 * MinBlocksForPrune {
		corpus = append(corpus, Entry{Share: 1, Path: fmt.Sprintf("common-%04d.txt", i)})
	}
	TreeOrder(corpus)
	seg := mustWriteBase(t, corpus, 32, 0.6)

	if seg.PrunedCount == 0 {
		t.Fatal("nothing was pruned in a corpus where every name shares a run")
	}
	kind, _ := seg.Lookup(search.TrigramOf('o', 'm', 'm'))
	if kind != LookupPruned {
		t.Errorf("a trigram in every block reported %v, want LookupPruned", kind)
	}
}

// Pruning only starts once the ratio is a statement about selectivity rather
// than an artefact of a tiny corpus.
func TestPruningDoesNotApplyBelowTheBlockFloor(t *testing.T) {
	var corpus []Entry
	for i := range 32 * (MinBlocksForPrune - 1) {
		corpus = append(corpus, Entry{Share: 1, Path: fmt.Sprintf("common-%04d.txt", i)})
	}
	TreeOrder(corpus)
	seg := mustWriteBase(t, corpus, 32, 0.6)

	if seg.BlockCount >= MinBlocksForPrune {
		t.Fatalf("the fixture has %d blocks, which is not below the floor", seg.BlockCount)
	}
	if seg.PrunedCount != 0 {
		t.Errorf("%d trigrams were pruned below the block floor", seg.PrunedCount)
	}
}

// Every offset in the header is validated against the buffer before anything
// is read through it: a segment is a file a crash or a bad disk can rewrite.
func TestOpenBaseRefusesAMalformedHeader(t *testing.T) {
	good, err := WriteBase(entries(64), 32, 0.6)
	if err != nil {
		t.Fatalf("WriteBase: %v", err)
	}

	cases := []struct {
		name   string
		mutate func([]byte)
	}{
		{"bad magic", func(b []byte) { b[0] ^= 0xff }},
		{"unsupported version", func(b []byte) { binary.LittleEndian.PutUint32(b[4:], Version+1) }},
		{"zero block size", func(b []byte) { binary.LittleEndian.PutUint32(b[8:], 0) }},
		{"a section past the end", func(b []byte) {
			binary.LittleEndian.PutUint64(b[88:], ^uint64(0)/2)
		}},
		{"a block count past the buffer", func(b []byte) {
			binary.LittleEndian.PutUint32(b[12:], 1<<30)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			buf := slices.Clone(good)
			c.mutate(buf)
			if _, err := OpenBase(buf); !errors.Is(err, ErrCorrupt) {
				t.Errorf("OpenBase = %v, want ErrCorrupt", err)
			}
		})
	}

	if _, err := OpenBase(good[:HeaderLen-1]); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a buffer shorter than a header = %v, want ErrCorrupt", err)
	}
}

// A block that does not decompress to exactly the recorded size is not the
// block that was written, and parsing names out of it would be parsing
// something else.
func TestBlockRefusesAWrongDecompressedLength(t *testing.T) {
	buf, err := WriteBase(entries(64), 32, 0.6)
	if err != nil {
		t.Fatalf("WriteBase: %v", err)
	}
	seg, err := OpenBase(buf)
	if err != nil {
		t.Fatalf("OpenBase: %v", err)
	}
	// Overwrite block 0's recorded raw length in the directory.
	binary.LittleEndian.PutUint32(buf[seg.blockDirOff+12:], 999_999)
	if _, err := seg.Block(0); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a block whose length disagrees = %v, want ErrCorrupt", err)
	}
}

// A bit flipped anywhere in the compressed block region stops the scan rather
// than serving whatever the bytes happened to decode to.
func TestABitFlipInABlockStopsTheScan(t *testing.T) {
	buf, err := WriteBase(entries(200), 32, 0.6)
	if err != nil {
		t.Fatalf("WriteBase: %v", err)
	}
	seg, err := OpenBase(buf)
	if err != nil {
		t.Fatalf("OpenBase: %v", err)
	}

	corrupted := 0
	for i := seg.blocksOff; i < seg.blocksOff+seg.blocksLen && corrupted < 40; i += 7 {
		clone := slices.Clone(buf)
		clone[i] ^= 0xff
		cseg, oerr := OpenBase(clone)
		if oerr != nil {
			corrupted++
			continue
		}
		if err := cseg.EachEntry(func(Entry) error { return nil }); err != nil {
			corrupted++
		}
	}
	if corrupted == 0 {
		t.Error("no bit flip in the block region was detected")
	}
}

// EachUnder is what makes an incremental update cost the directory that
// changed rather than the corpus.
func TestEachUnderVisitsOneSubtreeOnly(t *testing.T) {
	corpus := []Entry{
		{Share: 1, Path: "a/one.txt"},
		{Share: 1, Path: "a/two.txt"},
		{Share: 1, Path: "a/deep/three.txt"},
		{Share: 1, Path: "b/four.txt"},
		{Share: 2, Path: "a/five.txt"},
	}
	TreeOrder(corpus)
	seg := mustWriteBase(t, corpus, 2, 0.6)

	var got []string
	if err := seg.EachUnder(1, "a", func(e Entry) error {
		got = append(got, e.Path)
		return nil
	}); err != nil {
		t.Fatalf("EachUnder: %v", err)
	}
	slices.Sort(got)
	want := []string{"a/deep/three.txt", "a/one.txt", "a/two.txt"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The share root, named by the empty string, holds everything in that share
// and nothing from another.
func TestEachUnderAtTheShareRoot(t *testing.T) {
	corpus := []Entry{
		{Share: 1, Path: "a.txt"},
		{Share: 1, Path: "d/b.txt"},
		{Share: 2, Path: "c.txt"},
	}
	TreeOrder(corpus)
	seg := mustWriteBase(t, corpus, 2, 0.6)

	var got []string
	if err := seg.EachUnder(1, "", func(e Entry) error {
		got = append(got, e.Path)
		return nil
	}); err != nil {
		t.Fatalf("EachUnder: %v", err)
	}
	slices.Sort(got)
	if want := []string{"a.txt", "d/b.txt"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Tree order maps the separator below every other byte, so a directory's whole
// subtree is one contiguous run. Byte order gets this wrong: '.' is 0x2E and
// '/' is 0x2F, so "a.txt" would land between "a" and "a/b".
func TestTreeCompareKeepsSiblingsContiguous(t *testing.T) {
	if TreeCompare("a", "a/b") >= 0 {
		t.Error("a should sort before a/b")
	}
	if TreeCompare("a/b", "a.txt") >= 0 {
		t.Error("a/b should sort before a.txt, which plain byte order gets wrong")
	}
	if TreeCompare("a", "a") != 0 {
		t.Error("equal paths should compare equal")
	}
	if TreeCompare("b", "a") <= 0 {
		t.Error("b should sort after a")
	}
	// The 0xff byte is mapped to itself so the key function stays injective.
	if TreeCompare("\xff", "\xfe") <= 0 {
		t.Error("0xff should sort after 0xfe")
	}
}

func TestTreeOrderSortsByShareThenPath(t *testing.T) {
	in := []Entry{
		{Share: 2, Path: "a"},
		{Share: 1, Path: "b"},
		{Share: 1, Path: "a"},
	}
	TreeOrder(in)
	want := []Entry{{Share: 1, Path: "a"}, {Share: 1, Path: "b"}, {Share: 2, Path: "a"}}
	if !slices.Equal(in, want) {
		t.Errorf("got %v, want %v", in, want)
	}
}

func TestWriteBaseOnAnEmptyCorpus(t *testing.T) {
	seg := mustWriteBase(t, nil, 32, 0.6)
	if seg.BlockCount != 0 || seg.EntryCount != 0 {
		t.Errorf("an empty corpus produced %d blocks and %d entries", seg.BlockCount, seg.EntryCount)
	}
	if err := seg.EachUnder(1, "", func(Entry) error {
		t.Error("an empty segment yielded an entry")
		return nil
	}); err != nil {
		t.Errorf("EachUnder on an empty segment: %v", err)
	}
}

func FuzzOpenBaseNeverPanics(f *testing.F) {
	buf, err := WriteBase(entries(64), 32, 0.6)
	if err != nil {
		f.Fatalf("WriteBase: %v", err)
	}
	f.Add(buf)
	f.Add(make([]byte, HeaderLen))
	f.Fuzz(func(t *testing.T, in []byte) {
		seg, err := OpenBase(in)
		if err != nil {
			return
		}
		// A segment that opened must survive being read: either it yields
		// entries or it refuses, and neither may panic.
		if err := seg.EachEntry(func(Entry) error { return nil }); err != nil &&
			!errors.Is(err, ErrCorrupt) {
			t.Errorf("EachEntry returned a non-corrupt error: %v", err)
		}
		if err := seg.EachUnder(1, "a", func(Entry) error { return nil }); err != nil &&
			!errors.Is(err, ErrCorrupt) {
			t.Errorf("EachUnder returned a non-corrupt error: %v", err)
		}
		seg.Lookup(search.TrigramOf('a', 'b', 'c'))
	})
}

func FuzzDecodeBlockNeverPanics(f *testing.F) {
	f.Add(EncodeBlock([]Entry{{Share: 1, Path: "a.txt"}}))
	f.Add([]byte{0xff})
	f.Fuzz(func(t *testing.T, in []byte) {
		if _, err := DecodeBlock(in); err != nil && !errors.Is(err, ErrCorrupt) {
			t.Errorf("DecodeBlock returned a non-corrupt error: %v", err)
		}
	})
}
