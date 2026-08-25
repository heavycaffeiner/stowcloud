package state_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// The migration 4 durable tables: the persisted share registry and the
// operation store. Both are things the cache cannot rebuild, which is what it
// means for them to live here.

func addUserForOp(t *testing.T, d *state.DB, id int, name string) {
	t.Helper()
	if err := exec(t, d,
		`INSERT INTO user(id, name, pw_hash, created_ns) VALUES (?, ?, 'x', 1)`, id, name); err != nil {
		t.Fatalf("addUser: %v", err)
	}
}

func TestOperationLifecycle(t *testing.T) {
	d := open(t)
	addUserForOp(t, d, 7, "alice")
	ctx := context.Background()

	id, err := d.CreateOp(ctx, 7, state.OpCopy, 3, 100, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("CreateOp: %v", err)
	}
	op, results, err := d.GetOp(ctx, id)
	if err != nil {
		t.Fatalf("GetOp: %v", err)
	}
	if op.State != state.OpRunning || op.Total != 3 {
		t.Fatalf("fresh op = %+v, want running total 3", op)
	}
	if len(results) != 0 {
		t.Fatalf("fresh op has %d results, want none", len(results))
	}

	if serr := d.SetOpProgress(ctx, id, 1, "halfway"); serr != nil {
		t.Fatalf("SetOpProgress: %v", serr)
	}
	if ferr := d.FinishOp(ctx, id, state.OpDone, 3, "done", 200, []state.OpResult{
		{Operation: id, Idx: 0, Path: "a", OK: true, Reason: state.ReasonItemOk},
		{Operation: id, Idx: 1, Path: "b", OK: false, Reason: state.ReasonItemFailed},
	}); ferr != nil {
		t.Fatalf("FinishOp: %v", ferr)
	}

	op, results, err = d.GetOp(ctx, id)
	if err != nil {
		t.Fatalf("GetOp after finish: %v", err)
	}
	if op.State != state.OpDone || op.Progress != 3 {
		t.Fatalf("finished op = %+v, want done progress 3", op)
	}
	if len(results) != 2 || !results[0].OK || results[1].OK {
		t.Fatalf("results = %+v, want ok then failed", results)
	}

	// The third path was recorded at creation and never got a result, which is
	// what a job that stopped short leaves behind. Without it a client is told
	// how many items are missing and never which.
	attempting, pending, uerr := d.UnfinishedOpItems(ctx, id)
	if uerr != nil {
		t.Fatalf("UnfinishedOpItems: %v", uerr)
	}
	if len(attempting) != 0 {
		t.Fatalf("attempting = %v, want none: nothing was marked started", attempting)
	}
	if len(pending) != 1 || pending[0] != "c" {
		t.Fatalf("pending = %v, want the one path with no result", pending)
	}
}

// An item marked started and never finished is the process dying mid-item, and
// it is reported apart from the ones nothing touched: whether it landed is
// genuinely unknown, and only looking at the destination settles it.
func TestOperationItemInFlightIsReportedApartFromUntouched(t *testing.T) {
	d := open(t)
	addUserForOp(t, d, 3, "carol")
	ctx := context.Background()

	id, err := d.CreateOp(ctx, 3, state.OpCopy, 2, 100, []string{"held", "never"})
	if err != nil {
		t.Fatalf("CreateOp: %v", err)
	}
	if serr := d.StartOpItem(ctx, id, 0); serr != nil {
		t.Fatalf("StartOpItem: %v", serr)
	}
	if ierr := d.InterruptOp(ctx, id, 200); ierr != nil {
		t.Fatalf("InterruptOp: %v", ierr)
	}

	attempting, pending, uerr := d.UnfinishedOpItems(ctx, id)
	if uerr != nil {
		t.Fatalf("UnfinishedOpItems: %v", uerr)
	}
	if len(attempting) != 1 || attempting[0] != "held" {
		t.Fatalf("attempting = %v, want the started item", attempting)
	}
	if len(pending) != 1 || pending[0] != "never" {
		t.Fatalf("pending = %v, want the untouched item", pending)
	}
}

func TestOperationScopedToOwner(t *testing.T) {
	d := open(t)
	addUserForOp(t, d, 1, "a")
	addUserForOp(t, d, 2, "b")
	ctx := context.Background()

	id, err := d.CreateOp(ctx, 1, state.OpDelete, 0, 100, nil)
	if err != nil {
		t.Fatalf("CreateOp: %v", err)
	}
	op, _, err := d.GetOp(ctx, id)
	if err != nil {
		t.Fatalf("GetOp: %v", err)
	}
	if op.User != 1 {
		t.Fatalf("owner = %d, want 1", op.User)
	}
}

func TestOperationNotFound(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	if _, _, err := d.GetOp(ctx, 999); err != state.ErrNoSuchOp {
		t.Fatalf("GetOp(missing) = %v, want ErrNoSuchOp", err)
	}
}

func TestSharesRegistry(t *testing.T) {
	d := open(t)
	ctx := context.Background()

	rowid, ierr := d.InsertShare(ctx, "photos", "/srv/photos", 100)
	if ierr != nil {
		t.Fatalf("InsertShare: %v", ierr)
	}
	if rowid != 1 {
		t.Fatalf("rowid = %d, want 1", rowid)
	}
	rows, lerr := d.ListShares(ctx)
	if lerr != nil {
		t.Fatalf("ListShares: %v", lerr)
	}
	if len(rows) != 1 || rows[0].Name != "photos" || rows[0].Host != "/srv/photos" {
		t.Fatalf("rows = %+v", rows)
	}

	if err := d.UpdateShare(ctx, rowid, "pics", "/srv/pics"); err != nil {
		t.Fatalf("UpdateShare: %v", err)
	}
	rows, lerr = d.ListShares(ctx)
	if lerr != nil {
		t.Fatalf("ListShares after update: %v", lerr)
	}
	if rows[0].Name != "pics" || rows[0].Host != "/srv/pics" {
		t.Fatalf("after update rows = %+v", rows)
	}

	if err := d.SetTrashOverride(ctx, 5, true); err != nil {
		t.Fatalf("SetTrashOverride: %v", err)
	}
	if on, ok, terr := d.TrashOverrideFor(ctx, 5); terr != nil || !ok || !on {
		t.Fatalf("TrashOverrideFor = %v %v %v, want on", on, ok, terr)
	}
	if on, ok, terr := d.TrashOverrideFor(ctx, 6); terr != nil || ok || on {
		t.Fatalf("TrashOverrideFor(6) = %v %v %v, want off absent", on, ok, terr)
	}

	if err := d.SetIdentityOverride(ctx, 3, "video", "/srv/video"); err != nil {
		t.Fatalf("SetIdentityOverride: %v", err)
	}
	if o, ok, terr := d.IdentityOverrideFor(ctx, 3); terr != nil || !ok || o.Name != "video" {
		t.Fatalf("IdentityOverrideFor = %+v %v %v", o, ok, terr)
	}

	if err := d.DeleteShare(ctx, rowid); err != nil {
		t.Fatalf("DeleteShare: %v", err)
	}
	rows, lerr = d.ListShares(ctx)
	if lerr != nil {
		t.Fatalf("ListShares after delete: %v", lerr)
	}
	if len(rows) != 0 {
		t.Fatalf("after delete rows = %+v, want none", rows)
	}
}

var _ = sql.ErrNoRows
