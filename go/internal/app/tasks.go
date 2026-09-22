//go:build linux

// The background work a running deployment does on its own.
//
// Every task the startup check requires is here, and each one either does the
// real work or records that this build has nothing to run for it. A task that
// satisfies the check without doing the work would be worse than a missing
// one: the check passes and the thing it guarantees does not happen. So the
// ones with nothing to call say so in a comment naming what is absent.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/objstore"
	runtimetasks "github.com/heavycaffeiner/stowcloud/go/internal/runtime/tasks"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/server"
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
		{
			Name:  "direct-transfer.sweep",
			Every: sweepInterval,
			Run:   e.sweepDirectTransfers,
		},

		{
			Name:  "search.maintenance",
			Every: probeInterval,
			Run:   e.recoverSearchIndex,
		},

		// The three below are required by the startup check and have nothing
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
			Name:  "watch.maintenance",
			Every: maintenanceInterval,
			// Watches are released when their subscriber disconnects; there
			// is no periodic collection to call.
			Run: func(context.Context) error { return nil },
		},
	}
}

// startTasks starts each recurring task once for this engine. The first pass
// runs immediately, so stale upload parts and unreachable shares do not wait
// through a full interval after every restart.
func (e *Engine) startTasks() {
	periodic := e.tasks()
	table := make([]runtimetasks.Task, len(periodic))
	for i, item := range periodic {
		table[i] = runtimetasks.Task{Name: item.Name, Every: item.Every, Run: item.Run}
	}
	e.maintenance.Start(table)
}

// stopTasks cancels recurring work and waits before any service or database it
// may be using is closed.
func (e *Engine) stopTasks() {
	if e.maintenance == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobDrainTimeout)
	defer cancel()
	if err := e.maintenance.Stop(ctx); err != nil {
		e.logger.Warn("stopping maintenance tasks failed", "error", err)
	}
}

// now is the current time in nanoseconds, from the engine's clock.
func (e *Engine) now() int64 { return e.clk().Now().UnixNano() }

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
		e.markSearchIndexIncomplete()
		e.logger.Warn("share roots became unreachable", "count", len(broke))
	}
	if len(healed) > 0 {
		for _, def := range healed {
			e.watchShare(def)
		}
		e.markSearchIndexIncomplete()
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

// sweepDirectTransfers aborts only the multipart upload recorded by each
// expired reservation, then marks the reservation and releases its quota.
func (e *Engine) sweepDirectTransfers(ctx context.Context) error {
	rows, err := e.State.ListExpiredDirectTransfers(ctx, e.now(), 100)
	if err != nil {
		return fmt.Errorf("listing expired direct transfers: %w", err)
	}
	for _, row := range rows {
		if r, rerr := e.resolve(core.UserID(row.Owner), row.Path, acl.Write|acl.Create); rerr == nil {
			if provider, ok := directProvider(r.Root()); ok && provider.DirectTransfer() {
				if aerr := provider.AbortMultipart(ctx, row.ObjectKey, row.UploadID); aerr != nil && !errors.Is(aerr, objstore.ErrDirectTransferUnsupported) {
					e.logger.Warn("aborting expired direct transfer failed", "error", aerr)
					continue
				}
			}
		}
		if xerr := e.State.ExpireDirectTransfer(ctx, row.ID, e.now()); xerr != nil {
			continue
		}
		if quota, qerr := e.State.ReleaseDirectTransferQuota(ctx, row.ID); qerr == nil && quota > 0 {
			if relAmount, nerr := num.Narrow[int64](quota); nerr == nil {
				if relErr := state.NewQuota(e.State).Release(ctx, row.Owner, relAmount); relErr != nil {
					e.logger.Warn("releasing direct transfer quota failed", "error", relErr)
				}
			}
		}
	}
	return nil
}
