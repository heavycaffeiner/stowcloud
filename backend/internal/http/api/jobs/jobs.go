//go:build linux

// Package jobs serves authenticated long-running operation routes.
package jobs

import (
	"context"
	"net/http"
	"strconv"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/humabridge"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

const jobsPageSize = 100

// Deps contains only the operation services and composition callbacks needed by
// these routes.
type Deps struct {
	Core      *core.Core
	State     *state.DB
	Owner     func(*gin.Context) (core.UserID, bool)
	StartJobs func()
	NowNs     func() int64
}

// ListInput is the typed request for listing the caller's jobs.
type ListInput struct{}

// ListOutput preserves the native array response shape.
type ListOutput struct {
	Body []handler.OperationView
}

type operationInput struct {
	ID string `path:"id"`
}

type operationOutput struct {
	Body handler.OperationView
}

type noContentOutput struct {
	Status int `status:"204"`
}

// Register mounts the six conventional jobs operations below the API prefix.
func Register(api huma.API, d Deps) {
	h := &handlers{d: d}
	huma.Register[ListInput, ListOutput](api, huma.Operation{
		OperationID: "jobs.list", Method: http.MethodGet, Path: "/jobs",
	}, h.listHuma)
	huma.Register[operationInput, operationOutput](api, huma.Operation{
		OperationID: "jobs.get", Method: http.MethodGet, Path: "/jobs/{id}",
	}, h.getHuma)
	huma.Register[operationInput, noContentOutput](api, huma.Operation{
		OperationID: "jobs.cancel", Method: http.MethodPost, Path: "/jobs/{id}/cancel",
	}, h.cancelHuma)
	huma.Register[operationInput, noContentOutput](api, huma.Operation{
		OperationID: "jobs.retry", Method: http.MethodPost, Path: "/jobs/{id}/retry",
	}, h.retryHuma)
	huma.Register[operationInput, noContentOutput](api, huma.Operation{
		OperationID: "jobs.pause", Method: http.MethodPost, Path: "/jobs/{id}/pause",
	}, h.pauseHuma)
	huma.Register[operationInput, noContentOutput](api, huma.Operation{
		OperationID: "jobs.resume", Method: http.MethodPost, Path: "/jobs/{id}/resume",
	}, h.resumeHuma)
}

type handlers struct{ d Deps }

func (h *handlers) listHuma(ctx context.Context, _ *ListInput) (*ListOutput, error) {
	c := humabridge.Gin(ctx)
	owner, ok := h.d.Owner(c)
	if !ok {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.AuthRequired})
	}
	ops, err := h.d.Core.ListOperations(ctx, owner, jobsPageSize)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	return &ListOutput{Body: handler.OperationsOf(ops)}, nil
}

func parseOperationInput(id string) (core.OperationID, bool) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return core.OperationID(n), true
}

func (h *handlers) operationOwner(ctx context.Context) (core.UserID, error) {
	c := humabridge.Gin(ctx)
	owner, ok := h.d.Owner(c)
	if !ok {
		return 0, humabridge.Refusal(apierr.Classified{Class: apierr.AuthRequired})
	}
	return owner, nil
}

func (h *handlers) getHuma(ctx context.Context, in *operationInput) (*operationOutput, error) {
	owner, err := h.operationOwner(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOperationInput(in.ID)
	if !ok {
		return nil, humabridge.Failure(ctx, core.ErrNotFound)
	}
	op, err := h.d.Core.Operation(ctx, owner, id)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	return &operationOutput{Body: handler.OperationOf(op)}, nil
}

func (h *handlers) cancelHuma(ctx context.Context, in *operationInput) (*noContentOutput, error) {
	owner, err := h.operationOwner(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOperationInput(in.ID)
	if !ok {
		return nil, humabridge.Failure(ctx, core.ErrNotFound)
	}
	if err := h.d.Core.CancelOperation(ctx, owner, id); err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	return &noContentOutput{Status: http.StatusNoContent}, nil
}

func (h *handlers) retryHuma(ctx context.Context, in *operationInput) (*noContentOutput, error) {
	owner, err := h.operationOwner(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOperationInput(in.ID)
	if !ok {
		return nil, humabridge.Failure(ctx, core.ErrNotFound)
	}
	op, err := h.d.Core.Operation(ctx, owner, id)
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	if op.State != state.OpFailed && op.State != state.OpInterrupted {
		return nil, humabridge.Refusal(apierr.Classified{Class: apierr.Unprocessable})
	}
	if err := h.d.State.ResumeOp(ctx, int64(id), h.d.NowNs()); err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	h.d.StartJobs()
	return &noContentOutput{Status: http.StatusNoContent}, nil
}

func (h *handlers) pauseHuma(ctx context.Context, in *operationInput) (*noContentOutput, error) {
	return h.pauseResumeHuma(ctx, in, true)
}

func (h *handlers) resumeHuma(ctx context.Context, in *operationInput) (*noContentOutput, error) {
	return h.pauseResumeHuma(ctx, in, false)
}

func (h *handlers) pauseResumeHuma(ctx context.Context, in *operationInput, pause bool) (*noContentOutput, error) {
	owner, err := h.operationOwner(ctx)
	if err != nil {
		return nil, err
	}
	id, ok := parseOperationInput(in.ID)
	if !ok {
		return nil, humabridge.Failure(ctx, core.ErrNotFound)
	}
	if _, err = h.d.Core.Operation(ctx, owner, id); err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	if pause {
		err = h.d.State.PauseOp(ctx, int64(id))
	} else {
		err = h.d.State.ResumeOp(ctx, int64(id), h.d.NowNs())
	}
	if err != nil {
		return nil, humabridge.Failure(ctx, err)
	}
	h.d.StartJobs()
	return &noContentOutput{Status: http.StatusNoContent}, nil
}

var _ interface {
	ListOperations(context.Context, core.UserID, int) ([]core.Operation, error)
} = (*core.Core)(nil)
