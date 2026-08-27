package state_test

import (
	"context"
	"database/sql"
	"math"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// seedCappedUser inserts a user with a quota. A nil cap is no cap.
func seedCappedUser(t *testing.T, d *state.DB, id int64, cap *int64) {
	t.Helper()
	ctx := context.Background()
	if err := d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO user(id, name, pw_hash, quota_bytes, created_ns)
			 VALUES (?, ?, '', ?, 0)`, id, "u", cap)
		return err
	}); err != nil {
		t.Fatalf("seeding a capped user: %v", err)
	}
}

func usage(t *testing.T, d *state.DB, user int64) int64 {
	t.Helper()
	var n int64
	if err := d.SQL().QueryRowContext(context.Background(),
		`SELECT usage_bytes FROM user WHERE id = ?`, user).Scan(&n); err != nil {
		t.Fatalf("reading usage: %v", err)
	}
	return n
}

func TestReserveAdmitsBelowTheCapAndRefusesAtIt(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedCappedUser(t, d, 1, id64(1000))
	q := state.NewQuota(d)

	switch ok, err := q.Reserve(ctx, 1, 600); {
	case err != nil:
		t.Fatalf("Reserve: %v", err)
	case !ok:
		t.Fatal("a reservation inside the cap was refused")
	}
	if got := usage(t, d, 1); got != 600 {
		t.Errorf("usage is %d after reserving 600", got)
	}

	// 600 + 500 is past 1000, so the whole reservation is refused rather
	// than partly booked.
	switch ok, err := q.Reserve(ctx, 1, 500); {
	case err != nil:
		t.Fatalf("Reserve: %v", err)
	case ok:
		t.Fatal("a reservation past the cap was admitted")
	}
	if got := usage(t, d, 1); got != 600 {
		t.Errorf("a refused reservation moved usage to %d", got)
	}

	// Exactly to the cap is allowed: the comparison is <=.
	if ok, err := q.Reserve(ctx, 1, 400); err != nil || !ok {
		t.Fatalf("reserving exactly to the cap: %v (ok %v)", err, ok)
	}
	if got := usage(t, d, 1); got != 1000 {
		t.Errorf("usage is %d, want the full 1000", got)
	}
}

func TestANullCapAlwaysAdmits(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedCappedUser(t, d, 1, nil)
	q := state.NewQuota(d)

	for range 5 {
		if ok, err := q.Reserve(ctx, 1, 1<<40); err != nil || !ok {
			t.Fatalf("an uncapped reservation: %v (ok %v)", err, ok)
		}
	}
}

// A user who does not exist has no headroom either, so the refusal is the
// same shape as being at the cap: false with no error.
func TestReserveForAMissingUserRefusesWithoutAnError(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)

	ok, err := state.NewQuota(d).Reserve(ctx, 4242, 1)
	if err != nil {
		t.Fatalf("Reserve for a missing user: %v", err)
	}
	if ok {
		t.Error("a missing user was given headroom")
	}
}

// A byte count that does not fit the signed column is a caller error rather
// than a refusal: the two are different facts.
func TestAnUnstorableReservationErrors(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedCappedUser(t, d, 1, nil)

	if _, err := state.NewQuota(d).Reserve(ctx, 1, math.MaxUint64); err == nil {
		t.Fatal("a reservation past the signed range was accepted")
	}
}

// The cap check and the increment are one statement, so exactly one of the
// racing reservations gets the last slot.
func TestRacingReservationsAdmitExactlyOne(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedCappedUser(t, d, 1, id64(100))
	q := state.NewQuota(d)

	var (
		admitted atomic.Int64
		wg       sync.WaitGroup
	)
	errs := make([]error, 16)
	for i := range errs {
		wg.Add(1)
		task.Go(ctx, "quota: racing reservation", func() {
			defer wg.Done()
			ok, err := q.Reserve(ctx, 1, 100)
			errs[i] = err
			if ok {
				admitted.Add(1)
			}
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("reservation %d: %v", i, err)
		}
	}
	if got := admitted.Load(); got != 1 {
		t.Errorf("%d of 16 reservations were admitted for one slot, want 1", got)
	}
	if got := usage(t, d, 1); got != 100 {
		t.Errorf("usage is %d after the race, want exactly 100", got)
	}
}

// Reserve books the bytes durably, so Commit exists only to keep the call
// site honest and moves nothing.
func TestCommitIsANoOpAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedCappedUser(t, d, 1, id64(1000))
	q := state.NewQuota(d)

	if ok, err := q.Reserve(ctx, 1, 300); err != nil || !ok {
		t.Fatalf("Reserve: %v (ok %v)", err, ok)
	}
	for range 3 {
		if err := q.Commit(ctx, 1, 300); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}
	if got := usage(t, d, 1); got != 300 {
		t.Errorf("usage is %d after three commits, want the reserved 300", got)
	}
}

func TestReleaseCreditsAndClampsAtZero(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedCappedUser(t, d, 1, id64(1000))
	q := state.NewQuota(d)

	if ok, err := q.Reserve(ctx, 1, 500); err != nil || !ok {
		t.Fatalf("Reserve: %v (ok %v)", err, ok)
	}
	if err := q.Release(ctx, 1, 200); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := usage(t, d, 1); got != 300 {
		t.Errorf("usage is %d after crediting 200 of 500", got)
	}

	// More than is booked clamps rather than going negative.
	if err := q.Release(ctx, 1, 10_000); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := usage(t, d, 1); got != 0 {
		t.Errorf("usage is %d after an oversized credit, want 0", got)
	}
}

// The write path reserves and the delete path credits, so a negative value
// arriving here means the two were confused.
func TestANegativeReleaseIsACallerBug(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedCappedUser(t, d, 1, id64(1000))

	if err := state.NewQuota(d).Release(ctx, 1, -5); err == nil {
		t.Fatal("a negative release was booked")
	}
}

func TestReleasingZeroChangesNothing(t *testing.T) {
	ctx := context.Background()
	d, _ := open(t)
	seedCappedUser(t, d, 1, id64(1000))
	q := state.NewQuota(d)

	if ok, err := q.Reserve(ctx, 1, 400); err != nil || !ok {
		t.Fatalf("Reserve: %v (ok %v)", err, ok)
	}
	if err := q.Release(ctx, 1, 0); err != nil {
		t.Fatalf("Release(0): %v", err)
	}
	if got := usage(t, d, 1); got != 400 {
		t.Errorf("usage is %d after releasing nothing", got)
	}
}

// Reserve updates an existing row and never inserts one, so the size guard
// does not touch it: refusing here would block uploads for a reason that has
// nothing to do with the ledger.
func TestReserveIgnoresTheSizeGuard(t *testing.T) {
	ctx := context.Background()
	d, f := open(t)
	seedCappedUser(t, d, 1, id64(1000))

	f.SetWritesBlocked(true)
	ok, err := state.NewQuota(d).Reserve(ctx, 1, 100)
	if err != nil {
		t.Fatalf("Reserve under the guard: %v", err)
	}
	if !ok {
		t.Error("a reservation with headroom was refused because the guard was tripped")
	}
}
