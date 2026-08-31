package ident

import (
	"errors"
	"math"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

func ptr(v int64) *int64 { return &v }

func TestOfTakesTheStatsIdentity(t *testing.T) {
	st := vfs.Stat{Dev: 66306, Ino: 12345, BtimeNs: ptr(1700000000)}
	got := Of(7, st)
	want := Ident{Share: 7, Dev: 66306, Ino: 12345, Btime: ptr(1700000000)}
	if !got.Equal(want) {
		t.Errorf("Of gave %+v, want %+v", got, want)
	}
}

// An absent birth time and a zero one are different facts about a file, and
// the whole reason Btime is a pointer.
func TestAbsentBirthTimeIsNotAZeroOne(t *testing.T) {
	absent := Ident{Share: 1, Dev: 2, Ino: 3}
	zero := Ident{Share: 1, Dev: 2, Ino: 3, Btime: ptr(0)}
	if absent.Equal(zero) {
		t.Error("an identity with no birth time compares equal to one with a zero birth time")
	}
	if !zero.Equal(Ident{Share: 1, Dev: 2, Ino: 3, Btime: ptr(0)}) {
		t.Error("two zero birth times compare unequal")
	}
}

func TestEqualComparesByValueNotByPointer(t *testing.T) {
	a := Ident{Share: 4, Dev: 5, Ino: 6, Btime: ptr(99)}
	b := Ident{Share: 4, Dev: 5, Ino: 6, Btime: ptr(99)}
	if !a.Equal(b) {
		t.Error("two identities with equal birth times behind different pointers compare unequal")
	}
	for _, differs := range []Ident{
		{Share: 5, Dev: 5, Ino: 6, Btime: ptr(99)},
		{Share: 4, Dev: 6, Ino: 6, Btime: ptr(99)},
		{Share: 4, Dev: 5, Ino: 7, Btime: ptr(99)},
		{Share: 4, Dev: 5, Ino: 6, Btime: ptr(100)},
		{Share: 4, Dev: 5, Ino: 6},
	} {
		if a.Equal(differs) {
			t.Errorf("%+v compares equal to %+v", a, differs)
		}
	}
}

// The device and inode numbers a real filesystem hands out use the whole
// unsigned range, so the round trip has to survive the top bit being set.
func TestSQLRoundTripSurvivesTheFullUnsignedRange(t *testing.T) {
	for _, id := range []Ident{
		{Share: 0, Dev: 0, Ino: 0},
		{Share: 1, Dev: 66306, Ino: 12345, Btime: ptr(1700000000)},
		{Share: math.MaxUint32, Dev: math.MaxUint64, Ino: math.MaxUint64, Btime: ptr(math.MaxInt64)},
		{Share: 2, Dev: 1 << 63, Ino: math.MaxUint64 - 1, Btime: ptr(math.MinInt64)},
		{Share: 3, Dev: math.MaxUint64, Ino: 1 << 63},
	} {
		dev, ino, present, btime := id.ToSQL()
		back, err := FromSQL(int64(id.Share), dev, ino, present, btime)
		if err != nil {
			t.Fatalf("FromSQL(%+v): %v", id, err)
		}
		if !back.Equal(id) {
			t.Errorf("round trip of %+v gave %+v", id, back)
		}
	}
}

func TestToSQLReportsWhetherABirthTimeIsPresent(t *testing.T) {
	_, _, present, btime := Ident{Share: 1, Dev: 2, Ino: 3}.ToSQL()
	if present != 0 || btime != 0 {
		t.Errorf("an absent birth time reported present %d, btime %d", present, btime)
	}
	_, _, present, btime = Ident{Share: 1, Dev: 2, Ino: 3, Btime: ptr(-5)}.ToSQL()
	if present != 1 || btime != -5 {
		t.Errorf("a birth time of -5 reported present %d, btime %d", present, btime)
	}
}

// A stored share that no longer fits a share id is a corrupt row, which is
// worth saying rather than truncating into a different share's identity.
func TestFromSQLRefusesAShareThatNoLongerFits(t *testing.T) {
	_, err := FromSQL(int64(math.MaxUint32)+1, 0, 0, 0, 0)
	if !errors.Is(err, num.ErrNarrow) {
		t.Fatalf("an oversized share gave %v, want a narrowing refusal", err)
	}
	if _, err := FromSQL(-1, 0, 0, 0, 0); !errors.Is(err, num.ErrNarrow) {
		t.Fatalf("a negative share gave %v, want a narrowing refusal", err)
	}
}

// Each identity owns its birth time, so a caller writing through one
// identity's pointer cannot rewrite another's. A shared pointer would make
// two rows scanned in one loop the same identity.
func TestEachIdentityOwnsItsBirthTime(t *testing.T) {
	first, err := FromSQL(1, 2, 3, 1, 41)
	if err != nil {
		t.Fatalf("FromSQL: %v", err)
	}
	second, err := FromSQL(1, 2, 4, 1, 41)
	if err != nil {
		t.Fatalf("FromSQL: %v", err)
	}
	if first.Btime == second.Btime {
		t.Fatal("two identities share one birth-time pointer")
	}

	*first.Btime = 42
	if *second.Btime != 41 {
		t.Errorf("writing through one identity moved another's birth time to %d", *second.Btime)
	}
}

func TestRootIDIsZero(t *testing.T) {
	if RootID != 0 {
		t.Errorf("RootID is %d, want 0", RootID)
	}
}
