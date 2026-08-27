package journal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
)

// stepClock hands out a fresh nanosecond on every reading, so a test can
// force an order without waiting for a real one.
type stepClock struct {
	at time.Time
}

func (c *stepClock) Now() time.Time                  { c.at = c.at.Add(time.Millisecond); return c.at }
func (c *stepClock) Since(t time.Time) time.Duration { return c.at.Sub(t) }
func (c *stepClock) Nanos() int64                    { return c.Now().UnixNano() }

func openJournal(t *testing.T, clk clock.Clock) *DB {
	t.Helper()
	return openAt(t, filepath.Join(t.TempDir(), "journal.db"), clk)
}

func openAt(t *testing.T, path string, clk clock.Clock) *DB {
	t.Helper()
	f, err := dbfile.Open(context.Background(), Spec(path))
	if err != nil {
		t.Fatalf("opening the journal: %v", err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return New(f, clk)
}

func stepping() clock.Clock {
	return &stepClock{at: time.Unix(1_700_000_000, 0)}
}

func sharePath(t *testing.T, s string) vfs.SharePath {
	t.Helper()
	p, err := vfs.ParseSharePath(s)
	if err != nil {
		t.Fatalf("ParseSharePath(%q): %v", s, err)
	}
	return p
}

func record(t *testing.T, d *DB, account uint32, path string, op Op) {
	t.Helper()
	if err := d.Record(context.Background(), Event{
		Account: account, Share: 1, Path: sharePath(t, path), Op: op,
	}); err != nil {
		t.Fatalf("Record(%q): %v", path, err)
	}
}

func TestRecordUpsertsRatherThanAppending(t *testing.T) {
	ctx := context.Background()
	d := openJournal(t, stepping())

	record(t, d, 7, "notes/todo.md", OpUpload)
	first, err := d.Recent(ctx, 7, 0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("%d rows after one write, want 1", len(first))
	}

	record(t, d, 7, "notes/todo.md", OpEdit)
	got, err := d.Recent(ctx, 7, 0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("%d rows after re-writing the same file, want 1", len(got))
	}
	if got[0].Op != OpEdit {
		t.Errorf("op %v after an edit replaced an upload, want edit", got[0].Op)
	}
	if got[0].AtNs <= first[0].AtNs {
		t.Errorf("the second write stamped %d, not later than the first's %d",
			got[0].AtNs, first[0].AtNs)
	}
}

// The trim commits with the write that crossed the cap, so re-opening the
// file (which is what a crash leaves behind) already sees the cap enforced.
func TestTrimCommitsWithTheWriteThatCrossedTheCap(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.db")
	d := openAt(t, path, stepping())

	for i := range limits.JournalRowsPerAccount + 20 {
		record(t, d, 7, fmt.Sprintf("f%05d", i), OpUpload)
	}

	var n int
	if err := d.f.SQL().QueryRowContext(ctx,
		`SELECT count(*) FROM write_event WHERE user = 7`).Scan(&n); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if n != limits.JournalRowsPerAccount {
		t.Errorf("%d rows on disk, want the cap of %d", n, limits.JournalRowsPerAccount)
	}

	// The oldest ones went, and the newest survived.
	got, err := d.Recent(ctx, 7, 0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if got[0].Path.String() != fmt.Sprintf("f%05d", limits.JournalRowsPerAccount+19) {
		t.Errorf("newest row is %q, want the last write", got[0].Path.String())
	}
	for _, e := range got {
		if e.Path.String() < "f00020" {
			t.Errorf("a row below the trim boundary survived: %q", e.Path.String())
		}
	}
}

func TestTrimIsPerAccount(t *testing.T) {
	ctx := context.Background()
	d := openJournal(t, stepping())

	for i := range limits.JournalRowsPerAccount + 5 {
		record(t, d, 1, fmt.Sprintf("a%05d", i), OpUpload)
	}
	for i := range 10 {
		record(t, d, 2, fmt.Sprintf("b%05d", i), OpUpload)
	}
	for i := range limits.JournalRowsPerAccount + 5 {
		record(t, d, 1, fmt.Sprintf("c%05d", i), OpUpload)
	}

	quiet, err := d.Recent(ctx, 2, 0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(quiet) != 10 {
		t.Errorf("the quiet account has %d rows, want 10", len(quiet))
	}
}

func TestRecentOrdersNewestFirst(t *testing.T) {
	ctx := context.Background()
	d := openJournal(t, stepping())

	for i := range 5 {
		record(t, d, 7, fmt.Sprintf("f%d", i), OpUpload)
	}
	got, err := d.Recent(ctx, 7, 0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].AtNs < got[i].AtNs {
			t.Fatalf("row %d is older than row %d", i-1, i)
		}
	}
	if got[0].Path.String() != "f4" {
		t.Errorf("newest is %q, want f4", got[0].Path.String())
	}
}

// Two rows sharing a nanosecond still come back in a fixed order, which the
// rowid tiebreak is what provides.
func TestRecentIsDeterministicWhenTimestampsCollide(t *testing.T) {
	ctx := context.Background()
	d := openJournal(t, clock.Fixed(time.Unix(1_700_000_000, 0)))

	for i := range 4 {
		record(t, d, 7, fmt.Sprintf("f%d", i), OpUpload)
	}
	first, err := d.Recent(ctx, 7, 0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	for range 3 {
		again, err := d.Recent(ctx, 7, 0)
		if err != nil {
			t.Fatalf("Recent: %v", err)
		}
		for i := range first {
			if first[i].Path.String() != again[i].Path.String() {
				t.Fatalf("re-reading the same rows gave a different order at %d: %q then %q",
					i, first[i].Path.String(), again[i].Path.String())
			}
		}
	}
	if first[0].Path.String() != "f3" {
		t.Errorf("newest under a stopped clock is %q, want the last write f3", first[0].Path.String())
	}
}

func TestRecentSinceWindowsAndEmptyIsNotAnError(t *testing.T) {
	ctx := context.Background()
	d := openJournal(t, stepping())

	for i := range 6 {
		record(t, d, 7, fmt.Sprintf("f%d", i), OpUpload)
	}
	all, err := d.Recent(ctx, 7, 0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}

	// A window starting at the third-newest row's stamp.
	got, err := d.RecentSince(ctx, 7, all[2].AtNs, 0)
	if err != nil {
		t.Fatalf("RecentSince: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("%d rows at or after the third-newest stamp, want 3", len(got))
	}

	after, err := d.RecentSince(ctx, 7, all[0].AtNs+1, 0)
	if err != nil {
		t.Fatalf("RecentSince past every row: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("%d rows after every stamp, want none", len(after))
	}
}

func TestLimitClampsToTheCap(t *testing.T) {
	ctx := context.Background()
	d := openJournal(t, stepping())

	for i := range 20 {
		record(t, d, 7, fmt.Sprintf("f%02d", i), OpUpload)
	}
	for _, limit := range []int{0, -1, limits.JournalRowsPerAccount + 1_000_000} {
		got, err := d.Recent(ctx, 7, limit)
		if err != nil {
			t.Fatalf("Recent(limit %d): %v", limit, err)
		}
		if len(got) != 20 {
			t.Errorf("limit %d gave %d rows, want all 20", limit, len(got))
		}
	}
	got, err := d.Recent(ctx, 7, 5)
	if err != nil {
		t.Fatalf("Recent(5): %v", err)
	}
	if len(got) != 5 {
		t.Errorf("limit 5 gave %d rows", len(got))
	}
}

// A row this server would no longer accept fails the read rather than being
// dropped: this package cannot tell corrupt from stale, and the caller can.
func TestARefusedPathErrorsTheRead(t *testing.T) {
	ctx := context.Background()
	d := openJournal(t, stepping())

	record(t, d, 7, "ok.txt", OpUpload)
	if err := d.f.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO write_event(user, share, path, op, at_ns) VALUES (7, 1, '../escape', 'edit', 1)`)
		return err
	}); err != nil {
		t.Fatalf("planting the row: %v", err)
	}

	if _, err := d.Recent(ctx, 7, 0); err == nil {
		t.Fatal("a row carrying '../escape' read back without complaint")
	}
}

func TestAShareThatNoLongerFitsErrorsTheRead(t *testing.T) {
	ctx := context.Background()
	d := openJournal(t, stepping())

	if err := d.f.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO write_event(user, share, path, op, at_ns)
			 VALUES (7, 4294967296, 'ok.txt', 'edit', 1)`)
		return err
	}); err != nil {
		t.Fatalf("planting the row: %v", err)
	}
	if _, err := d.Recent(ctx, 7, 0); err == nil {
		t.Fatal("a row carrying an oversized share read back without complaint")
	}
}

func TestParseOpDefaultsAnUnknownLabelToUpload(t *testing.T) {
	for label, want := range map[string]Op{
		"upload":                  OpUpload,
		"edit":                    OpEdit,
		"copy":                    OpCopy,
		"move":                    OpMove,
		"restore":                 OpRestore,
		"a-verb-from-a-later-day": OpUpload,
		"":                        OpUpload,
	} {
		if got := ParseOp(label); got != want {
			t.Errorf("ParseOp(%q) = %v, want %v", label, got, want)
		}
	}
}

func TestOpLabelsRoundTrip(t *testing.T) {
	for _, op := range []Op{OpUpload, OpEdit, OpCopy, OpMove, OpRestore} {
		if got := ParseOp(op.String()); got != op {
			t.Errorf("%v stored as %q read back as %v", op, op.String(), got)
		}
	}
}

func TestARecordedOpSurvivesTheRoundTrip(t *testing.T) {
	ctx := context.Background()
	d := openJournal(t, stepping())

	record(t, d, 7, "f", OpRestore)
	got, err := d.Recent(ctx, 7, 0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if got[0].Op != OpRestore {
		t.Errorf("op %v, want restore", got[0].Op)
	}
	if got[0].Account != 7 || got[0].Share != 1 || got[0].Path.String() != "f" {
		t.Errorf("row came back as %+v", got[0])
	}
}

// A journal.db that could not be opened leaves a nil *DB, and every caller
// above it needs no branch for that.
func TestEveryMethodIsSafeOnANilReceiver(t *testing.T) {
	ctx := context.Background()
	var d *DB

	if d.Enabled() {
		t.Error("a nil journal reports itself enabled")
	}
	if err := d.Record(ctx, Event{Account: 7}); err != nil {
		t.Errorf("Record on a nil journal: %v", err)
	}
	switch got, err := d.Recent(ctx, 7, 0); {
	case err != nil:
		t.Errorf("Recent on a nil journal: %v", err)
	case got != nil:
		t.Errorf("Recent on a nil journal gave %v, want nil", got)
	}
	switch got, err := d.RecentSince(ctx, 7, 1, 10); {
	case err != nil:
		t.Errorf("RecentSince on a nil journal: %v", err)
	case got != nil:
		t.Errorf("RecentSince on a nil journal gave %v, want nil", got)
	}
}

func TestOpenedJournalReportsItselfEnabled(t *testing.T) {
	if d := openJournal(t, stepping()); !d.Enabled() {
		t.Error("an opened journal reports itself disabled")
	}
}

// Nothing rebuilds who wrote what, so a discard step is refused.
func TestTheJournalIsNotRebuildable(t *testing.T) {
	spec := Spec(filepath.Join(t.TempDir(), "journal.db"))
	if spec.Rebuildable {
		t.Error("the journal declares itself rebuildable")
	}
	spec.Migrations = append(spec.Migrations,
		dbfile.Migration{Name: "2: a discard", Discard: true, SQL: `DELETE FROM write_event`})
	if _, err := dbfile.Open(context.Background(), spec); !errors.Is(err, dbfile.ErrMigrationFailed) {
		t.Fatalf("a discard against the journal returned %v, want a refusal", err)
	}
}
