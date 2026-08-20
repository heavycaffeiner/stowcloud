package index

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The overlay, the union and the merge gate.

func newIndex(t *testing.T) *NameIndex {
	t.Helper()
	ix, err := Open(t.TempDir(), DefaultConfig())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ix
}

func query(t *testing.T, ix *NameIndex, needle string) Result {
	t.Helper()
	r, err := ix.Query([]byte(needle), 100)
	if err != nil {
		t.Fatalf("Query(%q): %v", needle, err)
	}
	return r
}

func paths(r Result) []string {
	out := make([]string, 0, len(r.Hits))
	for _, h := range r.Hits {
		out = append(out, h.Path)
	}
	return out
}

// The two fallback reasons are a type on the return, because "the index looked
// and found nothing" and "the index declined to look" are different answers.
func TestAShortQueryIsAFallbackNotAnEmptyResult(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{{Share: 1, Path: "data/report.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	r := query(t, ix, "ab")
	if r.Fallback != FallbackQueryTooShort {
		t.Fatalf("a two-byte query returned %v, want QueryTooShort", r.Fallback)
	}
	if !r.MustFallBack() {
		t.Fatal("the caller was not told to walk")
	}
	if len(r.Hits) != 0 {
		t.Fatalf("a fallback carried %d hits, which the caller would use", len(r.Hits))
	}
}

// A query that found nothing is not a fallback, and the caller must not walk.
func TestAnEmptyResultIsNotAFallback(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{{Share: 1, Path: "data/report.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	r := query(t, ix, "nothingmatchesthis")
	if r.MustFallBack() {
		t.Fatalf("an empty result reported %v, so the caller would walk for nothing", r.Fallback)
	}
	if len(r.Hits) != 0 {
		t.Fatalf("got %v, want nothing", paths(r))
	}
}

func TestAnAppendedNameIsFoundImmediately(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{
		{Share: 1, Path: "data/report-2026.pdf"},
		{Share: 1, Path: "data/photo.jpg"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got := paths(query(t, ix, "report"))
	if len(got) != 1 || got[0] != "data/report-2026.pdf" {
		t.Fatalf("got %v, want the report", got)
	}
}

// A tombstone hides a name without touching the base segment.
func TestATombstoneHidesAName(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{{Share: 1, Path: "data/report.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ix.Tombstone([]Entry{{Share: 1, Path: "data/report.txt"}}); err != nil {
		t.Fatalf("Tombstone: %v", err)
	}
	if got := paths(query(t, ix, "report")); len(got) != 0 {
		t.Fatalf("got %v, want the tombstoned name hidden", got)
	}
}

// A tombstone only hides a write that came before it. Getting this backwards
// makes a delete-then-recreate hide the file forever.
func TestARecreatedNameSurvivesItsOwnTombstone(t *testing.T) {
	ix := newIndex(t)
	e := []Entry{{Share: 1, Path: "data/report.txt"}}
	if err := ix.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ix.Tombstone(e); err != nil {
		t.Fatalf("Tombstone: %v", err)
	}
	if err := ix.Append(e); err != nil {
		t.Fatalf("the recreate: %v", err)
	}
	if got := paths(query(t, ix, "report")); len(got) != 1 {
		t.Fatalf("got %v, want the recreated file back", got)
	}
}

// A tombstone is scoped to its share, or deleting a name in one share would
// hide the same name everywhere.
func TestATombstoneDoesNotCrossShares(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{
		{Share: 1, Path: "data/report.txt"},
		{Share: 2, Path: "data/report.txt"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ix.Tombstone([]Entry{{Share: 1, Path: "data/report.txt"}}); err != nil {
		t.Fatalf("Tombstone: %v", err)
	}
	r := query(t, ix, "report")
	if len(r.Hits) != 1 || r.Hits[0].Share != 2 {
		t.Fatalf("got %+v, want only share 2", r.Hits)
	}
}

// Everything written has to survive a reopen, which is what a restart is.
func TestTheOverlaySurvivesAReopen(t *testing.T) {
	dir := t.TempDir()
	ix, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if aerr := ix.Append([]Entry{
		{Share: 1, Path: "data/keep.txt"},
		{Share: 1, Path: "data/gone.txt"},
	}); aerr != nil {
		t.Fatalf("Append: %v", aerr)
	}
	if terr := ix.Tombstone([]Entry{{Share: 1, Path: "data/gone.txt"}}); terr != nil {
		t.Fatalf("Tombstone: %v", terr)
	}

	reopened, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	if got := paths(query(t, reopened, "keep")); len(got) != 1 {
		t.Fatalf("got %v, want the kept name after a reopen", got)
	}
	if got := paths(query(t, reopened, "gone")); len(got) != 0 {
		t.Fatalf("got %v, want the tombstone to have survived", got)
	}
}

// A torn tail is the expected state after a crash, not corruption. Treating it
// as an error would disable the index every time the machine lost power.
func TestATornTailKeepsTheIntactPrefix(t *testing.T) {
	dir := t.TempDir()
	ix, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if aerr := ix.Append([]Entry{{Share: 1, Path: "data/keep-this-one.txt"}}); aerr != nil {
		t.Fatalf("Append: %v", aerr)
	}
	if aerr := ix.Append([]Entry{{Share: 1, Path: "data/being-written.txt"}}); aerr != nil {
		t.Fatalf("Append: %v", aerr)
	}

	// Cut the tail record in half, which is what a power failure mid-append
	// leaves behind.
	deltaPath := filepath.Join(dir, "delta.000.idx")
	info, err := os.Stat(deltaPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if terr := os.Truncate(deltaPath, info.Size()-6); terr != nil {
		t.Fatalf("truncating: %v", terr)
	}

	reopened, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("a torn tail refused to open: %v", err)
	}
	if got := paths(query(t, reopened, "keep-this-one")); len(got) != 1 {
		t.Fatalf("got %v, want the intact record before the tear", got)
	}

	// And the file is clean and appendable again.
	if aerr := reopened.Append([]Entry{{Share: 1, Path: "data/after-recovery.txt"}}); aerr != nil {
		t.Fatalf("appending after recovery: %v", aerr)
	}
	again, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("reopening after recovery: %v", err)
	}
	if got := paths(query(t, again, "after-recovery")); len(got) != 1 {
		t.Fatalf("got %v, want the record written after the tear", got)
	}
}

// A bit flip in a record body stops the scan there rather than returning a
// name nobody wrote.
func TestABitFlipStopsTheScan(t *testing.T) {
	dir := t.TempDir()
	ix, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if aerr := ix.Append([]Entry{{Share: 1, Path: "data/first.txt"}}); aerr != nil {
		t.Fatalf("Append: %v", aerr)
	}
	if aerr := ix.Append([]Entry{{Share: 1, Path: "data/second.txt"}}); aerr != nil {
		t.Fatalf("Append: %v", aerr)
	}

	deltaPath := filepath.Join(dir, "delta.000.idx")
	raw, err := os.ReadFile(deltaPath)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	raw[len(raw)-1] ^= 0xff
	//nolint:gosec // G703: deltaPath is built from t.TempDir and a fixed name.
	if werr := os.WriteFile(deltaPath, raw, 0o600); werr != nil {
		t.Fatalf("writing: %v", werr)
	}

	reopened, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("a flipped bit refused to open: %v", err)
	}
	if got := paths(query(t, reopened, "first")); len(got) != 1 {
		t.Fatalf("got %v, want the record before the damage", got)
	}
	if got := paths(query(t, reopened, "second")); len(got) != 0 {
		t.Fatalf("got %v, want the damaged record refused", got)
	}
}

// The merge gate is what bounds read cost: the linear delta scan can never
// grow past a fixed fraction of the base.
func TestTheMergeGateFires(t *testing.T) {
	ix := newIndex(t)
	if ix.NeedsMerge() {
		t.Fatal("an empty index wants a merge")
	}
	if err := ix.Append([]Entry{{Share: 1, Path: "data/a.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// With no base, anything at all is past the ratio.
	if !ix.NeedsMerge() {
		t.Fatal("an index with deltas and no base does not want a merge")
	}
}

func TestMergeFoldsTheOverlayIntoTheBase(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{
		{Share: 1, Path: "data/keep.txt"},
		{Share: 1, Path: "data/gone.txt"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ix.Tombstone([]Entry{{Share: 1, Path: "data/gone.txt"}}); err != nil {
		t.Fatalf("Tombstone: %v", err)
	}

	if err := ix.Merge(context.Background(), nil); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	s := ix.Stats()
	if s.DeltaEntries != 0 || s.Tombstones != 0 {
		t.Fatalf("the overlay survived the merge: %+v", s)
	}
	if s.BaseEntries != 1 {
		t.Fatalf("the base holds %d entries, want the one live name", s.BaseEntries)
	}
	if got := paths(query(t, ix, "keep")); len(got) != 1 {
		t.Fatalf("got %v, want the live name after the merge", got)
	}
	if got := paths(query(t, ix, "gone")); len(got) != 0 {
		t.Fatalf("got %v, want the tombstoned name gone for good", got)
	}
}

// A merge that was told to stop must leave every existing segment untouched. A
// refused merge damaging the index would be worse than never merging.
func TestARefusedMergeChangesNothing(t *testing.T) {
	dir := t.TempDir()
	ix, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if aerr := ix.Append([]Entry{{Share: 1, Path: "data/report.txt"}}); aerr != nil {
		t.Fatalf("Append: %v", aerr)
	}

	before := ix.Stats()
	if err := ix.Merge(context.Background(), func() bool { return false }); err != nil {
		t.Fatalf("a refused merge returned an error: %v", err)
	}
	after := ix.Stats()
	if before != after {
		t.Fatalf("a refused merge changed the index: %+v became %+v", before, after)
	}
	if got := paths(query(t, ix, "report")); len(got) != 1 {
		t.Fatalf("got %v, want the name still findable", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "base.idx")); err == nil {
		t.Fatal("a refused merge published a base segment")
	}
}

// The merge survives being cancelled the same way.
func TestACancelledMergeIsRefusedCleanly(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{{Share: 1, Path: "data/report.txt"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := ix.Merge(ctx, nil)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled merge returned %v", err)
	}
	if got := paths(query(t, ix, "report")); len(got) != 1 {
		t.Fatalf("got %v, want the name still findable after a cancelled merge", got)
	}
}

// The overlay wins over the base for the same path, and a name in both is
// reported once.
func TestANameInBothTheBaseAndTheDeltaIsReportedOnce(t *testing.T) {
	ix := newIndex(t)
	e := []Entry{{Share: 1, Path: "data/report.txt"}}
	if err := ix.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := ix.Merge(context.Background(), nil); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if err := ix.Append(e); err != nil {
		t.Fatalf("the second append: %v", err)
	}
	if got := paths(query(t, ix, "report")); len(got) != 1 {
		t.Fatalf("got %v, want one hit for a name in both segments", got)
	}
}

// Results come back best first, and ties break on the path so two runs agree.
func TestHitsAreOrderedByScoreThenPath(t *testing.T) {
	ix := newIndex(t)
	if err := ix.Append([]Entry{
		{Share: 1, Path: "data/zzz/report.txt"},
		{Share: 1, Path: "data/aaa/report.txt"},
		{Share: 1, Path: "data/my_report_final.txt"},
		{Share: 1, Path: "data/report"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	r := query(t, ix, "report")
	if len(r.Hits) < 3 {
		t.Fatalf("got %v, want several hits", paths(r))
	}
	// An exact name match outranks a prefix, which outranks a substring.
	if r.Hits[0].Path != "data/report" {
		t.Fatalf("the exact match is not first: %v", paths(r))
	}
	for i := 1; i < len(r.Hits); i++ {
		if r.Hits[i-1].Score < r.Hits[i].Score {
			t.Fatalf("the hits are not ordered by score: %+v", r.Hits)
		}
		if r.Hits[i-1].Score == r.Hits[i].Score && r.Hits[i-1].Path > r.Hits[i].Path {
			t.Fatalf("a tie did not break on the path: %+v", r.Hits)
		}
	}
}

func TestTheLimitTruncates(t *testing.T) {
	ix := newIndex(t)
	var entries []Entry
	for i := 0; i < 20; i++ {
		entries = append(entries, Entry{Share: 1, Path: fmt.Sprintf("data/report%02d.txt", i)})
	}
	if err := ix.Append(entries); err != nil {
		t.Fatalf("Append: %v", err)
	}
	r, err := ix.Query([]byte("report"), 5)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(r.Hits) != 5 {
		t.Fatalf("got %d hits, want the limit of 5", len(r.Hits))
	}
}

// The golden overlay: the Rust generator appended five names and tombstoned
// three, two of which are in the base and one of which was appended, because
// those are different paths on the read side.
func TestTheGoldenOverlayLoads(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"base.idx", "delta.000.idx", "tomb.idx"} {
		if err := os.WriteFile(filepath.Join(dir, name), readGolden(t, name), 0o600); err != nil {
			t.Fatalf("staging %s: %v", name, err)
		}
	}

	ix, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("opening the golden index: %v", err)
	}
	s := ix.Stats()
	if s.BaseEntries == 0 {
		t.Fatal("the base segment came back empty")
	}
	if s.DeltaEntries == 0 {
		t.Fatal("the delta segment came back empty, so the overlay is unexercised")
	}
	if s.Tombstones == 0 {
		t.Fatal("no tombstones loaded, so the deletion path is unexercised")
	}
}

// query.tsv is the whole read path end to end: the fold, the trigrams, the
// intersection, the block scan, the overlay and the ranking, against what the
// Rust implementation answered for the same corpus.
func TestQueriesAgainstTheGoldenFixture(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"base.idx", "delta.000.idx", "tomb.idx"} {
		if err := os.WriteFile(filepath.Join(dir, name), readGolden(t, name), 0o600); err != nil {
			t.Fatalf("staging %s: %v", name, err)
		}
	}
	ix, err := Open(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("opening the golden index: %v", err)
	}

	sc := bufio.NewScanner(bytes.NewReader(readGolden(t, "query.tsv")))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	checked := 0
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.HasPrefix(line, "# ") || line == "" {
			continue
		}
		row := strings.Split(line, "\t")
		if len(row) < 3 {
			t.Fatalf("a query.tsv row has %d fields: %q", len(row), line)
		}

		needleHex, limitStr, fallback := row[0], row[1], row[2]
		needle, herr := hex.DecodeString(needleHex)
		if herr != nil {
			t.Fatalf("the needle %q is not hex: %v", needleHex, herr)
		}
		limit, lerr := strconv.Atoi(limitStr)
		if lerr != nil {
			t.Fatalf("the limit %q is not a number: %v", limitStr, lerr)
		}

		got, qerr := ix.Query(needle, limit)
		if qerr != nil {
			t.Fatalf("Query(%q): %v", needle, qerr)
		}

		if got.Fallback.String() != fallback {
			t.Fatalf("Query(%q) fell back with %q, the fixture says %q",
				needle, got.Fallback, fallback)
		}
		if fallback != "-" {
			checked++
			continue
		}

		want := row[3:]
		if len(want) == 1 && want[0] == "" {
			want = nil
		}
		if len(got.Hits) != len(want) {
			t.Fatalf("Query(%q) returned %d hits, the fixture has %d\ngot:  %v\nwant: %v",
				needle, len(got.Hits), len(want), paths(got), want)
		}
		for i, field := range want {
			parts := strings.SplitN(field, ":", 3)
			if len(parts) != 3 {
				t.Fatalf("a hit field %q is malformed", field)
			}
			wShare, sherr := strconv.ParseUint(parts[0], 10, 32)
			if sherr != nil {
				t.Fatalf("the share %q is not a number: %v", parts[0], sherr)
			}
			wScoreBits, serr := strconv.ParseUint(parts[1], 16, 32)
			if serr != nil {
				t.Fatalf("the score %q is not a bit pattern: %v", parts[1], serr)
			}
			wPath := parts[2]

			h := got.Hits[i]
			if uint64(h.Share) != wShare || h.Path != wPath {
				t.Fatalf("Query(%q) hit %d is %d:%s, the fixture says %d:%s",
					needle, i, h.Share, h.Path, wShare, wPath)
			}
			if bits := math.Float32bits(h.Score); bits != uint32(wScoreBits) {
				t.Fatalf("Query(%q) hit %d scored %08x (%v), the fixture says %08x (%v)",
					needle, i, bits, h.Score, wScoreBits, math.Float32frombits(uint32(wScoreBits)))
			}
		}
		checked++
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanning query.tsv: %v", err)
	}
	if checked == 0 {
		t.Fatal("no query was checked")
	}
}

func TestRecordFramingRoundTrips(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.idx")

	for _, want := range []string{"first", "second"} {
		payload, err := EncodePayload(1, []Entry{{Share: 1, Path: want}})
		if err != nil {
			t.Fatalf("EncodePayload: %v", err)
		}
		if _, err := AppendRecord(p, payload); err != nil {
			t.Fatalf("AppendRecord: %v", err)
		}
	}

	rec, err := ReadRecords(p)
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(rec.Records) != 2 || rec.Torn {
		t.Fatalf("got %d records, torn=%v", len(rec.Records), rec.Torn)
	}
}

func TestAMissingSegmentIsEmptyNotAnError(t *testing.T) {
	rec, err := ReadRecords(filepath.Join(t.TempDir(), "nope.idx"))
	if err != nil {
		t.Fatalf("a missing segment returned %v", err)
	}
	if len(rec.Records) != 0 || rec.Torn {
		t.Fatalf("a missing segment produced %+v", rec)
	}
}

// A record length prefix comes off disk before the body does, so a corrupt one
// must not size an allocation.
func TestAnAbsurdRecordLengthIsRefused(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.idx")
	buf := []byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0}
	if err := os.WriteFile(p, buf, 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	rec, err := ReadRecords(p)
	if err != nil {
		t.Fatalf("ReadRecords: %v", err)
	}
	if len(rec.Records) != 0 {
		t.Fatalf("an absurd length produced %d records", len(rec.Records))
	}
	if !rec.Torn {
		t.Fatal("the garbage was not reported as a torn tail")
	}
}

func FuzzDecodePayload(f *testing.F) {
	if p, err := EncodePayload(7, []Entry{{Share: 1, Path: "a.txt"}}); err == nil {
		f.Add(p)
	}
	f.Add([]byte{0})
	f.Add([]byte{1})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, payload []byte) {
		seq, entries, err := DecodePayload(payload)
		if err != nil {
			return
		}
		_ = seq
		for _, e := range entries {
			if len(e.Path) > MaxRecord {
				t.Fatalf("a record produced a name of %d bytes", len(e.Path))
			}
		}
	})
}

func FuzzReadRecords(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0})

	f.Fuzz(func(t *testing.T, buf []byte) {
		dir := t.TempDir()
		p := filepath.Join(dir, "t.idx")
		if err := os.WriteFile(p, buf, 0o600); err != nil {
			t.Skip()
		}
		rec, err := ReadRecords(p)
		if err != nil {
			return
		}
		// The intact prefix can never be longer than the file, or the opener
		// would truncate to past the end.
		if rec.GoodLen > int64(len(buf)) {
			t.Fatalf("GoodLen %d is past the %d-byte file", rec.GoodLen, len(buf))
		}
		if rec.Torn != (rec.GoodLen < int64(len(buf))) {
			t.Fatalf("torn=%v disagrees with GoodLen %d of %d", rec.Torn, rec.GoodLen, len(buf))
		}
	})
}
