//go:build linux

// Package jobs serves the caller's long-running operations.
package jobs

import (
	"context"
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/backend/internal/db/state"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
)

const jobsPageSize = 100

// Handler serves the caller's long-running operations.
type Handler struct {
	Core      *files.Core
	State     *state.DB
	StartJobs func()
	NowNs     func() int64
}

// ListInput is the typed request for listing the caller's jobs.
type ListInput struct{}

// ListOutput preserves the native array response shape.
type ListOutput struct {
	Body []handler.OperationView
}

// OperationInput names one operation.
type OperationInput struct {
	ID string `path:"id"`
}

// OperationOutput is one operation.
type OperationOutput struct {
	Body handler.OperationView
}

// NoContentOutput is an empty 204.
type NoContentOutput struct {
	Status int `status:"204"`
}

// List answers the caller's most recent operations.
func (h *Handler) List(ctx context.Context, _ *ListInput) (*ListOutput, error) {
	owner, err := handler.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	ops, err := h.Core.ListOperations(ctx, owner, jobsPageSize)
	if err != nil {
		return nil, err
	}
	return &ListOutput{Body: handler.OperationsOf(ops)}, nil
}

func parseOperationInput(id string) (files.OperationID, bool) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return files.OperationID(n), true
}

// Get answers one operation.
func (h *Handler) Get(ctx context.Context, in *OperationInput) (*OperationOutput, error) {
	owner, err := handler.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOperationInput(in.ID)
	if !ok {
		return nil, files.ErrNotFound
	}
	op, err := h.Core.Operation(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	return &OperationOutput{Body: handler.OperationOf(op)}, nil
}

// Cancel stops an operation.
func (h *Handler) Cancel(ctx context.Context, in *OperationInput) (*NoContentOutput, error) {
	owner, err := handler.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOperationInput(in.ID)
	if !ok {
		return nil, files.ErrNotFound
	}
	if err := h.Core.CancelOperation(ctx, owner, id); err != nil {
		return nil, err
	}
	return &NoContentOutput{Status: http.StatusNoContent}, nil
}

// Retry restarts a failed or interrupted operation.
func (h *Handler) Retry(ctx context.Context, in *OperationInput) (*NoContentOutput, error) {
	owner, err := handler.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOperationInput(in.ID)
	if !ok {
		return nil, files.ErrNotFound
	}
	op, err := h.Core.Operation(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	if op.State != state.OpFailed && op.State != state.OpInterrupted {
		return nil, &apierr.ClassifiedError{Classified: apierr.Classified{Class: apierr.Unprocessable}}
	}
	if err := h.State.ResumeOp(ctx, int64(id), h.NowNs()); err != nil {
		return nil, err
	}
	h.StartJobs()
	return &NoContentOutput{Status: http.StatusNoContent}, nil
}

// Pause holds an operation.
func (h *Handler) Pause(ctx context.Context, in *OperationInput) (*NoContentOutput, error) {
	return h.pauseResume(ctx, in, true)
}

// Resume continues a held operation.
func (h *Handler) Resume(ctx context.Context, in *OperationInput) (*NoContentOutput, error) {
	return h.pauseResume(ctx, in, false)
}

func (h *Handler) pauseResume(ctx context.Context, in *OperationInput, pause bool) (*NoContentOutput, error) {
	owner, err := handler.OwnerFrom(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOperationInput(in.ID)
	if !ok {
		return nil, files.ErrNotFound
	}
	if _, err = h.Core.Operation(ctx, owner, id); err != nil {
		return nil, err
	}
	if pause {
		err = h.State.PauseOp(ctx, int64(id))
	} else {
		err = h.State.ResumeOp(ctx, int64(id), h.NowNs())
	}
	if err != nil {
		return nil, err
	}
	h.StartJobs()
	return &NoContentOutput{Status: http.StatusNoContent}, nil
}

var _ interface {
	ListOperations(context.Context, files.UserID, int) ([]files.Operation, error)
} = (*files.Core)(nil)
