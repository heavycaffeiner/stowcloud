package index

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

// A segment is a file on disk. A bad disk, a truncated write or a hostile one
// can have rewritten it, so every reader here is fed input the writer never
// produced. The index is a cache: a corrupt segment must be refused, never
// turned into wrong hits and never a panic.

func TestABlockPayloadRoundTrips(t *testing.T) {
	want := []Entry{
		{Share: 1, Path: "a/b.txt"},
		{Share: 7, Path: "\uc5ec\ub984\ud734\uac00\uc0ac\uc9c4.jpg"},
		{Share: 0, Path: ""},
	}
	got, err := DecodeBlock(EncodeBlock(want))
	if err != nil {
		t.Fatalf("DecodeBlock: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestATruncatedBlockIsRefused(t *testing.T) {
	full := EncodeBlock([]Entry{{Share: 1, Path: "abcdefgh"}})
	for cut := 1; cut < len(full); cut++ {
		if _, err := DecodeBlock(full[:cut]); err == nil {
			t.Fatalf("a block cut to %d of %d bytes was accepted", cut, len(full))
		}
	}
}

// A name length larger than what is left is a corrupt block, not a long name.
func TestANameLengthPastThePayloadIsRefused(t *testing.T) {
	var buf []byte
	buf = appendVarint(buf, 1)    // share
	buf = appendVarint(buf, 9999) // a length nothing backs
	buf = append(buf, "short"...)
	if _, err := DecodeBlock(buf); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

func TestASegmentShorterThanItsHeaderIsRefused(t *testing.T) {
	for _, n := range []int{0, 1, HeaderLen - 1} {
		if _, err := OpenBase(make([]byte, n)); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("a %d-byte segment returned %v, want ErrCorrupt", n, err)
		}
	}
}

func TestBadMagicAndVersionAreRefused(t *testing.T) {
	good := buildSmall(t)

	bad := bytes.Clone(good)
	bad[0] = 'X'
	if _, err := OpenBase(bad); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("bad magic returned %v, want ErrCorrupt", err)
	}

	bad = bytes.Clone(good)
	bad[4] = 99 // a version this build does not know
	if _, err := OpenBase(bad); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("an unknown version returned %v, want ErrCorrupt", err)
	}
}

// A header whose sections run past the file is refused at open, before
// anything is read through one of those offsets.
func TestSectionOffsetsPastTheFileAreRefused(t *testing.T) {
	for _, at := range []int{40, 56, 72, 88} {
		bad := bytes.Clone(buildSmall(t))
		for i := 0; i < 8; i++ {
			bad[at+i] = 0xff
		}
		if _, err := OpenBase(bad); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("a section offset at %d returned %v, want ErrCorrupt", at, err)
		}
	}
}

func TestAZeroBlockSizeIsRefused(t *testing.T) {
	bad := bytes.Clone(buildSmall(t))
	for i := 8; i < 12; i++ {
		bad[i] = 0
	}
	if _, err := OpenBase(bad); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a zero block size returned %v, want ErrCorrupt", err)
	}
}

func TestABlockIDPastTheCountIsRefused(t *testing.T) {
	s, err := OpenBase(buildSmall(t))
	if err != nil {
		t.Fatalf("OpenBase: %v", err)
	}
	if _, err := s.Block(s.BlockCount); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("block %d returned %v, want ErrCorrupt", s.BlockCount, err)
	}
}

// The recorded uncompressed length is a check, not just a hint: a payload that
// decompresses to a different size is not the block that was written.
func TestALengthMismatchIsRefused(t *testing.T) {
	comp, err := Compress([]byte("hello"))
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if _, err := DecompressHint(comp, 999); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if _, err := DecompressHint(comp, MaxDecompressed+1); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a declared size past the ceiling returned %v, want ErrCorrupt", err)
	}
}

func TestGarbageDoesNotDecompress(t *testing.T) {
	if _, err := Decompress([]byte("not a zstd frame at all")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
}

// Tree order is what makes block compression pay. A byte comparison puts
// "a.txt" between "a" and "a/b", scattering siblings.
func TestTreeOrderKeepsSiblingsTogether(t *testing.T) {
	if TreeCompare("a/b", "a.txt") >= 0 {
		t.Fatal("a.txt sorts before a/b, so siblings are scattered")
	}
	if TreeCompare("a", "a/b") >= 0 {
		t.Fatal("a directory does not sort before its children")
	}
	if TreeCompare("abc", "abc") != 0 {
		t.Fatal("equal paths do not compare equal")
	}

	entries := []Entry{
		{Share: 1, Path: "a.txt"},
		{Share: 1, Path: "a/c.txt"},
		{Share: 1, Path: "a/b.txt"},
		{Share: 0, Path: "z.txt"},
	}
	TreeOrder(entries)
	want := []string{"z.txt", "a/b.txt", "a/c.txt", "a.txt"}
	for i, w := range want {
		if entries[i].Path != w {
			t.Fatalf("entry %d is %q, want %q (order: %v)", i, entries[i].Path, w, entries)
		}
	}
}

// The compression win comes from prefix sharing between adjacent names, which
// only exists in tree order. This is the measurement behind that claim.
func TestTreeOrderCompressesBetterThanScattered(t *testing.T) {
	var ordered, scattered []Entry
	for i := 0; i < 64; i++ {
		ordered = append(ordered, Entry{Share: 1, Path: fmt.Sprintf("photos/2026/summer/IMG_%04d.jpg", i)})
		h := uint64(i) * 0x9e3779b97f4a7c15
		scattered = append(scattered, Entry{Share: 1, Path: fmt.Sprintf("%016x/%016x.dat", h, h>>21)})
	}
	ratio := func(e []Entry) float64 {
		raw := EncodeBlock(e)
		comp, err := Compress(raw)
		if err != nil {
			t.Fatalf("Compress: %v", err)
		}
		return float64(len(comp)) / float64(len(raw))
	}
	a, b := ratio(ordered), ratio(scattered)
	if a >= b {
		t.Fatalf("tree-ordered compresses to %.3f, scattered to %.3f: the ordering buys nothing", a, b)
	}
}

// Pruning refuses to run on a corpus too small for a document frequency to
// mean anything.
func TestPruningDoesNotRunBelowTheBlockFloor(t *testing.T) {
	var entries []Entry
	for i := 0; i < 8; i++ {
		entries = append(entries, Entry{Share: 1, Path: fmt.Sprintf("data/file%02d.txt", i)})
	}
	TreeOrder(entries)
	buf, err := WriteBase(entries, 1, 0.6)
	if err != nil {
		t.Fatalf("WriteBase: %v", err)
	}
	s, err := OpenBase(buf)
	if err != nil {
		t.Fatalf("OpenBase: %v", err)
	}
	if s.BlockCount >= MinBlocksForPrune {
		t.Fatalf("the fixture produced %d blocks, too many for this test", s.BlockCount)
	}
	if s.PrunedCount != 0 {
		t.Fatalf("%d trigrams were pruned below the block floor", s.PrunedCount)
	}
}

func TestAnEmptyCorpusProducesAReadableSegment(t *testing.T) {
	buf, err := WriteBase(nil, 32, 0.6)
	if err != nil {
		t.Fatalf("WriteBase: %v", err)
	}
	s, err := OpenBase(buf)
	if err != nil {
		t.Fatalf("OpenBase: %v", err)
	}
	if s.EntryCount != 0 || s.BlockCount != 0 {
		t.Fatalf("an empty corpus produced %+v", s)
	}
	if kind, _ := s.Lookup([3]byte{'a', 'b', 'c'}); kind != LookupMissing {
		t.Fatalf("a lookup in an empty segment returned %v", kind)
	}
}

// The whole reader, over bytes nothing wrote. Nothing may panic, and anything
// that opens must survive being read through.
func FuzzOpenBase(f *testing.F) {
	f.Add(buildSmallFor(f))
	f.Add(make([]byte, HeaderLen))
	f.Add([]byte("SCNB"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, buf []byte) {
		s, err := OpenBase(buf)
		if err != nil {
			return
		}
		// A segment that opened must be readable without panicking. An error is
		// fine: it is the refusal working.
		s.Lookup([3]byte{'a', 'b', 'c'})
		if ids, perr := s.Postings([3]byte{'a', 'b', 'c'}); perr == nil {
			for i := 1; i < len(ids); i++ {
				if ids[i] < ids[i-1] {
					t.Fatalf("a posting list came back descending: %v", ids)
				}
			}
		}
		for id := uint32(0); id < s.BlockCount && id < 64; id++ {
			if entries, berr := s.Block(id); berr == nil {
				for _, e := range entries {
					if len(e.Path) > MaxDecompressed {
						t.Fatalf("block %d produced a name of %d bytes", id, len(e.Path))
					}
				}
			}
		}
	})
}

func FuzzDecodeBlock(f *testing.F) {
	f.Add(EncodeBlock([]Entry{{Share: 1, Path: "a.txt"}}))
	f.Add([]byte{0x01})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, payload []byte) {
		entries, err := DecodeBlock(payload)
		if err != nil {
			return
		}
		// What decoded must re-encode to exactly what was read, which is what
		// stops a decoder inventing a name out of a partial record.
		if got := EncodeBlock(entries); !bytes.Equal(got, payload) {
			t.Fatalf("DecodeBlock(%x) re-encodes to %x", payload, got)
		}
	})
}

func buildSmall(t *testing.T) []byte {
	t.Helper()
	var entries []Entry
	for i := 0; i < 40; i++ {
		entries = append(entries, Entry{Share: 1, Path: fmt.Sprintf("data/dir%d/file%02d.txt", i%4, i)})
	}
	TreeOrder(entries)
	buf, err := WriteBase(entries, 4, 0.6)
	if err != nil {
		t.Fatalf("WriteBase: %v", err)
	}
	return buf
}

func buildSmallFor(f *testing.F) []byte {
	f.Helper()
	var entries []Entry
	for i := 0; i < 40; i++ {
		entries = append(entries, Entry{Share: 1, Path: fmt.Sprintf("data/dir%d/file%02d.txt", i%4, i)})
	}
	TreeOrder(entries)
	buf, err := WriteBase(entries, 4, 0.6)
	if err != nil {
		f.Fatalf("WriteBase: %v", err)
	}
	return buf
}

func appendVarint(out []byte, v uint64) []byte {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}
