package state

import (
	"context"
	"fmt"
)

// What restarting would disrupt.
//
// An in-flight upload loses whichever part was still arriving, and a running job
// halts wherever it stands. Both recover afterwards, since a session resumes and
// a job is recorded as interrupted, but neither should happen to someone
// unannounced. These counts exist to make the warning possible.

// ActiveWork holds both counts spanning every account rather than a single one,
// since a restart stops the process for all of them at once.
type ActiveWork struct {
	Uploads int
	Jobs    int
}

// CountActiveWork reads both counts fresh. There is no cached copy: the
// answer is only useful at the moment somebody is deciding whether to
// restart.
func (d *DB) CountActiveWork(ctx context.Context) (ActiveWork, error) {
	var w ActiveWork
	if err := d.f.SQL().QueryRowContext(ctx, sqlCountReceivingUploads).Scan(&w.Uploads); err != nil {
		return ActiveWork{}, fmt.Errorf("counting uploads in flight: %w", err)
	}
	if err := d.f.SQL().QueryRowContext(ctx, sqlCountRunningOps, int64(OpRunning)).Scan(&w.Jobs); err != nil {
		return ActiveWork{}, fmt.Errorf("counting running jobs: %w", err)
	}
	return w, nil
}

// Two read-only counts and no write path, so they stay here rather than
// taking a SQL file of their own.
const (
	// State zero means receiving. Sessions that aborted or completed have
	// nothing in flight left to lose.
	sqlCountReceivingUploads = `SELECT count(*) FROM upload_session WHERE state = 0`

	sqlCountRunningOps = `SELECT count(*) FROM operation WHERE state = ?`
)
