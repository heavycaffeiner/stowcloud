package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The device login flow's durable half.
//
// A client asks to sign in, a human approves in a browser, and the client
// collects an app password. The three steps happen in different processes
// minutes apart, so the flow between them is a row rather than memory.
//
// Nothing here holds a credential. Both tokens rest as digests and the
// password is minted at delivery, so a read of this table is a list of
// sign-ins in progress rather than a way to finish one.

// LoginFlowRow is one flow in progress.
type LoginFlowRow struct {
	PollDigest  []byte
	LoginDigest []byte
	CreatedNs   int64
	// ApprovedUser is nil until a human approves.
	ApprovedUser  *int64
	ApprovedLogin string
	LastPollNs    int64
}

// The refusals this file answers with. They are sentinels because the caller
// turns each into a different answer to the client: unknown is a 404, a
// second approval is a conflict, and a poll that is too soon is a 429.
var (
	// ErrLoginFlowUnknown is a digest no live flow has.
	ErrLoginFlowUnknown = errors.New("no such login flow")
	// ErrLoginFlowApproved refuses a second approval of one flow.
	ErrLoginFlowApproved = errors.New("the login flow is already approved")
	// ErrLoginFlowTooSoon is a poll inside the interval the last one started.
	ErrLoginFlowTooSoon = errors.New("polling too fast")
)

// PutLoginFlow stores a new flow.
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

// LoginFlowByPoll finds a flow by the token the client polls with.
func (d *DB) LoginFlowByPoll(ctx context.Context, digest []byte) (LoginFlowRow, error) {
	return d.loginFlowBy(ctx, sqlSelectLoginFlowByPoll, digest)
}

// LoginFlowByLogin finds a flow by the token in the browser's address bar.
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

// ApproveLoginFlow records who approved, and refuses a second approval.
//
// The refusal is the point rather than tidiness: without it, one login URL
// opened twice mints two credentials, and the second one goes to whoever
// replayed the link.
func (d *DB) ApproveLoginFlow(
	ctx context.Context, loginDigest []byte, user int64, login string,
) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		// The guard is in the statement rather than a read followed by a
		// write: two approvals arriving together would both pass a check
		// made before either wrote.
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
		// Nothing moved: either there is no such flow, or it was approved
		// already. The two are different answers to the caller.
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

// TouchLoginFlowPoll records a poll, refusing one that arrives too soon.
//
// minIntervalNs is how long a client must wait between polls. The comparison
// and the write are one statement so two polls racing cannot both pass.
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

// DropLoginFlow removes a flow once its credential has been delivered.
func (d *DB) DropLoginFlow(ctx context.Context, pollDigest []byte) error {
	return d.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, sqlDeleteLoginFlow, pollDigest); err != nil {
			return fmt.Errorf("dropping a login flow: %w", err)
		}
		return nil
	})
}

// SweepLoginFlows removes what has expired and reports how many. An
// abandoned flow is the normal case: a client that begins one and never
// opens the browser leaves a row nothing will ever collect.
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
