//go:build linux

package search

import (
	"context"
	"errors"
	"net/http"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/search/controller"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
)

type indexEstimateOutput struct{ Body IndexEstimateView }
type indexStatusOutput struct{ Body IndexStatusView }
type indexBuildOutput struct {
	Status int
	Body   files.OperationView
}

func (m *Manager) IndexEstimate(ctx context.Context, _ *struct{}) (*indexEstimateOutput, error) {
	result, estimate, err := m.Controller.Estimate(ctx)
	if err != nil {
		return nil, err
	}
	return &indexEstimateOutput{Body: IndexEstimateOf(result, estimate)}, nil
}

func (m *Manager) IndexStatus(context.Context, *struct{}) (*indexStatusOutput, error) {
	return &indexStatusOutput{Body: IndexStatusOf(m.Controller.IndexState())}, nil
}

func (m *Manager) IndexBuild(ctx context.Context, _ *struct{}) (*indexBuildOutput, error) {
	owner, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	if m.Controller == nil {
		return nil, apierr.AsClassified(apierr.SubsystemUnavailable, "")
	}
	op, err := m.Controller.StartIndexBuild(ctx, owner)
	switch {
	case errors.Is(err, controller.ErrIndexDisabled):
		return nil, apierr.AsClassified(apierr.SubsystemUnavailable, "search.index_disabled")
	case errors.Is(err, controller.ErrIndexBuilding):
		return nil, apierr.AsClassified(apierr.Conflict, "search.index_building")
	case err != nil:
		return nil, err
	}
	return &indexBuildOutput{Status: http.StatusAccepted, Body: files.OperationOf(op)}, nil
}
