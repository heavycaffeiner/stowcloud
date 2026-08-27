package state

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
)

// Quota is the per-user byte ledger. It is a separate value rather than
// methods on DB so the wiring site can hand it to whatever wants a ledger
// without handing over the whole durable store.
type Quota struct {
	db *DB
}

// NewQuota is the ledger over this database.
func NewQuota(db *DB) *Quota { return &Quota{db: db} }

// Reserve books additional bytes against the user's cap and reports whether
// there was room. A refusal is (false, nil), not an error: a user at the cap
// and a user who does not exist both have no headroom, and the caller that
// knows which sentinel its own layer uses is the one that maps this.
//
// It is not gated by the size guard. It updates an existing row and never
// inserts one, so it cannot grow the database; refusing it under a full
// volume would block uploads for a reason that has nothing to do with the
// ledger.
func (q *Quota) Reserve(ctx context.Context, user int64, additional uint64) (bool, error) {
	delta, err := num.Narrow[int64](additional)
	if err != nil {
		return false, fmt.Errorf("reserving %d bytes for user %d: %w", additional, user, err)
	}

	var affected int64
	if err := q.db.Write(ctx, func(tx *sql.Tx) error {
		res, xerr := tx.ExecContext(ctx, sqlReserveQuota, delta, user, delta)
		if xerr != nil {
			return xerr
		}
		affected, xerr = res.RowsAffected()
		return xerr
	}); err != nil {
		return false, fmt.Errorf("reserving quota for user %d: %w", user, err)
	}
	return affected == 1, nil
}

// Commit is a no-op that returns nil. Reserve already booked the bytes
// durably; this exists so the call site can say what it means.
func (q *Quota) Commit(context.Context, int64, uint64) error { return nil }

// Release credits bytes back, clamped at zero. A zero delta writes nothing
// and takes no write mutex. A negative one is a caller bug rather than a
// credit to book: the write path reserves and the delete path credits, so a
// negative value arriving here means the two were confused.
func (q *Quota) Release(ctx context.Context, user int64, delta int64) error {
	switch {
	case delta == 0:
		return nil
	case delta < 0:
		return fmt.Errorf("releasing %d bytes for user %d: a release is never negative", delta, user)
	}
	if err := q.db.Write(ctx, func(tx *sql.Tx) error {
		_, xerr := tx.ExecContext(ctx, sqlReleaseQuota, delta, user)
		return xerr
	}); err != nil {
		return fmt.Errorf("releasing quota for user %d: %w", user, err)
	}
	return nil
}
