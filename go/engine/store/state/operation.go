package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// The operation store holds a bounded history of long operations that survives
// restarts. Recursive copies, deletes and archives over a large subtree outlast
// the request that began them, so each operation receives an id, exposes its
// progress, and lets a client reattach after refreshing the tab.
//
// Any job running when the server stopped reads back as interrupted. Nothing
// resumes it, because the task tracking its progress died with the process.
// Completed history, progress figures and per-item results all remain
// readable.

// OpState gives an operation's machine state, whether interim or terminal.
type OpState int8

const (
	// OpRunning marks an operation still in flight.
	OpRunning OpState = iota
	// OpDone is one that completed.
	OpDone
	// OpFailed marks one that aborted with an error.
	OpFailed
	// OpCancelled marks one a client stopped through its own request.
	OpCancelled
	// OpInterrupted is a run that was not finished when the process stopped
	// and that nothing resumes. A refreshed client gets an honest terminal
	// state with its progress and results preserved.
	OpInterrupted

	// opStateSentinel is one past the last state and is never stored. It
	// exists so a walk over the states has a bound that moves with them.
	opStateSentinel
)

// OpStateCount is how many states exist.
//
// The states are consecutive from zero, so this is the bound a caller walks
// them with. It reads the sentinel rather than the last named state, so
// appending a state moves the bound without an edit here: a bound that has to
// be updated by hand is one that will not be.
func OpStateCount() int { return int(opStateSentinel) }

// OpKind names the sort of work an operation performs.
type OpKind int8

const (
	OpCopy OpKind = iota
	OpDelete
	OpArchive
	// OpIndexBuild walks every share to build the name index. Appended
	// rather than inserted: these are stored as numbers, so renumbering
	// would change what an already-written row means.
	OpIndexBuild
)

// OpResultReason is the typed reason an item-level result failed, in place
// of a lower-layer error sentence.
type OpResultReason int8

const (
	ReasonItemOk OpResultReason = iota
	ReasonItemFailed
	ReasonItemDenied
	ReasonItemNotFound
	ReasonItemConflict
	ReasonItemSkipped
)

// Op is one stored operation.
type Op struct {
	ID    int64
	User  int64
	Kind  OpKind
	State OpState
	// Progress and Total form the item counter behind a progress bar. Both
	// remain zero while an operation's total stays unknown until its walk
	// finishes.
	Progress int64
	Total    int64
	Message  string
	// Cancellation is a client-requested stop the running task honors
	// through its own context.
	Cancellation bool
	CreatedNs    int64
	FinishedNs   int64
}

// OpResult records how a single item fared within a batch operation.
type OpResult struct {
	Operation int64
	Idx       int64
	Path      string
	OK        bool
	Reason    OpResultReason
	Text      string
}

// ErrNoSuchOp reports an operation id backed by no row.
var ErrNoSuchOp = errors.New("no such operation")

// CreateOp begins a new operation. The returned id is what a client reattaches
// with, and createdNs is the durable timestamp supplied by the caller's clock.
//
// paths captures everything the operation was asked to handle, written up front
// rather than incrementally. A job that halts early can then report which items
// it never reached, which is the difference between saying four of five files
// moved and saying which one did not.
func (d *DB) CreateOp(
	ctx context.Context, user int64, kind OpKind, total, createdNs int64, paths []string,
) (int64, error) {
	// New operation and item rows are what grow the file.
	if err := d.f.EnsureWritable(); err != nil {
		return 0, err
	}
	var id int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertOp,
			user, int64(kind), int64(OpRunning), total, nil, createdNs)
		if ierr != nil {
			return ierr
		}
		var rerr error
		id, rerr = res.LastInsertId()
		if rerr != nil {
			return rerr
		}
		for i, p := range paths {
			if _, perr := tx.ExecContext(ctx, sqlInsertOpItem, id, int64(i), p); perr != nil {
				return perr
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("starting an operation: %w", err)
	}
	return id, nil
}

// StartOpItem flags an item as in flight, so a process dying partway through
// leaves behind a record of which item it held.
func (d *DB) StartOpItem(ctx context.Context, id, idx int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlMarkOpItemStarted, id, idx)
		return ierr
	})
}

// UnfinishedOpItems returns paths the operation was given but never recorded an
// outcome for, separated by whether work had begun on them.
//
// In practice the attempting set holds at most one entry, since the runner
// processes items serially and this is whatever it held when the process
// stopped. Whether that item completed is genuinely unknown, which is why it is
// reported apart from those never touched.
func (d *DB) UnfinishedOpItems(
	ctx context.Context, id int64,
) (attempting, pending []string, err error) {
	rows, err := d.f.SQL().QueryContext(ctx, sqlReadOpUnfinished, id)
	if err != nil {
		return nil, nil, fmt.Errorf("reading an operation's unfinished items: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var (
			path    string
			started bool
		)
		if serr := rows.Scan(&path, &started); serr != nil {
			return nil, nil, fmt.Errorf("reading an operation item: %w", serr)
		}
		if started {
			attempting = append(attempting, path)
			continue
		}
		pending = append(pending, path)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("reading an operation's unfinished items: %w", err)
	}
	return attempting, pending, nil
}

// GetOp retrieves an operation together with its results.
func (d *DB) GetOp(ctx context.Context, id int64) (op Op, results []OpResult, err error) {
	var (
		msg      sql.NullString
		finished sql.NullInt64
	)
	err = d.f.SQL().QueryRowContext(ctx, sqlReadOp, id).Scan(
		&op.ID, &op.User, &op.Kind, &op.State, &op.Progress, &op.Total,
		&msg, &op.Cancellation, &op.CreatedNs, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return Op{}, nil, ErrNoSuchOp
	}
	if err != nil {
		return Op{}, nil, fmt.Errorf("reading an operation: %w", err)
	}
	op.Message = msg.String
	op.FinishedNs = finished.Int64

	rows, err := d.f.SQL().QueryContext(ctx, sqlReadOpResults, id)
	if err != nil {
		return Op{}, nil, fmt.Errorf("reading an operation's results: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var (
			r      OpResult
			reason sql.NullInt64
			text   sql.NullString
		)
		if serr := rows.Scan(&r.Operation, &r.Idx, &r.Path, &r.OK, &reason, &text); serr != nil {
			return Op{}, nil, fmt.Errorf("reading an operation result: %w", serr)
		}
		code, rerr := num.Narrow[int8](reason.Int64)
		if rerr != nil {
			return Op{}, nil, fmt.Errorf(
				"operation %d item %d carries reason %d: %w", r.Operation, r.Idx, reason.Int64, rerr)
		}
		r.Reason = OpResultReason(code)
		r.Text = text.String
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return Op{}, nil, fmt.Errorf("reading an operation's results: %w", err)
	}
	return op, results, nil
}

// SetOpProgress revises a running operation's progress and message.
func (d *DB) SetOpProgress(ctx context.Context, id int64, progress int64, message string) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlSetOpProgress, progress, textArg(message), id)
		return ierr
	})
}

// RequestOpCancel marks a running operation for cancellation. The running
// task observes it through its own context and stops at the next item
// boundary.
func (d *DB) RequestOpCancel(ctx context.Context, id int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlRequestOpCancel, id)
		return ierr
	})
}

// FinishOp commits an operation's terminal state, final progress and complete
// item-result set within one transaction, so no client ever observes a finished
// operation whose results are still missing.
func (d *DB) FinishOp(
	ctx context.Context, id int64, state OpState, progress int64,
	message string, finishedNs int64, results []OpResult,
) error {
	// The result rows are new rows.
	if len(results) > 0 {
		if err := d.f.EnsureWritable(); err != nil {
			return err
		}
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, ierr := tx.ExecContext(ctx, sqlSetOpState,
			int64(state), progress, textArg(message), finishedNs, id); ierr != nil {
			return ierr
		}
		for _, r := range results {
			if _, ierr := tx.ExecContext(ctx, sqlInsertOpResult,
				r.Operation, r.Idx, r.Path, r.OK, int64(r.Reason), textArg(r.Text)); ierr != nil {
				return ierr
			}
		}
		return nil
	})
}

// InterruptOp marks a running operation as interrupted while keeping its
// progress and results, so a client that refreshes receives a truthful terminal
// state.
func (d *DB) InterruptOp(ctx context.Context, id int64, finishedNs int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlSetOpState,
			int64(OpInterrupted), 0,
			textArg("interrupted by server restart, not resumed"), finishedNs, id)
		return ierr
	})
}

// ListOps returns an account's unfinished operations, newest first, up to a
// limit.
//
// Unfinished covers running and interrupted, the two states a client can still
// act on. Finished operations are history, and a caller reading this is
// reattaching to work in flight. The bound exists because the table grows with
// every batch anyone runs.
func (d *DB) ListOps(ctx context.Context, user int64, limit int) (out []Op, err error) {
	if limit <= 0 {
		limit = defaultOpListing
	}
	rows, err := d.f.SQL().QueryContext(ctx, sqlListOps,
		user, int64(OpRunning), int64(OpInterrupted), limit)
	if err != nil {
		return nil, fmt.Errorf("listing operations: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var (
			op       Op
			msg      sql.NullString
			finished sql.NullInt64
		)
		if serr := rows.Scan(&op.ID, &op.User, &op.Kind, &op.State,
			&op.Progress, &op.Total, &msg, &op.CreatedNs, &finished); serr != nil {
			return nil, fmt.Errorf("reading an operation: %w", serr)
		}
		op.Message, op.FinishedNs = msg.String, finished.Int64
		out = append(out, op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing operations: %w", err)
	}
	return out, nil
}

// defaultOpListing is what an unset limit takes. A listing that returns the
// whole table is one that gets slower for as long as the deployment lives.
const defaultOpListing = 100

// textArg stores an empty string as SQL NULL, so "nothing to say" is one
// value on disk rather than two.
func textArg(s string) any {
	if s == "" {
		return nil
	}
	return s
}
