package cache_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

const (
	testShare vfs.ShareID = 3
	testDev   uint64      = 0x2A_00_00_00_00_00_00_01
)

func btime(v int64) *int64 { return &v }

// memOverrides is the durable half's role, played in memory. The real one
// lives in store/state; this package must not import it, and a fake here
// keeps the collision path testable without one.
type memOverrides struct {
	mu     sync.Mutex
	byID   map[ident.FileID]ident.Ident
	byKey  []ident.Assignment
	writes int
}

func newOverrides() *memOverrides {
	return &memOverrides{byID: map[ident.FileID]ident.Ident{}}
}

func (m *memOverrides) LookupFileID(_ context.Context, id ident.Ident) (ident.FileID, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.byKey {
		if a.Ident.Equal(id) {
			return a.ID, true, nil
		}
	}
	return 0, false, nil
}

func (m *memOverrides) LookupFileIDOwner(_ context.Context, id ident.FileID) (ident.Ident, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	owner, ok := m.byID[id]
	return owner, ok, nil
}

func (m *memOverrides) RecordFileIDs(_ context.Context, assignments ...ident.Assignment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes++
	for _, a := range assignments {
		m.byID[a.ID] = a.Ident
		replaced := false
		for i, have := range m.byKey {
			if have.Ident.Equal(a.Ident) {
				m.byKey[i], replaced = a, true
				break
			}
		}
		if !replaced {
			m.byKey = append(m.byKey, a)
		}
	}
	return nil
}

func (m *memOverrides) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byKey)
}

type fixture struct {
	c    *cache.DB
	f    *dbfile.DB
	ov   *memOverrides
	path string
}

// openCache opens a cache at path. bits of zero takes the shipping width;
// anything else narrows the derivation so a collision is reachable.
func openCache(t *testing.T, path string, ov *memOverrides, bits uint) *fixture {
	t.Helper()
	ctx := context.Background()
	f, err := dbfile.Open(ctx, cache.Spec(path))
	if err != nil {
		t.Fatalf("opening the cache: %v", err)
	}
	t.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	})

	var c *cache.DB
	if bits == 0 {
		c, err = cache.New(ctx, f, ov)
	} else {
		c, err = cache.NewNarrow(ctx, f, ov, bits)
	}
	if err != nil {
		t.Fatalf("wrapping the cache: %v", err)
	}
	return &fixture{c: c, f: f, ov: ov, path: path}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return openCache(t, filepath.Join(t.TempDir(), "cache.db"), newOverrides(), 0)
}

// entry is one file in a fixture tree.
type entry struct {
	path  string
	ino   uint64
	dir   bool
	btime *int64
	size  uint64
	mtime int64
}

func (e entry) stat() vfs.Stat {
	kind := vfs.KindFile
	if e.dir {
		kind = vfs.KindDir
	}
	return vfs.Stat{
		Dev: testDev, Ino: e.ino, BtimeNs: e.btime,
		Size: e.size, MtimeNs: e.mtime, Kind: kind,
	}
}

func depthOf(path string) int { return strings.Count(path, "/") }

// populate upserts a tree, parents before children, and reports every id it
// was given.
func populate(t *testing.T, fx *fixture, entries []entry) map[string]ident.FileID {
	t.Helper()
	ctx := context.Background()
	ids := map[string]ident.FileID{}

	ordered := slices.Clone(entries)
	slices.SortStableFunc(ordered, func(a, b entry) int { return depthOf(a.path) - depthOf(b.path) })

	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		for _, e := range ordered {
			parent := ident.RootID
			name := e.path
			if i := strings.LastIndexByte(e.path, '/'); i >= 0 {
				got, ok := ids[e.path[:i]]
				if !ok {
					return fmt.Errorf("%s: its parent %s was not upserted first", e.path, e.path[:i])
				}
				parent, name = got, e.path[i+1:]
			}
			id, err := fx.c.Upsert(ctx, tx, testShare, parent, name, e.stat())
			if err != nil {
				return fmt.Errorf("upserting %s: %w", e.path, err)
			}
			ids[e.path] = id
		}
		return nil
	}); err != nil {
		t.Fatalf("populating: %v", err)
	}
	return ids
}

func smallTree() []entry {
	return []entry{
		{path: "docs", ino: 10, dir: true, btime: btime(1)},
		{path: "docs/a.txt", ino: 11, btime: btime(2), size: 100, mtime: 5},
		{path: "docs/b.txt", ino: 12, btime: btime(3), size: 200, mtime: 6},
		{path: "docs/sub", ino: 13, dir: true, btime: btime(4)},
		{path: "docs/sub/c.txt", ino: 14, btime: btime(5), size: 300, mtime: 7},
	}
}

// The derivation is pure, so the same identity gives the same id with no
// database in the picture at all.
func TestDeriveIDIsPureAndDeterministic(t *testing.T) {
	id := ident.Ident{Share: testShare, Dev: testDev, Ino: 42, Btime: btime(99)}
	first := cache.DeriveID(id, 0)
	for range 100 {
		if got := cache.DeriveID(id, 0); got != first {
			t.Fatalf("the same identity derived %d then %d", first, got)
		}
	}
	if first <= 0 {
		t.Errorf("derived id %d; it must be positive so a client reading it as signed is not confused", first)
	}
	if same := cache.DeriveID(ident.Ident{
		Share: testShare, Dev: testDev, Ino: 42, Btime: btime(99),
	}, 0); same != first {
		t.Error("an equal identity behind a different pointer derived a different id")
	}
}

func TestDeriveIDSeparatesEveryFieldAndTheAttempt(t *testing.T) {
	base := ident.Ident{Share: testShare, Dev: testDev, Ino: 42, Btime: btime(99)}
	got := cache.DeriveID(base, 0)
	for _, other := range []ident.Ident{
		{Share: testShare + 1, Dev: testDev, Ino: 42, Btime: btime(99)},
		{Share: testShare, Dev: testDev + 1, Ino: 42, Btime: btime(99)},
		{Share: testShare, Dev: testDev, Ino: 43, Btime: btime(99)},
		{Share: testShare, Dev: testDev, Ino: 42, Btime: btime(100)},
		{Share: testShare, Dev: testDev, Ino: 42},
	} {
		if cache.DeriveID(other, 0) == got {
			t.Errorf("%+v derives the same id as %+v", other, base)
		}
	}
	if cache.DeriveID(base, 1) == got {
		t.Error("attempt 1 derives the same id as attempt 0")
	}
}

// A file with no birth time and one whose birth time is zero are different
// files, and the flag byte in the key is what keeps them apart.
func TestDeriveIDKeepsAbsentAndZeroBirthTimeApart(t *testing.T) {
	absent := ident.Ident{Share: testShare, Dev: 5, Ino: 5}
	zero := ident.Ident{Share: testShare, Dev: 5, Ino: 5, Btime: btime(0)}
	if cache.DeriveID(absent, 0) == cache.DeriveID(zero, 0) {
		t.Error("an absent birth time derives the same id as a zero one")
	}
}

// A birth time before the epoch is a fact about the file, not an error, and
// it has to reach the key without being refused or folded onto another value.
func TestDeriveIDAcceptsABirthTimeBeforeTheEpoch(t *testing.T) {
	before := ident.Ident{Share: testShare, Dev: 5, Ino: 5, Btime: btime(-1_000_000)}
	after := ident.Ident{Share: testShare, Dev: 5, Ino: 5, Btime: btime(1_000_000)}
	if cache.DeriveID(before, 0) == cache.DeriveID(after, 0) {
		t.Error("a pre-epoch birth time derives the same id as its positive counterpart")
	}
	if cache.DeriveID(before, 0) <= 0 {
		t.Error("a pre-epoch birth time derived a non-positive id")
	}
}

func TestUpsertAllocatesOnceAndIsStableAcrossCalls(t *testing.T) {
	fx := newFixture(t)
	first := populate(t, fx, smallTree())
	again := populate(t, fx, smallTree())

	for path, id := range first {
		if again[path] != id {
			t.Errorf("%s: id %d then %d", path, id, again[path])
		}
	}
	if n := fx.ov.count(); n != 0 {
		t.Errorf("%d overrides recorded with no collision forced", n)
	}
}

func TestUpsertRefreshesSizeAndMtimeWithoutMoving(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	ids := populate(t, fx, smallTree())

	changed := entry{path: "docs/a.txt", ino: 11, btime: btime(2), size: 999, mtime: 4242}
	after := populate(t, fx, []entry{
		{path: "docs", ino: 10, dir: true, btime: btime(1)},
		changed,
	})
	if after["docs/a.txt"] != ids["docs/a.txt"] {
		t.Fatal("refreshing a file changed its id")
	}

	var size, mtime int64
	if err := fx.f.SQL().QueryRowContext(ctx,
		`SELECT size, mtime_ns FROM node WHERE id = ?`, int64(ids["docs/a.txt"])).
		Scan(&size, &mtime); err != nil {
		t.Fatalf("reading the row: %v", err)
	}
	if size != 999 || mtime != 4242 {
		t.Errorf("row carries size %d, mtime %d; want 999 and 4242", size, mtime)
	}

	// The parent and the name did not move, so they are as they were.
	var parent int64
	var name string
	if err := fx.f.SQL().QueryRowContext(ctx,
		`SELECT parent, name FROM node WHERE id = ?`, int64(ids["docs/a.txt"])).
		Scan(&parent, &name); err != nil {
		t.Fatalf("reading the row: %v", err)
	}
	if parent != int64(ids["docs"]) || name != "a.txt" {
		t.Errorf("row moved to parent %d, name %q", parent, name)
	}
}

// The cache catching up with a rename another program performed: same
// identity, new parent and name.
func TestUpsertFollowsAnOutOfBandMove(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	ids := populate(t, fx, smallTree())

	moved := populate(t, fx, []entry{
		{path: "docs", ino: 10, dir: true, btime: btime(1)},
		{path: "docs/sub", ino: 13, dir: true, btime: btime(4)},
		{path: "docs/sub/a.txt", ino: 11, btime: btime(2), size: 100, mtime: 5},
	})
	if moved["docs/sub/a.txt"] != ids["docs/a.txt"] {
		t.Fatalf("the moved file got a new id %d, want the original %d",
			moved["docs/sub/a.txt"], ids["docs/a.txt"])
	}

	_, path, err := fx.c.Resolve(ctx, ids["docs/a.txt"])
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path.String() != "docs/sub/a.txt" {
		t.Errorf("resolves to %q, want docs/sub/a.txt", path.String())
	}
}

func TestUpsertFollowsAKindChange(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	ids := populate(t, fx, []entry{{path: "thing", ino: 20, btime: btime(1)}})

	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		st := vfs.Stat{Dev: testDev, Ino: 20, BtimeNs: btime(1), Kind: vfs.KindDir}
		_, err := fx.c.Upsert(ctx, tx, testShare, ident.RootID, "thing", st)
		return err
	}); err != nil {
		t.Fatalf("re-upserting as a directory: %v", err)
	}

	var flags int64
	if err := fx.f.SQL().QueryRowContext(ctx,
		`SELECT flags FROM node WHERE id = ?`, int64(ids["thing"])).Scan(&flags); err != nil {
		t.Fatalf("reading flags: %v", err)
	}
	if flags&1 == 0 {
		t.Error("the row still reads as a file after becoming a directory")
	}
}

// A new row is what grows the file, so it is what the guard refuses. A
// refresh of an existing row is not.
func TestTheGuardRefusesANewRowAndAdmitsARefresh(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	populate(t, fx, []entry{{path: "seen", ino: 30, btime: btime(1)}})

	fx.f.SetWritesBlocked(true)
	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		st := vfs.Stat{Dev: testDev, Ino: 31, BtimeNs: btime(2), Kind: vfs.KindFile}
		_, err := fx.c.Upsert(ctx, tx, testShare, ident.RootID, "unseen", st)
		return err
	}); !errors.Is(err, dbfile.ErrWritesBlocked) {
		t.Fatalf("upserting an unseen file under the guard returned %v, want ErrWritesBlocked", err)
	}

	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		st := vfs.Stat{Dev: testDev, Ino: 30, BtimeNs: btime(1), Size: 7, Kind: vfs.KindFile}
		_, err := fx.c.Upsert(ctx, tx, testShare, ident.RootID, "seen", st)
		return err
	}); err != nil {
		t.Errorf("refreshing a known file under the guard: %v", err)
	}
}

func TestLookupNeverAllocates(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)

	unseen := vfs.Stat{Dev: testDev, Ino: 77, BtimeNs: btime(7), Kind: vfs.KindFile}
	switch got, ok, err := fx.c.Lookup(ctx, testShare, unseen); {
	case err != nil:
		t.Fatalf("Lookup: %v", err)
	case ok:
		t.Fatalf("an unseen file already had id %d", got)
	}

	var n int
	if err := fx.f.SQL().QueryRowContext(ctx, `SELECT count(*) FROM node`).Scan(&n); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if n != 0 {
		t.Errorf("Lookup created %d rows", n)
	}

	ids := populate(t, fx, []entry{{path: "f", ino: 77, btime: btime(7)}})
	switch got, ok, err := fx.c.Lookup(ctx, testShare, unseen); {
	case err != nil:
		t.Fatalf("Lookup after an upsert: %v", err)
	case !ok:
		t.Fatal("Lookup missed a file that was just upserted")
	case got != ids["f"]:
		t.Errorf("Lookup gave %d, want %d", got, ids["f"])
	}
}

func TestLookupFindsAFileWithNoBirthTime(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	ids := populate(t, fx, []entry{{path: "f", ino: 78}})

	st := vfs.Stat{Dev: testDev, Ino: 78, Kind: vfs.KindFile}
	got, ok, err := fx.c.Lookup(ctx, testShare, st)
	if err != nil || !ok {
		t.Fatalf("Lookup of a file with no birth time: %v (found %v)", err, ok)
	}
	if got != ids["f"] {
		t.Errorf("Lookup gave %d, want %d", got, ids["f"])
	}
}

// The identity index must hold a no-birth-time file once, which version 1's
// single index did not, since SQLite holds every NULL distinct.
func TestAFileWithNoBirthTimeIsStoredOnce(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)

	for range 3 {
		populate(t, fx, []entry{{path: "f", ino: 79}})
	}
	var n int
	if err := fx.f.SQL().QueryRowContext(ctx, `SELECT count(*) FROM node`).Scan(&n); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if n != 1 {
		t.Errorf("%d rows for one no-birth-time file, want 1", n)
	}
}

func TestResolveWalksTheParentChain(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	ids := populate(t, fx, smallTree())

	share, path, err := fx.c.Resolve(ctx, ids["docs/sub/c.txt"])
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if share != testShare {
		t.Errorf("resolved share %d, want %d", share, testShare)
	}
	if path.String() != "docs/sub/c.txt" {
		t.Errorf("resolved %q, want docs/sub/c.txt", path.String())
	}
}

func TestResolveOfAnUnknownIDIsErrNoNode(t *testing.T) {
	fx := newFixture(t)
	if _, _, err := fx.c.Resolve(context.Background(), 123456); !errors.Is(err, cache.ErrNoNode) {
		t.Fatalf("resolving an unknown id returned %v, want ErrNoNode", err)
	}
}

func TestResolveOfTheRootSentinelIsErrNoNode(t *testing.T) {
	fx := newFixture(t)
	if _, _, err := fx.c.Resolve(context.Background(), ident.RootID); !errors.Is(err, cache.ErrNoNode) {
		t.Fatalf("resolving RootID returned %v, want ErrNoNode", err)
	}
}

// A cyclic chain should never exist, and "should not happen" is not a proof
// for data a disk could have corrupted.
func TestResolveOfACyclicChainErrorsRatherThanLooping(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)

	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		for _, row := range [][2]int64{{1, 2}, {2, 1}} {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO node(id, share, parent, name, dev, ino, btime_ns, flags, size, mtime_ns)
				 VALUES (?, ?, ?, ?, ?, ?, NULL, 0, 0, 0)`,
				row[0], int64(testShare), row[1], "n"+strconv.FormatInt(row[0], 10),
				int64(testDev), row[0]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("planting the cycle: %v", err)
	}

	_, _, err := fx.c.Resolve(ctx, 1)
	if err == nil {
		t.Fatal("a cyclic parent chain resolved without complaint")
	}
	if !strings.Contains(err.Error(), "cyclic") {
		t.Errorf("the refusal does not name the cycle: %v", err)
	}
}

// A component this server's own grammar refuses is an error, not a repaired
// string: the trust boundary here is stored data this process may not have
// written.
func TestResolveOfARefusedComponentErrors(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)

	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO node(id, share, parent, name, dev, ino, btime_ns, flags, size, mtime_ns)
			 VALUES (55, ?, 0, '..', ?, 55, NULL, 0, 0, 0)`,
			int64(testShare), int64(testDev))
		return err
	}); err != nil {
		t.Fatalf("planting the row: %v", err)
	}
	if _, _, err := fx.c.Resolve(ctx, 55); err == nil {
		t.Fatal("a node named '..' resolved without complaint")
	}
}

func TestRenameIsOneRowForAWholeSubtree(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	ids := populate(t, fx, smallTree())

	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		return fx.c.Rename(ctx, tx, ids["docs/sub"], ids["docs"], "renamed")
	}); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	_, path, err := fx.c.Resolve(ctx, ids["docs/sub/c.txt"])
	if err != nil {
		t.Fatalf("Resolve after the rename: %v", err)
	}
	if path.String() != "docs/renamed/c.txt" {
		t.Errorf("the child resolves to %q, want docs/renamed/c.txt", path.String())
	}
}

func TestRenameOfAnUnknownIDIsErrNoNode(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		return fx.c.Rename(ctx, tx, 999999, ident.RootID, "x")
	})
	if !errors.Is(err, cache.ErrNoNode) {
		t.Fatalf("renaming an unknown id returned %v, want ErrNoNode", err)
	}
}

// The rename is not gated: the row already exists, so it cannot grow the
// file, and a stale path is worse than a database over the floor.
func TestRenameIsNotGatedByTheGuard(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	ids := populate(t, fx, smallTree())

	fx.f.SetWritesBlocked(true)
	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		return fx.c.Rename(ctx, tx, ids["docs/a.txt"], ids["docs"], "renamed.txt")
	}); err != nil {
		t.Errorf("renaming under the guard: %v", err)
	}
}

func TestDirEtagRoundTripsAFreshAggregate(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	ids := populate(t, fx, smallTree())
	want := cache.Aggregate{Etag: "abc123", RSize: 600, RCount: 3}

	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		return fx.c.PutDirEtag(ctx, tx, testShare, ids["docs"], want, 0)
	}); err != nil {
		t.Fatalf("PutDirEtag: %v", err)
	}

	got, ok, err := fx.c.DirEtag(ctx, testShare, ids["docs"])
	if err != nil || !ok {
		t.Fatalf("DirEtag: %v (fresh %v)", err, ok)
	}
	if got != want {
		t.Errorf("read back %+v, want %+v", got, want)
	}
}

func TestDirEtagSaysRecomputeForEveryStaleShape(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	ids := populate(t, fx, smallTree())
	agg := cache.Aggregate{Etag: "abc123", RSize: 600, RCount: 3}

	// No row at all.
	if _, ok, err := fx.c.DirEtag(ctx, testShare, ids["docs"]); err != nil || ok {
		t.Fatalf("an uncached directory answered fresh %v (err %v)", ok, err)
	}

	// A dirty row.
	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		if err := fx.c.PutDirEtag(ctx, tx, testShare, ids["docs"], agg, 0); err != nil {
			return err
		}
		return fx.c.MarkDirty(ctx, tx, testShare, []ident.FileID{ids["docs"]})
	}); err != nil {
		t.Fatalf("marking dirty: %v", err)
	}
	if _, ok, err := fx.c.DirEtag(ctx, testShare, ids["docs"]); err != nil || ok {
		t.Fatalf("a dirty row answered fresh %v (err %v)", ok, err)
	}

	// A row stamped with a superseded generation.
	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		if err := fx.c.PutDirEtag(ctx, tx, testShare, ids["docs"], agg, 0); err != nil {
			return err
		}
		_, err := fx.c.BumpShareGen(ctx, tx, testShare)
		return err
	}); err != nil {
		t.Fatalf("bumping: %v", err)
	}
	if _, ok, err := fx.c.DirEtag(ctx, testShare, ids["docs"]); err != nil || ok {
		t.Fatalf("a superseded row answered fresh %v (err %v)", ok, err)
	}
}

// MarkDirty on an id with no row writes a placeholder that is already
// invalid, so the next read says recompute rather than erroring.
func TestMarkDirtyOnAnUncachedIDIsARecomputeNotAnError(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)

	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		return fx.c.MarkDirty(ctx, tx, testShare, []ident.FileID{4242})
	}); err != nil {
		t.Fatalf("MarkDirty on an uncached id: %v", err)
	}
	if _, ok, err := fx.c.DirEtag(ctx, testShare, 4242); err != nil || ok {
		t.Fatalf("the placeholder answered fresh %v (err %v)", ok, err)
	}
}

// Both are deliberately outside the guard: refusing them would leave a stale
// aggregate still flagged valid, which is a wrong answer rather than a saved
// page.
func TestPutDirEtagAndMarkDirtyIgnoreTheGuard(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	ids := populate(t, fx, smallTree())

	fx.f.SetWritesBlocked(true)
	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		if err := fx.c.PutDirEtag(ctx, tx, testShare, ids["docs"],
			cache.Aggregate{Etag: "x", RSize: 1, RCount: 1}, 0); err != nil {
			return err
		}
		return fx.c.MarkDirty(ctx, tx, testShare, []ident.FileID{ids["docs/sub"]})
	}); err != nil {
		t.Errorf("caching an aggregate under the guard: %v", err)
	}
}

func TestBumpShareGenIsPerShare(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	ids := populate(t, fx, smallTree())
	const other vfs.ShareID = testShare + 1
	agg := cache.Aggregate{Etag: "abc", RSize: 1, RCount: 1}

	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		if err := fx.c.PutDirEtag(ctx, tx, testShare, ids["docs"], agg, 0); err != nil {
			return err
		}
		return fx.c.PutDirEtag(ctx, tx, other, ids["docs"], agg, 0)
	}); err != nil {
		t.Fatalf("caching: %v", err)
	}

	if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		gen, err := fx.c.BumpShareGen(ctx, tx, testShare)
		if err != nil {
			return err
		}
		if gen != 1 {
			return fmt.Errorf("the first bump gave generation %d, want 1", gen)
		}
		return nil
	}); err != nil {
		t.Fatalf("bumping: %v", err)
	}

	if _, ok, err := fx.c.DirEtag(ctx, testShare, ids["docs"]); err != nil || ok {
		t.Errorf("the bumped share still answers fresh %v (err %v)", ok, err)
	}
	if _, ok, err := fx.c.DirEtag(ctx, other, ids["docs"]); err != nil || !ok {
		t.Errorf("the other share lost its aggregate: fresh %v (err %v)", ok, err)
	}
}

func TestShareGenStartsAtZeroAndCounts(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)

	switch gen, err := fx.c.ShareGen(ctx, testShare); {
	case err != nil:
		t.Fatalf("ShareGen: %v", err)
	case gen != 0:
		t.Errorf("a never-bumped share reads generation %d, want 0", gen)
	}

	for want := uint64(1); want <= 3; want++ {
		if err := fx.c.Write(ctx, func(tx *sql.Tx) error {
			gen, err := fx.c.BumpShareGen(ctx, tx, testShare)
			if err != nil {
				return err
			}
			if gen != want {
				return fmt.Errorf("bump gave %d, want %d", gen, want)
			}
			return nil
		}); err != nil {
			t.Fatalf("bumping to %d: %v", want, err)
		}
	}
}

// The identity is what a rebuild re-derives from, so deleting the file and
// walking the same tree gives every file the same id back.
func TestDeletingTheCacheRebuildsEveryID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.db")
	ov := newOverrides()

	first := openCache(t, path, ov, 0)
	before := populate(t, first, smallTree())
	if err := first.f.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("deleting %s: %v", path+suffix, err)
		}
	}

	// A different walk order, which is what a rebuild actually does.
	reversed := slices.Clone(smallTree())
	slices.Reverse(reversed)
	rebuilt := openCache(t, path, ov, 0)
	after := populate(t, rebuilt, reversed)

	for p, id := range before {
		if after[p] != id {
			t.Errorf("%s: id %d before the rebuild and %d after", p, id, after[p])
		}
	}
}

// Version 1's single identity index is the shape that shipped, and step 2
// discards it rather than trying to repair rows there is no principled way
// to choose between.
func TestMigrationTwoDiscardsAVersionOneDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")

	old, err := dbfile.Open(ctx, cache.SpecV1(path))
	if err != nil {
		t.Fatalf("opening at version 1: %v", err)
	}
	if err := old.Write(ctx, func(tx *sql.Tx) error {
		// Two rows for one no-btime identity, which version 1's index
		// allowed and version 2's must not.
		for _, id := range []int64{1, 2} {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO node(id, share, parent, name, dev, ino, btime_ns, flags, size, mtime_ns)
				 VALUES (?, ?, 0, 'f', ?, 9, NULL, 0, 0, 0)`,
				id, int64(testShare), int64(testDev)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seeding the duplicate: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	fx := openCache(t, path, newOverrides(), 0)
	var n int
	if err := fx.f.SQL().QueryRowContext(ctx, `SELECT count(*) FROM node`).Scan(&n); err != nil {
		t.Fatalf("counting after the discard: %v", err)
	}
	if n != 0 {
		t.Errorf("%d rows survived the discard", n)
	}

	// And the duplicate cannot come back.
	populate(t, fx, []entry{{path: "f", ino: 9}})
	populate(t, fx, []entry{{path: "f", ino: 9}})
	if err := fx.f.SQL().QueryRowContext(ctx, `SELECT count(*) FROM node`).Scan(&n); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if n != 1 {
		t.Errorf("%d rows for one no-btime identity under version 2, want 1", n)
	}
}
