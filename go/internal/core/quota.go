//go:build linux

package core

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/internal/num"
)

// Quota is two different mechanisms under one word, and losing one of them is
// how a port answers the same protocol question with only half the feature.
//
// 1. The free-space floor is what the filesystem has left. It is reported to
//    clients as RFC 4331 disk space and refuses a write that would fill the
//    disk. It is not per account.
// 2. The per-user byte quota is a cap on an account and a running ledger of
//    what it has used, both columns on the user row. It is enforced through
//    a reserve-then-commit seam: a write reserves before it starts and
//    commits or releases when it ends, so two concurrent uploads cannot both
//    pass a check against the same headroom. The compat layer reports it to
//    clients, so it is client-visible as well as enforced.

// QuotaSink is the ledger side of the per-user quota. It is attached like the
// other optional stores: a Core with none attached enforces nothing, which is
// a legitimate quota-less deployment state.
type QuotaSink interface {
	// Reserve atomically books additional bytes against user, if the cap
	// allows it. It is the "two concurrent writes cannot both pass the same
	// headroom" mechanism, so it must be a single guarded update, never a
	// read-then-write. A refusal means the user is already at the cap.
	Reserve(ctx context.Context, user UserID, additional uint64) error

	// Commit settles a reservation that the write it paid for has succeeded.
	// The bytes were already booked by Reserve, so Commit is idempotent and
	// exists to keep the caller's intent explicit.
	Commit(ctx context.Context, user UserID, additional uint64) error

	// Release returns reserved bytes that the write they were booked for did
	// not land, and credits bytes freed by a permanent delete. The same
	// operation serves both, which is why it takes a signed delta.
	Release(ctx context.Context, user UserID, delta int64) error
}

// AttachQuotaSink wires the ledger. A second call is a wiring bug, not a
// runtime condition.
func (c *Core) AttachQuotaSink(sink QuotaSink) error {
	if c.quota != nil {
		return errors.New("a quota sink is already attached")
	}
	c.quota = sink
	return nil
}

// checkQuota consults the ledger before a write that grows the user's use.
func (c *Core) checkQuota(ctx context.Context, user UserID, additional uint64) error {
	if c.quota == nil || additional == 0 {
		return nil
	}
	if err := c.quota.Reserve(ctx, user, additional); err != nil {
		return errf(ErrQuotaExceeded, "reserve %d bytes", additional)
	}
	return nil
}

// chargeQuota settles a delta against the ledger after the filesystem change
// it belongs to has committed. Negative is a freed delete.
func (c *Core) chargeQuota(ctx context.Context, user UserID, delta int64) {
	if c.quota == nil || delta == 0 {
		return
	}
	if err := c.quota.Release(ctx, user, delta); err != nil {
		c.warn("settling the quota ledger failed; the filesystem change has committed",
			"error", err)
	}
}

// quotaChecker is nil-safe access to the sink, for the delete path that only
// credits freed bytes.
func (c *Core) freeChecker() QuotaSink { return c.quota }

// NewSQLQuota returns a quota ledger over the user rows. It needs the durable
// database's serialised write path, which is state.DB.
func NewSQLQuota(db sqlWriteDB) QuotaSink {
	return &sqlQuota{db: db}
}

// sqlWriteDB is the fragment of the store the sink needs.
type sqlWriteDB interface {
	Write(context.Context, func(*sql.Tx) error) error
}

// sqlQuota is the ledger over the user row's quota_bytes and usage_bytes
// columns. The reserve is one guarded UPDATE, which is what makes
// simultaneous uploads against the same headroom mutually exclusive at the
// point of booking.
type sqlQuota struct {
	db sqlWriteDB
}

// reserveStmt is the atomic booking. usage_bytes is never allowed to go
// negative, and a user with no cap is unlimited.
const reserveStmt = `
UPDATE user
SET usage_bytes = usage_bytes + ?
WHERE id = ?
  AND (quota_bytes IS NULL OR usage_bytes + ? <= quota_bytes)`

// releaseStmt credits freed bytes or returns reserved ones, never going below
// zero. A negative delta books more, which Release is never called with, so
// the guard only clamps the recovery direction.
const releaseStmt = `
UPDATE user
SET usage_bytes = max(0, usage_bytes - ?)
WHERE id = ?`

// Reserve implements QuotaSink. The guarded UPDATE is the whole mechanism:
// read-then-write would let N concurrent uploads all observe the same
// headroom and all proceed.
func (q *sqlQuota) Reserve(ctx context.Context, user UserID, additional uint64) error {
	delta, err := num.Narrow[int64](additional)
	if err != nil {
		return err
	}
	var n int64
	err = q.db.Write(ctx, func(tx *sql.Tx) error {
		res, rerr := tx.ExecContext(ctx, reserveStmt, delta, int64(user), delta)
		if rerr != nil {
			return rerr
		}
		n, rerr = res.RowsAffected()
		return rerr
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrQuotaExceeded
	}
	return nil
}

// Commit is a no-op: the bytes were already booked atomically by Reserve.
func (q *sqlQuota) Commit(ctx context.Context, user UserID, additional uint64) error {
	return nil
}

// Release credits the ledger. Exported only through the interface.
func (q *sqlQuota) Release(ctx context.Context, user UserID, delta int64) error {
	if delta == 0 {
		return nil
	}
	// A negative delta would book more bytes, which this sink is never asked
	// for: the write path reserves and the delete path credits.
	if delta < 0 {
		return fmt.Errorf("releasing a negative delta would book bytes, which callers never do")
	}
	return q.db.Write(ctx, func(tx *sql.Tx) error {
		_, rerr := tx.ExecContext(ctx, releaseStmt, delta, int64(user))
		return rerr
	})
}
