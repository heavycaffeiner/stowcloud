//go:build linux

package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

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

type userPathInput struct {
	ID string `path:"id"`
}
type userCreateInput struct{ Body adminCreateUserRequest }
type userUpdateInput struct {
	ID   string `path:"id"`
	Body adminUpdateUserRequest
}
type groupCreateInput struct{ Body groupRequest }
type groupUpdateInput struct {
	ID   string `path:"id"`
	Body groupRequest
}
type memberAddInput struct {
	ID   string `path:"id"`
	Body adminMemberRequest
}
type memberRemoveInput struct {
	ID   string `path:"id"`
	User string `path:"user"`
}
type auditInput struct {
	Event  string `query:"event"`
	Before string `query:"before"`
	Limit  string `query:"limit"`
}

type usersOutput struct{ Body []UserView }
type userOutput struct {
	Status int
	Body   UserView
}
type groupsOutput struct{ Body []GroupView }
type groupOutput struct {
	Status int
	Body   GroupView
}
type auditOutput struct{ Body AuditPageView }

func (h *AdminUsersHandlers) UsersList(ctx context.Context, _ *struct{}) (*usersOutput, error) {
	rows, err := h.d.Auth.ListUsers(ctx)
	if err != nil {
		return nil, adminErr(err)
	}
	return &usersOutput{Body: UsersOf(rows)}, nil
}

func (h *AdminUsersHandlers) UsersCreate(ctx context.Context, in *userCreateInput) (*userOutput, error) {
	req := in.Body
	id, err := h.d.Auth.CreateUser(ctx, req.Login, req.Display, secret.New([]byte(req.Password)))
	if err != nil {
		return nil, adminErr(err)
	}
	row, err := h.d.Auth.UserByID(ctx, id)
	if err != nil {
		return nil, adminErr(err)
	}
	return &userOutput{Status: http.StatusCreated, Body: UserOf(row)}, nil
}

func (h *AdminUsersHandlers) UsersUpdate(ctx context.Context, in *userUpdateInput) (*userOutput, error) {
	caller, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	target, ok := positiveID(in.ID)
	if !ok {
		return nil, errNotFound()
	}
	req := in.Body
	if req.Disabled != nil && *req.Disabled && target == caller {
		return nil, apierr.AsClassified(apierr.Denied, "")
	}
	if req.Password != nil {
		if err = h.d.Auth.SetPassword(ctx, target, secret.New([]byte(*req.Password))); err != nil {
			return nil, adminErr(err)
		}
	}
	if req.ClearQuota {
		err = h.d.Auth.SetQuota(ctx, target, nil)
	} else if req.Quota != nil {
		err = h.d.Auth.SetQuota(ctx, target, req.Quota)
	}
	if err != nil {
		return nil, adminErr(err)
	}
	if req.Disabled != nil {
		if *req.Disabled {
			err = h.d.Auth.DisableAccount(ctx, target)
		} else {
			err = h.d.Auth.EnableAccount(ctx, target)
		}
		if err != nil {
			return nil, adminErr(err)
		}
	}
	row, err := h.d.Auth.UserByID(ctx, target)
	if err != nil {
		return nil, adminErr(err)
	}
	return &userOutput{Status: http.StatusOK, Body: UserOf(row)}, nil
}

func (h *AdminUsersHandlers) UsersDelete(ctx context.Context, in *userPathInput) (*noContentOutput, error) {
	caller, err := middleware.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	target, ok := positiveID(in.ID)
	if !ok {
		return nil, errNotFound()
	}
	if target == caller {
		return nil, apierr.AsClassified(apierr.Denied, "")
	}
	if h.d.CleanupHome != nil {
		if err = h.d.CleanupHome(ctx, files.UserID(target)); err != nil {
			h.d.Logger.Warn("cleaning up deleted user home failed", "user", target, "error", err)
		}
	}
	if err = h.d.Auth.DeleteUser(ctx, target); err != nil {
		return nil, adminErr(err)
	}
	return &noContentOutput{Status: http.StatusNoContent}, nil
}

func (h *AdminUsersHandlers) GroupsList(ctx context.Context, _ *struct{}) (*groupsOutput, error) {
	rows, err := h.d.Auth.ListGroups(ctx)
	if err != nil {
		return nil, adminErr(err)
	}
	return &groupsOutput{Body: GroupsOf(rows)}, nil
}

func (h *AdminUsersHandlers) GroupsCreate(ctx context.Context, in *groupCreateInput) (*groupOutput, error) {
	id, err := h.d.Auth.CreateGroup(ctx, in.Body.Name)
	if err != nil {
		return nil, adminErr(err)
	}
	return &groupOutput{Status: http.StatusCreated, Body: GroupView{ID: strconv.FormatInt(id, 10), Name: in.Body.Name, Members: []string{}}}, nil
}

func (h *AdminUsersHandlers) GroupsUpdate(ctx context.Context, in *groupUpdateInput) (*groupOutput, error) {
	id, ok := positiveID(in.ID)
	if !ok {
		return nil, errNotFound()
	}
	row, err := h.d.Auth.RenameGroup(ctx, id, in.Body.Name)
	if err != nil {
		return nil, adminErr(err)
	}
	return &groupOutput{Status: http.StatusOK, Body: GroupOf(row)}, nil
}

func (h *AdminUsersHandlers) GroupsDelete(ctx context.Context, in *userPathInput) (*noContentOutput, error) {
	id, ok := positiveID(in.ID)
	if !ok {
		return nil, errNotFound()
	}
	if err := h.d.Auth.DeleteGroup(ctx, id); err != nil {
		return nil, adminErr(err)
	}
	return &noContentOutput{Status: http.StatusNoContent}, nil
}

func (h *AdminUsersHandlers) MemberAdd(ctx context.Context, in *memberAddInput) (*noContentOutput, error) {
	group, ok := positiveID(in.ID)
	if !ok {
		return nil, errNotFound()
	}
	user, ok := positiveID(in.Body.User)
	if !ok {
		return nil, errNotFound()
	}
	if err := h.d.Auth.AddToGroup(ctx, user, group); err != nil {
		return nil, adminErr(err)
	}
	return &noContentOutput{Status: http.StatusNoContent}, nil
}

func (h *AdminUsersHandlers) MemberRemove(ctx context.Context, in *memberRemoveInput) (*noContentOutput, error) {
	group, ok := positiveID(in.ID)
	if !ok {
		return nil, errNotFound()
	}
	user, ok := positiveID(in.User)
	if !ok {
		return nil, errNotFound()
	}
	if err := h.d.Auth.RemoveFromGroup(ctx, user, group); err != nil {
		return nil, adminErr(err)
	}
	return &noContentOutput{Status: http.StatusNoContent}, nil
}

func (h *AdminUsersHandlers) Audit(ctx context.Context, in *auditInput) (*auditOutput, error) {
	rows, next, err := h.d.Auth.AuditPage(ctx, auth.AuditFilter{Event: in.Event, Before: queryInt(in.Before), Limit: adminAuditLimit(in.Limit)})
	if err != nil {
		return nil, adminErr(err)
	}
	return &auditOutput{Body: AuditPageOf(rows, next)}, nil
}

func positiveID(raw string) (int64, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && n > 0
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

func errNotFound() error { return apierr.AsClassified(apierr.NotFound, "") }

// adminErr answers a missing file as a bare not-found, without the file
// surface's reason key.
func adminErr(err error) error {
	if errors.Is(err, files.ErrNotFound) {
		return errNotFound()
	}
	return err
}
