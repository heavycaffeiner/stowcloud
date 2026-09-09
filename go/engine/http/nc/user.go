//go:build linux && compat_nc

package nc

import (
	"context"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The account record, and the two app-password endpoints a client mints and
// revokes its own device credential through.

// unlimitedQuotaSentinel is what a Nextcloud client reads as "no cap": the
// PHP constant FileInfo::SPACE_UNLIMITED. A cap of zero is a different,
// real fact (an account that may write nothing), and only this sentinel
// reads as unlimited.
const unlimitedQuotaSentinel = -3

// currentUser answers GET /cloud/user: the caller's own record.
func (s *Server) currentUser(c *fiber.Ctx, p Principal) (Val, bool, *Error) {
	ctx := c.UserContext()
	info, err := s.deps.Auth.AccountInfo(ctx, p.UserID)
	if err != nil {
		return Val{}, false, ocsErrorOf(err, apierr.VisibilityHidden)
	}
	return s.accountValOf(c, p.UserID, info), true, nil
}

// otherUser answers GET /cloud/users/{login}. A stranger and an absent
// account fold into one not-found answer: the account service's own
// directory rule already makes that decision, and this handler only
// translates it onto the wire.
func (s *Server) otherUser(c *fiber.Ctx, p Principal, login string) (Val, bool, *Error) {
	ctx := c.UserContext()
	info, visible, err := s.deps.Auth.AccountInfoByLogin(ctx, p.UserID, login)
	if err != nil {
		return Val{}, false, ocsErrorOf(err, apierr.VisibilityHidden)
	}
	if !visible {
		return Val{}, false, NotFound("The requested user could not be found")
	}
	return s.accountValOf(c, info.ID, info), true, nil
}

// accountValOf renders the account fields the reference clients read.
//
// Quota comes from two sources that answer different questions:
// auth.AccountInfo is the account's own cap and usage, while
// core.FreeSpace at a root the account can reach is what the filesystem
// underneath actually has left. The Android client compares a file's size
// against the reported free figure before it starts an upload, so a
// fabricated zero there is an upload that never begins. "No cap" and "a
// cap of zero" have to stay distinguishable, which is what the unlimited
// sentinel is for.
func (s *Server) accountValOf(c *fiber.Ctx, id int64, info auth.AccountInfo) Val {
	ctx := c.UserContext()
	login := info.LoginName

	free, total := s.quotaSpace(ctx, id)
	// A usage figure too large to render as a signed number is reported as
	// zero rather than as a negative one, which a client draws as a bar past
	// its own end.
	used, uerr := num.Narrow[int64](info.UsageBytes)
	if uerr != nil {
		used = 0
	}
	var quotaCap int64 = unlimitedQuotaSentinel
	if info.QuotaBytes != nil {
		quotaCap = *info.QuotaBytes
	}
	var relative float64
	if quotaCap > 0 {
		relative = float64(used) / float64(quotaCap) * 100
	}

	quota := Obj(
		P("free", Int(free)),
		P("used", Int(used)),
		P("total", Int(total)),
		P("relative", Float(relative)),
		P("quota", Int(quotaCap)),
	)

	// A failure to read the role reports the narrower answer: an account
	// wrongly shown as an administrator is a screen offering actions it
	// cannot perform.
	admin, aerr := s.deps.Auth.IsAdmin(ctx, id)
	if aerr != nil {
		s.log.Warn("the account's role could not be read", "error", aerr)
		admin = false
	}
	groups := make([]Val, 0, len(info.Groups))
	subadmin := make([]Val, 0)
	for _, g := range info.Groups {
		groups = append(groups, Str(g))
	}
	if admin {
		// This engine carries no separate subadmin role: an administrator
		// administers every group, which is the closest true statement
		// the client's own subadmin screen can be shown without inventing
		// a permission this engine does not have.
		subadmin = append(subadmin, groups...)
	}

	return Obj(
		P("id", Str(login)),
		P("display-name", Str(info.DisplayName)),
		P("displayname", Str(info.DisplayName)),
		P("email", Str("")),
		P("phone", Str("")),
		P("address", Str("")),
		P("website", Str("")),
		P("twitter", Str("")),
		P("organisation", Str("")),
		P("role", Str("")),
		P("headline", Str("")),
		P("biography", Str("")),
		P("language", Str("")),
		P("locale", Str("")),
		P("enabled", Bool(info.Enabled)),
		P("groups", List(groups...)),
		P("subadmin", List(subadmin...)),
		P("quota", quota),
		P("backend", Str("Database")),
		P("backendCapabilities", Obj(
			P("setDisplayName", Bool(false)),
			P("setPassword", Bool(true)),
		)),
		P("lastLogin", Int(s.lastLoginMillis(ctx, id))),
	)
}

// lastLoginMillis is the account's most recent session, milliseconds since
// the epoch: the unit the client's own field expects. The newest live
// session is what "last signed in" means to a client asking about another
// account's activity; a session-less account (only ever reached by a
// device credential) answers zero.
func (s *Server) lastLoginMillis(ctx context.Context, id int64) int64 {
	rows, err := s.deps.Auth.Sessions(ctx, id)
	if err != nil || len(rows) == 0 {
		return 0
	}
	var latest int64
	for _, r := range rows {
		if r.LastSeenNs > latest {
			latest = r.LastSeenNs
		}
	}
	return latest / int64(1_000_000)
}

// quotaSpace answers the filesystem numbers a client compares an upload
// against: the free and total bytes at a root the account can reach. The
// account's own root listing supplies the target, since that is the same
// tree an upload would land in; a home-enabled deployment always has one,
// and an account with no reachable root at all answers zero for both,
// which a client reads as "cannot write here" rather than "unlimited".
func (s *Server) quotaSpace(ctx context.Context, id int64) (free, total int64) {
	principal := Principal{UserID: id}
	for _, r := range s.roots(ctx, principal) {
		if !r.Perms.Has(acl.Write | acl.Create) {
			continue
		}
		share, ok := shareIDOf(r.Share)
		if !ok {
			continue
		}
		vpath, err := s.deps.VpathOf(core.UserID(id), share, grantSubpathOf(r.Subpath.String()))
		if err != nil {
			continue
		}
		res, err := s.deps.Resolve(core.UserID(id), vpath, acl.Read)
		if err != nil {
			continue
		}
		fs, err := s.deps.Core.FreeSpace(ctx, res)
		if err != nil {
			continue
		}
		avail, aerr := num.Narrow[int64](fs.Available)
		size, serr := num.Narrow[int64](fs.Total)
		if aerr != nil || serr != nil {
			continue
		}
		return avail, size
	}
	return 0, 0
}

// appPassword answers GET /core/getapppassword and its onetime variant:
// mints a device credential for the authenticated caller.
//
// Refused for a caller already authenticated with a device credential
// rather than a session: a token that could mint another token would make
// every revocation incomplete, since the derived token would outlive the
// one that made it.
func (s *Server) appPassword(c *fiber.Ctx, p Principal) (Val, bool, *Error) {
	if p.Kind != middleware.CredentialSessionCookie {
		return Val{}, false, Forbidden("a device credential cannot mint another one")
	}
	token, _, err := s.deps.Auth.CreateSyncCredential(c.UserContext(), p.UserID, "device login")
	if err != nil {
		return Val{}, false, ocsErrorOf(err, apierr.VisibilityHidden)
	}
	return Obj(P("apppassword", Str(token))), true, nil
}

// revokeAppPassword answers DELETE /core/apppassword: revokes the
// credential the request itself arrived on. A session-authenticated
// caller carries none to revoke.
func (s *Server) revokeAppPassword(c *fiber.Ctx, p Principal) (Val, bool, *Error) {
	if p.Kind == middleware.CredentialSessionCookie || p.AppPasswordID == 0 {
		return Val{}, false, BadRequest("this session holds no app password to revoke")
	}
	if err := s.deps.Auth.RevokeAppPassword(c.UserContext(), p.UserID, p.AppPasswordID); err != nil {
		return Val{}, false, ocsErrorOf(err, apierr.VisibilityHidden)
	}
	return Obj(), true, nil
}
