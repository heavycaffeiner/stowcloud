//go:build linux

package core

import (
	"context"
	"errors"

	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/task"
)

// Long operations: recursive copy, delete or archive that outlives the request
// that started it. The operation gets an id, progress is readable, and a client
// that refreshes the tab reattaches.
//
// In Go this is a task.Go with a context that is not the request's: cancelling
// on client disconnect is exactly the bug. The operation's own cancellation is
// explicit, a separate API call that marks the row; the running task observes
// it through its own context and stops at the next item boundary.

// OperationID is a long operation's external identity.
type OperationID int64

// Operation is one long operation, as a client sees it.
type Operation struct {
	ID    OperationID
	Kind  state.OpKind
	State state.OpState
	// Progress and Total are the item counter a progress bar is drawn from.
	// Total is zero for an operation whose size is not known until its walk
	// ends.
	Progress int64
	Total    int64
	// Message is the failure line the runner recorded, empty while the
	// operation is running or when it succeeded.
	Message string
	// Results is the bounded per-item outcome, present once the operation is
	// terminal. Nothing streams during the run.
	Results []state.OpResult
	// Attempting is what the runner had started and never recorded an outcome
	// for, which is only non-empty for an operation the process died during.
	// Whether the item landed is genuinely unknown, so it is reported apart
	// from the ones nothing touched.
	Attempting []string
	// Pending is what the operation was asked for and never reached. Untouched,
	// so re-running exactly these is safe, which is what lets a client offer
	// them as a to-do list rather than only a count.
	Pending []string
}

// Operation returns a stored operation's restart-visible state and its results.
func (c *Core) Operation(ctx context.Context, owner UserID, id OperationID) (Operation, error) {
	op, results, err := c.state.GetOp(ctx, int64(id))
	if errors.Is(err, state.ErrNoSuchOp) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, err
	}
	// Scoped to its owner: another user's operation id answers NotFound, so an
	// id-probing client learns nothing.
	if op.User != int64(owner) {
		return Operation{}, ErrNotFound
	}
	// What it never finished, which is only ever interesting once it has
	// stopped: a running operation has outstanding items by definition.
	var attempting, pending []string
	if op.State != state.OpRunning {
		attempting, pending, err = c.state.UnfinishedOpItems(ctx, int64(id))
		if err != nil {
			return Operation{}, err
		}
	}

	return Operation{
		ID: OperationID(op.ID), Kind: op.Kind, State: op.State,
		Progress: op.Progress, Total: op.Total, Message: op.Message,
		Results: results, Attempting: attempting, Pending: pending,
	}, nil
}

// CancelOperation requests a running operation's cancellation. It goes through
// the operation's own context; disconnecting the request that created it does
// not.
func (c *Core) CancelOperation(ctx context.Context, owner UserID, id OperationID) error {
	op, _, err := c.state.GetOp(ctx, int64(id))
	if errors.Is(err, state.ErrNoSuchOp) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if op.User != int64(owner) {
		return ErrNotFound
	}
	return c.state.RequestOpCancel(ctx, int64(id))
}

// StartCopy is the long form of a recursive copy, bound by an operation row.
// The protocol layer calls it, and the work runs on a goroutine this package
// started through task.Go, which is the only legal spawn in the tree.
//
// The destination is checked before the row is created, so a name already
// taken is a refusal the caller can act on rather than a job that reports the
// conflict minutes later. It used to check nothing at all: copyFile replaced
// whatever was there, so "duplicate" wrote a file over itself and the conflict
// dialogue the client draws could never open.
//
// vpath is the caller's own name for the destination, used to re-resolve it
// when a rename picks a different leaf. It is passed in because this package
// does not parse virtual paths.
func (c *Core) StartCopy(
	ctx context.Context, owner UserID, from, to Resolved, policy OnConflict,
) (CopyStart, error) {
	// The source's own stat, not a zero value. copyRecursive branches on it to
	// decide whether it is walking a tree or copying one file, so handing it an
	// empty one said "not a directory" about every directory: the copy took the
	// single-file path, failed on a source that is a directory, and the caller
	// had already been answered 202. A recursive COPY over WebDAV produced
	// nothing at all, with a status saying it had started.
	st, serr := from.root.Stat(from.path)
	if serr != nil {
		return CopyStart{}, mapVFSErr(serr)
	}
	// A copy onto itself or into its own subtree is a walk that does not
	// terminate: each pass copies what the previous one wrote.
	if err := RefuseSelfDescendant(from, to); err != nil {
		return CopyStart{}, err
	}

	exists, eerr := pathExists(to.root, to.path)
	if eerr != nil {
		return CopyStart{}, eerr
	}
	if exists {
		switch policy {
		case ConflictFail:
			return CopyStart{}, ErrConflict
		case ConflictSkip:
			return CopyStart{Dest: to, Skipped: true}, nil
		case ConflictRename:
			free, ferr := c.uniqueSiblingName(to.root, to.path)
			if ferr != nil {
				return CopyStart{}, ferr
			}
			to.path = free
		case ConflictOverwrite:
			// A directory is removed first, for the same reason an overwriting
			// move removes one: copying into it merges the two, so a member the
			// destination had and the source does not survives a copy that was
			// supposed to replace it. A file is replaced by the durable write
			// itself, which leaves no window with neither version present.
			dstSt, dserr := to.root.Stat(to.path)
			if dserr != nil {
				return CopyStart{}, mapVFSErr(dserr)
			}
			if dstSt.Kind.IsDir() {
				if derr := c.deleteResolved(ctx, to, dstSt, false); derr != nil {
					return CopyStart{}, derr
				}
			}
		}
	}

	// One item, named, so a copy interrupted mid-run can say what it was on
	// rather than only that it did not finish.
	id, err := c.state.CreateOp(ctx, int64(owner), state.OpCopy, 1, c.clk.Nanos(),
		[]string{to.path.String()})
	if err != nil {
		return CopyStart{}, err
	}
	// The request's context ends when the response is written and this outlives
	// it by design, so the work takes a detached one: the caller polls the
	// operation row for the result.
	ctx = context.WithoutCancel(ctx)
	dest := to
	task.Go(ctx, "core: long copy", func() {
		if serr := c.state.StartOpItem(ctx, id, 0); serr != nil {
			c.warn("an operation item could not be marked as started", "error", serr)
		}
		cerr := c.copyRecursive(ctx, from, dest, st, c.cancelGate(ctx, id))
		now := c.clk.Nanos()
		// Every terminal path writes the item's result. An item with none is
		// read as one the runner never got to, which is what a client is shown
		// as still outstanding: a finished copy that recorded nothing would
		// report itself as done and list its own file as never reached.
		//
		// A cancel is the exception and deliberately leaves none: what was
		// written stays and nothing here undoes it, so the file is genuinely in
		// an unknown state and saying so is the honest answer.
		path := dest.path.String()
		switch {
		case errors.Is(cerr, errOpCancelled):
			_ = c.state.FinishOp(ctx, id, state.OpCancelled, 0, "", now, nil) //nolint:errcheck // the row is best-effort.
		case cerr != nil:
			results := []state.OpResult{{
				Operation: id, Path: path,
				Reason: opReasonFor(cerr), Text: cerr.Error(),
			}}
			_ = c.state.FinishOp(ctx, id, state.OpFailed, 0, cerr.Error(), now, results) //nolint:errcheck // the failure is already the answer; the row is best-effort.
		default:
			results := []state.OpResult{{
				Operation: id, Path: path, OK: true, Reason: state.ReasonItemOk,
			}}
			_ = c.state.FinishOp(ctx, id, state.OpDone, 1, "", now, results) //nolint:errcheck // the row is best-effort beside the copy's own result.
		}
	})
	return CopyStart{ID: OperationID(id), Dest: dest, Started: true}, nil
}

// errOpCancelled ends a walk because the operation row was marked. It never
// reaches a client: the row's own state is what says the job was cancelled.
var errOpCancelled = errors.New("core: the operation was cancelled")

// cancelGate reads the cancellation flag off the operation row, so a cancel
// reaches a copy that is already running rather than only stopping one that has
// not started. Without it the request was recorded and the walk ran to the end.
//
// Polled at directory and file boundaries rather than continuously: the row is
// a database read, and one per item is cheap beside copying the item.
func (c *Core) cancelGate(ctx context.Context, id int64) func() bool {
	return func() bool {
		op, _, err := c.state.GetOp(ctx, id)
		if err != nil {
			// A row that cannot be read is not a reason to stop copying: the
			// work is real and the bookkeeping is what failed.
			return false
		}
		return op.Cancellation
	}
}

// CopyStart is what starting a copy answered.
//
// Skipped is its own field rather than an error because nothing went wrong: the
// destination was taken and the caller asked for it to be left alone, which is
// a completed request with no job behind it.
type CopyStart struct {
	ID OperationID
	// Dest is where the copy will land, which is not the requested path when
	// the conflict policy renamed it.
	Dest Resolved
	// Started reports a job exists to poll. False for a skip.
	Started bool
	Skipped bool
}

// opReasonFor classifies a runner's failure into the stored reason a client
// branches on. The tray opens a conflict dialogue for one of these and shows a
// message for the rest, so a copy that failed on a taken name has to arrive as
// a conflict rather than as prose.
func opReasonFor(err error) state.OpResultReason {
	switch {
	case errors.Is(err, ErrConflict), errors.Is(err, ErrExists):
		return state.ReasonItemConflict
	case errors.Is(err, ErrNotFound):
		return state.ReasonItemNotFound
	case errors.Is(err, ErrDenied):
		return state.ReasonItemDenied
	}
	return state.ReasonItemFailed
}

// RefuseSelfDescendant refuses a transfer whose destination is the source or
// sits inside it.
//
// Without it a directory copied into its own subtree is a walk that does not
// terminate: each pass copies what the previous one wrote, and the operation
// runs until the disk is full. RFC 4918 9.8.4 and 9.9.4 make it a 403 for
// WebDAV, and the native surface wants the same answer for the same reason.
//
// Compared component-wise on the resolved paths, never on the request strings:
// a destination is inside the source only when every component of the source is
// a prefix of it, and comparing text would make "/a/bc" look like a child of
// "/a/b".
func RefuseSelfDescendant(from, to Resolved) error {
	if from.share != to.share {
		return nil
	}
	src := from.path.Components()
	dst := to.path.Components()
	if len(dst) < len(src) {
		return nil
	}
	for i, comp := range src {
		if dst[i] != comp {
			return nil
		}
	}
	// Equal length is the destination being the source itself; longer is a
	// descendant of it.
	return errf(ErrDenied, "the destination is inside the source")
}

// ListOperations returns one account's operations, newest first.
//
// Scoped to the caller for the same reason a single one is: an operation list
// that showed somebody else's would name their paths.
func (c *Core) ListOperations(ctx context.Context, owner UserID, limit int) ([]Operation, error) {
	ops, err := c.state.ListOps(ctx, int64(owner), limit)
	if err != nil {
		return nil, err
	}
	out := make([]Operation, 0, len(ops))
	for _, op := range ops {
		// The per-item results are deliberately not read here. They are
		// bounded per operation and a listing would multiply that by the page,
		// and the screen this feeds shows progress rather than outcomes.
		one := Operation{
			ID: OperationID(op.ID), Kind: op.Kind, State: op.State,
			Progress: op.Progress, Total: op.Total, Message: op.Message,
		}
		// What an interrupted one left undone, which is the whole reason this
		// listing exists: it feeds the tray's re-attach after a restart, and a
		// row saying only "interrupted" gives nobody anything to redo. Read
		// for the interrupted rows alone, so the ordinary listing stays one
		// query.
		if op.State == state.OpInterrupted {
			attempting, pending, uerr := c.state.UnfinishedOpItems(ctx, op.ID)
			if uerr != nil {
				return nil, uerr
			}
			one.Attempting, one.Pending = attempting, pending
		}
		out = append(out, one)
	}
	return out, nil
}
