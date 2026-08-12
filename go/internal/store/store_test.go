package store_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/store"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/journal"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

const testShare vfs.ShareID = 3

// stepClock hands out a different nanosecond on every reading, so that rows
// written in a loop have an order the cap can be asserted against.
type stepClock struct{ n int64 }

func (c *stepClock) Now() time.Time                  { return time.Unix(0, c.Nanos()) }
func (c *stepClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }
func (c *stepClock) Nanos() int64                    { c.n++; return c.n }

func open(t *testing.T, dir string) *store.Store {
	t.Helper()
	s, err := store.Open(dir, store.Options{Clock: &stepClock{}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// file is one entry of a synthetic share.
type file struct {
	path  string
	ino   uint64
	dir   bool
	btime int64
}

func tree() []file {
	return []file{
		{path: "docs", ino: 10, dir: true, btime: 1},
		{path: "media", ino: 11, dir: true, btime: 2},
		{path: "docs/report.txt", ino: 12, btime: 3},
		{path: "docs/notes", ino: 13, dir: true, btime: 4},
		{path: "docs/notes/a.md", ino: 14, btime: 5},
		{path: "media/clip.mp4", ino: 15, btime: 6},
	}
}

func (f file) stat() vfs.Stat {
	b := f.btime
	st := vfs.Stat{Dev: 0x901, Ino: f.ino, BtimeNs: &b, MtimeNs: 99, Size: f.ino, Kind: vfs.KindFile}
	if f.dir {
		st.Kind = vfs.KindDir
	}
	return st
}

func parentOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return ""
}

func nameOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func mustPath(t *testing.T, s string) vfs.SharePath {
	t.Helper()
	p, err := vfs.ParseSharePath(s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return p
}

// walk populates the cache from the tree in the order given.
func walk(t *testing.T, s *store.Store, files []file) map[string]cache.FileID {
	t.Helper()
	ctx := context.Background()
	c := s.Cache()
	ids := map[string]cache.FileID{"": cache.RootID}
	if err := c.Write(ctx, func(tx *sql.Tx) error {
		for _, f := range files {
			parent, ok := ids[parentOf(f.path)]
			if !ok {
				t.Fatalf("%s was walked before its parent", f.path)
			}
			id, err := c.Upsert(ctx, tx, testShare, parent, nameOf(f.path), f.stat())
			if err != nil {
				return err
			}
			ids[f.path] = id
		}
		return nil
	}); err != nil {
		t.Fatalf("walking: %v", err)
	}
	delete(ids, "")
	return ids
}

// The principle, executed against the assembled store: delete the cache,
// start again, and nothing was lost.
func TestDeletingTheCacheAndRestartingLosesNothing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	s := open(t, dir)
	before := walk(t, s, tree())

	// One row in each of the other two files, so that "no loss" means more
	// than the ids coming back.
	pinned := cache.Ident{Share: testShare, Dev: 0x901, Ino: 777}
	if err := s.State().RecordFileIDs(ctx, cache.Assignment{Ident: pinned, ID: 4242}); err != nil {
		t.Fatalf("recording an override: %v", err)
	}
	if err := s.Journal().Record(ctx, journal.Event{
		Account: 5, Share: testShare, Path: mustPath(t, "docs/report.txt"), Op: journal.OpEdit,
	}); err != nil {
		t.Fatalf("recording a write: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := os.Remove(filepath.Join(dir, store.CacheFile)); err != nil {
		t.Fatalf("deleting the cache: %v", err)
	}

	restarted := open(t, dir)
	defer func() {
		if err := restarted.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	// A different walk order, which is what a second crawl of the same tree
	// honestly produces.
	reordered := slices.Clone(tree())
	slices.Reverse(reordered)
	slices.SortStableFunc(reordered, func(a, b file) int {
		return len(parentOf(a.path)) - len(parentOf(b.path))
	})
	after := walk(t, restarted, reordered)

	for p, id := range before {
		if after[p] != id {
			t.Errorf("%s: id %d before the rebuild and %d after", p, id, after[p])
		}
	}

	got, ok, err := restarted.State().LookupFileID(ctx, pinned)
	if err != nil || !ok || got != 4242 {
		t.Errorf("the override read back as %d (found %v, err %v), want 4242", got, ok, err)
	}
	recent, err := restarted.Journal().Recent(ctx, 5, 10)
	if err != nil {
		t.Fatalf("reading the journal: %v", err)
	}
	if len(recent) != 1 || recent[0].Path.String() != "docs/report.txt" || recent[0].Op != journal.OpEdit {
		t.Errorf("the journal came back as %+v", recent)
	}
}

// The cap is deterministic and clock-independent, and it is applied in the
// same transaction as the upsert so it holds even if the process dies next.
func TestJournalIsCappedByRowCount(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	const overflow = 5
	for i := range limits.JournalRowsPerAccount + overflow {
		p, err := vfs.ParseSharePath("f" + strconv.Itoa(i))
		if err != nil {
			t.Fatalf("parsing: %v", err)
		}
		if err := s.Journal().Record(ctx, journal.Event{
			Account: 1, Share: testShare, Path: p, Op: journal.OpUpload,
		}); err != nil {
			t.Fatalf("recording %d: %v", i, err)
		}
	}

	rows, err := s.Journal().Recent(ctx, 1, limits.JournalRowsPerAccount)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if len(rows) != limits.JournalRowsPerAccount {
		t.Fatalf("the journal holds %d rows, want the cap of %d", len(rows), limits.JournalRowsPerAccount)
	}
	// Newest first, and the oldest are the ones that went.
	if rows[0].Path.String() != "f"+strconv.Itoa(limits.JournalRowsPerAccount+overflow-1) {
		t.Errorf("the newest row is %s", rows[0].Path.String())
	}
	for _, r := range rows {
		if r.Path.String() == "f0" {
			t.Error("the oldest row survived the cap")
		}
	}
}

// Another account's rows are not touched by one account's cap.
func TestTheJournalCapIsPerAccount(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir())
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	if err := s.Journal().Record(ctx, journal.Event{
		Account: 2, Share: testShare, Path: mustPath(t, "quiet.txt"), Op: journal.OpUpload,
	}); err != nil {
		t.Fatalf("recording: %v", err)
	}
	for i := range limits.JournalRowsPerAccount + 3 {
		if err := s.Journal().Record(ctx, journal.Event{
			Account: 1, Share: testShare, Path: mustPath(t, "loud"+strconv.Itoa(i)), Op: journal.OpUpload,
		}); err != nil {
			t.Fatalf("recording: %v", err)
		}
	}

	rows, err := s.Journal().Recent(ctx, 2, 10)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("the quiet account has %d rows, want 1", len(rows))
	}
}

// Losing the journal costs a listing. It must not cost a running server.
func TestAnUnopenableJournalIsADisabledFeature(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, store.JournalFile), 0o750); err != nil {
		t.Fatalf("standing a directory in the journal's place: %v", err)
	}

	s, err := store.Open(dir, store.Options{})
	if err != nil {
		t.Fatalf("Open refused to start over the journal: %v", err)
	}
	defer func() {
		if cerr := s.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	}()

	if s.Journal().Enabled() {
		t.Error("the journal reported itself enabled")
	}
	p, err := vfs.ParseSharePath("f")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if err := s.Journal().Record(ctx, journal.Event{Account: 1, Share: testShare, Path: p}); err != nil {
		t.Errorf("recording into a disabled journal: %v", err)
	}
	if rows, rerr := s.Journal().Recent(ctx, 1, 10); rerr != nil || len(rows) != 0 {
		t.Errorf("a disabled journal returned %d rows and %v", len(rows), rerr)
	}

	// And the two that matter are open and working.
	if _, _, err := s.State().LookupFileID(ctx, cache.Ident{Share: testShare, Dev: 1, Ino: 1}); err != nil {
		t.Errorf("the durable half is not usable: %v", err)
	}
}

// A database from a newer binary refuses to open, and says which one.
func TestOpenRefusesADatabaseFromANewerBinary(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if err := s.State().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE schema_version SET version = version + 10 WHERE id = 1`)
		return err
	}); err != nil {
		t.Fatalf("winding the version forward: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := store.Open(dir, store.Options{}); !errors.Is(err, store.ErrSchemaAhead) {
		t.Fatalf("Open returned %v, want ErrSchemaAhead", err)
	}
}
