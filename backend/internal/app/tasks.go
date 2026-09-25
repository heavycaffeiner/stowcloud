//go:build linux

// The application assembles recurring work over its feature services.
package app

import (
	"context"
	"fmt"

	directtransfer "github.com/heavycaffeiner/stowcloud/backend/internal/feature/directtransfer"
	filehttp "github.com/heavycaffeiner/stowcloud/backend/internal/http/api/files"
	runtimetasks "github.com/heavycaffeiner/stowcloud/backend/internal/runtime/tasks"
)

func (e *Engine) tasks() []runtimetasks.Task {
	return runtimetasks.Schedule(runtimetasks.Policy{
		Now: e.now,
		SweepDavLocks: func(ctx context.Context, now int64) error {
			_, err := e.State.SweepDavLocks(ctx, now)
			return err
		},
		SweepLoginFlowMaterial: func(ctx context.Context, cutoff int64) (int64, error) {
			return e.State.SweepLoginFlowMaterial(ctx, cutoff)
		},
		SweepLoginFlows: func(ctx context.Context, cutoff int64) (int64, error) {
			return e.State.SweepLoginFlows(ctx, cutoff)
		},
		ProbeShares:          e.probeShares,
		SweepUploads:         e.sweepUploads,
		SweepDirectTransfers: e.sweepDirectTransfers,
		RecoverSearch:        e.searchController.Recover,
	})
}

func (e *Engine) startTasks(items []runtimetasks.Task) {
	e.maintenance.Start(items)
}

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

// probeShares rechecks that every share root is still reachable.
//
// A share whose mount vanished lists as an empty directory, which reads to the
// user as their files having been deleted. Probing turns that into a broken
// share the interface can say something about.
func (e *Engine) probeShares(ctx context.Context) error {
	broke, healed := e.Core.ProbeShares(ctx)
	if len(broke) > 0 {
		e.searchController.MarkIncomplete()
		e.logger.Warn("share roots became unreachable", "count", len(broke))
	}
	if len(healed) > 0 {
		for _, def := range healed {
			e.watchShare(def)
		}
		e.searchController.MarkIncomplete()
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

// sweepDirectTransfers delegates durable transfer recovery to its feature owner.
func (e *Engine) sweepDirectTransfers(ctx context.Context) error {
	_, err := directtransfer.Sweep(ctx, e.State, e.now(), directtransfer.ProviderForRow(e.Core, filehttp.Resolve(e.Core)), e.log())
	return err
}
