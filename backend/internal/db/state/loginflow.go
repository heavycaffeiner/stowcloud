package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The persistent side of the device login flow.
//
// A client requests sign-in, a person approves it in a browser, and the client
// retrieves an app password. Those three steps occur in separate processes
// minutes apart, so what connects them is a row rather than memory.
//
// No credential is stored here. Both tokens sit as digests and the password is
// generated at delivery time, so reading this table reveals which sign-ins are
// underway without providing any means to complete one.

// LoginFlowRow describes a single in-progress flow.
type LoginFlowRow struct {
	PollDigest  []byte
	LoginDigest []byte
	CreatedNs   int64
	// ApprovedUser stays nil until a person approves.
	ApprovedUser  *int64
	ApprovedLogin string
	LastPollNs    int64
}

// The rejections this file produces. Each is a sentinel because the caller maps
// them to distinct client responses: an unknown flow becomes a 404, a repeat
// approval becomes a conflict, and an early poll becomes a 429.
var (
	// ErrLoginFlowUnknown reports a digest matching no live flow.
	ErrLoginFlowUnknown = errors.New("no such login flow")
	// ErrLoginFlowApproved rejects approving the same flow twice.
	ErrLoginFlowApproved = errors.New("the login flow is already approved")
	// ErrLoginFlowTooSoon reports a poll arriving within the interval opened by
	// the previous one.
	ErrLoginFlowTooSoon = errors.New("polling too fast")
)

// PutLoginFlow persists a new flow.
func (d *DB) PutLoginFlow(ctx context.Context, rec LoginFlowRow) error {
	// A new row is what grows the file.
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	return d.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, sqlInsertLoginFlow,
			rec.PollDigest, rec.LoginDigest, rec.CreatedNs)
		if err != nil {
			return fmt.Errorf("storing a login flow: %w", err)
		}
		return nil
	})
}

// LoginFlowByPoll locates a flow using the token the client polls with.
func (d *DB) LoginFlowByPoll(ctx context.Context, digest []byte) (LoginFlowRow, error) {
	return d.loginFlowBy(ctx, sqlSelectLoginFlowByPoll, digest)
}

// LoginFlowByLogin locates a flow using the token from the browser's address
// bar.
func (d *DB) LoginFlowByLogin(ctx context.Context, digest []byte) (LoginFlowRow, error) {
	return d.loginFlowBy(ctx, sqlSelectLoginFlowByLogin, digest)
}

func (d *DB) loginFlowBy(ctx context.Context, query string, digest []byte) (LoginFlowRow, error) {
	var (
		out  LoginFlowRow
		user sql.NullInt64
	)
	err := d.f.SQL().QueryRowContext(ctx, query, digest).Scan(
		&out.PollDigest, &out.LoginDigest, &out.CreatedNs,
		&user, &out.ApprovedLogin, &out.LastPollNs)
	if errors.Is(err, sql.ErrNoRows) {
		return LoginFlowRow{}, ErrLoginFlowUnknown
	}
	if err != nil {
		return LoginFlowRow{}, fmt.Errorf("reading a login flow: %w", err)
	}
	if user.Valid {
		u := user.Int64
		out.ApprovedUser = &u
	}
	return out, nil
}

// ApproveLoginFlow records the approver and rejects any repeat approval.
//
// That rejection is a security property rather than housekeeping. Without it, a
// single login URL opened twice issues two credentials, and the second lands
// with whoever replayed the link.
func (d *DB) ApproveLoginFlow(
	ctx context.Context, loginDigest []byte, user int64, login string,
) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		// The guard lives inside the statement instead of a read followed by a
		// write, since two approvals arriving together would both satisfy a
		// check performed before either one wrote.
		res, err := tx.ExecContext(ctx, sqlApproveLoginFlow, user, login, loginDigest)
		if err != nil {
			return fmt.Errorf("approving a login flow: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("approving a login flow: %w", err)
		}
		if n == 1 {
			return nil
		}
		// No row changed, meaning either the flow does not exist or it was
		// already approved. The caller must distinguish the two.
		var approved sql.NullInt64
		qerr := tx.QueryRowContext(ctx, sqlSelectLoginFlowApproval, loginDigest).Scan(&approved)
		if errors.Is(qerr, sql.ErrNoRows) {
			return ErrLoginFlowUnknown
		}
		if qerr != nil {
			return fmt.Errorf("approving a login flow: %w", qerr)
		}
		if approved.Valid {
			return ErrLoginFlowApproved
		}
		return ErrLoginFlowUnknown
	})
}

// TouchLoginFlowPoll registers a poll and rejects one arriving prematurely.
//
// minIntervalNs sets the mandatory wait between polls. Comparison and write
// occupy one statement so that racing polls cannot both succeed.
func (d *DB) TouchLoginFlowPoll(
	ctx context.Context, pollDigest []byte, nowNs, minIntervalNs int64,
) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlTouchLoginFlowPoll,
			nowNs, pollDigest, nowNs-minIntervalNs)
		if err != nil {
			return fmt.Errorf("recording a login poll: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("recording a login poll: %w", err)
		}
		if n == 1 {
			return nil
		}
		var last int64
		qerr := tx.QueryRowContext(ctx, sqlSelectLoginFlowLastPoll, pollDigest).Scan(&last)
		if errors.Is(qerr, sql.ErrNoRows) {
			return ErrLoginFlowUnknown
		}
		if qerr != nil {
			return fmt.Errorf("recording a login poll: %w", qerr)
		}
		return ErrLoginFlowTooSoon
	})
}

// DropLoginFlow deletes a flow after its credential has been handed over.
func (d *DB) DropLoginFlow(ctx context.Context, pollDigest []byte) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlDeleteLoginFlow, pollDigest); err != nil {
			return fmt.Errorf("dropping a login flow: %w", err)
		}
		return nil
	})
}

// SweepLoginFlows deletes expired flows and returns the count. Abandonment is
// the ordinary case: a client that starts a flow and never opens the browser
// leaves behind a row nothing else would ever reclaim.
func (d *DB) SweepLoginFlows(ctx context.Context, olderThanNs int64) (int64, error) {
	var n int64
	err := d.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, sqlSweepLoginFlows, olderThanNs)
		if err != nil {
			return fmt.Errorf("sweeping login flows: %w", err)
		}
		n, err = res.RowsAffected()
		if err != nil {
			return fmt.Errorf("sweeping login flows: %w", err)
		}
		return nil
	})
	return n, err
}
