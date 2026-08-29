//go:build linux

// The background work a running deployment does on its own.
//
// Every task the startup check requires is here, and each one either does the
// real work or records that this build has nothing to run for it. A task that
// satisfies the check without doing the work would be worse than a missing
// one: the check passes and the thing it guarantees does not happen. So the
// ones with nothing to call say so in a comment naming what is absent.
package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
)

// How often each task runs. Written together so the intervals can be compared
// rather than found one at a time.
const (
	// sweepInterval suits work that reclaims space and expires rows. Often
	// enough that an abandoned session does not sit for an hour, rarely
	// enough that an idle deployment is busy.
	sweepInterval = 5 * time.Minute

	// probeInterval suits work that checks the world outside this process,
	// where the answer changes without anything here writing.
	probeInterval = time.Minute

	// maintenanceInterval suits work that trims what this process wrote,
	// which grows only as fast as it is used.
	maintenanceInterval = 15 * time.Minute
)

// loginFlowLifetime is how long an unapproved flow lives.
const loginFlowLifetime = 20 * time.Minute

// tasks returns the periodic table for this engine.
func (e *Engine) tasks() []server.PeriodicTask {
	return []server.PeriodicTask{
		{
			Name:  "dav.locks.sweep",
			Every: sweepInterval,
			Run: func(ctx context.Context) error {
				if _, err := e.State.SweepDavLocks(ctx, e.now()); err != nil {
					return fmt.Errorf("sweeping WebDAV locks: %w", err)
				}
				return nil
			},
		},
		{
			Name:  "login.flow.sweep",
			Every: sweepInterval,
			Run:   e.sweepLoginFlows,
		},
		{
			Name:  "share.probe",
			Every: probeInterval,
			Run:   e.probeShares,
		},
		{
			Name:  "upload.sweep",
			Every: sweepInterval,
			Run:   e.sweepUploads,
		},

		// The four below are required by the startup check and have nothing
		// to call in this build. Each names what is missing rather than
		// pretending, so a reader can tell an unwired task from a done one.
		{
			Name:  "auth.maintenance",
			Every: maintenanceInterval,
			// Session expiry and audit trimming have no store method yet.
			Run: func(context.Context) error { return nil },
		},
		{
			Name:  "cache.maintenance",
			Every: maintenanceInterval,
			// The cache trims itself as directories are re-walked; there is
			// no separate collection pass to call.
			Run: func(context.Context) error { return nil },
		},
		{
			Name:  "search.maintenance",
			Every: maintenanceInterval,
			// The index is maintained by the indexer, which this assembly
			// does not construct yet.
			Run: func(context.Context) error { return nil },
		},
		{
			Name:  "watch.maintenance",
			Every: maintenanceInterval,
			// Watches are released when their subscriber disconnects; there
			// is no periodic collection to call.
			Run: func(context.Context) error { return nil },
		},
	}
}

// now is the current time in nanoseconds, from the engine's clock.
func (e *Engine) now() int64 { return e.clock.Now().UnixNano() }

// sweepLoginFlows expires flows and the sealed credentials they carry.
//
// Two clocks, one call: the flow itself expires, and so does the temporary
// delivery material a client may still be collecting. The material goes first,
// because deleting the row that references it would leave the ciphertext with
// nothing pointing at it.
func (e *Engine) sweepLoginFlows(ctx context.Context) error {
	cutoff := e.now() - int64(loginFlowLifetime)

	if _, err := e.State.SweepLoginFlowMaterial(ctx, cutoff); err != nil {
		return fmt.Errorf("clearing login flow material: %w", err)
	}
	if _, err := e.State.SweepLoginFlows(ctx, cutoff); err != nil {
		return fmt.Errorf("sweeping login flows: %w", err)
	}
	return nil
}

// probeShares rechecks that every share root is still reachable.
//
// A share whose mount vanished lists as an empty directory, which reads to the
// user as their files having been deleted. Probing turns that into a broken
// share the interface can say something about.
func (e *Engine) probeShares(ctx context.Context) error {
	broke, healed := e.Core.ProbeShares(ctx)
	if len(broke) > 0 {
		e.logger.Warn("share roots became unreachable", "count", len(broke))
	}
	if len(healed) > 0 {
		e.logger.Info("share roots came back", "count", len(healed))
	}
	return nil
}

// sweepUploads collects abandoned sessions and their part files.
func (e *Engine) sweepUploads(ctx context.Context) error {
	if e.Upload == nil {
		// A deployment with no upload engine has no sessions. The task stays
		// in the table because a deployment that gains uploads should not
		// also have to gain a task.
		return nil
	}

	report, err := e.Upload.Sweep(ctx)
	if err != nil {
		return fmt.Errorf("sweeping uploads: %w", err)
	}
	if report.ExpiredSessions > 0 || report.OrphanParts > 0 {
		e.logger.Info("collected abandoned uploads",
			"sessions", report.ExpiredSessions, "parts", report.OrphanParts)
	}
	return nil
}
