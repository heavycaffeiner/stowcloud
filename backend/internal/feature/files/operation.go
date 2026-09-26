//go:build linux

package core

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

// OperationID is what a client reattaches to a long operation with.
type OperationID int64

// Operation is one long-running job as a client sees it.
type Operation struct {
	ID           OperationID
	Kind         state.OpKind
	State        state.OpState
	Progress     int64
	Total        int64
	ProgressUnit string
	Attempt      int
	MaxAttempts  int
	NextRunNs    int64
	ErrorKey     string
	Message      string
	Results      []state.OpResult
	Attempting   []string
	Pending      []string
}

// KindName is the wire name of an operation's kind, and StateName of its
// state. They live here because the stored values are the persistence tier's
// numbers and the presentation tier may not import that tier to read them.
//
// Names rather than the numbers themselves: the numbers may only be appended
// to, and a client that learned them would make renaming or reordering one a
// wire break.
func (o Operation) KindName() string {
	switch o.Kind {
	case state.OpCopy:
		return "copy"
	case state.OpDelete:
		return "delete"
	case state.OpArchive:
		return "archive"
	case state.OpIndexBuild:
		return "index_build"
	default:
		return "unknown"
	}
}

// StateName is the wire name of the operation's state.
func (o Operation) StateName() string {
	switch o.State {
	case state.OpQueued:
		return "queued"
	case state.OpRunning:
		return "running"
	case state.OpPaused:
		return "paused"
	case state.OpRetrying:
		return "retrying"
	case state.OpDone:
		return "done"
	case state.OpFailed:
		return "failed"
	case state.OpCancelled:
		return "cancelled"
	case state.OpInterrupted:
		return "interrupted"
	default:
		return "unknown"
	}
}

// Terminal reports whether the operation has finished, in any way.
//
// The one place that decides. A client polls until this is true, and a list of
// terminal states written twice is how one of them is forgotten and a job
// polls forever.
func (o Operation) Terminal() bool {
	switch o.State {
	case state.OpDone, state.OpFailed, state.OpCancelled, state.OpInterrupted:
		return true
	case state.OpQueued, state.OpRunning, state.OpPaused, state.OpRetrying:
		return false
	default:
		return true
	}
}

// OperationStateNames lists every state name with whether it is terminal.
//
// Exported because the presentation tier decides terminality from the name,
// having no access to the stored numbers, and two lists of the same states is
// how one of them drifts. That tier checks its own answer against this.
func OperationStateNames() map[string]bool {
	return map[string]bool{
		"queued": false, "running": false, "paused": false, "retrying": false,
		"done": true, "failed": true, "cancelled": true, "interrupted": true,
	}
}

// OperationItem is one item's outcome.
type OperationItem struct {
	Index int64
	Path  string
	OK    bool
	// Reason is empty for an item that succeeded: that case is described by
	// OK, and naming it would invite a client to switch on the reason rather
	// than on the flag.
	Reason string
	Text   string
}

// Items projects the per-item results.
func (o Operation) Items() []OperationItem {
	out := make([]OperationItem, 0, len(o.Results))
	for _, r := range o.Results {
		out = append(out, OperationItem{
			Index:  r.Idx,
			Path:   r.Path,
			OK:     r.OK,
			Reason: opResultReasonName(r.Reason),
			Text:   r.Text,
		})
	}
	return out
}

// opResultReasonName is the wire name of an item's failure reason.
func opResultReasonName(r state.OpResultReason) string {
	switch r {
	case state.ReasonItemOk:
		return ""
	case state.ReasonItemFailed:
		return "failed"
	case state.ReasonItemDenied:
		return "denied"
	case state.ReasonItemNotFound:
		return "not_found"
	case state.ReasonItemConflict:
		return "conflict"
	case state.ReasonItemSkipped:
		return "skipped"
	default:
		return "unknown"
	}
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
		if row.State != state.OpRunning {
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

func operationOf(row state.Op, results []state.OpResult) Operation {
	return Operation{
		ID: OperationID(row.ID), Kind: row.Kind, State: row.State,
		Progress: row.Progress, Total: row.Total, ProgressUnit: row.ProgressUnit,
		Attempt: row.Attempt, MaxAttempts: row.MaxAttempts, NextRunNs: row.NextRunNs,
		ErrorKey: row.ErrorKey, Message: row.Message, Results: results,
	}
}

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
	// The source's own stat is required before the synchronous authority walk:
	// copy execution must never guess whether a target is a file or tree.
	st, err := from.root.Stat(from.path)
	if err != nil {
		return CopyStart{}, mapVFSErr(err)
	}

	dest, overwriting, done, err := c.applyConflict(ctx, to, policy, nil)
	if err != nil {
		return CopyStart{}, err
	}
	if done {
		return CopyStart{Dest: dest, Skipped: true}, nil
	}

	// Conflict selection is read-only. Check the component relationship before
	// any stage, quota booking, or operation row is created.
	if err = RefuseSelfDescendant(from, dest); err != nil {
		return CopyStart{}, err
	}
	if _, err = c.validateCopyTree(
		ctx, from, dest, st, acl.Read|acl.Download, overwriting,
	); err != nil {
		return CopyStart{}, err
	}
	payload, err := json.Marshal(copyJobPayload{
		Owner: int64(owner), FromShare: uint32(from.Share()), FromPath: from.Path().String(),
		ToShare: uint32(dest.Share()), ToPath: dest.Path().String(),
		SourceKind: uint8(st.Kind), Overwriting: overwriting,
	})
	if err != nil {
		return CopyStart{}, err
	}
	created := c.clk.Nanos()
	id, err := c.state.CreateQueuedOp(ctx, state.QueuedOp{
		User: int64(owner), Kind: state.OpCopy, Total: 1, CreatedNs: created,
		Payload: payload, ProgressUnit: "items", MaxAttempts: 4,
		NextRunNs: created, Paths: []string{dest.path.String()},
	})
	if err != nil {
		return CopyStart{}, err
	}
	if c.jobsStopped() {
		if err := c.state.InterruptOp(ctx, id, c.clk.Nanos()); err != nil {
			return CopyStart{}, err
		}
		return CopyStart{ID: OperationID(id), Dest: dest, Started: true}, nil
	}
	c.StartJobs()
	return CopyStart{ID: OperationID(id), Dest: dest, Started: true}, nil
}

// StopJobs tells the work a request started and left running to stop at its
// next item boundary.
//
// Called by a shutdown before DrainJobs. Without it the drain is bounded only
// by a clock, which leaves the case it exists for exactly where it started: a
// copy of a large tree outlives the wait and then writes into a closed
// database. Stopping first bounds the wait by one item instead.
func (c *Core) StopJobs() {
	if c.jobsStop != nil {
		c.jobsStop()
	}
}

// jobsStopped reports whether a shutdown asked the detached work to stop.
func (c *Core) jobsStopped() bool {
	return c.jobsCtx != nil && c.jobsCtx.Err() != nil
}

// DrainJobs waits for the work a request started and left running. When ctx
// ends first, the work is told to stop and waited for, so nothing writes into
// the databases the caller is about to close.
func (c *Core) DrainJobs(ctx context.Context) error {
	for {
		runnable, err := c.state.HasRunnableOp(ctx, c.clk.Nanos())
		if err != nil {
			return errors.Join(err, c.stopAndWait())
		}
		if !runnable && len(c.jobSlots) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), c.stopAndWait())
		case <-time.After(5 * time.Millisecond):
		}
	}
	c.StopJobs()
	return c.jobs.Wait(ctx)
}

// stopGrace bounds the wait after a stop: long enough for a copy to finish its
// current item and record the interruption, short enough to keep a restart
// from hanging on work that ignores the cancel.
const stopGrace = 5 * time.Second

// stopAndWait cancels detached work and waits a bounded time for it to
// observe the cancel.
func (c *Core) stopAndWait() error {
	c.StopJobs()
	ctx, cancel := context.WithTimeout(context.Background(), stopGrace)
	defer cancel()
	return c.jobs.Wait(ctx)
}

// Every terminal path writes the item's result row, because an item with no
// result row is read as one the runner never got to: a finished copy that
// recorded nothing would report itself done and list its own file as never
// reached. FinishOp errors are ignored throughout, since the copy's own
// outcome is already the answer and the row is best-effort bookkeeping.
func (c *Core) runCopy(ctx context.Context, id int64, from, to Resolved, st vfs.Stat) {
	c.runCopyPolicy(ctx, id, from, to, st, false)
}

func (c *Core) runCopyPolicy(
	ctx context.Context, id int64, from, to Resolved, st vfs.Stat, overwriting bool,
) {
	if err := c.state.StartOpItem(ctx, id, 0); err != nil {
		c.warn("marking a copy's item as started failed; the copy runs anyway",
			"operation", id, "error", err)
	}

	err := c.copyTreeStaged(
		ctx, from, to, st, acl.Read|acl.Download, c.cancelGate(ctx, id), overwriting,
	)
	now := c.clk.Nanos()
	path := to.path.String()

	switch {
	case errors.Is(err, errOpCancelled) && c.jobsStopped():
		// Stopped by a shutdown rather than by its owner. Interrupted is what
		// a client reads as work that is still theirs and was not resumed,
		// which is the truth about a copy the server walked away from.
		if ierr := c.state.InterruptOp(ctx, id, now); ierr != nil {
			c.warn("recording a copy's interruption failed",
				"operation", id, "error", ierr)
		}
	case errors.Is(err, errOpCancelled):
		// The one deliberate exception to the result-row rule: the stage was
		// discarded before publication, so the item is genuinely unfinished.
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
		// A shutdown stops the walk without a row to read, so a restart is
		// not held open for the length of a tree.
		if c.jobsStopped() {
			return true
		}
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
