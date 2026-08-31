package state_test

import (
	"context"
	"errors"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/ident"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// overrides is cache.Overrides, spelled again rather than imported. The two
// packages are peers and neither may import the other, which is the whole
// reason the identity types live in a third package; a test that reached for
// cache to name the interface would create exactly the edge the design
// removes. Every type in the method set below comes from the neutral
// package, so this compiling is what proves the two halves still meet.
type overrides interface {
	LookupFileID(ctx context.Context, id ident.Ident) (ident.FileID, bool, error)
	LookupFileIDOwner(ctx context.Context, id ident.FileID) (ident.Ident, bool, error)
	RecordFileIDs(ctx context.Context, assignments ...ident.Assignment) error
}

var _ overrides = (*state.DB)(nil)

func TestNoOverrideIsNotAnError(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	if _, ok, err := d.LookupFileID(ctx, ident.Ident{Share: 1, Dev: 1, Ino: 1}); err != nil || ok {
		t.Errorf("an identity nothing recorded: %v (found %v)", err, ok)
	}
	if _, ok, err := d.LookupFileIDOwner(ctx, 4242); err != nil || ok {
		t.Errorf("an id nothing reserved: %v (found %v)", err, ok)
	}
	if n, err := d.CountFileIDOverrides(ctx); err != nil || n != 0 {
		t.Errorf("a fresh database holds %d overrides (err %v)", n, err)
	}
}

func TestOverridesRoundTripBothDirections(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	id := ident.Ident{Share: 3, Dev: 1 << 63, Ino: ^uint64(0), Btime: btime(-9)}
	if err := d.RecordFileIDs(ctx, ident.Assignment{Ident: id, ID: 12345}); err != nil {
		t.Fatalf("RecordFileIDs: %v", err)
	}

	got, ok, err := d.LookupFileID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("LookupFileID: %v (found %v)", err, ok)
	}
	if got != 12345 {
		t.Errorf("the recorded id read back as %d", got)
	}

	owner, ok, err := d.LookupFileIDOwner(ctx, 12345)
	if err != nil || !ok {
		t.Fatalf("LookupFileIDOwner: %v (found %v)", err, ok)
	}
	if !owner.Equal(id) {
		t.Errorf("the owner read back as %+v, want %+v", owner, id)
	}
}

// An absent birth time and a zero one are different files, so they are
// different rows.
func TestOverridesKeepAbsentAndZeroBirthTimeApart(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	absent := ident.Ident{Share: 1, Dev: 5, Ino: 5}
	zero := ident.Ident{Share: 1, Dev: 5, Ino: 5, Btime: btime(0)}
	if err := d.RecordFileIDs(ctx,
		ident.Assignment{Ident: absent, ID: 111},
		ident.Assignment{Ident: zero, ID: 222}); err != nil {
		t.Fatalf("RecordFileIDs: %v", err)
	}

	for name, tc := range map[string]struct {
		id   ident.Ident
		want ident.FileID
	}{"absent": {absent, 111}, "zero": {zero, 222}} {
		got, ok, err := d.LookupFileID(ctx, tc.id)
		if err != nil || !ok {
			t.Fatalf("%s: %v (found %v)", name, err, ok)
		}
		if got != tc.want {
			t.Errorf("%s read back as %d, want %d", name, got, tc.want)
		}
	}
}

// A rebuild that reaches the same decision writes nothing; one that reaches
// a different decision is refused rather than overwriting, because answering
// a sync client with either value would be a guess.
func TestARepeatedRecordIsFineAndAContradictionIsRefused(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	id := ident.Ident{Share: 1, Dev: 2, Ino: 3, Btime: btime(4)}
	for range 3 {
		if err := d.RecordFileIDs(ctx, ident.Assignment{Ident: id, ID: 500}); err != nil {
			t.Fatalf("re-recording the same assignment: %v", err)
		}
	}
	if n, err := d.CountFileIDOverrides(ctx); err != nil || n != 1 {
		t.Errorf("%d rows after three identical records (err %v)", n, err)
	}

	err := d.RecordFileIDs(ctx, ident.Assignment{Ident: id, ID: 600})
	if !errors.Is(err, state.ErrOverrideConflict) {
		t.Fatalf("a contradicting record returned %v, want ErrOverrideConflict", err)
	}
	got, _, err := d.LookupFileID(ctx, id)
	if err != nil {
		t.Fatalf("LookupFileID: %v", err)
	}
	if got != 500 {
		t.Errorf("the refused write moved the recorded id to %d", got)
	}
}

// The id column is UNIQUE, so a second identity claiming one id fails rather
// than producing two answers to the same question.
func TestTwoIdentitiesCannotClaimOneID(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	first := ident.Ident{Share: 1, Dev: 1, Ino: 1}
	second := ident.Ident{Share: 1, Dev: 1, Ino: 2}
	if err := d.RecordFileIDs(ctx, ident.Assignment{Ident: first, ID: 900}); err != nil {
		t.Fatalf("RecordFileIDs: %v", err)
	}
	if err := d.RecordFileIDs(ctx, ident.Assignment{Ident: second, ID: 900}); err == nil {
		t.Fatal("two identities were recorded against one id")
	}
}

// Both sides of a collision commit together, so a rebuild never sees half a
// decision.
func TestRecordingBothSidesIsOneTransaction(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	base := ident.Ident{Share: 1, Dev: 1, Ino: 1}
	newcomer := ident.Ident{Share: 1, Dev: 1, Ino: 2}
	// The second assignment collides with an id already taken, so the whole
	// call fails and neither row lands.
	if err := d.RecordFileIDs(ctx, ident.Assignment{Ident: base, ID: 700}); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	third := ident.Ident{Share: 1, Dev: 1, Ino: 3}
	err := d.RecordFileIDs(ctx,
		ident.Assignment{Ident: newcomer, ID: 800},
		ident.Assignment{Ident: third, ID: 700})
	if err == nil {
		t.Fatal("a batch with a conflicting assignment committed")
	}
	if _, ok, err := d.LookupFileID(ctx, newcomer); err != nil || ok {
		t.Errorf("the first half of a failed batch committed: %v (found %v)", err, ok)
	}
}

func TestRecordingNothingIsANoOp(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	if err := d.RecordFileIDs(ctx); err != nil {
		t.Errorf("recording an empty batch: %v", err)
	}
}

// The counter is what stands between a cold walk and several million queries
// against a table that is almost always empty, so a write has to invalidate
// it rather than leaving a stale zero behind.
func TestTheEmptyTableShortcutIsInvalidatedByAWrite(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	id := ident.Ident{Share: 1, Dev: 2, Ino: 3}
	// Prime the counter at zero.
	if _, ok, err := d.LookupFileID(ctx, id); err != nil || ok {
		t.Fatalf("priming the counter: %v (found %v)", err, ok)
	}
	if err := d.RecordFileIDs(ctx, ident.Assignment{Ident: id, ID: 42}); err != nil {
		t.Fatalf("RecordFileIDs: %v", err)
	}
	got, ok, err := d.LookupFileID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("the write did not invalidate the empty-table shortcut: %v (found %v)", err, ok)
	}
	if got != 42 {
		t.Errorf("read back %d, want 42", got)
	}
}

// A test driving the real cache against this table would have to import the
// cache, and the two are peers that may not import each other in either
// direction: that edge is exactly what moving the identity types to a third
// package removed. What each half can prove alone is proven alone. The
// interface conformance above is this side; the cache's own tests drive the
// collision and rebuild paths against a stand-in for this table; and the
// end-to-end run belongs to the layer that wires both together.
