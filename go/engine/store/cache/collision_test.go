package cache_test

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

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
)

// collisionBits folds the derivation into a space small enough that a
// hundred files collide, which a 63-bit space needs a corpus nobody can
// build in a test to reach.
const collisionBits = 8

func crowd() []entry {
	out := []entry{{path: "d", ino: 1, dir: true, btime: btime(1)}}
	for i := range 100 {
		out = append(out, entry{
			path:  "d/f" + strconv.Itoa(i),
			ino:   uint64(100 + i),
			btime: btime(int64(i)),
		})
	}
	return out
}

func TestAForcedCollisionRecordsAnOverrideAndKeepsIDsUnique(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.db")
	ov := newOverrides()

	fx := openCache(t, path, ov, collisionBits)
	before := populate(t, fx, crowd())

	if ov.count() == 0 {
		t.Fatalf("%d files at %d bits recorded no override: the collision path never ran",
			len(crowd()), collisionBits)
	}

	seen := make(map[ident.FileID]string, len(before))
	for p, id := range before {
		if other, dup := seen[id]; dup {
			t.Errorf("%s and %s both hold id %d", p, other, id)
		}
		seen[id] = p
	}
}

// A rebuild does not reproduce insertion order, so the override table is
// what makes it reproduce the same ids anyway.
func TestARebuildReproducesEveryCollisionDecision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.db")
	ov := newOverrides()

	fx := openCache(t, path, ov, collisionBits)
	before := populate(t, fx, crowd())
	recorded := ov.count()
	if err := fx.f.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	dropFile(t, path)

	reversed := slices.Clone(crowd())
	slices.Reverse(reversed)
	rebuilt := openCache(t, path, ov, collisionBits)
	after := populate(t, rebuilt, reversed)

	for p, id := range before {
		if after[p] != id {
			t.Errorf("%s: id %d before the rebuild and %d after", p, id, after[p])
		}
	}
	if ov.count() != recorded {
		t.Errorf("the rebuild changed the override count from %d to %d", recorded, ov.count())
	}
}

// Recording only the newcomer answers a two-file collision correctly and a
// three-file one wrongly on a later rebuild: the third file would find the
// base holder's id unclaimed and take it. Both sides are recorded, so it
// cannot.
func TestAThirdFileCannotStealTheBaseHoldersID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.db")
	ov := newOverrides()

	// Two identities that collide at attempt zero, found rather than assumed.
	const bits = 8
	first, second, ok := collidingPair(bits)
	if !ok {
		t.Fatalf("no colliding pair found at %d bits", bits)
	}

	fx := openCache(t, path, ov, bits)
	ids := populate(t, fx, []entry{
		collider(t, "a", first),
		collider(t, "b", second),
	})
	if ids["a"] == ids["b"] {
		t.Fatal("the two colliding files were given the same id")
	}
	if err := fx.f.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	dropFile(t, path)

	// The rebuild walks the second file first, and a third file that
	// collides with the same base arrives before the original holder does.
	third, ok := collidesWith(bits, first, second)
	if !ok {
		t.Fatalf("no third colliding identity found at %d bits", bits)
	}

	rebuilt := openCache(t, path, ov, bits)
	after := populate(t, rebuilt, []entry{
		collider(t, "b", second),
		collider(t, "c", third),
		collider(t, "a", first),
	})

	if after["a"] != ids["a"] {
		t.Errorf("the base holder came back as %d, want its original %d", after["a"], ids["a"])
	}
	if after["b"] != ids["b"] {
		t.Errorf("the second file came back as %d, want its original %d", after["b"], ids["b"])
	}
	if after["c"] == after["a"] || after["c"] == after["b"] {
		t.Errorf("the third file stole an id already held: %d", after["c"])
	}
}

// A recorded override outranks the derivation, because which file won a base
// id was an insertion-order decision that is never revisited.
func TestARecordedOverrideOutranksTheDerivation(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)

	id := ident.Ident{Share: testShare, Dev: testDev, Ino: 9, Btime: btime(9)}
	const forced ident.FileID = 424242
	if cache.DeriveID(id, 0) == forced {
		t.Fatal("the derivation happens to agree with the forced id, so this test proves nothing")
	}
	if err := fx.ov.RecordFileIDs(ctx, ident.Assignment{Ident: id, ID: forced}); err != nil {
		t.Fatalf("recording the override: %v", err)
	}

	ids := populate(t, fx, []entry{{path: "f", ino: 9, btime: btime(9)}})
	if ids["f"] != forced {
		t.Errorf("the allocation returned %d, want the recorded %d", ids["f"], forced)
	}
}

// Reaching the bound means the derivation is not distributing, which is a
// worse problem than one collision and must not be hidden by a retry loop.
func TestExhaustingTheAttemptsIsAHardError(t *testing.T) {
	ctx := context.Background()
	// One bit of id space: two ids exist, so the third distinct identity
	// runs out of attempts.
	fx := openCache(t, filepath.Join(t.TempDir(), "cache.db"), newOverrides(), 1)

	err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		for i := range 8 {
			st := vfs.Stat{
				Dev: testDev, Ino: uint64(500 + i), BtimeNs: btime(int64(i)), Kind: vfs.KindFile,
			}
			if _, err := fx.c.Upsert(ctx, tx, testShare, ident.RootID,
				"f"+strconv.Itoa(i), st); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		t.Fatal("eight files in a two-id space allocated without complaint")
	}
	if !strings.Contains(err.Error(), "attempts") {
		t.Errorf("the refusal does not name the attempt bound: %v", err)
	}
}

// The override write commits before the node row that uses the id, so a
// crash between them leaves a reservation with no node, which is the state
// every rebuild starts from anyway.
func TestTheOverrideIsRecordedBeforeTheNodeRow(t *testing.T) {
	ctx := context.Background()
	fx := openCache(t, filepath.Join(t.TempDir(), "cache.db"), newOverrides(), collisionBits)

	// Abort the transaction after the allocation. The node rows roll back
	// with it; the overrides, written outside it, do not.
	sentinel := errors.New("stop here")
	err := fx.c.Write(ctx, func(tx *sql.Tx) error {
		for i := range 40 {
			st := vfs.Stat{
				Dev: testDev, Ino: uint64(700 + i), BtimeNs: btime(int64(i)), Kind: vfs.KindFile,
			}
			if _, err := fx.c.Upsert(ctx, tx, testShare, ident.RootID,
				"f"+strconv.Itoa(i), st); err != nil {
				return err
			}
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("the aborted write returned %v", err)
	}

	var nodes int
	if err := fx.f.SQL().QueryRowContext(ctx, `SELECT count(*) FROM node`).Scan(&nodes); err != nil {
		t.Fatalf("counting nodes: %v", err)
	}
	if nodes != 0 {
		t.Errorf("%d node rows survived a rolled-back transaction", nodes)
	}
	if fx.ov.count() == 0 {
		t.Error("no override survived, so the collision decision was rolled back with the nodes")
	}
}

// collidingPair finds two inode numbers whose identities derive the same id
// at the given width.
func collidingPair(bits uint) (uint64, uint64, bool) {
	seen := map[ident.FileID]uint64{}
	for ino := uint64(1); ino < 4096; ino++ {
		id := cache.DeriveNarrow(identOf(ino), 0, bits)
		if first, ok := seen[id]; ok {
			return first, ino, true
		}
		seen[id] = ino
	}
	return 0, 0, false
}

// collidesWith finds a third inode number deriving the same base id as the
// two given ones, and equal to neither.
func collidesWith(bits uint, a, b uint64) (uint64, bool) {
	want := cache.DeriveNarrow(identOf(a), 0, bits)
	for ino := uint64(1); ino < 65536; ino++ {
		if ino == a || ino == b {
			continue
		}
		if cache.DeriveNarrow(identOf(ino), 0, bits) == want {
			return ino, true
		}
	}
	return 0, false
}

// collider is one entry of a forced collision, built from the same identity
// the search below walks, so the two cannot drift apart.
func collider(t *testing.T, path string, ino uint64) entry {
	t.Helper()
	id := identOf(ino)
	return entry{path: path, ino: id.Ino, btime: id.Btime}
}

// identOf is the identity the collision search walks. The birth time is
// derived from the inode number so every candidate is a distinct identity;
// the search's own loop keeps the value well inside the signed range, and a
// value that did not fit answers zero, which still separates the candidates
// from each other by inode number.
func identOf(ino uint64) ident.Ident {
	b, err := num.Narrow[int64](ino)
	if err != nil {
		b = 0
	}
	return ident.Ident{Share: testShare, Dev: testDev, Ino: ino, Btime: &b}
}

func dropFile(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("deleting %s: %v", path+suffix, err)
		}
	}
}
