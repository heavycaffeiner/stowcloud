package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
)

// ErrLockConflict reports a lock another one already covers.
var ErrLockConflict = errors.New("a conflicting lock is held")

// The two lock scopes, as stored.
const (
	// LockShared admits other shared locks and no exclusive one.
	LockShared int64 = 0
	// LockExclusive admits nothing else on the same resource.
	LockExclusive int64 = 1
)

// The two depths a lock may take.
const (
	// LockDepthZero covers only the named resource.
	LockDepthZero int64 = 0
	// LockDepthInfinity covers the resource and everything under it.
	LockDepthInfinity int64 = -1
)

// AdmitDavLock takes a lock only if nothing live conflicts with it.
//
// The expiry sweep, the conflict scan, the per-principal count and the insert
// all run inside one serialized write transaction. Split across separate
// statements, two conflicting exclusive LOCKs arriving together would each
// scan before either inserted and both would be granted.
//
// A unique index cannot express this. Shared locks coexist on one path, so
// (share, path) is legitimately not unique, and a constraint that allowed the
// duplicates shared locks need would also allow the ones exclusive locks must
// not have.
func (d *DB) AdmitDavLock(ctx context.Context, l DavLock, nowNs int64) error {
	if err := d.f.EnsureWritable(); err != nil {
		return err
	}
	dev, ino, present, btime := l.Ident.ToSQL()

	return d.Write(ctx, func(tx *sql.Tx) error {
		// Two guards against an expired lock, and either alone is enough:
		// removing just one leaves the behaviour unchanged, removing both
		// lets a dead lock refuse a live request. The delete is what keeps
		// the table from growing without bound between periodic sweeps; the
		// deadline on the read below is what makes the decision correct even
		// if the delete were ever removed.
		if _, err := tx.ExecContext(ctx, sqlSweepDavLocks, nowNs); err != nil {
			return fmt.Errorf("clearing expired locks: %w", err)
		}

		live, err := liveLocksInShare(ctx, tx, int64(l.Ident.Share), nowNs)
		if err != nil {
			return err
		}
		for _, held := range live {
			if LocksConflict(held, l) {
				return ErrLockConflict
			}
		}

		var n int64
		if cerr := tx.QueryRowContext(ctx, sqlCountDavLocks, l.Principal, nowNs).Scan(&n); cerr != nil {
			return fmt.Errorf("counting locks: %w", cerr)
		}
		if n >= limits.DavLocksPerUser {
			return limits.Exceed("dav locks per user", limits.DavLocksPerUser, n+1)
		}

		_, err = tx.ExecContext(ctx, sqlInsertDavLock,
			l.Token, int64(l.Ident.Share), dev, ino, present, btime,
			l.Path, l.Principal, l.Owner, l.Depth, l.Scope, l.ExpiresNs, l.TimeoutS)
		if err != nil {
			return fmt.Errorf("recording the lock: %w", err)
		}
		return nil
	})
}

// liveLocksInShare reads the rows a new lock could collide with.
func liveLocksInShare(ctx context.Context, tx *sql.Tx, share, nowNs int64) (out []DavLock, err error) {
	rows, err := tx.QueryContext(ctx, sqlLiveDavLocksInShare, share, nowNs)
	if err != nil {
		return nil, fmt.Errorf("reading live locks: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()

	for rows.Next() {
		l, serr := scanDavLock(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading live locks: %w", err)
	}
	return out, nil
}

// LocksConflict reports whether a held lock blocks a requested one.
//
// Two shared locks coexist. Anything involving an exclusive lock conflicts,
// but only where the two actually overlap: the same path, a depth-infinity
// ancestor covering the request, or a descendant of a requested depth-infinity
// lock.
func LocksConflict(held, want DavLock) bool {
	if held.Scope == LockShared && want.Scope == LockShared {
		return false
	}
	return locksOverlap(held, want)
}

// locksOverlap reports whether two locks address any resource in common.
func locksOverlap(held, want DavLock) bool {
	switch {
	case held.Path == want.Path:
		return true
	case held.Depth == LockDepthInfinity && pathCovers(held.Path, want.Path):
		return true
	case want.Depth == LockDepthInfinity && pathCovers(want.Path, held.Path):
		return true
	default:
		return false
	}
}

// pathCovers reports whether ancestor contains descendant.
//
// The boundary is a "/" and not a prefix: without it "/a" would cover "/ab",
// so locking one file would lock every sibling whose name begins with it.
func pathCovers(ancestor, descendant string) bool {
	if ancestor == descendant {
		return true
	}
	if ancestor == "/" {
		return true
	}
	return strings.HasPrefix(descendant, strings.TrimSuffix(ancestor, "/")+"/")
}
