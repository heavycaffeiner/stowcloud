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
	// Results is the bounded per-item outcome, present once the operation is
	// terminal. Nothing streams during the run.
	Results []state.OpResult
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
	return Operation{ID: OperationID(op.ID), Kind: op.Kind, State: op.State, Results: results}, nil
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
func (c *Core) StartCopy(ctx context.Context, owner UserID, from, to Resolved) (int64, error) {
	// The source's own stat, not a zero value. copyRecursive branches on it to
	// decide whether it is walking a tree or copying one file, so handing it an
	// empty one said "not a directory" about every directory: the copy took the
	// single-file path, failed on a source that is a directory, and the caller
	// had already been answered 202. A recursive COPY over WebDAV produced
	// nothing at all, with a status saying it had started.
	st, serr := from.root.Stat(from.path)
	if serr != nil {
		return 0, mapVFSErr(serr)
	}

	id, err := c.state.CreateOp(ctx, int64(owner), state.OpCopy, 1, c.clk.Nanos())
	if err != nil {
		return 0, err
	}
	// The request's context ends when the response is written and this outlives
	// it by design, so the work takes a detached one: the caller polls the
	// operation row for the result.
	ctx = context.WithoutCancel(ctx)
	task.Go(ctx, "core: long copy", func() {
		now := c.clk.Nanos()
		if cerr := c.copyRecursive(ctx, from, to, st); cerr != nil {
			_ = c.state.FinishOp(ctx, id, state.OpFailed, 0, cerr.Error(), now, nil) //nolint:errcheck // the failure is already the answer; the row is best-effort.
			return
		}
		_ = c.state.FinishOp(ctx, id, state.OpDone, 1, "", now, nil) //nolint:errcheck // the row is best-effort beside the copy's own result.
	})
	return id, nil
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
		out = append(out, Operation{ID: OperationID(op.ID), Kind: op.Kind, State: op.State})
	}
	return out, nil
}
