package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/num"
)

// The operation store: the bounded, restart-visible history of long
// operations. A recursive copy, delete or archive of a large subtree outlives
// the request that started it; the operation gets an id, its progress is
// readable, and a client that refreshes the tab reattaches.
//
// A job that was running when the Rust server stopped is imported as
// interrupted, because no Go task exists to resume it. Completed history,
// progress and per-item results remain readable.

// OpState is the terminal or interim machine state of one operation.
type OpState int8

const (
	// OpRunning is an operation in flight.
	OpRunning OpState = iota
	// OpDone is one that completed.
	OpDone
	// OpFailed is one that aborted with an error.
	OpFailed
	// OpCancelled is one a client cancelled through its own call.
	OpCancelled
	// OpInterrupted is an imported run that was not finished when the process
	// stopped, and no Go task exists to resume. A refreshed client gets an
	// honest terminal state with its progress and results preserved.
	OpInterrupted
)

// OpKind is what kind of work an operation is.
type OpKind int8

const (
	OpCopy OpKind = iota
	OpDelete
	OpArchive
	// OpIndexBuild walks every share to build the name index. Appended rather
	// than inserted: these are stored as numbers, so renumbering would change
	// what an already-written row means.
	OpIndexBuild
)

// OpResultReason is the typed reason an item-level result failed, replacing a
// lower-layer error sentence.
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
	// Progress and Total are the item counter, which is what a progress bar
	// is drawn from. They are 0 under total 0 for an operation whose total is
	// unknown until the walk ends.
	Progress int64
	Total    int64
	Message  string
	// Cancellation is a client-requested stop that the running task honours
	// through its own context.
	Cancellation bool
	CreatedNs    int64
	FinishedNs   int64
}

// OpResult is one item's outcome from a batch operation.
type OpResult struct {
	Operation int64
	Idx       int64
	Path      string
	OK        bool
	Reason    OpResultReason
	Text      string
}

// CreateOp starts a fresh operation. The id it returns is what a client
// reattaches with. createdNs is the durable stamp the caller's clock provided.
func (d *DB) CreateOp(ctx context.Context, user int64, kind OpKind, total, createdNs int64) (int64, error) {
	var id int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, ierr := tx.ExecContext(ctx, sqlInsertOp,
			user, int64(kind), int64(OpRunning), total, nil, createdNs)
		if ierr != nil {
			return ierr
		}
		var rerr error
		id, rerr = res.LastInsertId()
		return rerr
	})
	if err != nil {
		return 0, fmt.Errorf("starting an operation: %w", err)
	}
	return id, nil
}

// GetOp reads one operation and its results.
func (d *DB) GetOp(ctx context.Context, id int64) (Op, []OpResult, error) {
	var (
		op       Op
		msg      sql.NullString
		finished sql.NullInt64
	)
	err := d.f.SQL().QueryRowContext(ctx, sqlReadOp, id).Scan(
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
	var out []OpResult
	for rows.Next() {
		var r OpResult
		var reason, text sql.NullInt64
		if serr := rows.Scan(&r.Operation, &r.Idx, &r.Path, &r.OK, &reason, &text); serr != nil {
			return Op{}, nil, serr
		}
		reasonCode, rerr := num.Narrow[int8](reason.Int64)
		if rerr != nil {
			return Op{}, nil, fmt.Errorf("an operation result carries a reason that does not fit: %w", rerr)
		}
		r.Reason = OpResultReason(reasonCode)
		r.Text = ""
		out = append(out, r)
	}
	return op, out, rows.Err()
}

// SetOpProgress updates a running operation's progress and message.
func (d *DB) SetOpProgress(ctx context.Context, id int64, progress int64, message string) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlSetOpProgress, progress, strArg(message), id)
		return ierr
	})
}

// RequestOpCancel marks a running operation for cancellation. The running task
// observes it through its own context and stops at the next item boundary.
func (d *DB) RequestOpCancel(ctx context.Context, id int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlRequestOpCancel, id)
		return ierr
	})
}

// FinishOp writes an operation's terminal state, its final progress, and its
// whole item-result set in one transaction. finishedNs is the caller's clock.
func (d *DB) FinishOp(ctx context.Context, id int64, state OpState, progress int64, message string, finishedNs int64, results []OpResult) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, ierr := tx.ExecContext(ctx, sqlSetOpState,
			int64(state), progress, strArg(message), finishedNs, id); ierr != nil {
			return ierr
		}
		if len(results) == 0 {
			return nil
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

// InterruptOp marks a running operation interrupted, preserving its progress
// and results so a refreshed client gets an honest terminal state. There is no
// result set of its own because the import wrote the completed ones.
func (d *DB) InterruptOp(ctx context.Context, id int64, finishedNs int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, ierr := tx.ExecContext(ctx, sqlSetOpState,
			int64(OpInterrupted), 0, strArg("interrupted by server restart, not resumed"), finishedNs, id)
		return ierr
	})
}

// ErrNoSuchOp is an operation id that holds no row.
var ErrNoSuchOp = errors.New("no such operation")

func strArg(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func textArg(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ListOps returns one account's operations, newest first and bounded.
//
// Bounded because the table grows with every batch anyone runs, and a listing
// that returns all of it is one that gets slower for as long as the deployment
// lives. The screen shows what is running and what just finished.
func (d *DB) ListOps(ctx context.Context, user int64, limit int) (out []Op, err error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.SQL().QueryContext(ctx, sqlListOps, user, limit)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		var op Op
		if serr := rows.Scan(&op.ID, &op.User, &op.Kind, &op.State,
			&op.Progress, &op.Total, &op.Message, &op.CreatedNs, &op.FinishedNs); serr != nil {
			return nil, serr
		}
		out = append(out, op)
	}
	return out, rows.Err()
}
