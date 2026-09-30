//go:build linux

package files

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/fs/vfs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/shares/acl"
)

// Resolve returns the one path gate used by authenticated file transports.
// Malformed paths and paths outside the caller's grants have the same answer.
func Resolve(c *Core) func(UserID, string, acl.Perms) (Resolved, error) {
	return func(owner UserID, raw string, need acl.Perms) (Resolved, error) {
		p, err := vfs.ParseVpath(raw)
		if err != nil {
			return Resolved{}, ErrNotFound
		}
		return c.Resolve(owner, p, need)
	}
}

// OpenBoundClaim opens a claim for one purpose and binds it to the session
// account. A claim narrows an authenticated session; it never authenticates.
func OpenBoundClaim(key ClaimKey, now func() int64) func(*gin.Context, ClaimPurpose, UserID) (Claim, bool) {
	return func(c *gin.Context, purpose ClaimPurpose, owner UserID) (Claim, bool) {
		value := c.Query("claim")
		if value == "" {
			return Claim{}, false
		}
		if now == nil {
			return Claim{}, false
		}
		claim, err := OpenClaim(map[uint32][]byte{key.Version: key.Key}, purpose, value, now())
		if err != nil || claim.UserID != int64(owner) {
			return Claim{}, false
		}
		return claim, true
	}
}

// Projection owns the wire projection of core entries and the claims each row
// carries. The application supplies only the deployment key, clock, and log.
type Projection struct {
	core *Core
	key  ClaimKey
	now  func() int64
	log  *slog.Logger
}

// ProjectionDeps configures a file response projection.
type ProjectionDeps struct {
	Core     *Core
	ClaimKey ClaimKey
	Now      func() int64
	Logger   *slog.Logger
}

// NewProjection constructs the entry projection used by file transports.
func NewProjection(d ProjectionDeps) *Projection {
	return &Projection{core: d.Core, key: d.ClaimKey, now: d.Now, log: d.Logger}
}

// EntryView projects one entry with its client path and sealed references.
func (p *Projection) EntryView(owner UserID, r Resolved, entry Entry) EntryView {
	vpath := p.Vpath(owner, r, entry)
	return EntryOf(entry, vpath, p.refs(owner, entry, vpath))
}

// Vpath returns the client-facing path for an entry.
func (p *Projection) Vpath(owner UserID, r Resolved, entry Entry) string {
	vp, err := p.core.VpathFor(owner, r.Share(), entry.Path)
	if err != nil {
		return entry.Path.String()
	}
	return vp.String()
}

// Refs returns the per-row reference sealer for one account.
func (p *Projection) Refs(owner UserID) func(Entry, string) EntryRefs {
	return func(entry Entry, vpath string) EntryRefs {
		return p.refs(owner, entry, vpath)
	}
}

func (p *Projection) refs(user UserID, entry Entry, vpath string) EntryRefs {
	if entry.IsDir || vpath == "" || p.now == nil {
		return EntryRefs{}
	}
	var refs EntryRefs
	content, err := SealClaim(p.key, Claim{
		Purpose: PurposeContent,
		UserID:  int64(user),
		Path:    vpath,
	}, p.now())
	if err != nil {
		p.warn("an entry's content reference could not be sealed", err)
		return EntryRefs{}
	}
	refs.Content = content
	if !Previewable(entry) {
		return refs
	}
	thumb, err := SealClaim(p.key, Claim{
		Purpose: PurposeThumb,
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
