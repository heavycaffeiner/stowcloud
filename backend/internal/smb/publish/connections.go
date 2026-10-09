//go:build linux

package publish

import (
	"context"
	"slices"

	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
	"github.com/heavycaffeiner/stowcloud/backend/internal/smb"
	"github.com/heavycaffeiner/stowcloud/backend/internal/smb/agent"
)

// Connections compares current authority with the last daemon report. No
// credential material, host path, or private share owned by somebody else leaves it.
func (p *Publisher) Connections(ctx context.Context, owner int64) (smb.SMBConnectionsView, error) {
	settings := p.deps.Settings(ctx)
	view := smb.SMBConnectionsView{Enabled: settings.Config.Enabled, Server: settings.Config.ServerName, Folders: []smb.SMBFolderView{}}
	row, err := p.deps.Auth.UserByID(ctx, owner)
	if err != nil {
		return view, err
	}
	state, err := p.deps.Auth.SMBStateOf(ctx, owner)
	if err != nil {
		return view, err
	}
	d, err := p.exportDeps(ctx, settings)
	if err != nil {
		return view, err
	}
	exports, err := shareDefs(ctx, d)
	if err != nil {
		return view, err
	}
	report := p.LastReport()
	globalReason := ""
	switch {
	case !view.Enabled:
		globalReason = "server_disabled"
	case !settings.Configured || settings.Socket == "" || report == nil || report.Smbd == agent.ActionFailed || report.Smbd == agent.ActionStopped:
		globalReason = "server_unavailable"
	case state.Credential == auth.SMBCredentialNone:
		globalReason = "account_unavailable"
	case slices.Contains(report.MissingPassdb, row.Name):
		globalReason = "credential_not_applied"
	}
	roots := p.deps.Core.Roots(files.UserID(owner))
	hasHome := false
	for _, root := range roots {
		id, err := number.Narrow[uint32](root.Share)
		if err != nil {
			continue
		}
		personal := files.IsHomeShare(files.ShareID(id))
		folder := smb.SMBFolderView{Label: root.Label, Personal: personal}
		hasHome = hasHome || personal
		var expected *smb.ShareDef
		for i := range exports {
			export := &exports[i]
			if !slices.Contains(export.ValidUsers, row.Name) || export.Private != personal {
				continue
			}
			if personal {
				expected = export
				break
			}
			if def, ok := p.deps.Core.Share(files.ShareID(id)); ok && export.Name == def.Name {
				expected = export
				break
			}
		}
		if expected != nil {
			folder.Share = expected.Name
		}
		switch {
		case globalReason != "":
			folder.Reason = globalReason
		case root.BrokenReason != "":
			folder.Reason = "folder_unavailable"
		case personal && expected == nil:
			folder.Reason = "home_not_ready"
		case !personal && root.Subpath.Len() != 0:
			folder.Reason = "subpath_permissions"
		case expected == nil:
			folder.Reason = "not_supported"
		case !slices.Contains(report.Shares, expected.Name):
			folder.Reason = "not_applied"
		case slices.Contains(report.MissingPaths, expected.Path):
			folder.Reason = "path_not_mounted"
		default:
			folder.Available = true
		}
		view.Folders = append(view.Folders, folder)
	}
	if settings.HomesEnabled && !hasHome {
		view.Folders = append(view.Folders, smb.SMBFolderView{Label: "Home", Personal: true, Reason: "home_not_ready"})
	}
	return view, nil
}
