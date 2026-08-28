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

	rowid, ierr := d.InsertShare(ctx, state.ShareRow{
		Name: "photos", Host: "/srv/photos", SymlinkPolicy: "deny",
	}, 100)
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

	// Every property of a share is on the row, which is what folding the two
	// kinds into one is: there is no second table an edit half lands in.
	if err := d.UpdateShare(ctx, rowid, state.ShareRow{
		Name: "pics", Host: "/srv/pics", TrashEnabled: true,
		SharedExternally: true, SymlinkPolicy: "within_share",
	}); err != nil {
		t.Fatalf("UpdateShare: %v", err)
	}
	rows, lerr = d.ListShares(ctx)
	if lerr != nil {
		t.Fatalf("ListShares after update: %v", lerr)
	}
	got := rows[0]
	if got.Name != "pics" || got.Host != "/srv/pics" || !got.TrashEnabled ||
		!got.SharedExternally || got.SymlinkPolicy != "within_share" {
		t.Fatalf("after update rows = %+v", got)
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

// A running operation has no message and no finish time, and this listing
// returns exactly the operations that are still running. Scanning either column
// into a plain value therefore failed the whole query the moment one existed.
//
// The route behind it is GET /api/jobs, which the progress tray polls, so the
// listing broke precisely when there was something to show and worked whenever
// there was nothing. GetOp reads the same two columns through null types and
// always did; this one did not.
func TestTheListingReadsARunningOperation(t *testing.T) {
	d := open(t)
	addUserForOp(t, d, 7, "alice")
	ctx := context.Background()

	id, err := d.CreateOp(ctx, 7, state.OpCopy, 2, 100, []string{"a", "b"})
	if err != nil {
		t.Fatalf("CreateOp: %v", err)
	}

	ops, err := d.ListOps(ctx, 7, 10)
	if err != nil {
		t.Fatalf("ListOps with a running operation: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("the listing returned %d operations, want the running one", len(ops))
	}
	if ops[0].ID != id || ops[0].State != state.OpRunning {
		t.Errorf("the listing returned %+v", ops[0])
	}
	// A row that has not finished reports no message and no finish time rather
	// than inventing either.
	if ops[0].Message != "" {
		t.Errorf("a running operation carries the message %q", ops[0].Message)
	}
	if ops[0].FinishedNs != 0 {
		t.Errorf("a running operation reports a finish time of %d", ops[0].FinishedNs)
	}
}

// A finished operation's message survives the same listing, so the null
// handling did not turn every message into an empty string.
func TestTheListingKeepsAnInterruptedOperationsMessage(t *testing.T) {
	d := open(t)
	addUserForOp(t, d, 7, "alice")
	ctx := context.Background()

	id, err := d.CreateOp(ctx, 7, state.OpCopy, 2, 100, []string{"a", "b"})
	if err != nil {
		t.Fatalf("CreateOp: %v", err)
	}
	// Interrupted rather than done: the listing returns running and interrupted
	// operations, which are the two a client can still act on.
	if ferr := d.FinishOp(ctx, id, state.OpInterrupted, 1, "the process stopped", 200, nil); ferr != nil {
		t.Fatalf("FinishOp: %v", ferr)
	}

	ops, err := d.ListOps(ctx, 7, 10)
	if err != nil {
		t.Fatalf("ListOps: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("the listing returned %d operations", len(ops))
	}
	if ops[0].Message != "the process stopped" {
		t.Errorf("the message came back as %q", ops[0].Message)
	}
	if ops[0].FinishedNs != 200 {
		t.Errorf("the finish time came back as %d", ops[0].FinishedNs)
	}
}
