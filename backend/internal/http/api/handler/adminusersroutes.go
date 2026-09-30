//go:build linux

package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	secret "github.com/heavycaffeiner/stowcloud/backend/internal/platform/security/secret"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
)

// AdminUsersDeps are the explicit services used by the administrator account
// and group routes. CleanupHome is optional for deployments without home
// folders; when present it runs before account deletion.
type AdminUsersDeps struct {
	Auth        *auth.Service
	CleanupHome func(ctx context.Context, user files.UserID) error
	Logger      *slog.Logger
}

// NewAdminUsersHandlers builds administrator account, group and audit routes.
func NewAdminUsersHandlers(d AdminUsersDeps) *AdminUsersHandlers {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &AdminUsersHandlers{d: d}
}

type AdminUsersHandlers struct{ d AdminUsersDeps }

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

func (h *AdminUsersHandlers) UsersList(c *gin.Context) {
	rows, err := h.d.Auth.ListUsers(c.Request.Context())
	if err != nil {
		adminFail(c, err)
		return
	}
	c.JSON(http.StatusOK, UsersOf(rows))
}
func (h *AdminUsersHandlers) UsersCreate(c *gin.Context) {
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
	c.JSON(http.StatusCreated, UserOf(row))
}
func (h *AdminUsersHandlers) UsersUpdate(c *gin.Context) {
	caller, _ := middleware.UserOf(c)
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
		middleware.Refuse(c, apierr.Classified{Class: apierr.Denied})
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
	c.JSON(http.StatusOK, UserOf(row))
}
func (h *AdminUsersHandlers) UsersDelete(c *gin.Context) {
	caller, _ := middleware.UserOf(c)
	target, ok := adminPathID(c)
	if !ok {
		adminNotFound(c)
		return
	}
	if target == caller {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Denied})
		return
	}
	if h.d.CleanupHome != nil {
		if err := h.d.CleanupHome(c.Request.Context(), files.UserID(target)); err != nil {
			h.d.Logger.Warn("cleaning up deleted user home failed", "user", target, "error", err)
		}
	}
	if err := h.d.Auth.DeleteUser(c.Request.Context(), target); err != nil {
		adminFail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *AdminUsersHandlers) GroupsList(c *gin.Context) {
	rows, err := h.d.Auth.ListGroups(c.Request.Context())
	if err != nil {
		adminFail(c, err)
		return
	}
	c.JSON(http.StatusOK, GroupsOf(rows))
}
func (h *AdminUsersHandlers) GroupsCreate(c *gin.Context) {
	var req groupRequest
	if !adminDecode(c, &req) {
		return
	}
	id, err := h.d.Auth.CreateGroup(c.Request.Context(), req.Name)
	if err != nil {
		adminFail(c, err)
		return
	}
	c.JSON(http.StatusCreated, GroupView{ID: strconv.FormatInt(id, 10), Name: req.Name, Members: []string{}})
}
func (h *AdminUsersHandlers) GroupsUpdate(c *gin.Context) {
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
	c.JSON(http.StatusOK, GroupOf(row))
}
func (h *AdminUsersHandlers) GroupsDelete(c *gin.Context) {
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
func (h *AdminUsersHandlers) MemberAdd(c *gin.Context) {
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
func (h *AdminUsersHandlers) MemberRemove(c *gin.Context) {
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
func (h *AdminUsersHandlers) Audit(c *gin.Context) {
	rows, next, err := h.d.Auth.AuditPage(c.Request.Context(), auth.AuditFilter{Event: c.Query("event"), Before: adminQueryInt(c.Query("before")), Limit: adminAuditLimit(c.Query("limit"))})
	if err != nil {
		adminFail(c, err)
		return
	}
	c.JSON(http.StatusOK, AuditPageOf(rows, next))
}

func adminDecode(c *gin.Context, v any) bool {
	if err := middleware.DecodeJSON(c.Request.Body, v); err != nil {
		middleware.Refuse(c, apierr.Classified{Class: apierr.Malformed})
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
func adminNotFound(c *gin.Context) { middleware.Refuse(c, apierr.Classified{Class: apierr.NotFound}) }

// adminFail answers a missing file as a bare not-found, without the file
// surface's reason key.
func adminFail(c *gin.Context, err error) {
	if errors.Is(err, files.ErrNotFound) {
		adminNotFound(c)
		return
	}
	middleware.Fail(c, err)
}
