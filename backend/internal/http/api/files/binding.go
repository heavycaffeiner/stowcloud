//go:build linux

package files

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/fs/vfs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

// Resolve returns the one path gate used by authenticated file transports.
// Malformed paths and paths outside the caller's grants have the same answer.
func Resolve(c *files.Core) func(files.UserID, string, acl.Perms) (files.Resolved, error) {
	return func(owner files.UserID, raw string, need acl.Perms) (files.Resolved, error) {
		p, err := vfs.ParseVpath(raw)
		if err != nil {
			return files.Resolved{}, files.ErrNotFound
		}
		return c.Resolve(owner, p, need)
	}
}

// Decode applies the shared JSON body limit and decoder.
func Decode(c *gin.Context, into any) error {
	return middleware.DecodeJSON(c.Request.Body, into)
}

// OpenBoundClaim opens a claim for one purpose and binds it to the session
// account. A claim narrows an authenticated session; it never authenticates.
func OpenBoundClaim(key handler.ClaimKey, now func() int64) func(*gin.Context, handler.ClaimPurpose, files.UserID) (handler.Claim, bool) {
	return func(c *gin.Context, purpose handler.ClaimPurpose, owner files.UserID) (handler.Claim, bool) {
		value := c.Query("claim")
		if value == "" {
			return handler.Claim{}, false
		}
		if now == nil {
			return handler.Claim{}, false
		}
		claim, err := handler.OpenClaim(map[uint32][]byte{key.Version: key.Key}, purpose, value, now())
		if err != nil || claim.UserID != int64(owner) {
			return handler.Claim{}, false
		}
		return claim, true
	}
}

// Projection owns the wire projection of core entries and the claims each row
// carries. The application supplies only the deployment key, clock, and log.
type Projection struct {
	core *files.Core
	key  handler.ClaimKey
	now  func() int64
	log  *slog.Logger
}

// ProjectionDeps configures a file response projection.
type ProjectionDeps struct {
	Core     *files.Core
	ClaimKey handler.ClaimKey
	Now      func() int64
	Logger   *slog.Logger
}

// NewProjection constructs the entry projection used by file transports.
func NewProjection(d ProjectionDeps) *Projection {
	return &Projection{core: d.Core, key: d.ClaimKey, now: d.Now, log: d.Logger}
}

// EntryView projects one entry with its client path and sealed references.
func (p *Projection) EntryView(owner files.UserID, r files.Resolved, entry files.Entry) handler.EntryView {
	vpath := p.Vpath(owner, r, entry)
	return handler.EntryOf(entry, vpath, p.refs(owner, entry, vpath))
}

// Vpath returns the client-facing path for an entry.
func (p *Projection) Vpath(owner files.UserID, r files.Resolved, entry files.Entry) string {
	vp, err := p.core.VpathFor(owner, r.Share(), entry.Path)
	if err != nil {
		return entry.Path.String()
	}
	return vp.String()
}

// Refs returns the per-row reference sealer for one account.
func (p *Projection) Refs(owner files.UserID) func(files.Entry, string) handler.EntryRefs {
	return func(entry files.Entry, vpath string) handler.EntryRefs {
		return p.refs(owner, entry, vpath)
	}
}

func (p *Projection) refs(user files.UserID, entry files.Entry, vpath string) handler.EntryRefs {
	if entry.IsDir || vpath == "" || p.now == nil {
		return handler.EntryRefs{}
	}
	var refs handler.EntryRefs
	content, err := handler.SealClaim(p.key, handler.Claim{
		Purpose: handler.PurposeContent,
		UserID:  int64(user),
		Path:    vpath,
	}, p.now())
	if err != nil {
		p.warn("an entry's content reference could not be sealed", err)
		return handler.EntryRefs{}
	}
	refs.Content = content
	if !handler.Previewable(entry) {
		return refs
	}
	thumb, err := handler.SealClaim(p.key, handler.Claim{
		Purpose: handler.PurposeThumb,
		UserID:  int64(user),
		Path:    vpath,
	}, p.now())
	if err != nil {
		p.warn("an entry's thumbnail reference could not be sealed", err)
		return refs
	}
	refs.Thumb = thumb
	return refs
}

func (p *Projection) warn(message string, err error) {
	if p.log != nil {
		p.log.Warn(message, "subsystem", "files", "error", err)
	}
}
