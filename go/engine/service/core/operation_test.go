//go:build linux

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// waitForOp polls until an operation leaves the running state, which is how
// a test observes the detached runner without reaching into it.
func waitForOp(t *testing.T, c *Core, owner UserID, id OperationID) Operation {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		op, err := c.Operation(context.Background(), owner, id)
		if err != nil {
			t.Fatalf("reading operation %d: %v", id, err)
		}
		if op.State != state.OpRunning {
			return op
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("operation %d never left the running state", id)
	return Operation{}
}

func TestAnOperationIsScopedToItsOwner(t *testing.T) {
	c, st, srcHost, _, src, dst := twoShares(t)
	ctx := context.Background()
	seedUser(t, st, 2, "bob")
	writeFile(t, srcHost, "note.txt", "body")

	start, err := c.StartCopy(ctx, 1, at(t, src, "note.txt"), at(t, dst, "note.txt"), ConflictFail)
	if err != nil {
		t.Fatalf("StartCopy: %v", err)
	}
	waitForOp(t, c, 1, start.ID)

	// A row owned by somebody else and a row that does not exist are one
	// answer, so an id-probing client learns nothing.
	if _, err := c.Operation(ctx, 2, start.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reading another owner's operation returned %v, want ErrNotFound", err)
	}
	if err := c.CancelOperation(ctx, 2, start.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelling another owner's operation returned %v, want ErrNotFound", err)
	}
	ops, err := c.ListOperations(ctx, 2, 10)
	if err != nil {
		t.Fatalf("listing another owner's operations: %v", err)
	}
	if len(ops) != 0 {
		t.Fatalf("another owner saw %d operations, want none", len(ops))
	}
}

func TestAMissingOperationIdIsNotFound(t *testing.T) {
	c, _, _, _, _, _ := twoShares(t)
	ctx := context.Background()

	if _, err := c.Operation(ctx, 1, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reading a missing operation returned %v, want ErrNotFound", err)
	}
	if err := c.CancelOperation(ctx, 1, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelling a missing operation returned %v, want ErrNotFound", err)
	}
}

func TestTheUnfinishedSplitIsReadOnlyOnceAnOperationStopped(t *testing.T) {
	c, st, _, _, _, _ := twoShares(t)
	ctx := context.Background()

	id, err := st.CreateOp(ctx, 1, state.OpCopy, 2, 0, []string{"first", "second"})
	if err != nil {
		t.Fatalf("creating an operation: %v", err)
	}
	if err := st.StartOpItem(ctx, id, 0); err != nil {
		t.Fatalf("marking the first item started: %v", err)
	}

	// A running operation has outstanding items by definition, so the split
	// carries nothing until it has stopped.
	running, err := c.Operation(ctx, 1, OperationID(id))
	if err != nil {
		t.Fatalf("reading the running operation: %v", err)
	}
	if len(running.Attempting) != 0 || len(running.Pending) != 0 {
		t.Fatalf("a running operation reported %+v, want an empty split", running)
	}

	if err := st.InterruptOp(ctx, id, 0); err != nil {
		t.Fatalf("interrupting: %v", err)
	}
	stopped, err := c.Operation(ctx, 1, OperationID(id))
	if err != nil {
		t.Fatalf("reading the interrupted operation: %v", err)
	}
	// Whether the started item landed is genuinely unknown, so it is
	// reported apart from the one nothing touched.
	if len(stopped.Attempting) != 1 || stopped.Attempting[0] != "first" {
		t.Fatalf("Attempting is %+v, want the started item", stopped.Attempting)
	}
	if len(stopped.Pending) != 1 || stopped.Pending[0] != "second" {
		t.Fatalf("Pending is %+v, want the untouched item", stopped.Pending)
	}
}

func TestListingCarriesNoResultsAndSplitsOnlyInterruptedRows(t *testing.T) {
	c, st, _, _, _, _ := twoShares(t)
	ctx := context.Background()

	// The listing is the tray's view: running rows to draw progress for, and
	// interrupted ones to offer a re-attach. A finished row is not in it.
	running, err := st.CreateOp(ctx, 1, state.OpCopy, 1, 0, []string{"in flight"})
	if err != nil {
		t.Fatalf("creating the running operation: %v", err)
	}

	interrupted, err := st.CreateOp(ctx, 1, state.OpCopy, 1, 0, []string{"never reached"})
	if err != nil {
		t.Fatalf("creating the interrupted operation: %v", err)
	}
	if err := st.InterruptOp(ctx, interrupted, 0); err != nil {
		t.Fatalf("interrupting: %v", err)
	}

	ops, err := c.ListOperations(ctx, 1, 10)
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	var sawRunning, sawInterrupted bool
	for _, op := range ops {
		// A listing shows progress rather than outcomes, so per-item results
		// stay unread however the row ended.
		if len(op.Results) != 0 {
			t.Fatalf("the listing carried %d results for operation %d", len(op.Results), op.ID)
		}
		switch op.ID {
		case OperationID(running):
			sawRunning = true
			if len(op.Pending) != 0 || len(op.Attempting) != 0 {
				t.Fatalf("a running row carried the unfinished split: %+v", op)
			}
		case OperationID(interrupted):
			sawInterrupted = true
			// The tray's re-attach needs something to redo, so an
			// interrupted row is the one the split is read for.
			if len(op.Pending) != 1 || op.Pending[0] != "never reached" {
				t.Fatalf("the interrupted row carried Pending %+v", op.Pending)
			}
		}
	}
	if !sawRunning || !sawInterrupted {
		t.Fatalf("the listing missed a row: running=%v interrupted=%v", sawRunning, sawInterrupted)
	}
}

func TestStartCopyWalksADirectorySource(t *testing.T) {
	c, _, srcHost, dstHost, src, dst := twoShares(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(srcHost, "tree/inner"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, srcHost, "tree/top.txt", "top")
	writeFile(t, srcHost, "tree/inner/leaf.txt", "leaf")

	// The zero-stat regression: an empty stat once said "not a directory"
	// about every directory, so a recursive copy took the single-file path
	// and produced nothing after answering the caller that it had started.
	start, err := c.StartCopy(ctx, 1, at(t, src, "tree"), at(t, dst, "tree"), ConflictFail)
	if err != nil {
		t.Fatalf("StartCopy: %v", err)
	}
	if !start.Started {
		t.Fatal("StartCopy reported no job for a real copy")
	}
	op := waitForOp(t, c, 1, start.ID)
	if op.State != state.OpDone {
		t.Fatalf("the copy ended in state %d with %q, want done", op.State, op.Message)
	}
	if op.Progress != 1 {
		t.Fatalf("the finished copy reports progress %d, want 1", op.Progress)
	}
	if len(op.Results) != 1 || !op.Results[0].OK || op.Results[0].Reason != state.ReasonItemOk {
		t.Fatalf("the finished copy recorded %+v, want one OK result", op.Results)
	}
	if got := readHost(t, dstHost, "tree/inner/leaf.txt"); got != "leaf" {
		t.Fatalf("the copied leaf holds %q", got)
	}
}

func TestStartCopyChecksTheDestinationBeforeCreatingARow(t *testing.T) {
	ctx := context.Background()

	t.Run("fail", func(t *testing.T) {
		c, _, srcHost, dstHost, src, dst := twoShares(t)
		writeFile(t, srcHost, "note.txt", "source")
		writeFile(t, dstHost, "note.txt", "taken")

		_, err := c.StartCopy(ctx, 1, at(t, src, "note.txt"), at(t, dst, "note.txt"), ConflictFail)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("a taken destination returned %v, want ErrConflict", err)
		}
		// The refusal is immediate, so no job exists to report it later.
		ops, lerr := c.ListOperations(ctx, 1, 10)
		if lerr != nil {
			t.Fatalf("ListOperations: %v", lerr)
		}
		if len(ops) != 0 {
			t.Fatalf("a refused copy created %d rows, want none", len(ops))
		}
	})

	t.Run("skip", func(t *testing.T) {
		c, _, srcHost, dstHost, src, dst := twoShares(t)
		writeFile(t, srcHost, "note.txt", "source")
		writeFile(t, dstHost, "note.txt", "taken")

		start, err := c.StartCopy(ctx, 1, at(t, src, "note.txt"), at(t, dst, "note.txt"), ConflictSkip)
		if err != nil {
			t.Fatalf("the skip policy: %v", err)
		}
		if !start.Skipped || start.Started {
			t.Fatalf("the skip returned %+v, want Skipped with no job", start)
		}
		if got := readHost(t, dstHost, "note.txt"); got != "taken" {
			t.Fatalf("the skip wrote %q", got)
		}
	})

	t.Run("rename", func(t *testing.T) {
		c, _, srcHost, dstHost, src, dst := twoShares(t)
		writeFile(t, srcHost, "note.txt", "source")
		writeFile(t, dstHost, "note.txt", "taken")

		start, err := c.StartCopy(ctx, 1, at(t, src, "note.txt"), at(t, dst, "note.txt"), ConflictRename)
		if err != nil {
			t.Fatalf("the rename policy: %v", err)
		}
		if start.Dest.path.Name() != "note (2).txt" {
			t.Fatalf("the copy will land at %q, want \"note (2).txt\"", start.Dest.path.Name())
		}
		waitForOp(t, c, 1, start.ID)
		if got := readHost(t, dstHost, "note (2).txt"); got != "source" {
			t.Fatalf("the renamed copy holds %q", got)
		}
		if got := readHost(t, dstHost, "note.txt"); got != "taken" {
			t.Fatal("the rename overwrote the original destination")
		}
	})

	t.Run("overwrite a directory", func(t *testing.T) {
		c, _, srcHost, dstHost, src, dst := twoShares(t)
		if err := os.MkdirAll(filepath.Join(srcHost, "tree"), 0o755); err != nil {
			t.Fatalf("building the source: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(dstHost, "tree"), 0o755); err != nil {
			t.Fatalf("building the destination: %v", err)
		}
		writeFile(t, srcHost, "tree/kept.txt", "from source")
		writeFile(t, dstHost, "tree/stale.txt", "only in the destination")

		start, err := c.StartCopy(ctx, 1, at(t, src, "tree"), at(t, dst, "tree"), ConflictOverwrite)
		if err != nil {
			t.Fatalf("the overwrite policy: %v", err)
		}
		if op := waitForOp(t, c, 1, start.ID); op.State != state.OpDone {
			t.Fatalf("the overwriting copy ended in state %d with %q", op.State, op.Message)
		}
		// Copying into an existing directory merges the two, so a member the
		// destination had and the source does not must not survive.
		if _, serr := os.Stat(filepath.Join(dstHost, "tree/stale.txt")); !errors.Is(serr, os.ErrNotExist) {
			t.Fatal("the overwrite merged rather than replaced")
		}
		if got := readHost(t, dstHost, "tree/kept.txt"); got != "from source" {
			t.Fatalf("the replaced directory holds %q", got)
		}
	})
}

func TestStartCopyRefusesASelfDescendantDestination(t *testing.T) {
	c, _, srcHost, _, src, _ := twoShares(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(srcHost, "tree/inner"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}

	_, err := c.StartCopy(ctx, 1, at(t, src, "tree"), at(t, src, "tree/inner/copy"), ConflictFail)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("copying into its own subtree returned %v, want ErrDenied", err)
	}
}

func TestAFailedCopyRecordsATypedResultRow(t *testing.T) {
	c, _, srcHost, dstHost, src, dst := twoShares(t)
	ctx := context.Background()
	writeFile(t, srcHost, "note.txt", "body")
	// A read-only destination parent is what makes the durable write fail
	// after the row already exists.
	if err := os.Chmod(dstHost, 0o555); err != nil {
		t.Fatalf("sealing the destination: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dstHost, 0o755); err != nil {
			t.Errorf("unsealing the destination: %v", err)
		}
	})

	start, err := c.StartCopy(ctx, 1, at(t, src, "note.txt"), at(t, dst, "note.txt"), ConflictFail)
	if err != nil {
		t.Fatalf("StartCopy: %v", err)
	}
	op := waitForOp(t, c, 1, start.ID)
	if op.State != state.OpFailed {
		t.Fatalf("a failing copy ended in state %d, want failed", op.State)
	}
	if op.Message == "" {
		t.Fatal("a failed copy recorded no message")
	}
	// An item with no result row is read as one the runner never got to, so
	// a failure has to leave one behind.
	if len(op.Results) != 1 || op.Results[0].OK {
		t.Fatalf("a failed copy recorded %+v, want one failing result", op.Results)
	}
}

func TestOpReasonForClassifiesEverySentinelAClientBranchesOn(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want state.OpResultReason
	}{
		{ErrConflict, state.ReasonItemConflict},
		{ErrExists, state.ReasonItemConflict},
		{ErrNotFound, state.ReasonItemNotFound},
		{ErrDenied, state.ReasonItemDenied},
		{errors.New("a disk that went away"), state.ReasonItemFailed},
		// Wrapped, because every refusal the domain returns arrives through
		// errf rather than as a bare sentinel.
		{errf(ErrConflict, "the destination is taken"), state.ReasonItemConflict},
	} {
		if got := opReasonFor(tc.err); got != tc.want {
			t.Fatalf("opReasonFor(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

func TestACancelledCopyStopsAndRecordsNoResult(t *testing.T) {
	c, st, srcHost, dstHost, src, dst := twoShares(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(srcHost, "tree"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	writeFile(t, srcHost, "tree/a.txt", "content")

	// The runner is driven directly with the row already marked, rather than
	// racing a cancel against a copy that may finish first. That race is
	// real but it is the scheduler's, not this rule's: what is under test is
	// that a runner whose gate answers true stops and records no outcome.
	id, err := st.CreateOp(ctx, 1, state.OpCopy, 1, c.clk.Nanos(), []string{"tree"})
	if err != nil {
		t.Fatalf("creating the operation: %v", err)
	}
	if err := c.CancelOperation(ctx, 1, OperationID(id)); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}

	from := at(t, src, "tree")
	srcSt, err := from.root.Stat(from.path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	c.runCopy(ctx, id, from, at(t, dst, "tree"), srcSt)

	op, err := c.Operation(ctx, 1, OperationID(id))
	if err != nil {
		t.Fatalf("reading the cancelled operation: %v", err)
	}
	if op.State != state.OpCancelled {
		t.Fatalf("a cancelled copy ended in state %d, want cancelled", op.State)
	}
	// The one deliberate exception to the result-row rule: what was written
	// stays and nothing undoes it, so the item is genuinely in an unknown
	// state and recording no outcome is the honest answer.
	if len(op.Results) != 0 {
		t.Fatalf("a cancelled copy recorded %+v, want no result rows", op.Results)
	}
	// The gate is polled at the top of every call, so a cancel already
	// standing when the walk begins stops it before the first item.
	if _, serr := os.Stat(filepath.Join(dstHost, "tree")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatal("the walk wrote past a cancellation that was already standing")
	}
}

func TestACancelMidWalkStopsAtTheNextItemBoundary(t *testing.T) {
	c, _, srcHost, dstHost, src, dst := twoShares(t)
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(srcHost, "tree"), 0o755); err != nil {
		t.Fatalf("building the tree: %v", err)
	}
	for _, name := range []string{"a", "b", "c", "d"} {
		writeFile(t, srcHost, "tree/"+name+".txt", "content")
	}

	// A gate that admits the directory and the first file, then reports the
	// cancel. The poll is once per item, so the walk must stop at the next
	// boundary rather than partway through a file or at the very end.
	polls := 0
	gate := func() bool {
		polls++
		return polls > 2
	}

	from := at(t, src, "tree")
	srcSt, err := from.root.Stat(from.path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	cerr := c.copyRecursive(ctx, from, at(t, dst, "tree"), srcSt, gate)
	if !errors.Is(cerr, errOpCancelled) {
		t.Fatalf("the walk returned %v, want errOpCancelled", cerr)
	}

	// One file landed whole before the stop; nothing is half-written,
	// because the poll never happens inside a file.
	entries, rerr := os.ReadDir(filepath.Join(dstHost, "tree"))
	if rerr != nil {
		t.Fatalf("reading the partial copy: %v", rerr)
	}
	if len(entries) != 1 {
		t.Fatalf("the stopped walk left %d entries, want the one item it finished", len(entries))
	}
	if got := readHost(t, dstHost, "tree/"+entries[0].Name()); got != "content" {
		t.Fatalf("the finished item holds %q, want a whole file", got)
	}
}

func TestACopyOutlivesTheRequestThatStartedIt(t *testing.T) {
	c, _, srcHost, dstHost, src, dst := twoShares(t)
	writeFile(t, srcHost, "note.txt", "body")

	// The request's context ends when the response is written; cancelling the
	// work on client disconnect is exactly the bug the detachment avoids.
	reqCtx, cancel := context.WithCancel(context.Background())
	start, err := c.StartCopy(reqCtx, 1, at(t, src, "note.txt"), at(t, dst, "note.txt"), ConflictFail)
	if err != nil {
		t.Fatalf("StartCopy: %v", err)
	}
	cancel()

	if op := waitForOp(t, c, 1, start.ID); op.State != state.OpDone {
		t.Fatalf("the detached copy ended in state %d with %q, want done", op.State, op.Message)
	}
	if got := readHost(t, dstHost, "note.txt"); got != "body" {
		t.Fatalf("the detached copy landed %q", got)
	}
}
