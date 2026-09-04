//go:build linux

// Administration: accounts, groups and the audit log.
//
// The chain requires a browser session for everything under this prefix but
// says nothing about who the session belongs to, so every route here checks
// that the caller is an administrator. That check is one function, and a test
// walks the whole route table to prove no route reaches a service without it.
package lifecycle

import (
	"strconv"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
)

// admin answers who is calling, and stops here if they do not run this
// deployment.
//
// The bool is the decision and the error is the written response, in that
// order. An error alone cannot work here: refuse writes the refusal and
// returns nil, so a caller testing only the error would read a refusal as
// permission granted. That mistake has already been made twice in this
// package, once producing a nil dereference.
func (e *Engine) admin(c *fiber.Ctx) (int64, bool, error) {
	owner, ok := ownerOf(c)
	if !ok {
		return 0, false, refuse(c, apierr.Classified{Class: apierr.AuthRequired})
	}

	isAdmin, err := e.Auth.IsAdmin(c.UserContext(), int64(owner))
	if err != nil {
		return 0, false, failKnown(c, err)
	}
	if !isAdmin {
		// Denied rather than hidden. The caller holds a valid session and
		// knows the administrative surface exists; pretending these routes
		// are absent would only make a legitimate operator think the build
		// lacks them.
		return 0, false, refuse(c, apierr.Classified{Class: apierr.Denied})
	}
	return int64(owner), true, nil
}

// adminUsersList answers every account.
func (e *Engine) adminUsersList(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	rows, err := e.Auth.ListUsers(c.UserContext())
	if err != nil {
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.UsersOf(rows))
}

// createUserRequest is a new account.
type createUserRequest struct {
	Login    string `json:"login"`
	Display  string `json:"display"`
	Password string `json:"password"`
}

// adminUsersCreate makes one.
func (e *Engine) adminUsersCreate(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	var req createUserRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	// The name rule and the password floor are the service's, not repeated
	// here: a second copy is a second answer, and the one that ran would
	// depend on which surface the request arrived through.
	id, err := e.Auth.CreateUser(c.UserContext(), req.Login, req.Display,
		secret.New([]byte(req.Password)))
	if err != nil {
		return failKnown(c, err)
	}

	row, err := e.Auth.UserByID(c.UserContext(), id)
	if err != nil {
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusCreated, handler.UserOf(row))
}

// updateUserRequest carries only what is being changed. Every field is a
// pointer so "absent" and "set to the zero value" are different requests: a
// nil display leaves it alone, an empty one clears it.
//
// There is no display name here. The auth service has no setter for one, and
// adding a store write from this handler would be a second path into the
// accounts table that skips whatever the service does around them.
type updateUserRequest struct {
	Disabled *bool   `json:"disabled"`
	Password *string `json:"password"`
	Quota    *int64  `json:"quota_bytes"`

	// ClearQuota removes the limit. A null quota cannot express this on its
	// own, because null is also how a client says "leave it alone".
	ClearQuota bool `json:"clear_quota"`
}

// adminUsersUpdate changes one account.
func (e *Engine) adminUsersUpdate(c *fiber.Ctx) error {
	caller, ok, written := e.admin(c)
	if !ok {
		return written
	}
	target, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	var req updateUserRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	// An administrator cannot disable themselves. The service enforces the
	// last-admin rule, but that only catches the final one: a deployment with
	// two administrators would let either lock themselves out, which is a
	// mistake nobody makes deliberately.
	if req.Disabled != nil && *req.Disabled && target == caller {
		return refuse(c, apierr.Classified{Class: apierr.Denied})
	}

	if err := e.applyUserPatch(c, target, req); err != nil {
		return err
	}

	row, err := e.Auth.UserByID(c.UserContext(), target)
	if err != nil {
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.UserOf(row))
}

// applyUserPatch runs each requested change, stopping at the first failure.
//
// Not a transaction, and it cannot be: the changes go through separate
// service operations that each republish. A failure partway leaves the
// earlier ones applied, which the response reflects by returning the row as
// it actually is rather than as it was asked to be.
func (e *Engine) applyUserPatch(c *fiber.Ctx, target int64, req updateUserRequest) error {
	ctx := c.UserContext()

	if req.Password != nil {
		if err := e.Auth.SetPassword(ctx, target, secret.New([]byte(*req.Password))); err != nil {
			return failKnown(c, err)
		}
	}
	if req.ClearQuota {
		if err := e.Auth.SetQuota(ctx, target, nil); err != nil {
			return failKnown(c, err)
		}
	} else if req.Quota != nil {
		if err := e.Auth.SetQuota(ctx, target, req.Quota); err != nil {
			return failKnown(c, err)
		}
	}
	if req.Disabled != nil {
		var err error
		if *req.Disabled {
			err = e.Auth.DisableAccount(ctx, target)
		} else {
			err = e.Auth.EnableAccount(ctx, target)
		}
		if err != nil {
			return failKnown(c, err)
		}
	}
	return nil
}

// adminUsersDelete removes an account and everything it owned.
func (e *Engine) adminUsersDelete(c *fiber.Ctx) error {
	caller, ok, written := e.admin(c)
	if !ok {
		return written
	}
	target, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	// Same reasoning as disabling: the last-admin rule catches only the final
	// administrator, and deleting yourself is not a thing anyone means to do.
	if target == caller {
		return refuse(c, apierr.Classified{Class: apierr.Denied})
	}

	if err := e.Auth.DeleteUser(c.UserContext(), target); err != nil {
		return failKnown(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// adminGroupsList answers every group with its members.
func (e *Engine) adminGroupsList(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	rows, err := e.Auth.ListGroups(c.UserContext())
	if err != nil {
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.GroupsOf(rows))
}

// groupRequest names a group.
type groupRequest struct {
	Name string `json:"name"`
}

// adminGroupsCreate makes one.
func (e *Engine) adminGroupsCreate(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	var req groupRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	id, err := e.Auth.CreateGroup(c.UserContext(), req.Name)
	if err != nil {
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusCreated, handler.GroupView{
		ID:      strconv.FormatInt(id, 10),
		Name:    req.Name,
		Members: []string{},
	})
}

// adminGroupsUpdate renames one.
func (e *Engine) adminGroupsUpdate(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	id, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	var req groupRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	group, err := e.Auth.RenameGroup(c.UserContext(), id, req.Name)
	if err != nil {
		return failKnown(c, err)
	}
	// The whole row, as the create route answers: the screen swaps the
	// renamed group for what came back, members and all. Answering no content
	// left it with nothing to swap in, and the rename applied while the
	// dialogue said it had not.
	return writeJSON(c, fiber.StatusOK, handler.GroupOf(group))
}

// adminGroupsDelete removes one.
//
// The grants that named it go with it, which is the service's cascade rather
// than a loop here: a group deleted while its grants survived would leave
// permissions attached to a name nothing resolves.
func (e *Engine) adminGroupsDelete(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	id, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	if err := e.Auth.DeleteGroup(c.UserContext(), id); err != nil {
		return failKnown(c, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// memberRequest names the account joining a group.
type memberRequest struct {
	User string `json:"user"`
}

// adminGroupMemberAdd puts an account in a group.
func (e *Engine) adminGroupMemberAdd(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	group, ok := pathID(c)
	if !ok {
		return notFound(c)
	}

	var req memberRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}
	user, err := strconv.ParseInt(req.User, 10, 64)
	if err != nil || user <= 0 {
		return notFound(c)
	}

	if aerr := e.Auth.AddToGroup(c.UserContext(), user, group); aerr != nil {
		return failKnown(c, aerr)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// adminGroupMemberRemove takes an account out of a group.
func (e *Engine) adminGroupMemberRemove(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}
	group, ok := pathID(c)
	if !ok {
		return notFound(c)
	}
	user, err := strconv.ParseInt(c.Params("user"), 10, 64)
	if err != nil || user <= 0 {
		return notFound(c)
	}

	if rerr := e.Auth.RemoveFromGroup(c.UserContext(), user, group); rerr != nil {
		return failKnown(c, rerr)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// adminAudit answers a page of the log.
func (e *Engine) adminAudit(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	rows, next, err := e.Auth.AuditPage(c.UserContext(), auth.AuditFilter{
		Event:  c.Query("event"),
		Before: queryInt(c.Query("before")),
		Limit:  auditLimit(c.Query("limit")),
	})
	if err != nil {
		return failKnown(c, err)
	}
	return writeJSON(c, fiber.StatusOK, handler.AuditPageOf(rows, next))
}

// queryInt reads an optional decimal, zero when absent or unusable.
func queryInt(raw string) int64 {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// auditLimit bounds a page. The log grows without limit, so an unbounded
// request is a scan of every event the deployment has ever recorded.
//
// The ceiling is exported for the test that checks it, because proving it by
// response alone needs more rows than the ceiling and the fixture would spend
// its time writing them. The bound is checked directly instead.
func auditLimit(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return auditPageDefault
	}
	return min(n, auditPageCeiling)
}

// The page bounds. Default is what a screen shows; the ceiling is what a
// caller may ask for at most.
const (
	auditPageDefault = 100
	auditPageCeiling = 1000
)
