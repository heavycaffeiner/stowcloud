//go:build linux

package handler

import (
	"context"
	"errors"
	"math"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/fs/objstore"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	secret "github.com/heavycaffeiner/stowcloud/backend/internal/platform/security/secret"
	"github.com/heavycaffeiner/stowcloud/backend/internal/uploads"
	uploadlimits "github.com/heavycaffeiner/stowcloud/backend/internal/uploads/limits"
)

// SessionDetailsDeps are the explicit application capabilities needed to
// project the deployment-specific portion of /auth/session.
type SessionDetailsDeps struct {
	Auth     *auth.Service
	Core     *files.Core
	Upload   *uploads.Engine
	Features FeaturesInputs
}

// FeaturesInputs are the live capabilities that determine which UI surfaces
// this deployment serves. Callbacks keep transport independent of app.Engine.
type FeaturesInputs struct {
	SMBEnabled     func() bool
	PreviewEnabled func() bool
	SearchHasIndex func() bool
}

// SessionDetailsOf projects deployment-specific account and capability state.
func SessionDetailsOf(ctx context.Context, id int64, d SessionDetailsDeps) (SessionDetails, error) {
	if d.Auth == nil || d.Core == nil {
		return SessionDetails{}, errors.New("session projection is not wired")
	}
	row, err := d.Auth.UserByID(ctx, id)
	if err != nil {
		return SessionDetails{}, err
	}
	smb, err := d.Auth.SMBStateOf(ctx, id)
	if err != nil {
		return SessionDetails{}, err
	}
	var oidc SessionOidcView
	if link, linkErr := d.Auth.OIDCLinkOf(ctx, id); linkErr == nil {
		oidc = SessionOidcOf(link)
	} else if !errors.Is(linkErr, auth.ErrNoOIDCLink) {
		return SessionDetails{}, linkErr
	}
	return SessionDetails{
		TOTPEnabled:          row.TOTPEnabled,
		SMBOptOut:            smb.OptOut,
		SMBEnabled:           smb.Enabled,
		SMBCredential:        string(smb.Credential),
		SMBUnavailableReason: smbUnavailableReason(smb),
		Oidc:                 oidc,
		Roots:                RootViews(d.Core, files.UserID(id)),
		Limits:               LimitsViewOf(d.Upload),
		Features:             FeaturesViewOf(d.Core, d.Features),
	}, nil
}

func smbUnavailableReason(smb auth.SMBState) string {
	if smb.Credential == auth.SMBCredentialNone {
		return string(smb.Reason)
	}
	return ""
}

// RootViews projects the account's reachable roots for the wire.
func RootViews(fc *files.Core, owner files.UserID) []RootView {
	if fc == nil {
		return []RootView{}
	}
	roots := fc.Roots(owner)
	out := make([]RootView, 0, len(roots))
	for _, root := range roots {
		out = append(out, RootView{
			Label: root.Label, Perms: files.PermNames(root.Perms),
			SharedExternally: root.SharedExternally, TrashEnabled: root.TrashEnabled,
			BrokenReason: root.BrokenReason,
		})
	}
	return out
}

const defaultUploadParallel = 4

// LimitsViewOf projects the live upload limits for a client plan.
func LimitsViewOf(engine *uploads.Engine) LimitsView {
	if engine == nil {
		return LimitsView{ChunkSize: uploadlimits.UploadChunkSizeDefault, ChunkMin: uploadlimits.UploadChunkMinDefault, Parallel: defaultUploadParallel}
	}
	minBytes, defaultBytes := engine.Settings().Snapshot()
	return LimitsView{ChunkSize: chunkBytes(defaultBytes), ChunkMin: chunkBytes(minBytes), Parallel: defaultUploadParallel}
}

func chunkBytes(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// FeaturesViewOf projects deployment capabilities without requiring an
// application Engine-shaped dependency.
func FeaturesViewOf(fc *files.Core, in FeaturesInputs) FeaturesView {
	directUploads := false
	if fc != nil {
		for _, share := range fc.Shares() {
			if share.BrokenReason != "" || share.Backend != files.BackendS3 {
				continue
			}
			if root, ok := fc.ShareRoot(share.ID); ok {
				if provider, ok := root.(objstore.DirectTransferProvider); ok && provider.DirectTransfer() {
					directUploads = true
					break
				}
			}
		}
	}
	search := "walk"
	if in.SearchHasIndex != nil && in.SearchHasIndex() {
		search = "name"
	}
	return FeaturesView{
		WebDAV:        true,
		SMB:           in.SMBEnabled != nil && in.SMBEnabled(),
		Preview:       in.PreviewEnabled != nil && in.PreviewEnabled(),
		Trash:         true,
		Shares:        true,
		Search:        search,
		DirectUploads: directUploads,
	}
}

// Reconfirm checks the account password for a sensitive settings operation.
func Reconfirm(c *gin.Context, service *auth.Service, owner int64, password string) bool {
	if password == "" {
		refuseTransport(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return false
	}
	ok, err := service.VerifyAccountPassword(c.Request.Context(), owner, secret.New([]byte(password)))
	if err != nil {
		failKnownTransport(c, err)
		return false
	}
	if !ok {
		refuseTransport(c, apierr.Classify(auth.ErrCredentials, apierr.VisibilityKnown))
		return false
	}
	return true
}

// Admin authorizes a session for an administrator-owned route.
func Admin(c *gin.Context, service *auth.Service) (int64, bool) {
	owner, ok := ownerTransport(c)
	if !ok {
		refuseTransport(c, apierr.Classified{Class: apierr.AuthRequired})
		return 0, false
	}
	isAdmin, err := service.IsAdmin(c.Request.Context(), owner)
	if err != nil {
		failKnownTransport(c, err)
		return 0, false
	}
	if !isAdmin {
		refuseTransport(c, apierr.Classified{Class: apierr.Denied})
		return 0, false
	}
	return owner, true
}
