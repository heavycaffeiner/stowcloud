//go:build linux

package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/feature/auth"
	core "github.com/heavycaffeiner/stowcloud/go/internal/feature/files"
	secret "github.com/heavycaffeiner/stowcloud/go/internal/platform/security/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
)

// AdminUsersDeps are the explicit services used by the administrator account
// and group routes. CleanupHome is optional for deployments without home
// folders; when present it runs before account deletion.
type AdminUsersDeps struct {
	Auth        *auth.Service
	CleanupHome func(ctx context.Context, user core.UserID) error
	Logger      *slog.Logger
}

// NewAdminUsersHandlers builds administrator account, group and audit routes.
func NewAdminUsersHandlers(d AdminUsersDeps) map[string]gin.HandlerFunc {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	h := &adminUsersHandlers{d: d}
	return map[string]gin.HandlerFunc{
		"admin.users.list": h.usersList, "admin.users.create": h.usersCreate,
		"admin.users.update": h.usersUpdate, "admin.users.delete": h.usersDelete,
		"admin.groups.list": h.groupsList, "admin.groups.create": h.groupsCreate,
		"admin.groups.update": h.groupsUpdate, "admin.groups.delete": h.groupsDelete,
		"admin.groups.members.add": h.memberAdd, "admin.groups.members.remove": h.memberRemove,
		"admin.audit": h.audit,
	}
}

type adminUsersHandlers struct{ d AdminUsersDeps }

func (h *adminUsersHandlers) admin(c *gin.Context) (int64, bool) {
	v, ok := c.Get(string(middleware.KeyCredential))
	if !ok {
		adminRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return 0, false
	}
	p, ok := v.(middleware.Principal)
	if !ok || p.UserID == 0 {
		adminRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return 0, false
	}
	is, err := h.d.Auth.IsAdmin(c.Request.Context(), p.UserID)
	if err != nil {
		adminFail(c, err)
		return 0, false
	}
	if !is {
		adminRefuse(c, apierr.Classified{Class: apierr.Denied})
		return 0, false
	}
	return p.UserID, true
}

type adminCreateUserRequest struct {
	Login    string `json:"login"`
	Display  string `json:"display"`
	Password string `json:"password"`
}
type adminUpdateUserRequest struct {
	Disabled   *bool   `json:"disabled"`
	Password   *string `json:"password"`
	Quota      *int64  `json:"quota_bytes"`
	ClearQuota bool    `json:"clear_quota"`
}
type groupRequest struct {
	Name string `json:"name"`
}
type adminMemberRequest struct {
	User string `json:"user"`
}

func (h *adminUsersHandlers) usersList(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	rows, err := h.d.Auth.ListUsers(c.Request.Context())
	if err != nil {
		adminFail(c, err)
		return
	}
	adminJSON(c, http.StatusOK, UsersOf(rows))
}
func (h *adminUsersHandlers) usersCreate(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	var req adminCreateUserRequest
	if !adminDecode(c, &req) {
		return
	}
	id, err := h.d.Auth.CreateUser(c.Request.Context(), req.Login, req.Display, secret.New([]byte(req.Password)))
	if err != nil {
		adminFail(c, err)
		return
	}
	row, err := h.d.Auth.UserByID(c.Request.Context(), id)
	if err != nil {
		adminFail(c, err)
		return
	}
	adminJSON(c, http.StatusCreated, UserOf(row))
}
func (h *adminUsersHandlers) usersUpdate(c *gin.Context) {
	caller, ok := h.admin(c)
	if !ok {
		return
	}
	target, ok := adminPathID(c)
	if !ok {
		adminNotFound(c)
		return
	}
	var req adminUpdateUserRequest
	if !adminDecode(c, &req) {
		return
	}
	if req.Disabled != nil && *req.Disabled && target == caller {
		adminRefuse(c, apierr.Classified{Class: apierr.Denied})
		return
	}
	ctx := c.Request.Context()
	if req.Password != nil {
		if err := h.d.Auth.SetPassword(ctx, target, secret.New([]byte(*req.Password))); err != nil {
			adminFail(c, err)
			return
		}
	}
	if req.ClearQuota {
		if err := h.d.Auth.SetQuota(ctx, target, nil); err != nil {
			adminFail(c, err)
			return
		}
	} else if req.Quota != nil {
		if err := h.d.Auth.SetQuota(ctx, target, req.Quota); err != nil {
			adminFail(c, err)
			return
		}
	}
	if req.Disabled != nil {
		var err error
		if *req.Disabled {
			err = h.d.Auth.DisableAccount(ctx, target)
		} else {
			err = h.d.Auth.EnableAccount(ctx, target)
		}
		if err != nil {
			adminFail(c, err)
			return
		}
	}
	row, err := h.d.Auth.UserByID(ctx, target)
	if err != nil {
		adminFail(c, err)
		return
	}
	adminJSON(c, http.StatusOK, UserOf(row))
}
func (h *adminUsersHandlers) usersDelete(c *gin.Context) {
	caller, ok := h.admin(c)
	if !ok {
		return
	}
	target, ok := adminPathID(c)
	if !ok {
		adminNotFound(c)
		return
	}
	if target == caller {
		adminRefuse(c, apierr.Classified{Class: apierr.Denied})
		return
	}
	if h.d.CleanupHome != nil {
		if err := h.d.CleanupHome(c.Request.Context(), core.UserID(target)); err != nil {
			h.d.Logger.Warn("cleaning up deleted user home failed", "user", target, "error", err)
		}
	}
	if err := h.d.Auth.DeleteUser(c.Request.Context(), target); err != nil {
		adminFail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *adminUsersHandlers) groupsList(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	rows, err := h.d.Auth.ListGroups(c.Request.Context())
	if err != nil {
		adminFail(c, err)
		return
	}
	adminJSON(c, http.StatusOK, GroupsOf(rows))
}
func (h *adminUsersHandlers) groupsCreate(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	var req groupRequest
	if !adminDecode(c, &req) {
		return
	}
	id, err := h.d.Auth.CreateGroup(c.Request.Context(), req.Name)
	if err != nil {
		adminFail(c, err)
		return
	}
	adminJSON(c, http.StatusCreated, GroupView{ID: strconv.FormatInt(id, 10), Name: req.Name, Members: []string{}})
}
func (h *adminUsersHandlers) groupsUpdate(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	id, ok := adminPathID(c)
	if !ok {
		adminNotFound(c)
		return
	}
	var req groupRequest
	if !adminDecode(c, &req) {
		return
	}
	row, err := h.d.Auth.RenameGroup(c.Request.Context(), id, req.Name)
	if err != nil {
		adminFail(c, err)
		return
	}
	adminJSON(c, http.StatusOK, GroupOf(row))
}
func (h *adminUsersHandlers) groupsDelete(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	id, ok := adminPathID(c)
	if !ok {
		adminNotFound(c)
		return
	}
	if err := h.d.Auth.DeleteGroup(c.Request.Context(), id); err != nil {
		adminFail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *adminUsersHandlers) memberAdd(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	group, ok := adminPathID(c)
	if !ok {
		adminNotFound(c)
		return
	}
	var req adminMemberRequest
	if !adminDecode(c, &req) {
		return
	}
	user, err := strconv.ParseInt(req.User, 10, 64)
	if err != nil || user <= 0 {
		adminNotFound(c)
		return
	}
	if err = h.d.Auth.AddToGroup(c.Request.Context(), user, group); err != nil {
		adminFail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *adminUsersHandlers) memberRemove(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	group, ok := adminPathID(c)
	if !ok {
		adminNotFound(c)
		return
	}
	user, err := strconv.ParseInt(c.Param("user"), 10, 64)
	if err != nil || user <= 0 {
		adminNotFound(c)
		return
	}
	if err = h.d.Auth.RemoveFromGroup(c.Request.Context(), user, group); err != nil {
		adminFail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *adminUsersHandlers) audit(c *gin.Context) {
	if _, ok := h.admin(c); !ok {
		return
	}
	rows, next, err := h.d.Auth.AuditPage(c.Request.Context(), auth.AuditFilter{Event: c.Query("event"), Before: adminQueryInt(c.Query("before")), Limit: adminAuditLimit(c.Query("limit"))})
	if err != nil {
		adminFail(c, err)
		return
	}
	adminJSON(c, http.StatusOK, AuditPageOf(rows, next))
}

func adminDecode(c *gin.Context, v any) bool {
	if err := middleware.DecodeJSON(middleware.LimitBody(c.Request.Body, route.BodyJSON), v); err != nil {
		adminRefuse(c, apierr.Classified{Class: apierr.Malformed})
		return false
	}
	return true
}
func adminPathID(c *gin.Context) (int64, bool) {
	n, err := strconv.ParseInt(c.Param("id"), 10, 64)
	return n, err == nil && n > 0
}
func adminQueryInt(raw string) int64 {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
func adminAuditLimit(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 100
	}
	if n > 1000 {
		return 1000
	}
	return n
}
func adminJSON(c *gin.Context, status int, v any) { c.JSON(status, v) }
func adminNotFound(c *gin.Context)                { adminFail(c, core.ErrNotFound) }
func adminRefuse(c *gin.Context, class apierr.Classified) {
	status, body := apierr.REST(class)
	adminJSON(c, status, body)
}
func adminFail(c *gin.Context, err error) {
	if errors.Is(err, core.ErrNotFound) {
		adminRefuse(c, apierr.Classified{Class: apierr.NotFound})
		return
	}
	adminRefuse(c, apierr.Classify(err, apierr.VisibilityKnown))
}
