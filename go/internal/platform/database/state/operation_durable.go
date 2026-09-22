package state

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
)

func newLeaseID() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating operation lease: %w", err)
	}
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[2*i], out[2*i+1] = hex[v>>4], hex[v&15]
	}
	return string(out), nil
}

// CreateQueuedOp creates operation and item rows in the queued state.
func (d *DB) CreateQueuedOp(ctx context.Context, q QueuedOp) (int64, error) {
	if q.User <= 0 || q.Total < 0 || q.MaxAttempts < 0 {
		return 0, fmt.Errorf("invalid queued operation arguments")
	}
	if err := d.f.EnsureWritable(); err != nil {
		return 0, err
	}
	if q.ProgressUnit == "" {
		q.ProgressUnit = "items"
	}
	if q.NextRunNs == 0 {
		q.NextRunNs = q.CreatedNs
	}
	var id int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlInsertQueuedOp, q.User, int64(q.Kind), int64(OpQueued), q.Total,
			q.CreatedNs, q.Payload, q.ProgressUnit, q.MaxAttempts, q.NextRunNs, q.CreatedNs)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		for i, path := range q.Paths {
			if _, err := tx.ExecContext(ctx, sqlInsertOpItem, id, int64(i), path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("creating queued operation: %w", err)
	}
	return id, nil
}

// GetOpPayload returns the persisted raw payload without attempting to decode it.
func (d *DB) GetOpPayload(ctx context.Context, id int64) ([]byte, error) {
	var payload []byte
	if err := d.f.SQL().QueryRowContext(ctx, `SELECT payload FROM operation WHERE id = ?`, id).Scan(&payload); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSuchOp
	} else if err != nil {
		return nil, fmt.Errorf("reading operation payload: %w", err)
	}
	return append([]byte(nil), payload...), nil
}

// HasRunnableOp reports whether a queued or retrying operation is ready.
func (d *DB) HasRunnableOp(ctx context.Context, nowNs int64) (bool, error) {
	var id int64
	err := d.f.SQL().QueryRowContext(ctx, `
SELECT id FROM operation
WHERE state IN (?, ?) AND next_run_ns <= ? AND (max_attempts = 0 OR attempt < max_attempts)
ORDER BY next_run_ns, created_ns, id LIMIT 1`, int64(OpQueued), int64(OpRetrying), nowNs).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ClaimNextOp atomically claims the oldest runnable queued/retrying operation.
func (d *DB) ClaimNextOp(ctx context.Context, nowNs, leaseNs int64) (OpClaim, error) {
	if leaseNs <= 0 {
		return OpClaim{}, fmt.Errorf("operation lease must be positive")
	}
	leaseID, err := newLeaseID()
	if err != nil {
		return OpClaim{}, err
	}
	var id int64
	err = d.f.SQL().QueryRowContext(ctx, `
SELECT id FROM operation
WHERE state IN (?, ?) AND next_run_ns <= ? AND (max_attempts = 0 OR attempt < max_attempts)
ORDER BY next_run_ns, created_ns, id LIMIT 1`, int64(OpQueued), int64(OpRetrying), nowNs).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return OpClaim{}, ErrNoRunnableOp
	}
	if err != nil {
		return OpClaim{}, err
	}
	var claim OpClaim
	err = d.Write(ctx, func(tx *sql.Tx) error {
		until := nowNs + leaseNs
		res, xerr := tx.ExecContext(ctx, `
UPDATE operation SET state = ?, attempt = attempt + 1, started_ns = ?, updated_ns = ?,
 lease_id = ?, lease_expires_ns = ?, pause_requested = 0
WHERE id = ? AND state IN (?, ?) AND next_run_ns <= ?`, int64(OpRunning), nowNs, nowNs,
			leaseID, until, id, int64(OpQueued), int64(OpRetrying), nowNs)
		if xerr != nil {
			return xerr
		}
		n, aerr := res.RowsAffected()
		if aerr != nil {
			return aerr
		}
		if n != 1 {
			return ErrNoRunnableOp
		}
		var op Op
		if serr := scanOperation(tx.QueryRowContext(ctx, sqlReadOp, id), &op); serr != nil {
			return serr
		}
		claim = OpClaim{Op: op, Payload: append([]byte(nil), op.Payload...), LeaseID: leaseID, LeaseExpiresNs: until}
		return nil
	})
	if err != nil {
		return OpClaim{}, err
	}
	return claim, nil
}

// RenewOpLease extends a worker's lease while it remains current.
func (d *DB) RenewOpLease(ctx context.Context, id int64, leaseID string, untilNs int64) error {
	return d.leaseUpdate(ctx, `UPDATE operation SET lease_expires_ns = ?, updated_ns = ? WHERE id = ? AND state = ? AND lease_id = ?`, untilNs, untilNs, id, int64(OpRunning), leaseID)
}

// SetOpProgressLease conditionally records progress for a leased operation.
func (d *DB) SetOpProgressLease(ctx context.Context, id int64, leaseID string, progress int64, message string, updatedNs int64) error {
	return d.leaseUpdate(ctx, `UPDATE operation SET progress = ?, message = ?, updated_ns = ? WHERE id = ? AND state = ? AND lease_id = ?`, progress, textArg(message), updatedNs, id, int64(OpRunning), leaseID)
}

// FinishOpLease conditionally commits a terminal result set for a leased operation.
func (d *DB) FinishOpLease(ctx context.Context, id int64, leaseID string, state OpState, progress int64, message, errorKey, errorDetail string, finishedNs int64, results []OpResult) error {
	if state != OpDone && state != OpFailed && state != OpCancelled && state != OpInterrupted {
		return ErrOpInvalidTransition
	}
	if len(results) > 0 {
		if err := d.f.EnsureWritable(); err != nil {
			return err
		}
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE operation SET state = ?, progress = ?, message = ?, finished_ns = ?, updated_ns = ?, error_key = ?, error_detail = ?, lease_id = NULL, lease_expires_ns = 0 WHERE id = ? AND state = ? AND lease_id = ?`, int64(state), progress, textArg(message), finishedNs, finishedNs, textArg(errorKey), textArg(errorDetail), id, int64(OpRunning), leaseID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return opLeaseFailure(ctx, tx, id)
		}
		for _, r := range results {
			if _, err := tx.ExecContext(ctx, sqlInsertOpResult, r.Operation, r.Idx, r.Path, r.OK, int64(r.Reason), textArg(r.Text)); err != nil {
				return err
			}
		}
		return nil
	})
}

// FailOpLease records a retryable or terminal worker failure as failed.
func (d *DB) FailOpLease(ctx context.Context, id int64, leaseID string, progress int64, message, errorKey, errorDetail string, finishedNs int64, results []OpResult) error {
	return d.FinishOpLease(ctx, id, leaseID, OpFailed, progress, message, errorKey, errorDetail, finishedNs, results)
}

// RetryOpLease puts a leased operation back into the retry queue.
func (d *DB) RetryOpLease(ctx context.Context, id int64, leaseID string, nextRunNs, updatedNs int64, errorKey, errorDetail string) error {
	return d.leaseUpdate(ctx, `UPDATE operation SET state = ?, next_run_ns = ?, updated_ns = ?, error_key = ?, error_detail = ?, lease_id = NULL, lease_expires_ns = 0 WHERE id = ? AND state = ? AND lease_id = ?`, int64(OpRetrying), nextRunNs, updatedNs, textArg(errorKey), textArg(errorDetail), id, int64(OpRunning), leaseID)
}

// PauseOp pauses queued/retrying work, or asks a running worker to pause.
func (d *DB) PauseOp(ctx context.Context, id int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE operation SET state = CASE WHEN state IN (?, ?) THEN ? ELSE state END, pause_requested = 1, updated_ns = updated_ns + 1 WHERE id = ? AND state IN (?, ?, ?)`, int64(OpQueued), int64(OpRetrying), int64(OpPaused), id, int64(OpQueued), int64(OpRetrying), int64(OpRunning))
		if err != nil {
			return err
		}
		return affectedOrOpError(ctx, tx, id, res)
	})
}

// ResumeOp makes paused work runnable again.
func (d *DB) ResumeOp(ctx context.Context, id int64, nextRunNs int64) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE operation SET state = ?, next_run_ns = ?, pause_requested = 0, updated_ns = updated_ns + 1 WHERE id = ? AND state = ?`, int64(OpQueued), nextRunNs, id, int64(OpPaused))
		if err != nil {
			return err
		}
		return affectedOrOpError(ctx, tx, id, res)
	})
}

// CancelOpLease conditionally finishes a leased operation as cancelled.
func (d *DB) CancelOpLease(ctx context.Context, id int64, leaseID string, progress, finishedNs int64) error {
	return d.FinishOpLease(ctx, id, leaseID, OpCancelled, progress, "cancelled", "cancelled", "", finishedNs, nil)
}

// RecoverExpiredOps requeues operations whose worker lease expired.
func (d *DB) RecoverExpiredOps(ctx context.Context, nowNs int64) (int, error) {
	var n int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE operation SET state = CASE WHEN max_attempts > 0 AND attempt >= max_attempts THEN ? ELSE ? END, error_key = CASE WHEN max_attempts > 0 AND attempt >= max_attempts THEN 'lease_expired' ELSE error_key END, error_detail = CASE WHEN max_attempts > 0 AND attempt >= max_attempts THEN 'worker lease expired' ELSE error_detail END, next_run_ns = ?, updated_ns = ?, lease_id = NULL, lease_expires_ns = 0 WHERE state = ? AND lease_expires_ns > 0 AND lease_expires_ns <= ?`, int64(OpFailed), int64(OpRetrying), nowNs, nowNs, int64(OpRunning), nowNs)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return int(n), err
}

func (d *DB) leaseUpdate(ctx context.Context, stmt string, args ...any) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, stmt, args...)
		if err != nil {
			return err
		}
		return affectedOrOpError(ctx, tx, opIDFromArgs(args), res)
	})
}

func opIDFromArgs(args []any) int64 {
	for _, arg := range args {
		if id, ok := arg.(int64); ok && id > 0 {
			return id
		}
	}
	return 0
}

func affectedOrOpError(ctx context.Context, tx *sql.Tx, id int64, res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	return opLeaseFailure(ctx, tx, id)
}

func opLeaseFailure(ctx context.Context, tx *sql.Tx, id int64) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM operation WHERE id = ?`, id).Scan(&n); errors.Is(err, sql.ErrNoRows) {
		return ErrNoSuchOp
	} else if err != nil {
		return err
	}
	return ErrOpLostLease
}

func scanOperation(row interface{ Scan(...any) error }, op *Op) error {
	var (
		msg, unit, errorKey, errorDetail, leaseID         sql.NullString
		finished, nextRun, updated, started, leaseExpires sql.NullInt64
		payload                                           []byte
		attempt, maxAttempts                              int
		pause                                             bool
	)
	if err := row.Scan(&op.ID, &op.User, &op.Kind, &op.State, &op.Progress, &op.Total, &msg,
		&op.Cancellation, &op.CreatedNs, &finished, &payload, &unit, &attempt, &maxAttempts,
		&nextRun, &updated, &started, &errorKey, &errorDetail, &leaseID, &leaseExpires, &pause); err != nil {
		return err
	}
	op.Message, op.FinishedNs = msg.String, finished.Int64
	op.Payload = append([]byte(nil), payload...)
	op.ProgressUnit, op.Attempt, op.MaxAttempts = unit.String, attempt, maxAttempts
	op.NextRunNs, op.UpdatedNs, op.StartedNs = nextRun.Int64, updated.Int64, started.Int64
	op.ErrorKey, op.ErrorDetail, op.LeaseID = errorKey.String, errorDetail.String, leaseID.String
	op.LeaseExpiresNs, op.PauseRequested = leaseExpires.Int64, pause
	return nil
}
