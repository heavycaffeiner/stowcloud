//go:build linux

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/concurrency"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/database/state"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
)

const (
	operationLeaseDuration = 30 * time.Second
	operationPollInterval  = 500 * time.Millisecond
	operationMaxWorkers    = 2
)

// copyJobPayload is the complete, server-owned description needed to resume a
// copy after a process restart. Paths are share-relative and were validated
// before this value was persisted; the runner validates them again on read.
type copyJobPayload struct {
	Owner       int64  `json:"owner"`
	FromShare   uint32 `json:"from_share"`
	FromPath    string `json:"from_path"`
	ToShare     uint32 `json:"to_share"`
	ToPath      string `json:"to_path"`
	SourceKind  uint8  `json:"source_kind"`
	Overwriting bool   `json:"overwriting"`
}

type operationOutcome struct {
	state       state.OpState
	progress    int64
	message     string
	errorKey    string
	errorDetail string
	results     []state.OpResult
	retry       bool
	pause       bool
}

// StartJobs starts the durable dispatcher once after recovering stale leases.
func (c *Core) StartJobs() {
	c.jobStartOnce.Do(func() {
		if c.jobsCtx == nil {
			c.jobsCtx, c.jobsStop = context.WithCancel(context.Background())
		}
		if _, err := c.state.RecoverExpiredOps(c.jobsCtx, c.clk.Nanos()); err != nil && c.jobsCtx.Err() == nil {
			c.warn("recovering expired operation leases failed", "error", err)
		}
		c.jobSlots = make(chan struct{}, operationMaxWorkers)
		c.jobs.Go(c.jobsCtx, "core: durable operation dispatcher", c.dispatchJobs)
	})
}

func (c *Core) dispatchJobs() {
	for {
		select {
		case <-c.jobsCtx.Done():
			return
		default:
		}

		now := c.clk.Nanos()
		runnable, err := c.state.HasRunnableOp(c.jobsCtx, now)
		if err != nil {
			if c.jobsCtx.Err() == nil {
				c.warn("checking for durable operations failed", "error", err)
				if !c.waitForJobPoll() {
					return
				}
			}
			continue
		}
		if !runnable {
			if !c.waitForJobPoll() {
				return
			}
			continue
		}

		select {
		case c.jobSlots <- struct{}{}:
		case <-c.jobsCtx.Done():
			return
		}
		claim, err := c.state.ClaimNextOp(c.jobsCtx, now, int64(operationLeaseDuration))
		if errors.Is(err, state.ErrNoRunnableOp) {
			<-c.jobSlots
			continue
		}
		if err != nil {
			<-c.jobSlots
			if c.jobsCtx.Err() == nil {
				c.warn("claiming a durable operation failed", "error", err)
				if !c.waitForJobPoll() {
					return
				}
			}
			continue
		}
		job := claim
		c.jobs.Go(c.jobsCtx, "core: durable operation", func() {
			defer func() { <-c.jobSlots }()
			c.runClaim(job)
		})
	}
}

func (c *Core) waitForJobPoll() bool {
	t := time.NewTimer(operationPollInterval)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-c.jobsCtx.Done():
		return false
	}
}

func (c *Core) runClaim(claim state.OpClaim) {
	ctx, cancel := context.WithCancel(c.jobsCtx)
	defer cancel()

	var leaseLost atomic.Bool
	renewDone := make(chan struct{})
	concurrency.Go(ctx, "core: operation lease renewer", func() {
		defer close(renewDone)
		t := time.NewTicker(operationLeaseDuration / 3)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				until := c.clk.Nanos() + int64(operationLeaseDuration)
				if err := c.state.RenewOpLease(ctx, claim.Op.ID, claim.LeaseID, until); err != nil {
					if !errors.Is(err, state.ErrOpLostLease) && ctx.Err() == nil {
						c.warn("renewing an operation lease failed", "operation", claim.Op.ID, "error", err)
					}
					leaseLost.Store(true)
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	})

	outcome := c.executeClaim(ctx, claim, &leaseLost)
	cancel()
	<-renewDone
	if leaseLost.Load() {
		return
	}
	if c.jobsStopped() && outcome.state != state.OpDone && outcome.state != state.OpFailed {
		outcome = operationOutcome{state: state.OpInterrupted, progress: outcome.progress, message: "interrupted by server shutdown", errorKey: "interrupted"}
	}
	c.completeClaim(claim, outcome)
}

func (c *Core) executeClaim(ctx context.Context, claim state.OpClaim, leaseLost *atomic.Bool) operationOutcome {
	if claim.Op.Cancellation {
		return operationOutcome{state: state.OpCancelled, message: "cancelled", errorKey: "cancelled"}
	}
	if claim.Op.PauseRequested {
		return operationOutcome{pause: true}
	}

	switch claim.Op.Kind {
	case state.OpCopy:
		return c.executeCopyClaim(ctx, claim, leaseLost)
	default:
		return operationOutcome{
			state: state.OpFailed, message: "operation kind is not supported", errorKey: "unsupported_operation",
			errorDetail: fmt.Sprintf("unsupported operation kind %d", claim.Op.Kind),
		}
	}
}

func (c *Core) executeCopyClaim(ctx context.Context, claim state.OpClaim, leaseLost *atomic.Bool) operationOutcome {
	var payload copyJobPayload
	if err := json.Unmarshal(claim.Payload, &payload); err != nil {
		return operationOutcome{state: state.OpFailed, message: "operation payload is invalid", errorKey: "payload_invalid", errorDetail: err.Error()}
	}
	if payload.Owner != claim.Op.User || payload.FromPath == "" || payload.ToPath == "" || payload.FromShare == 0 || payload.ToShare == 0 {
		return operationOutcome{state: state.OpFailed, message: "operation payload is invalid", errorKey: "payload_invalid", errorDetail: "copy payload has invalid ownership or path fields"}
	}
	from, err := c.resolveStoredPath(payload.Owner, payload.FromShare, payload.FromPath, acl.Read|acl.Download)
	if err != nil {
		return permanentOutcome(err)
	}
	to, err := c.resolveStoredPath(payload.Owner, payload.ToShare, payload.ToPath, acl.Write|acl.Create)
	if err != nil {
		return permanentOutcome(err)
	}
	st, err := from.root.Stat(from.path)
	if err != nil {
		return operationErrorOutcome(mapVFSErr(err))
	}
	if uint8(st.Kind) != payload.SourceKind {
		return operationOutcome{state: state.OpFailed, message: "source changed before the operation ran", errorKey: "precondition", errorDetail: "source kind differs from the queued operation"}
	}

	if serr := c.state.StartOpItem(ctx, claim.Op.ID, 0); serr != nil && !errors.Is(serr, context.Canceled) {
		c.warn("marking a durable copy item as started failed", "operation", claim.Op.ID, "error", serr)
	}
	if perr := c.state.SetOpProgressLease(ctx, claim.Op.ID, claim.LeaseID, 0, "copying", c.clk.Nanos()); perr != nil && !errors.Is(perr, state.ErrOpLostLease) {
		c.warn("recording durable copy progress failed", "operation", claim.Op.ID, "error", perr)
	}
	err = c.copyTreeStaged(ctx, from, to, st, acl.Read|acl.Download, c.leaseCancelGate(ctx, claim.Op.ID, claim.LeaseID), payload.Overwriting)
	if errors.Is(err, errOpCancelled) {
		op, _, gerr := c.state.GetOp(context.Background(), claim.Op.ID)
		if gerr == nil && op.PauseRequested {
			return operationOutcome{pause: true}
		}
		if gerr == nil && op.Cancellation {
			return operationOutcome{state: state.OpCancelled, message: "cancelled", errorKey: "cancelled"}
		}
		if c.jobsStopped() || ctx.Err() != nil {
			return operationOutcome{state: state.OpInterrupted, progress: 0, message: "interrupted by server shutdown", errorKey: "interrupted"}
		}
		return operationOutcome{state: state.OpCancelled, message: "cancelled", errorKey: "cancelled"}
	}
	if err != nil {
		out := operationErrorOutcome(err)
		if len(out.results) == 1 {
			out.results[0].Operation = claim.Op.ID
			out.results[0].Path = payload.ToPath
		}
		return out
	}
	return operationOutcome{
		state: state.OpDone, progress: 1,
		results: []state.OpResult{{Operation: claim.Op.ID, Idx: 0, Path: payload.ToPath, OK: true, Reason: state.ReasonItemOk}},
	}
}

func (c *Core) completeClaim(claim state.OpClaim, outcome operationOutcome) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	now := c.clk.Nanos()
	if outcome.pause {
		if err := c.state.RetryOpLease(ctx, claim.Op.ID, claim.LeaseID, now, now, "paused", "paused at an item boundary"); err != nil {
			if !errors.Is(err, state.ErrOpLostLease) {
				c.warn("pausing a durable operation failed", "operation", claim.Op.ID, "error", err)
			}
			return
		}
		if err := c.state.PauseOp(ctx, claim.Op.ID); err != nil && !errors.Is(err, state.ErrOpLostLease) {
			c.warn("recording a durable operation pause failed", "operation", claim.Op.ID, "error", err)
		}
		return
	}
	if outcome.retry && (claim.Op.MaxAttempts == 0 || claim.Op.Attempt < claim.Op.MaxAttempts) {
		delay := retryDelay(claim.Op.Attempt)
		if err := c.state.RetryOpLease(ctx, claim.Op.ID, claim.LeaseID, now+int64(delay), now, outcome.errorKey, outcome.errorDetail); err != nil && !errors.Is(err, state.ErrOpLostLease) {
			c.warn("requeueing a durable operation failed", "operation", claim.Op.ID, "error", err)
		}
		return
	}
	if outcome.state == state.OpCancelled {
		if err := c.state.CancelOpLease(ctx, claim.Op.ID, claim.LeaseID, outcome.progress, now); err != nil && !errors.Is(err, state.ErrOpLostLease) {
			c.warn("recording a durable operation cancellation failed", "operation", claim.Op.ID, "error", err)
		}
		return
	}
	if err := c.state.FinishOpLease(ctx, claim.Op.ID, claim.LeaseID, outcome.state, outcome.progress, outcome.message, outcome.errorKey, outcome.errorDetail, now, outcome.results); err != nil && !errors.Is(err, state.ErrOpLostLease) {
		c.warn("recording a durable operation outcome failed", "operation", claim.Op.ID, "error", err)
	}
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := time.Second
	for i := 1; i < attempt && d < 5*time.Minute; i++ {
		d *= 5
	}
	if d > 5*time.Minute {
		return 5 * time.Minute
	}
	return d
}

func operationErrorOutcome(err error) operationOutcome {
	key := operationErrorKey(err)
	return operationOutcome{
		state: state.OpFailed, message: "operation failed", errorKey: key,
		errorDetail: err.Error(), retry: retryableOperationError(err),
		results: []state.OpResult{{Idx: 0, OK: false, Reason: operationResultReason(err), Text: err.Error()}},
	}
}

func permanentOutcome(err error) operationOutcome {
	out := operationErrorOutcome(err)
	out.retry = false
	return out
}

func operationErrorKey(err error) string {
	switch {
	case errors.Is(err, ErrDenied):
		return "denied"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrConflict), errors.Is(err, ErrExists):
		return "conflict"
	case errors.Is(err, ErrPrecondition):
		return "precondition"
	case errors.Is(err, ErrQuotaExceeded), errors.Is(err, ErrNoSpace):
		return "quota"
	default:
		return "failed"
	}
}

func operationResultReason(err error) state.OpResultReason {
	switch {
	case errors.Is(err, ErrDenied):
		return state.ReasonItemDenied
	case errors.Is(err, ErrNotFound):
		return state.ReasonItemNotFound
	case errors.Is(err, ErrConflict), errors.Is(err, ErrExists):
		return state.ReasonItemConflict
	default:
		return state.ReasonItemFailed
	}
}

func retryableOperationError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	return !errors.Is(err, ErrDenied) && !errors.Is(err, ErrNotFound) &&
		!errors.Is(err, ErrConflict) && !errors.Is(err, ErrExists) &&
		!errors.Is(err, ErrPrecondition) && !errors.Is(err, ErrUnprocessable) &&
		!errors.Is(err, ErrQuotaExceeded)
}

func (c *Core) leaseCancelGate(ctx context.Context, id int64, leaseID string) func() bool {
	return func() bool {
		if c.jobsStopped() || ctx.Err() != nil {
			return true
		}
		op, _, err := c.state.GetOp(ctx, id)
		if err != nil || op.LeaseID != leaseID {
			return true
		}
		return op.Cancellation || op.PauseRequested
	}
}

func (c *Core) resolveStoredPath(owner int64, share uint32, raw string, need acl.Perms) (Resolved, error) {
	sp, err := vfs.ParseSharePath(raw)
	if err != nil {
		return Resolved{}, errf(ErrUnprocessable, "stored operation path is invalid")
	}
	path, err := sp.Safe()
	if err != nil {
		return Resolved{}, errf(ErrUnprocessable, "stored operation path is invalid")
	}
	entry, ok := c.shareEntry(ShareID(share))
	if !ok || entry.root == nil {
		return Resolved{}, ErrNotFound
	}
	at := acl.Vpath{Share: int64(share), Path: aclPath(path)}
	if !c.acl.Evaluate(owner, at, need).Allowed {
		return Resolved{}, ErrDenied
	}
	root, rerr := restrictScopedRoot(entry.root, path.Len() > 0)
	if rerr != nil {
		return Resolved{}, mapVFSErr(rerr)
	}
	return Resolved{user: UserID(owner), share: ShareID(share), root: root, path: path, perms: c.acl.Effective(owner, at)}, nil
}
