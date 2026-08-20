package handler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/secret"
)

// The administrator's account surface: accounts and groups.
//
// Everything here is reachable only with a session, never an app password. An
// app password is a filesystem capability handed to a device, and a device that
// can create an administrator is a device that can grant itself anything.

// adminUser is one account as the admin screen reads it.
//
// The two counters are strings because they exceed what a JSON number carries
// exactly, and a quota that silently rounds is a quota that is not the one that
// was set.
type adminUser struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name"`
	IsAdmin     bool    `json:"is_admin"`
	Disabled    bool    `json:"disabled"`
	TOTPEnabled bool    `json:"totp_enabled"`
	SMBEnabled  bool    `json:"smb_enabled"`
	CreatedNs   string  `json:"created_ns"`
	QuotaBytes  *string `json:"quota_bytes"`
	UsageBytes  string  `json:"usage_bytes"`
}

// AdminUsers answers GET and POST /api/admin/users.
func AdminUsers(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		if r.Method == http.MethodGet {
			users, err := listAdminUsers(r.Context(), d)
			if err != nil {
				return err
			}
			return writeJSON(w, http.StatusOK, users)
		}

		var req struct {
			Name     string `json:"name"`
			Password string `json:"password"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if req.Name == "" || req.Password == "" {
			return apierr.BadRequest("admin.user_fields", "name")
		}
		id, err := d.Auth.CreateUser(r.Context(), req.Name, req.Name, secret.New([]byte(req.Password)))
		if err != nil {
			return err
		}
		one, err := adminUserByID(r.Context(), d, id)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusCreated, one)
	})
}

// AdminUser answers PATCH and DELETE /api/admin/users/{id}.
func AdminUser(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		actor, err := requireAdmin(r, d.Auth)
		if err != nil {
			return err
		}
		id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if perr != nil {
			return apierr.BadRequest("admin.bad_user_id", "id")
		}

		if r.Method == http.MethodDelete {
			// An administrator deleting themselves leaves a deployment nobody
			// can administer, and the recovery is a database edit. Refused
			// rather than confirmed, because a confirmation dialogue is a
			// thing people click through.
			if id == actor {
				return &apierr.RequestError{
					Status: http.StatusConflict, Code: apierr.CodeFsConflict,
					Message: "an administrator cannot delete their own account",
					Key:     "admin.cannot_delete_self",
				}
			}
			if derr := d.Auth.DeleteUser(r.Context(), id); derr != nil {
				return derr
			}
			w.WriteHeader(http.StatusNoContent)
			return nil
		}

		var patch struct {
			Disabled   *bool  `json:"disabled"`
			QuotaBytes *int64 `json:"quota_bytes"`
		}
		if derr := decodeJSON(r, &patch); derr != nil {
			return derr
		}
		if patch.Disabled != nil {
			// Same reasoning as the delete above: an administrator who
			// disables their own account is locked out of the screen that
			// would re-enable it.
			if id == actor && *patch.Disabled {
				return &apierr.RequestError{
					Status: http.StatusConflict, Code: apierr.CodeFsConflict,
					Message: "an administrator cannot disable their own account",
					Key:     "admin.cannot_disable_self",
				}
			}
			if *patch.Disabled {
				if aerr := d.Auth.DisableAccount(r.Context(), id); aerr != nil {
					return aerr
				}
			} else if aerr := d.Auth.EnableAccount(r.Context(), id); aerr != nil {
				return aerr
			}
		}
		if patch.QuotaBytes != nil {
			if qerr := d.Auth.SetQuota(r.Context(), id, patch.QuotaBytes); qerr != nil {
				return qerr
			}
		}
		one, uerr := adminUserByID(r.Context(), d, id)
		if uerr != nil {
			return uerr
		}
		return writeJSON(w, http.StatusOK, one)
	})
}

func listAdminUsers(ctx context.Context, d Deps) ([]adminUser, error) {
	rows, err := d.Auth.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]adminUser, 0, len(rows))
	for _, u := range rows {
		out = append(out, toAdminUser(u))
	}
	return out, nil
}

func adminUserByID(ctx context.Context, d Deps, id int64) (adminUser, error) {
	rows, err := d.Auth.ListUsers(ctx)
	if err != nil {
		return adminUser{}, err
	}
	for _, u := range rows {
		if u.ID == id {
			return toAdminUser(u), nil
		}
	}
	return adminUser{}, &apierr.RequestError{
		Status: http.StatusNotFound, Code: apierr.CodeFsNotFound,
		Message: "no such account", Key: "admin.user_missing",
	}
}

func toAdminUser(u auth.UserRow) adminUser {
	out := adminUser{
		ID: u.ID, Name: u.Name, DisplayName: u.Display,
		IsAdmin: u.IsAdmin, Disabled: u.Disabled,
		TOTPEnabled: u.TOTPEnabled, SMBEnabled: u.SMBEnabled,
		CreatedNs:  strconv.FormatInt(u.CreatedNs, 10),
		UsageBytes: strconv.FormatUint(u.UsageBytes, 10),
	}
	if u.QuotaBytes != nil {
		q := strconv.FormatInt(*u.QuotaBytes, 10)
		out.QuotaBytes = &q
	}
	return out
}

// adminGroup is one group as the admin screen reads it.
type adminGroup struct {
	ID      int64   `json:"id"`
	Name    string  `json:"name"`
	Members []int64 `json:"members"`
}

// AdminGroups answers GET and POST /api/admin/groups.
func AdminGroups(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		if r.Method == http.MethodGet {
			groups, err := d.Auth.ListGroups(r.Context())
			if err != nil {
				return err
			}
			out := make([]adminGroup, 0, len(groups))
			for _, g := range groups {
				out = append(out, adminGroup{ID: g.ID, Name: g.Name, Members: g.Members})
			}
			return writeJSON(w, http.StatusOK, out)
		}

		var req struct {
			Name string `json:"name"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if req.Name == "" {
			return apierr.BadRequest("admin.group_fields", "name")
		}
		id, err := d.Auth.CreateGroup(r.Context(), req.Name)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusCreated, adminGroup{ID: id, Name: req.Name, Members: []int64{}})
	})
}

// AdminGroup answers DELETE /api/admin/groups/{id}.
func AdminGroup(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if perr != nil {
			return apierr.BadRequest("admin.bad_group_id", "id")
		}
		if err := d.Auth.DeleteGroup(r.Context(), id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &apierr.RequestError{
					Status: http.StatusNotFound, Code: apierr.CodeFsNotFound,
					Message: "no such group", Key: "admin.group_missing",
				}
			}
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// AdminGroupMembers answers the membership routes.
//
// Adding is a PUT of the whole set and removing names one account, which is the
// shape the client already uses: the screen edits a list and the deletion is a
// single row's button.
func AdminGroupMembers(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		gid, perr := strconv.ParseInt(r.PathValue("gid"), 10, 64)
		if perr != nil {
			return apierr.BadRequest("admin.bad_group_id", "gid")
		}

		if r.Method == http.MethodDelete {
			uid, uerr := strconv.ParseInt(r.PathValue("user"), 10, 64)
			if uerr != nil {
				return apierr.BadRequest("admin.bad_user_id", "user")
			}
			if err := d.Auth.RemoveFromGroup(r.Context(), uid, gid); err != nil {
				return err
			}
			w.WriteHeader(http.StatusNoContent)
			return nil
		}

		var req struct {
			User int64 `json:"user"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if req.User == 0 {
			return apierr.BadRequest("admin.user_fields", "user")
		}
		if err := d.Auth.AddToGroup(r.Context(), req.User, gid); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}
