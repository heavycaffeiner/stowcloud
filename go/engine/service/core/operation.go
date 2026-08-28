//go:build linux

package core

import (
	"context"
	"errors"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/task"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
)

// OperationID is what a client reattaches to a long operation with.
type OperationID int64

// Operation is one long-running job as a client sees it.
type Operation struct {
	ID    OperationID
	Kind  state.OpKind
	State state.OpState

	// Progress and Total form the item counter behind a progress bar. Total
	// stays zero while the size remains unknown until the walk completes.
	Progress int64
	Total    int64

	// Message is the failure line the runner recorded; empty while running
	// or on success.
	Message string

	// Results is the bounded per-item outcome set, present once the
	// operation is terminal. Nothing streams during the run.
	Results []state.OpResult

	// Attempting is what the runner had started and never recorded an
	// outcome for, so it is only non-empty for an operation the process died
	// during. Whether the item landed is genuinely unknown, which is why it
	// is reported apart from the ones nothing touched.
	Attempting []string

	// Pending is what the operation was asked for and never reached.
	// Untouched, so re-running exactly these is safe, which is what lets a
	// client offer them as a to-do list rather than only a count.
	Pending []string
}

// errOpCancelled ends a walk because the row was marked. It never reaches a
// client; the row's own OpCancelled state is what says the job was cancelled.
var errOpCancelled = errors.New("core: the operation was cancelled")

// Operation reads one job, scoped to its owner.
//
// A row owned by somebody else and a row that does not exist are the same
// answer, so an id-probing client learns nothing. This is the existence rule
// the resolve gate applies to paths, applied to operation ids.
func (c *Core) Operation(ctx context.Context, owner UserID, id OperationID) (Operation, error) {
	row, results, err := c.state.GetOp(ctx, int64(id))
	if err != nil {
		if errors.Is(err, state.ErrNoSuchOp) {
			return Operation{}, ErrNotFound
		}
		return Operation{}, err
	}
	if row.User != int64(owner) {
		return Operation{}, ErrNotFound
	}

	op := operationOf(row, results)
	// A running operation has outstanding items by definition, so the split
	// is only interesting once it has stopped.
	if row.State != state.OpRunning {
		attempting, pending, uerr := c.state.UnfinishedOpItems(ctx, row.ID)
		if uerr != nil {
			return Operation{}, uerr
		}
		op.Attempting, op.Pending = attempting, pending
	}
	return op, nil
}

// CancelOperation marks the row so a running copy stops at its next item
// boundary.
//
// This is the only way to cancel: disconnecting the request that created the
// operation does nothing, by design.
func (c *Core) CancelOperation(ctx context.Context, owner UserID, id OperationID) error {
	row, _, err := c.state.GetOp(ctx, int64(id))
	if err != nil {
		if errors.Is(err, state.ErrNoSuchOp) {
			return ErrNotFound
		}
		return err
	}
	if row.User != int64(owner) {
		return ErrNotFound
	}
	return c.state.RequestOpCancel(ctx, int64(id))
}

// ListOperations reads an owner's recent jobs, newest first.
//
// Per-item results are deliberately not read: they are bounded per operation,
// a listing would multiply that by the page, and the screen this feeds shows
// progress rather than outcomes. The unfinished split is read for interrupted
// rows alone, because the listing feeds the tray's re-attach after a restart
// and a row saying only "interrupted" gives nobody anything to redo.
func (c *Core) ListOperations(ctx context.Context, owner UserID, limit int) ([]Operation, error) {
	rows, err := c.state.ListOps(ctx, int64(owner), limit)
	if err != nil {
		return nil, err
	}
	out := make([]Operation, 0, len(rows))
	for _, row := range rows {
		op := operationOf(row, nil)
		if row.State == state.OpInterrupted {
			attempting, pending, uerr := c.state.UnfinishedOpItems(ctx, row.ID)
			if uerr != nil {
				return nil, uerr
			}
			op.Attempting, op.Pending = attempting, pending
		}
		out = append(out, op)
	}
	return out, nil
}

// operationOf projects a stored row into the domain value.
func operationOf(row state.Op, results []state.OpResult) Operation {
	return Operation{
		ID:       OperationID(row.ID),
		Kind:     row.Kind,
		State:    row.State,
		Progress: row.Progress,
		Total:    row.Total,
		Message:  row.Message,
		Results:  results,
	}
}

// CopyStart is what a caller polls a started copy with.
type CopyStart struct {
	ID OperationID

	// Dest is where the copy will land, which differs from the request under
	// ConflictRename.
	Dest Resolved

	// Started says a job exists to poll. False for a skip.
	Started bool

	// Skipped is a field rather than an error: the destination was taken and
	// the caller asked for it to be left alone, which is a completed request
	// with no job behind it.
	Skipped bool
}

// StartCopy checks a copy synchronously and then runs it on a detached
// goroutine.
//
// Everything that can refuse the request happens before any row exists, so a
// taken name is a refusal the caller can act on immediately rather than a job
// that reports the conflict minutes later.
func (c *Core) StartCopy(
	ctx context.Context, owner UserID, from, to Resolved, policy OnConflict,
) (CopyStart, error) {
	// The source's own stat, never a zero value: copyRecursive branches on it
	// to decide whether it is walking a tree or copying one file. An empty
	// stat once said "not a directory" about every directory, so a recursive
	// COPY took the single-file path, failed, and the caller had already been
	// answered 202.
	st, err := from.root.Stat(from.path)
	if err != nil {
		return CopyStart{}, mapVFSErr(err)
	}
	if err = RefuseSelfDescendant(from, to); err != nil {
		return CopyStart{}, err
	}

	dest, _, done, err := c.applyConflict(ctx, to, policy, nil)
	if err != nil {
		return CopyStart{}, err
	}
	if done {
		return CopyStart{Dest: dest, Skipped: true}, nil
	}

	// A single named item, so a copy interrupted partway can report what it was
	// processing instead of merely that it stopped.
	id, err := c.state.CreateOp(ctx, int64(owner), state.OpCopy, 1,
		c.clk.Nanos(), []string{dest.path.String()})
	if err != nil {
		return CopyStart{}, err
	}

	// The request's context ends when the response is written and this work
	// outlives it by design. Cancelling on client disconnect is exactly the
	// bug this detachment avoids; the caller polls the row for the result.
	runCtx := context.WithoutCancel(ctx)
	task.Go(runCtx, "core: long copy", func() {
		c.runCopy(runCtx, id, from, dest, st)
	})
	return CopyStart{ID: OperationID(id), Dest: dest, Started: true}, nil
}

// runCopy is the detached half of StartCopy.
//
// Every terminal path writes the item's result row, because an item with no
// result row is read as one the runner never got to: a finished copy that
// recorded nothing would report itself done and list its own file as never
// reached. FinishOp errors are ignored throughout, since the copy's own
// outcome is already the answer and the row is best-effort bookkeeping.
func (c *Core) runCopy(ctx context.Context, id int64, from, to Resolved, st vfs.Stat) {
	if err := c.state.StartOpItem(ctx, id, 0); err != nil {
		c.warn("marking a copy's item as started failed; the copy runs anyway",
			"operation", id, "error", err)
	}

	err := c.copyRecursive(ctx, from, to, st, c.cancelGate(ctx, id))
	now := c.clk.Nanos()
	path := to.path.String()

	switch {
	case errors.Is(err, errOpCancelled):
		// The one deliberate exception to the result-row rule: what was
		// written stays and nothing undoes it, so the item is genuinely in an
		// unknown state and recording no outcome is the honest answer.
		c.finish(ctx, id, state.OpCancelled, 0, "", now, nil)
	case err != nil:
		results := []state.OpResult{{
			Operation: id, Idx: 0, Path: path,
			Reason: opReasonFor(err), Text: err.Error(),
		}}
		c.finish(ctx, id, state.OpFailed, 0, err.Error(), now, results)
	default:
		results := []state.OpResult{{
			Operation: id, Idx: 0, Path: path,
			OK: true, Reason: state.ReasonItemOk,
		}}
		c.finish(ctx, id, state.OpDone, 1, "", now, results)
	}
}

// finish writes an operation's terminal row, logging rather than returning a
// bookkeeping failure.
func (c *Core) finish(
	ctx context.Context, id int64, st state.OpState, progress int64,
	message string, nowNs int64, results []state.OpResult,
) {
	if err := c.state.FinishOp(ctx, id, st, progress, message, nowNs, results); err != nil {
		c.warn("recording a copy's outcome failed; the copy itself is done",
			"operation", id, "error", err)
	}
}

// cancelGate returns the closure copyRecursive polls at item boundaries.
//
// Reading the row is what makes a cancel reach a copy that is already
// running rather than only stopping one that has not started; without it the
// request was recorded and the walk ran to the end. A row that cannot be read
// answers false: the work is real and the bookkeeping is what failed, so a
// store hiccup must not abort a copy.
func (c *Core) cancelGate(ctx context.Context, id int64) func() bool {
	return func() bool {
		row, _, err := c.state.GetOp(ctx, id)
		if err != nil {
			return false
		}
		return row.Cancellation
	}
}

// opReasonFor classifies a runner failure into the stored reason a client
// branches on. The tray opens a conflict dialogue for a conflict and shows a
// message for the rest, so a copy that failed on a taken name has to arrive
// as a typed conflict rather than as prose.
func opReasonFor(err error) state.OpResultReason {
	switch {
	case errors.Is(err, ErrConflict), errors.Is(err, ErrExists):
		return state.ReasonItemConflict
	case errors.Is(err, ErrNotFound):
		return state.ReasonItemNotFound
	case errors.Is(err, ErrDenied):
		return state.ReasonItemDenied
	default:
		return state.ReasonItemFailed
	}
}
