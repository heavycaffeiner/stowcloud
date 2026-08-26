package state

import (
	"context"
	"fmt"
)

// What a restart would interrupt.
//
// An upload in flight loses the part nobody has finished sending, and a
// running job stops where it is. Both are recoverable (a session resumes, a
// job is recorded as interrupted) and neither is something to do to somebody
// without saying so, which is what these counts are for.

// ActiveWork is the two counts, across every account rather than one: a
// restart takes the process down for all of them.
type ActiveWork struct {
	Uploads int
	Jobs    int
}

// CountActiveWork reads both counts.
func (d *DB) CountActiveWork(ctx context.Context) (ActiveWork, error) {
	var w ActiveWork
	if err := d.SQL().QueryRowContext(ctx, sqlCountReceivingUploads).Scan(&w.Uploads); err != nil {
		return ActiveWork{}, fmt.Errorf("counting uploads in flight: %w", err)
	}
	if err := d.SQL().QueryRowContext(ctx, sqlCountRunningOps, int64(OpRunning)).Scan(&w.Jobs); err != nil {
		return ActiveWork{}, fmt.Errorf("counting running jobs: %w", err)
	}
	return w, nil
}

const (
	// State zero is receiving. An aborted or finished session has nothing in
	// flight to lose.
	sqlCountReceivingUploads = `SELECT count(*) FROM upload_session WHERE state = 0`

	sqlCountRunningOps = `SELECT count(*) FROM operation WHERE state = ?`
)
