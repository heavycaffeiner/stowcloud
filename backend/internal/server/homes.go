//go:build linux

package server

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/heavycaffeiner/stowcloud/backend/internal/admin"
	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
)

func (e *Engine) homesRoot(configured string) string {
	if configured != "" {
		return configured
	}
	return filepath.Join(e.dataDir, "homes")
}

func (e *Engine) validateHomes(ctx context.Context, body map[string]any) error {
	values := e.Settings.Values(ctx)
	if enabled, ok := body["enabled"].(bool); ok {
		values.HomesEnabled = enabled
	}
	if root, present := body["root"]; present {
		if root == nil {
			values.HomesRoot = ""
		} else if value, ok := root.(string); ok {
			values.HomesRoot = value
		}
	}
	if !values.HomesEnabled {
		return nil
	}
	return e.Core.ValidateHomesRoot(e.homesRoot(values.HomesRoot))
}

func (e *Engine) retryHome(ctx context.Context, user files.UserID) error {
	values := e.Settings.Values(ctx)
	if !values.HomesEnabled {
		return files.ErrDenied
	}
	e.applyHomes(ctx, values)
	if !e.homeView(ctx, int64(user)).Ready {
		return files.ErrShareBroken
	}
	return nil
}

func (e *Engine) homeView(ctx context.Context, id int64) auth.HomeView {
	status := e.Core.HomeStatusOf(ctx, files.UserID(id))
	enabled := e.Settings.Values(ctx).HomesEnabled
	reason := status.Reason
	if enabled && !status.Enabled {
		reason = "not_ready"
	}
	return auth.HomeView{Enabled: enabled, Ready: enabled && status.Ready, Reason: reason}
}

func (e *Engine) homesSummary(ctx context.Context) admin.HomeSummaryView {
	values := e.Settings.Values(ctx)
	view := admin.HomeSummaryView{Enabled: values.HomesEnabled}
	if !view.Enabled {
		return view
	}
	for _, def := range e.Core.Shares() {
		if files.IsHomeShare(def.ID) && def.BrokenReason == "" {
			view.Ready = true
		}
	}
	users, err := e.Auth.ListUsers(ctx)
	if err != nil {
		view.Ready = false
		return view
	}
	for _, user := range users {
		if !e.homeView(ctx, user.ID).Ready {
			view.Pending++
		}
	}
	if p := e.smbPublisherOf(); p != nil {
		if report := p.LastReport(); report != nil {
			prefix := filepath.Clean(e.homesRoot(values.HomesRoot)) + string(filepath.Separator)
			for _, path := range report.MissingPaths {
				if strings.HasPrefix(path, prefix) {
					view.SMBMissing++
				}
			}
		}
	}
	return view
}
