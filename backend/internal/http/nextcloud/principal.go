//go:build linux && compat_nc

package nc

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
	num "github.com/heavycaffeiner/stowcloud/backend/internal/platform/number"
)

type Principal = middleware.Principal

func principalOf(c *gin.Context) (Principal, bool) {
	v, exists := c.Get(string(middleware.KeyCredential))
	p, ok := v.(Principal)
	if !exists || !ok || p.UserID == 0 {
		return Principal{}, false
	}
	return p, true
}
func principalOfRequest(r *http.Request) (Principal, bool) {
	p, ok := r.Context().Value(middleware.KeyCredential).(Principal)
	if !ok || p.UserID == 0 {
		return Principal{}, false
	}
	return p, true
}
func user(p Principal) core.UserID { return core.UserID(p.UserID) }
func (s *Server) resolve(ctx context.Context, p Principal, path string, need acl.Perms) (core.Resolved, error) {
	if !s.shareAllowed(p, labelOf(path)) {
		return core.Resolved{}, core.ErrNotFound
	}
	res, err := s.deps.Resolve(user(p), path, need)
	if err != nil {
		return core.Resolved{}, err
	}
	if !p.Mask.IsEmpty() {
		res = res.WithMask(p.Mask)
		if !res.Has(need) {
			return core.Resolved{}, core.ErrDenied
		}
	}
	return res, nil
}
func labelOf(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	if i := strings.IndexByte(trimmed, '/'); i >= 0 {
		return trimmed[:i]
	}
	return trimmed
}
func (s *Server) resolveComponents(ctx context.Context, p Principal, comps []string, need acl.Perms) (core.Resolved, error) {
	return s.resolve(ctx, p, joinComponents(comps), need)
}
func (s *Server) shareAllowed(p Principal, label string) bool {
	if len(p.Shares) == 0 || label == "" {
		return true
	}
	for _, allowed := range p.Shares {
		if allowed == label {
			return true
		}
	}
	return false
}
func joinComponents(comps []string) string {
	if len(comps) == 0 {
		return ""
	}
	out := comps[0]
	for _, c := range comps[1:] {
		out += "/" + c
	}
	return out
}
func (s *Server) roots(ctx context.Context, p Principal) []acl.RootEntry {
	all := s.deps.Core.Roots(user(p))
	hidden, err := s.deps.Store.EncryptedShares(ctx)
	if err != nil {
		s.log.Warn("the encrypted-share set could not be read; the listing is empty", "error", err)
		return nil
	}
	out := make([]acl.RootEntry, 0, len(all))
	for _, rt := range all {
		if !s.shareAllowed(p, rt.Label) || hiddenShare(hidden, rt.Share) {
			continue
		}
		out = append(out, rt)
	}
	return out
}
func hiddenShare(hidden map[core.ShareID]bool, id int64) bool {
	narrowed, err := num.Narrow[uint32](id)
	if err != nil {
		return true
	}
	return hidden[core.ShareID(narrowed)]
}
