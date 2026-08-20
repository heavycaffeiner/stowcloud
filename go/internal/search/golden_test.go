package search

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The golden fixtures are what turn "does the Go search work" from a judgement
// into a diff. They are produced by the Rust implementation, whose on-disk
// format is not changing, so a disagreement here is a bug in this package
// rather than a difference of opinion.
//
// Every reader is deliberately strict: a fixture line it cannot parse fails
// the test rather than being skipped, because a silently skipped fixture is a
// check nobody is running.

func goldenPath(name string) string {
	return filepath.Join("..", "..", "testdata", "golden", "search", name)
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(goldenPath(name))
	if err != nil {
		t.Fatalf("reading the fixture %s: %v", name, err)
	}
	return b
}

// tsvRows returns the non-comment rows of a TSV fixture, split on tabs.
//
// A blank field is meaningful (the empty input case), so the split keeps
// empties and only the leading "# " comment lines are dropped.
func tsvRows(t *testing.T, name string) [][]string {
	t.Helper()
	var out [][]string
	sc := bufio.NewScanner(strings.NewReader(string(readGolden(t, name))))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.HasPrefix(line, "# ") {
			continue
		}
		out = append(out, strings.Split(line, "\t"))
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanning %s: %v", name, err)
	}
	if len(out) == 0 {
		t.Fatalf("%s produced no rows, so it is checking nothing", name)
	}
	return out
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("the fixture field %q is not hex: %v", s, err)
	}
	return b
}

// varint.bin is self-describing so a failure names the value that broke rather
// than an offset. All integers are little-endian.
//
//	"SCVI"
//	u32  scalar case count
//	  per case: u64 value, u8 encoded length, that many bytes
//	u32  ascending-list case count
//	  per case: u32 id count, that many u32 ids, u32 encoded length, those bytes
func TestVarintAgainstTheGoldenFixture(t *testing.T) {
	buf := readGolden(t, "varint.bin")
	r := &leReader{b: buf, t: t}

	if magic := string(r.take(4)); magic != "SCVI" {
		t.Fatalf("varint.bin starts with %q, want SCVI", magic)
	}

	scalars := r.u32()
	if scalars == 0 {
		t.Fatal("the fixture holds no scalar cases")
	}
	for i := uint32(0); i < scalars; i++ {
		value := r.u64()
		encLen := int(r.u8())
		want := r.take(encLen)

		got := PutVarint(nil, value)
		if string(got) != string(want) {
			t.Fatalf("PutVarint(%d) = %x, want %x", value, got, want)
		}
		if n := VarintLen(value); n != encLen {
			t.Fatalf("VarintLen(%d) = %d, want %d", value, n, encLen)
		}
		back, pos, err := Varint(want, 0)
		if err != nil {
			t.Fatalf("Varint(%x): %v", want, err)
		}
		if back != value {
			t.Fatalf("Varint(%x) = %d, want %d", want, back, value)
		}
		if pos != len(want) {
			t.Fatalf("Varint(%x) consumed %d bytes of %d", want, pos, len(want))
		}
	}

	lists := r.u32()
	for i := uint32(0); i < lists; i++ {
		n := r.u32()
		ids := make([]uint32, n)
		for j := range ids {
			ids[j] = r.u32()
		}
		encLen := int(r.u32())
		want := r.take(encLen)

		got := PutAscending(nil, ids)
		if string(got) != string(want) {
			t.Fatalf("PutAscending(%v) = %x, want %x", ids, got, want)
		}
		back, err := Ascending(want)
		if err != nil {
			t.Fatalf("Ascending(%x): %v", want, err)
		}
		if len(back) != len(ids) {
			t.Fatalf("Ascending(%x) = %v, want %v", want, back, ids)
		}
		for j := range ids {
			if back[j] != ids[j] {
				t.Fatalf("Ascending(%x)[%d] = %d, want %d", want, j, back[j], ids[j])
			}
		}
	}
	r.done()
}

func TestFoldAgainstTheGoldenFixture(t *testing.T) {
	for _, row := range tsvRows(t, "fold.tsv") {
		if len(row) < 2 {
			t.Fatalf("a fold.tsv row has %d fields, want at least 2: %q", len(row), row)
		}
		in, want := unhex(t, row[0]), unhex(t, row[1])
		got := Fold(in)
		if string(got) != string(want) {
			t.Fatalf("Fold(%x) = %x, want %x", in, got, want)
		}
	}
}

func TestTrigramsAgainstTheGoldenFixture(t *testing.T) {
	for _, row := range tsvRows(t, "trigram.tsv") {
		if len(row) < 2 {
			t.Fatalf("a trigram.tsv row has %d fields, want at least 2: %q", len(row), row)
		}
		in := unhex(t, row[0])

		var want []Trigram
		if row[1] != "" {
			for _, field := range strings.Split(row[1], ",") {
				b := unhex(t, field)
				if len(b) != 3 {
					t.Fatalf("a trigram field %q is %d bytes, want 3", field, len(b))
				}
				want = append(want, Trigram{b[0], b[1], b[2]})
			}
		}

		got := DistinctTrigrams(in)
		if len(got) != len(want) {
			t.Fatalf("DistinctTrigrams(%x) = %v, want %v", in, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("DistinctTrigrams(%x)[%d] = %x, want %x", in, i, got[i], want[i])
			}
		}
	}
}

// The estimate is compared as a bit pattern rather than a rounded decimal. The
// register update and the bias correction are both floating point, and a
// fixture that rounded could not tell a correct implementation from one that
// is wrong in the last place.
func TestHLLAgainstTheGoldenFixture(t *testing.T) {
	for _, row := range tsvRows(t, "hll.tsv") {
		if len(row) < 4 {
			t.Fatalf("an hll.tsv row has %d fields, want 4: %q", len(row), row)
		}
		kind, arg, result := row[0], row[1], row[3]

		if kind == "hash" {
			want, err := strconv.ParseUint(result, 16, 64)
			if err != nil {
				t.Fatalf("the hash result %q is not 16 hex digits: %v", result, err)
			}
			in := unhex(t, arg)
			if got := Hash64(in); got != want {
				t.Fatalf("Hash64(%x) = %016x, want %016x", in, got, want)
			}
			continue
		}

		n, err := strconv.Atoi(arg)
		if err != nil {
			t.Fatalf("the %s argument %q is not a count: %v", kind, arg, err)
		}
		p := HLLDefaultPrecision
		if row[2] != "-" && row[2] != "" {
			pp, perr := strconv.Atoi(row[2])
			if perr != nil {
				t.Fatalf("the precision %q is not a number: %v", row[2], perr)
			}
			p = pp
		}
		bitsWant, err := strconv.ParseUint(result, 16, 64)
		if err != nil {
			t.Fatalf("the estimate %q is not a bit pattern: %v", result, err)
		}

		h := NewHLL(uint8(p))
		switch kind {
		case "item":
			for i := 0; i < n; i++ {
				h.Add([]byte(fmt.Sprintf("item-%d", i)))
			}
		case "trigram":
			for i := 0; i < n; i++ {
				s := fmt.Sprintf("photos/2026/trip%d/IMG_%05d.jpg", i%977, i)
				Trigrams([]byte(s), func(tg Trigram) { h.Add(tg[:]) })
			}
		case "dup":
			for i := 0; i < n; i++ {
				h.Add([]byte("the same trigram source over and over"))
			}
		default:
			t.Fatalf("an hll.tsv row has an unknown kind %q", kind)
		}

		got := math.Float64bits(h.Estimate())
		if got != bitsWant {
			t.Fatalf("%s(%d): estimate bits %016x (%v), want %016x (%v)",
				kind, n, got, h.Estimate(), bitsWant, math.Float64frombits(bitsWant))
		}
	}
}

// leReader reads the little-endian fixture format, failing the test rather
// than returning an error: a fixture that does not parse is not a case to
// handle, it is a broken check.
type leReader struct {
	b []byte
	i int
	t *testing.T
}

func (r *leReader) take(n int) []byte {
	r.t.Helper()
	if r.i+n > len(r.b) {
		r.t.Fatalf("the fixture ended after %d bytes, wanting %d more", r.i, n)
	}
	out := r.b[r.i : r.i+n]
	r.i += n
	return out
}

func (r *leReader) u8() uint8   { return r.take(1)[0] }
func (r *leReader) u32() uint32 { return binary.LittleEndian.Uint32(r.take(4)) }
func (r *leReader) u64() uint64 { return binary.LittleEndian.Uint64(r.take(8)) }

// done fails if anything is left, which catches a reader that stopped early
// and a fixture that grew a field this test does not know about.
func (r *leReader) done() {
	r.t.Helper()
	if r.i != len(r.b) {
		r.t.Fatalf("%d bytes of the fixture were never read", len(r.b)-r.i)
	}
}
