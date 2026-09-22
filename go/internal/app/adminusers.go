//go:build linux

// Administration: accounts, groups and the audit log.
//
// The chain requires a browser session for everything under this prefix but
// says nothing about who the session belongs to, so every route here checks
// that the caller is an administrator. That check is one function, and a test
// walks the whole route table to prove no route reaches a service without it.
package app

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/go/internal/kit/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/handler"
)

// admin answers who is calling, and stops here if they do not run this
// deployment.
//
// The bool is the decision and the error is the written response, in that
// order. An error alone cannot work here: refuse writes the refusal and
// returns nil, so a caller testing only the error would read a refusal as
// permission granted. That mistake has already been made twice in this
// package, once producing a nil dereference.
func (e *Engine) admin(c *gin.Context) (int64, bool) {
	owner, ok := ownerOf(c)
	if !ok {
		refuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return 0, false
	}
	isAdmin, err := e.Auth.IsAdmin(c.Request.Context(), int64(owner))
	if err != nil {
		failKnown(c, err)
		return 0, false
	}
	if !isAdmin {
		refuse(c, apierr.Classified{Class: apierr.Denied})
		return 0, false
	}
	return int64(owner), true
}

// adminUsersList answers every account.
func (e *Engine) adminUsersList(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}

	rows, err := e.Auth.ListUsers(c.Request.Context())
	if err != nil {
		failKnown(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.UsersOf(rows))
}

// createUserRequest is a new account.
type createUserRequest struct {
	Login    string `json:"login"`
	Display  string `json:"display"`
	Password string `json:"password"`
}

// adminUsersCreate makes one.
func (e *Engine) adminUsersCreate(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}

	var req createUserRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}

	// The name rule and the password floor are the service's, not repeated
	// here: a second copy is a second answer, and the one that ran would
	// depend on which surface the request arrived through.
	id, err := e.Auth.CreateUser(c.Request.Context(), req.Login, req.Display,
		secret.New([]byte(req.Password)))
	if err != nil {
		failKnown(c, err)
		return
	}

	row, err := e.Auth.UserByID(c.Request.Context(), id)
	if err != nil {
		failKnown(c, err)
		return
	}
	writeJSON(c, http.StatusCreated, handler.UserOf(row))
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
func (e *Engine) adminUsersUpdate(c *gin.Context) {
	caller, ok := e.admin(c)
	if !ok {
		return
	}
	target, ok := pathID(c)
	if !ok {
		notFound(c)
		return
	}

	var req updateUserRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}

	// An administrator cannot disable themselves. The service enforces the
	// last-admin rule, but that only catches the final one: a deployment with
	// two administrators would let either lock themselves out, which is a
	// mistake nobody makes deliberately.
	if req.Disabled != nil && *req.Disabled && target == caller {
		refuse(c, apierr.Classified{Class: apierr.Denied})
		return
	}

	if !e.applyUserPatch(c, target, req) {
		return
	}

	row, err := e.Auth.UserByID(c.Request.Context(), target)
	if err != nil {
		failKnown(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.UserOf(row))
}

// applyUserPatch runs each requested change, stopping at the first failure.
//
// Not a transaction, and it cannot be: the changes go through separate
// service operations that each republish. A failure partway leaves the
// earlier ones applied, which the response reflects by returning the row as
// it actually is rather than as it was asked to be.
func (e *Engine) applyUserPatch(c *gin.Context, target int64, req updateUserRequest) bool {
	ctx := c.Request.Context()

	if req.Password != nil {
		if err := e.Auth.SetPassword(ctx, target, secret.New([]byte(*req.Password))); err != nil {
			failKnown(c, err)
			return false
		}
	}
	if req.ClearQuota {
		if err := e.Auth.SetQuota(ctx, target, nil); err != nil {
			failKnown(c, err)
			return false
		}
	} else if req.Quota != nil {
		if err := e.Auth.SetQuota(ctx, target, req.Quota); err != nil {
			failKnown(c, err)
			return false
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
			failKnown(c, err)
			return false
		}
	}
	return true
}

// adminUsersDelete removes an account and everything it owned.
func (e *Engine) adminUsersDelete(c *gin.Context) {
	caller, ok := e.admin(c)
	if !ok {
		return
	}
	target, ok := pathID(c)
	if !ok {
		notFound(c)
		return
	}

	// Same reasoning as disabling: the last-admin rule catches only the final
	// administrator, and deleting yourself is not a thing anyone means to do.
	if target == caller {
		refuse(c, apierr.Classified{Class: apierr.Denied})
		return
	}
	if cerr := e.Core.CleanupHome(c.Request.Context(), core.UserID(target)); cerr != nil {
		e.logger.Warn("cleaning up deleted user home failed", "user", target, "error", cerr)
	}
	if err := e.Auth.DeleteUser(c.Request.Context(), target); err != nil {
		failKnown(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// adminGroupsList answers every group with its members.
func (e *Engine) adminGroupsList(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}

	rows, err := e.Auth.ListGroups(c.Request.Context())
	if err != nil {
		failKnown(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.GroupsOf(rows))
}

// groupRequest names a group.
type groupRequest struct {
	Name string `json:"name"`
}

// adminGroupsCreate makes one.
func (e *Engine) adminGroupsCreate(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}

	var req groupRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}

	id, err := e.Auth.CreateGroup(c.Request.Context(), req.Name)
	if err != nil {
		failKnown(c, err)
		return
	}
	writeJSON(c, http.StatusCreated, handler.GroupView{
		ID:      strconv.FormatInt(id, 10),
		Name:    req.Name,
		Members: []string{},
	})
}

// adminGroupsUpdate renames one.
func (e *Engine) adminGroupsUpdate(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		notFound(c)
		return
	}

	var req groupRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}

	group, err := e.Auth.RenameGroup(c.Request.Context(), id, req.Name)
	if err != nil {
		failKnown(c, err)
		return
	}
	// The whole row, as the create route answers: the screen swaps the
	// renamed group for what came back, members and all. Answering no content
	// left it with nothing to swap in, and the rename applied while the
	// dialogue said it had not.
	writeJSON(c, http.StatusOK, handler.GroupOf(group))
}

// adminGroupsDelete removes one.
//
// The grants that named it go with it, which is the service's cascade rather
// than a loop here: a group deleted while its grants survived would leave
// permissions attached to a name nothing resolves.
func (e *Engine) adminGroupsDelete(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		notFound(c)
		return
	}

	if err := e.Auth.DeleteGroup(c.Request.Context(), id); err != nil {
		failKnown(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// memberRequest names the account joining a group.
type memberRequest struct {
	User string `json:"user"`
}

// adminGroupMemberAdd puts an account in a group.
func (e *Engine) adminGroupMemberAdd(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}
	group, ok := pathID(c)
	if !ok {
		notFound(c)
		return
	}

	var req memberRequest
	if err := decodeBody(c, &req); err != nil {
		refuse(c, apierr.Classified{Class: apierr.Malformed})
		return
	}
	user, err := strconv.ParseInt(req.User, 10, 64)
	if err != nil || user <= 0 {
		notFound(c)
		return
	}

	if aerr := e.Auth.AddToGroup(c.Request.Context(), user, group); aerr != nil {
		failKnown(c, aerr)
		return
	}
	c.Status(http.StatusNoContent)
}

// adminGroupMemberRemove takes an account out of a group.
func (e *Engine) adminGroupMemberRemove(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}
	group, ok := pathID(c)
	if !ok {
		notFound(c)
		return
	}
	user, err := strconv.ParseInt(c.Param("user"), 10, 64)
	if err != nil || user <= 0 {
		notFound(c)
		return
	}

	if rerr := e.Auth.RemoveFromGroup(c.Request.Context(), user, group); rerr != nil {
		failKnown(c, rerr)
		return
	}
	c.Status(http.StatusNoContent)
}

// adminAudit answers a page of the log.
func (e *Engine) adminAudit(c *gin.Context) {
	if _, ok := e.admin(c); !ok {
		return
	}

	rows, next, err := e.Auth.AuditPage(c.Request.Context(), auth.AuditFilter{
		Event:  c.Query("event"),
		Before: queryInt(c.Query("before")),
		Limit:  auditLimit(c.Query("limit")),
	})
	if err != nil {
		failKnown(c, err)
		return
	}
	writeJSON(c, http.StatusOK, handler.AuditPageOf(rows, next))
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
