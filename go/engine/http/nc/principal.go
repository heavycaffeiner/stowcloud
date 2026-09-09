//go:build linux && compat_nc

package nc

import (
	"context"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// Reading the caller, and turning a client's path into a capability.
//
// Every handler here starts with one of these two and nothing else. The chain
// has already resolved the credential by the time a request arrives, so what
// is left is to narrow it: a device credential carries a permission mask and
// possibly a share allowlist, and both have to bite on this surface because
// the native route table's own check never runs for these paths.

// Principal is the caller as the chain resolved them.
type Principal = middleware.Principal

// principalOf reads the caller from a framework request.
func principalOf(c *fiber.Ctx) (Principal, bool) {
	p, ok := c.Locals(middleware.KeyCredential).(Principal)
	if !ok || p.UserID == 0 {
		return Principal{}, false
	}
	return p, true
}

// principalOfRequest reads the caller from a standard-library request, which
// is what the DAV half is served through.
func principalOfRequest(r *http.Request) (Principal, bool) {
	p, ok := r.Context().Value(middleware.KeyCredential).(Principal)
	if !ok || p.UserID == 0 {
		return Principal{}, false
	}
	return p, true
}

// user is the account id a principal names.
func user(p Principal) core.UserID { return core.UserID(p.UserID) }

// resolve turns a client's path into a capability, with the caller's
// credential scope applied.
//
// Three refusals fold into one answer. A path whose share is outside a device
// credential's allowlist answers as absent, because a credential scoped to one
// share must not learn that another exists. A permission the credential's mask
// withholds answers denied, because the caller does know this path and is
// being told what they may not do with it. Everything else is whatever the
// core said.
func (s *Server) resolve(
	ctx context.Context, p Principal, path string, need acl.Perms,
) (core.Resolved, error) {
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

// labelOf is the share label a client path names, which is its first
// component. The virtual root names none.
func labelOf(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	if i := strings.IndexByte(trimmed, '/'); i >= 0 {
		return trimmed[:i]
	}
	return trimmed
}

// resolveComponents is resolve for a path a request already split.
func (s *Server) resolveComponents(
	ctx context.Context, p Principal, comps []string, need acl.Perms,
) (core.Resolved, error) {
	return s.resolve(ctx, p, joinComponents(comps), need)
}

// shareAllowed reports whether a device credential's allowlist admits a share
// label. An empty allowlist admits everything, and the virtual root has no
// label to check.
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

// joinComponents spells a component list as the engine's own path.
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

// roots is the caller's top-level listing: one entry per share they may read,
// with the encrypted ones left out.
//
// A client here cannot decrypt anything, so a listing that showed an
// encrypted share would present ciphertext as the account's files. A failure
// to read the encrypted set leaves every share out rather than risking one:
// the safe side of not knowing is an empty listing, which a client reports as
// nothing to sync instead of syncing the wrong bytes.
func (s *Server) roots(ctx context.Context, p Principal) []acl.RootEntry {
	all := s.deps.Core.Roots(user(p))
	hidden, err := s.deps.Store.EncryptedShares(ctx)
	if err != nil {
		s.log.Warn("the encrypted-share set could not be read; the listing is empty", "error", err)
		return nil
	}
	out := make([]acl.RootEntry, 0, len(all))
	for _, rt := range all {
		if !s.shareAllowed(p, rt.Label) {
			continue
		}
		if hiddenShare(hidden, rt.Share) {
			continue
		}
		out = append(out, rt)
	}
	return out
}

// hiddenShare reports whether a store row's share id names a share in the
// encrypted set.
//
// A row's id is wider than the share type because the column is a generic
// foreign key. An id too wide to be one is a corrupt row, and it counts as
// hidden: the safe side of not knowing whether it is encrypted.
func hiddenShare(hidden map[core.ShareID]bool, id int64) bool {
	narrowed, err := num.Narrow[uint32](id)
	if err != nil {
		return true
	}
	return hidden[core.ShareID(narrowed)]
}
