package journal_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/journal"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"

	_ "modernc.org/sqlite" // the driver the fixture is written with
)

// rustSchema is what the tree this replaces creates, verbatim including the
// IF NOT EXISTS clauses, because the point of the check is that the file
// another implementation wrote is recognised as it is.
const rustSchema = `
CREATE TABLE IF NOT EXISTS write_event (
    user   INTEGER NOT NULL,
    share  INTEGER NOT NULL,
    path   TEXT    NOT NULL,
    op     TEXT    NOT NULL,
    at_ns  INTEGER NOT NULL,
    UNIQUE (user, share, path)
);
CREATE INDEX IF NOT EXISTS write_event_by_user ON write_event(user, at_ns DESC);`

// mkjournal writes a journal.db by hand and closes it.
func mkjournal(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("creating %s: %v", filepath.Base(path), err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func openJournal(t *testing.T, path string) (*journal.DB, func()) {
	t.Helper()
	f, err := dbfile.Open(context.Background(), journal.Spec(path))
	if err != nil {
		t.Fatalf("opening %s: %v", filepath.Base(path), err)
	}
	return journal.New(f, clock.Fixed(time.Unix(0, 5_000))), func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}
}

func mustPath(t *testing.T, s string) vfs.SharePath {
	t.Helper()
	p, err := vfs.ParseSharePath(s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return p
}

// A populated Rust journal is adopted, not recreated: every row it holds is
// still there afterwards, the Go API can write to it, and it survives a close
// and a reopen.
func TestAPopulatedRustJournalIsAdopted(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.db")
	mkjournal(t, path, rustSchema,
		`INSERT INTO write_event VALUES (5, 7, 'docs/old.txt', 'edit', 1000)`,
		`INSERT INTO write_event VALUES (5, 7, 'docs/older.txt', 'upload', 900)`)

	d, closeIt := openJournal(t, path)
	got, err := d.Recent(ctx, 5, 10)
	if err != nil {
		t.Fatalf("reading the adopted journal: %v", err)
	}
	if len(got) != 2 || got[0].Path.String() != "docs/old.txt" {
		t.Fatalf("the adopted journal reads back as %+v", got)
	}

	if rerr := d.Record(ctx, journal.Event{
		Account: 5, Share: 7, Path: mustPath(t, "docs/new.txt"), Op: journal.OpUpload,
	}); rerr != nil {
		t.Fatalf("recording into the adopted journal: %v", rerr)
	}
	closeIt()

	d, closeAgain := openJournal(t, path)
	defer closeAgain()
	got, err = d.Recent(ctx, 5, 10)
	if err != nil {
		t.Fatalf("re-reading: %v", err)
	}
	if len(got) != 3 || got[0].Path.String() != "docs/new.txt" {
		t.Fatalf("after a reopen the journal reads back as %+v", got)
	}
}

// Adopting means recording the version and nothing else. A migration that ran
// would have failed against the table that is already there.
func TestAdoptionRecordsVersionOneAndRunsNoMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.db")
	mkjournal(t, path, rustSchema,
		`INSERT INTO write_event VALUES (5, 7, 'docs/old.txt', 'edit', 1000)`)

	f, err := dbfile.Open(ctx, journal.Spec(path))
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	if v, verr := f.Version(ctx); verr != nil || v != 1 {
		t.Fatalf("version %d, %v; want 1 and no error", v, verr)
	}
	var rows int
	if qerr := f.SQL().QueryRowContext(ctx, `SELECT count(*) FROM write_event`).Scan(&rows); qerr != nil {
		t.Fatalf("counting: %v", qerr)
	}
	if rows != 1 {
		t.Errorf("the adopted journal holds %d rows, want 1", rows)
	}
}

// A shape that is nearly the right one is refused rather than claimed. Store
// turns that into a warning and an empty listing, and the file is untouched.
func TestANearMissIsNotAdopted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema string
	}{
		{
			"a column renamed",
			`CREATE TABLE write_event (account INTEGER NOT NULL, share INTEGER NOT NULL,
			   path TEXT NOT NULL, op TEXT NOT NULL, at_ns INTEGER NOT NULL,
			   UNIQUE (account, share, path));
			 CREATE INDEX write_event_by_user ON write_event(account, at_ns DESC)`,
		},
		{
			"nothing making it unique",
			`CREATE TABLE write_event (user INTEGER NOT NULL, share INTEGER NOT NULL,
			   path TEXT NOT NULL, op TEXT NOT NULL, at_ns INTEGER NOT NULL);
			 CREATE INDEX write_event_by_user ON write_event(user, at_ns DESC)`,
		},
		{
			"the listing index missing",
			`CREATE TABLE write_event (user INTEGER NOT NULL, share INTEGER NOT NULL,
			   path TEXT NOT NULL, op TEXT NOT NULL, at_ns INTEGER NOT NULL,
			   UNIQUE (user, share, path))`,
		},
		{
			"a table nobody here wrote",
			rustSchema + `; CREATE TABLE activity (id INTEGER PRIMARY KEY)`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "journal.db")
			mkjournal(t, path, tc.schema,
				`INSERT INTO write_event VALUES (5, 7, 'docs/old.txt', 'edit', 1000)`)

			f, err := dbfile.Open(ctx, journal.Spec(path))
			if err == nil {
				if cerr := f.Close(); cerr != nil {
					t.Errorf("closing: %v", cerr)
				}
				t.Fatal("a journal this binary does not recognise was opened anyway")
			}
			if !errors.Is(err, journal.ErrNotAdoptable) {
				t.Fatalf("opening returned %v, want ErrNotAdoptable", err)
			}

			// The refusal leaves the file as it was, which is what makes it
			// recoverable rather than a rewrite of somebody's history.
			db, oerr := sql.Open("sqlite", path)
			if oerr != nil {
				t.Fatal(oerr)
			}
			defer func() {
				if cerr := db.Close(); cerr != nil {
					t.Errorf("closing: %v", cerr)
				}
			}()
			var rows int
			if qerr := db.QueryRowContext(ctx, `SELECT count(*) FROM write_event`).Scan(&rows); qerr != nil {
				t.Fatalf("counting: %v", qerr)
			}
			if rows != 1 {
				t.Errorf("the refused journal holds %d rows, want its original 1", rows)
			}
		})
	}
}

// A file that does not exist yet is not an adoption case: the runner creates
// the shape from migration 1.
func TestAFreshJournalIsMigratedNormally(t *testing.T) {
	ctx := context.Background()
	f, err := dbfile.Open(ctx, journal.Spec(filepath.Join(t.TempDir(), "journal.db")))
	if err != nil {
		t.Fatalf("opening a fresh journal: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	if v, verr := f.Version(ctx); verr != nil || v != 1 {
		t.Fatalf("version %d, %v; want 1 and no error", v, verr)
	}
}
