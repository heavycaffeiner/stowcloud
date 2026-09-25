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
	"strings"

	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/state"
	num "github.com/heavycaffeiner/stowcloud/go/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/objstore"
	runtimetasks "github.com/heavycaffeiner/stowcloud/go/internal/runtime/tasks"
	directtransfer "github.com/heavycaffeiner/stowcloud/go/internal/transport/http/directtransfer"
	filehttp "github.com/heavycaffeiner/stowcloud/go/internal/transport/http/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/server"
)

func (e *Engine) tasks() []server.PeriodicTask {
	items := runtimetasks.Schedule(runtimetasks.Policy{
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
		RecoverSearch:        e.searchRuntime.Recover,
	})
	periodic := make([]server.PeriodicTask, len(items))
	for i, item := range items {
		periodic[i] = server.PeriodicTask{Name: item.Name, Every: item.Every, Run: item.Run}
	}
	return periodic
}

func (e *Engine) startTasks() {
	periodic := e.tasks()
	table := make([]runtimetasks.Task, len(periodic))
	for i, item := range periodic {
		table[i] = runtimetasks.Task{Name: item.Name, Every: item.Every, Run: item.Run}
	}
	e.maintenance.Start(table)
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
		e.searchRuntime.MarkIncomplete()
		e.logger.Warn("share roots became unreachable", "count", len(broke))
	}
	if len(healed) > 0 {
		for _, def := range healed {
			e.watchShare(def)
		}
		e.searchRuntime.MarkIncomplete()
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

// sweepDirectTransfers aborts only expired pending multipart uploads. A row in
// completing state represents an ambiguous provider commit and is reconciled by
// the completion endpoint rather than aborted by cleanup.
func (e *Engine) sweepDirectTransfers(ctx context.Context) error {
	rows, err := e.State.ListExpiredDirectTransfers(ctx, e.now(), 100)
	if err != nil {
		return fmt.Errorf("listing expired direct transfers: %w", err)
	}
	for _, row := range rows {
		if r, rerr := filehttp.Resolve(e.Core)(core.UserID(row.Owner), row.Path, acl.Write|acl.Create); rerr == nil {
			if provider, ok := directtransfer.Provider(r.Root()); ok && provider.DirectTransfer() {
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
	if err := e.reconcileDirectTransfers(ctx); err != nil {
		return err
	}

	return nil
}

// reconcileDirectTransfers finalizes publication intents after a restart.
// Provider metadata is the only success evidence; absent or mismatched objects
// remain completing and are never silently reported as complete.
func (e *Engine) reconcileDirectTransfers(ctx context.Context) error {
	rows, err := e.State.ListCompletingDirectTransfers(ctx, 100)
	if err != nil {
		return fmt.Errorf("listing completing direct transfers: %w", err)
	}
	for _, row := range rows {
		provider, ok, perr := directtransfer.ProviderForRow(e.Core, filehttp.Resolve(e.Core))(ctx, row)
		if perr != nil || !ok || !provider.DirectTransfer() {
			continue
		}
		size, etag, checksum, found, merr := provider.ObjectMetadata(ctx, row.ObjectKey)
		if merr != nil || !found || size != row.ExpectedSize || (row.ExpectedChecksum != "" && (checksum == "" || !strings.EqualFold(checksum, row.ExpectedChecksum))) {
			continue
		}
		if perr := e.State.PublishDirectTransfer(ctx, row.ID, row.Owner, size, etag, checksum, e.now()); perr != nil {
			e.logger.Warn("recording reconciled direct transfer failed", "error", perr)
			continue
		}
		if quota, qerr := e.State.ReleaseDirectTransferQuota(ctx, row.ID); qerr == nil && quota > row.PriorSize {
			if relAmount, nerr := num.Narrow[int64](quota - row.PriorSize); nerr == nil {
				if relErr := state.NewQuota(e.State).Release(ctx, row.Owner, relAmount); relErr != nil {
					e.logger.Warn("releasing reconciled direct transfer quota failed", "error", relErr)
				}
			}
		}
	}
	return nil
}
