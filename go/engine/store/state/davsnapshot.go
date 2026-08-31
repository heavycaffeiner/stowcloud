package state

import (
	"context"
	"database/sql"
	"fmt"
)

// LockTarget names one resource a request addresses.
type LockTarget struct {
	// Share is the share the path belongs to.
	Share int64
	// Path is the virtual path.
	Path string
}

// TargetState is what one target held at snapshot time.
type TargetState struct {
	// Covering are the live locks that cover this target, whether by naming
	// it, by being a depth-infinity ancestor, or by being a descendant that a
	// depth-infinity request would reach.
	Covering []DavLock
}

// LockSnapshot is one consistent view of the lock table.
type LockSnapshot struct {
	// Targets is keyed by the target it was asked about.
	Targets map[LockTarget]TargetState
}

// Covering returns the locks over one target, or nothing.
func (s LockSnapshot) Covering(t LockTarget) []DavLock {
	return s.Targets[t].Covering
}

// SnapshotDavLocks reads every target's covering locks in one transaction.
//
// A mutating method checks its If header and its lock coverage against this
// one view. Reading the table twice, once per question, lets a lock appear
// between them: the If header is evaluated against a table without it and the
// coverage guard against a table with it, so a request is refused for a lock
// its own precondition was never allowed to name.
//
// The read runs inside a write transaction rather than against the pool. A
// plain read is not serialized against the writer, so two targets in one
// snapshot could otherwise come from either side of another admission.
func (d *DB) SnapshotDavLocks(ctx context.Context, targets []LockTarget, nowNs int64) (LockSnapshot, error) {
	snap := LockSnapshot{Targets: make(map[LockTarget]TargetState, len(targets))}
	if len(targets) == 0 {
		return snap, nil
	}

	// One query per distinct share, not per target: a request addressing many
	// paths in one share reads that share's locks once.
	shares := map[int64]bool{}
	for _, t := range targets {
		shares[t.Share] = true
	}

	err := d.Write(ctx, func(tx *sql.Tx) error {
		byShare := make(map[int64][]DavLock, len(shares))
		for share := range shares {
			live, err := liveLocksInShare(ctx, tx, share, nowNs)
			if err != nil {
				return err
			}
			byShare[share] = live
		}

		for _, t := range targets {
			var covering []DavLock
			for _, held := range byShare[t.Share] {
				if lockCovers(held, t.Path) {
					covering = append(covering, held)
				}
			}
			snap.Targets[t] = TargetState{Covering: covering}
		}
		return nil
	})
	if err != nil {
		return LockSnapshot{}, fmt.Errorf("snapshotting locks: %w", err)
	}
	return snap, nil
}

// lockCovers reports whether a held lock reaches a path.
func lockCovers(held DavLock, path string) bool {
	if held.Path == path {
		return true
	}
	return held.Depth == LockDepthInfinity && pathCovers(held.Path, path)
}
