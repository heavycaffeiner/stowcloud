package search

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

// The fixtures prove agreement with the recorded format. These prove the
// properties a fixture cannot: what happens on input the generator never
// produced, and what the arithmetic is supposed to mean.

func TestVarintRoundTripsEveryWidthBoundary(t *testing.T) {
	for _, v := range []uint64{
		0, 1, 127, 128, 129, 16383, 16384,
		1<<21 - 1, 1 << 21, 1<<28 - 1, 1 << 28,
		math.MaxUint32, math.MaxUint64,
	} {
		buf := PutVarint(nil, v)
		if len(buf) != VarintLen(v) {
			t.Fatalf("PutVarint(%d) is %d bytes, VarintLen says %d", v, len(buf), VarintLen(v))
		}
		got, pos, err := Varint(buf, 0)
		if err != nil {
			t.Fatalf("Varint(%x): %v", buf, err)
		}
		if got != v || pos != len(buf) {
			t.Fatalf("Varint(%x) = %d after %d bytes, want %d after %d", buf, got, pos, v, len(buf))
		}
	}
}

// A truncated varint is refused rather than read as the value it would have
// been. A decoder that guessed would turn a corrupt segment into wrong hits.
func TestATruncatedVarintIsRefused(t *testing.T) {
	buf := PutVarint(nil, 300)
	if _, _, err := Varint(buf[:len(buf)-1], 0); !errors.Is(err, ErrVarint) {
		t.Fatalf("a truncated varint returned %v, want ErrVarint", err)
	}
	if _, _, err := Varint(nil, 0); !errors.Is(err, ErrVarint) {
		t.Fatalf("an empty buffer returned %v, want ErrVarint", err)
	}
}

// An overlong encoding is a second spelling of a value. Accepting it would
// mean the same number has two representations and the format stops being a
// bijection.
func TestAnOverlongVarintIsRefused(t *testing.T) {
	// Ten continuation bytes shifts past 64 bits.
	overlong := []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x00}
	if _, _, err := Varint(overlong, 0); !errors.Is(err, ErrVarint) {
		t.Fatalf("an overlong varint returned %v, want ErrVarint", err)
	}
}

func TestAscendingListsRoundTrip(t *testing.T) {
	ids := []uint32{0, 1, 5, 400, 401, 100_000, math.MaxUint32}
	buf := PutAscending(nil, ids)
	got, err := Ascending(buf)
	if err != nil {
		t.Fatalf("Ascending: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("got %v, want %v", got, ids)
	}
	for i := range ids {
		if got[i] != ids[i] {
			t.Fatalf("got[%d] = %d, want %d", i, got[i], ids[i])
		}
	}
	if len(PutAscending(nil, nil)) != 0 {
		t.Fatal("an empty list encoded to something")
	}
}

// A delta that carries the running total past a block id is a corrupt list,
// not a wider one.
func TestAnAscendingListPastTheBlockIDRangeIsRefused(t *testing.T) {
	buf := PutVarint(nil, math.MaxUint32)
	buf = PutVarint(buf, 2)
	if _, err := Ascending(buf); !errors.Is(err, ErrVarint) {
		t.Fatalf("a list running past uint32 returned %v, want ErrVarint", err)
	}
}

// Folding: a name is a byte string, so one that is not UTF-8 is still foldable
// and still findable.
func TestFoldKeepsInvalidUTF8Findable(t *testing.T) {
	raw := []byte("ABC\xff\xfe.bin")
	got := Fold(raw)
	if string(got) != "abc\xff\xfe.bin" {
		t.Fatalf("Fold(%x) = %x", raw, got)
	}
	if !Contains(got, []byte{0xff, 0xfe}) {
		t.Fatal("the invalid bytes did not survive folding, so the name is unfindable")
	}
}

// NFC and NFD spellings of the same name must fold together, or a file another
// program wrote is found by only one of its spellings.
func TestNFCAndNFDFoldTogether(t *testing.T) {
	nfc := "caf\u00e9.txt"
	nfd := "cafe\u0301.txt"
	if string(FoldString(nfc)) != string(FoldString(nfd)) {
		t.Fatalf("%q folds to %x but %q folds to %x",
			nfc, FoldString(nfc), nfd, FoldString(nfd))
	}
}

// Hangul has no case, so folding must not perturb the bytes and a syllable
// query must still be a substring of the folded name.
func TestFoldLeavesHangulIntact(t *testing.T) {
	folded := FoldString("\uc5ec\ub984\ud734\uac00.jpg")
	for _, q := range []string{"\ud734\uac00", "\uc5ec\ub984"} {
		if !Contains(folded, []byte(q)) {
			t.Fatalf("the query %q is not in the folded name %x", q, folded)
		}
	}
}

func TestIsFoldedASCIIAgreesWithFold(t *testing.T) {
	for _, s := range []string{"", "a", "abc.txt", "img_0001.jpg"} {
		if !IsFoldedASCII([]byte(s)) {
			t.Errorf("IsFoldedASCII(%q) = false", s)
		}
	}
	for _, s := range []string{"A", "IMG.JPG", "caf\u00e9"} {
		if IsFoldedASCII([]byte(s)) {
			t.Errorf("IsFoldedASCII(%q) = true", s)
		}
	}
}

func TestContainsASCIIFoldIsCaseInsensitive(t *testing.T) {
	if !ContainsASCIIFold([]byte("Vacation_Photo.JPG"), []byte("photo")) {
		t.Fatal("a case-insensitive match was missed")
	}
	if ContainsASCIIFold([]byte("Vacation.JPG"), []byte("photo")) {
		t.Fatal("a non-match was reported")
	}
	if !ContainsASCIIFold([]byte("abc"), nil) {
		t.Fatal("an empty needle should match everything")
	}
}

// A Hangul syllable is exactly three bytes, which is the property that makes
// a byte-trigram index work for CJK at all.
func TestAHangulSyllableIsExactlyOneTrigram(t *testing.T) {
	one := []byte("\ud734")
	if len(one) != 3 {
		t.Fatalf("the syllable is %d bytes, want 3", len(one))
	}
	if got := DistinctTrigrams(one); len(got) != 1 {
		t.Fatalf("one syllable produced %d trigrams, want 1", len(got))
	}
	// Two syllables are six bytes, so four overlapping windows.
	if got := DistinctTrigrams([]byte("\ud734\uac00")); len(got) != 4 {
		t.Fatalf("two syllables produced %d trigrams, want 4", len(got))
	}
}

func TestAShortNameHasNoTrigrams(t *testing.T) {
	for _, s := range []string{"", "a", "ab"} {
		if got := DistinctTrigrams([]byte(s)); len(got) != 0 {
			t.Fatalf("DistinctTrigrams(%q) = %v, want none", s, got)
		}
	}
	if TrigramOccurrences(2) != 0 || TrigramOccurrences(3) != 1 || TrigramOccurrences(10) != 8 {
		t.Fatal("TrigramOccurrences disagrees with the window count")
	}
}

// Every trigram of a query must be a trigram of a name that contains it, or
// the index would prune away a document the walk would have found.
func TestAQueryTrigramSetIsASubsetOfTheNames(t *testing.T) {
	name := DistinctTrigrams([]byte("\uc5ec\ub984\ud734\uac00\uc0ac\uc9c4.jpg"))
	inName := map[Trigram]bool{}
	for _, tg := range name {
		inName[tg] = true
	}
	for _, q := range []string{"\ud734\uac00", "\uc5ec\ub984", "\uc0ac\uc9c4"} {
		for _, tg := range DistinctTrigrams([]byte(q)) {
			if !inName[tg] {
				t.Fatalf("the query %q has a trigram %x the name does not", q, tg)
			}
		}
	}
}

// The sketch is what separates a CJK corpus from a Latin one, so being wrong
// here sends the estimator to the wrong tier.
func TestHLLIsNearExactForSmallCardinalities(t *testing.T) {
	for _, n := range []int{0, 1, 10, 100, 1000} {
		h := NewHLL(HLLDefaultPrecision)
		for i := 0; i < n; i++ {
			h.Add([]byte(fmt.Sprintf("item-%d", i)))
		}
		est := h.Estimate()
		want := float64(n)
		if want == 0 {
			if est != 0 {
				t.Fatalf("an empty sketch estimated %v, want 0", est)
			}
			continue
		}
		if err := math.Abs(est-want) / want; err > 0.05 {
			t.Fatalf("n=%d estimated %.0f, off by %.1f%%", n, est, err*100)
		}
	}
}

func TestHLLDuplicatesDoNotInflate(t *testing.T) {
	h := NewHLL(HLLDefaultPrecision)
	for i := 0; i < 10_000; i++ {
		h.Add([]byte("the same trigram source over and over"))
	}
	if got := h.EstimateUint(); got != 1 {
		t.Fatalf("ten thousand copies estimated %d distinct, want 1", got)
	}
}

func TestHLLMergeIsAUnion(t *testing.T) {
	a, b, both := NewHLL(HLLDefaultPrecision), NewHLL(HLLDefaultPrecision), NewHLL(HLLDefaultPrecision)
	for i := 0; i < 5000; i++ {
		a.Add([]byte(fmt.Sprintf("a%d", i)))
		both.Add([]byte(fmt.Sprintf("a%d", i)))
		b.Add([]byte(fmt.Sprintf("b%d", i)))
		both.Add([]byte(fmt.Sprintf("b%d", i)))
	}
	if err := a.Merge(b); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if err := math.Abs(a.Estimate()-both.Estimate()) / both.Estimate(); err > 0.001 {
		t.Fatalf("the merged sketch is %v, the union is %v", a.Estimate(), both.Estimate())
	}
}

func TestMergingDifferentPrecisionsIsRefused(t *testing.T) {
	if err := NewHLL(14).Merge(NewHLL(12)); !errors.Is(err, ErrHLLPrecision) {
		t.Fatalf("err = %v, want ErrHLLPrecision", err)
	}
}

func TestTheSketchIsSixteenKilobytes(t *testing.T) {
	if got := NewHLL(HLLDefaultPrecision).MemoryBytes(); got != 16384 {
		t.Fatalf("the sketch is %d bytes, want 16384", got)
	}
}

// Precision is clamped rather than refused: it is an internal knob, never a
// value from a request.
func TestHLLPrecisionIsClamped(t *testing.T) {
	if got := NewHLL(1).Precision(); got != hllMinPrecision {
		t.Fatalf("precision 1 became %d, want %d", got, hllMinPrecision)
	}
	if got := NewHLL(99).Precision(); got != hllMaxPrecision {
		t.Fatalf("precision 99 became %d, want %d", got, hllMaxPrecision)
	}
}

// Ranking: the order the weights are supposed to produce.
func TestExactBeatsPrefixBeatsSubstring(t *testing.T) {
	score := func(name, needle string) float32 {
		return Score(RankInput{NameFolded: []byte(name), Needle: []byte(needle)})
	}
	exact := score("report", "report")
	prefix := score("report_final", "report")
	sub := score("my_report_final", "report")

	// An exact match is also a prefix match, so the two weights add.
	if exact != 5.0 {
		t.Fatalf("an exact match scored %v, want 5", exact)
	}
	if prefix != 2.0 {
		t.Fatalf("a prefix match scored %v, want 2", prefix)
	}
	if sub != 0.0 {
		t.Fatalf("a substring match scored %v, want 0", sub)
	}
	if !(exact > prefix && prefix > sub) {
		t.Fatalf("the ordering is wrong: %v %v %v", exact, prefix, sub)
	}
}

func TestAHiddenNameIsPenalised(t *testing.T) {
	got := Score(RankInput{NameFolded: []byte(".report"), Needle: []byte("report")})
	if got != -1.0 {
		t.Fatalf("a hidden exact-substring match scored %v, want -1", got)
	}
}

func TestRecencyDecaysOverTheWindow(t *testing.T) {
	now := int64(RecencyWindow) * 4
	at := func(mtime int64) float32 {
		m := mtime
		return Score(RankInput{NameFolded: []byte("a"), NowNs: now, MTimeNs: &m})
	}
	fresh := at(now)
	half := at(now - int64(RecencyWindow)/2)
	old := at(now - int64(RecencyWindow)*2)

	if math.Abs(float64(fresh)-0.5) > 1e-5 {
		t.Fatalf("a fresh file scored %v, want 0.5", fresh)
	}
	if math.Abs(float64(half)-0.25) > 1e-3 {
		t.Fatalf("a half-old file scored %v, want 0.25", half)
	}
	if old != 0 {
		t.Fatalf("a file past the window scored %v, want 0", old)
	}
}

// No stat means no recency term, which is the honest answer rather than a
// guess. It is also what keeps a name-only query from paying for metadata.
func TestNoStatMeansNoRecencyTerm(t *testing.T) {
	got := Score(RankInput{NameFolded: []byte("a"), NowNs: int64(time.Hour)})
	if got != 0 {
		t.Fatalf("a candidate with no mtime scored %v, want 0", got)
	}
}

func TestScopeIsComponentAware(t *testing.T) {
	for _, tc := range []struct {
		path, scope string
		want        bool
	}{
		{"photos/a.jpg", "photos", true},
		{"photos", "photos", true},
		{"photos/", "photos", true},
		{"photography/a.jpg", "photos", false},
		{"anything", "", true},
		{"photos/a.jpg", "photos/", true},
	} {
		if got := InScope(tc.path, tc.scope); got != tc.want {
			t.Errorf("InScope(%q, %q) = %v, want %v", tc.path, tc.scope, got, tc.want)
		}
	}
}

func TestBM25IsClampedAndZeroOnTheWalk(t *testing.T) {
	base := RankInput{NameFolded: []byte("a")}
	if got := Score(base); got != 0 {
		t.Fatalf("a walk candidate scored %v, want 0", got)
	}
	base.BM25 = 1
	if got := Score(base); got != 1 {
		t.Fatalf("bm25 of 1 scored %v, want 1", got)
	}
	// Out-of-range values are clamped rather than trusted: the caller computes
	// this and a bug there must not outrank an exact match.
	base.BM25 = 99
	if got := Score(base); got != 1 {
		t.Fatalf("bm25 of 99 scored %v, want it clamped to 1", got)
	}
	base.BM25 = -5
	if got := Score(base); got != 0 {
		t.Fatalf("a negative bm25 scored %v, want it clamped to 0", got)
	}
}

// The codec reads bytes off disk that a corrupt or hostile segment could have
// written, so it is fuzzed. What is asserted is that it never panics and never
// returns a list that violates the format's own invariant.
func FuzzVarint(f *testing.F) {
	for _, seed := range [][]byte{
		{}, {0x00}, {0x7f}, {0x80, 0x01}, {0xff, 0xff, 0xff, 0xff, 0x0f},
		{0x80}, {0xff}, {0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x00},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, buf []byte) {
		v, pos, err := Varint(buf, 0)
		if err != nil {
			return
		}
		if pos <= 0 || pos > len(buf) {
			t.Fatalf("Varint(%x) consumed %d of %d bytes", buf, pos, len(buf))
		}
		// Whatever came out has to re-encode to what was consumed, which is
		// what refusing overlong encodings buys.
		if got := PutVarint(nil, v); string(got) != string(buf[:pos]) {
			t.Fatalf("Varint(%x) = %d re-encodes to %x, not the %x it read",
				buf, v, got, buf[:pos])
		}
	})
}

func FuzzAscending(f *testing.F) {
	for _, seed := range [][]byte{{}, {0x00}, {0x01, 0x01}, {0xff, 0xff, 0xff, 0xff, 0x0f}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, buf []byte) {
		ids, err := Ascending(buf)
		if err != nil {
			return
		}
		// The format says the list is strictly ascending. A decoder that
		// produced anything else would feed the intersection a list it cannot
		// merge against.
		for i := 1; i < len(ids); i++ {
			if ids[i] < ids[i-1] {
				t.Fatalf("Ascending(%x) = %v, which descends at %d", buf, ids, i)
			}
		}
		if got := PutAscending(nil, ids); string(got) != string(buf) {
			t.Fatalf("Ascending(%x) re-encodes to %x", buf, got)
		}
	})
}

// Folding runs on names that came off a filesystem, which is untrusted input.
func FuzzFold(f *testing.F) {
	for _, seed := range []string{
		"", "a", "IMG_0001.JPG", "caf\u00e9.txt", "\uc5ec\ub984.jpg",
		"\xff\xfe", "ABC\xff.bin", "\u0130stanbul",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, name []byte) {
		got := Fold(name)

		// Folding is not idempotent, and must not be assumed to be: lowercasing
		// can emit a combining mark that a second NFC pass would reorder, so
		// Fold(Fold(x)) can differ from Fold(x). Every caller folds a raw name
		// exactly once, which is what makes that unreachable rather than a bug.
		// What has to hold is that folding is deterministic.
		if again := Fold(name); string(again) != string(got) {
			t.Fatalf("Fold(%x) returned %x and then %x", name, got, again)
		}

		// An ASCII name can only lose case, never length, which is the fast
		// path the walk depends on.
		if IsFoldedASCII(name) {
			if len(got) != len(name) {
				t.Fatalf("Fold(%x) changed the length of an already-folded name", name)
			}
			if string(got) != string(name) {
				t.Fatalf("Fold(%x) = %x, but it was already folded", name, got)
			}
		}

		// A folded name is never uppercase ASCII, whatever went in.
		for _, c := range got {
			if c >= 'A' && c <= 'Z' {
				t.Fatalf("Fold(%x) = %x, which still carries ASCII case", name, got)
			}
		}
	})
}
