package handler

import (
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
)

// The admin folder-share surface: create, list, update and delete the shares
// an administrator owns. Every route here is access-gated to the admin role
// by the requirement the route table declares; the handlers only do the work.
//
// Every write here republishes SMB. A share is a section in that
// configuration, so adding, renaming, repointing or removing one that stops in
// this process leaves the daemon serving the previous set: a share deleted
// here and still reachable there.

// sharesChanged tells SMB the share set moved.
//
// A publish failure does not fail the request. The share is already written
// and this server is already serving the new set; what is behind is the second
// surface, and that is recorded as a degradation rather than turned into a
// refusal for a change that already happened.
func sharesChanged(r *http.Request, d Deps) {
	if d.SMBChanged != nil {
		d.SMBChanged(r.Context())
	}
}

type shareRequest struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	TrashEnabled *bool  `json:"trash_enabled,omitempty"`
}

type shareResponse struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	TrashEnabled   bool   `json:"trash_enabled"`
	SharedExternal bool   `json:"shared_external"`
}

// shareIDOf bounds an admin-named share id to the core's range before it
// crosses into a uint32-typed ShareID. An id outside the range cannot name a
// share this build holds, so it is refused as 0, which no registered share
// has and which the core turns into not-found.
func shareIDOf(id int64) core.ShareID {
	if id <= 0 {
		return 0
	}
	n, err := num.Narrow[uint32](id)
	if err != nil {
		return 0
	}
	return core.ShareID(n)
}

// Shares answers GET and POST /api/admin/shares.
func Shares(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, aerr := requireAdmin(r, d.Auth); aerr != nil {
			return aerr
		}
		if r.Method == http.MethodGet {
			defs := d.Core.Shares()
			out := make([]shareResponse, 0, len(defs))
			for _, def := range defs {
				out = append(out, shareResponse{
					ID: int64(def.ID), Name: def.Name,
					TrashEnabled: def.TrashEnabled, SharedExternal: def.SharedExternally,
				})
			}
			return writeJSON(w, http.StatusOK, map[string]any{"shares": out})
		}
		var req shareRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		if req.Name == "" || req.Host == "" {
			return apierr.BadRequest("admin.share_fields", "name")
		}
		share, err := d.Core.CreateShare(r.Context(), core.ShareSpec{Name: req.Name, Host: req.Host})
		if err != nil {
			return err
		}
		sharesChanged(r, d)
		return writeJSON(w, http.StatusCreated, shareResponse{ID: int64(share.ID), Name: share.Name, TrashEnabled: share.TrashEnabled})
	})
}

// ShareUpdate answers PATCH /api/admin/shares/{id}.
func ShareUpdate(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, aerr := requireAdmin(r, d.Auth); aerr != nil {
			return aerr
		}
		id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if perr != nil {
			return apierr.BadRequest("admin.share_id", "id")
		}
		var req shareRequest
		if derr := decodeJSON(r, &req); derr != nil {
			return derr
		}
		patch := core.SharePatch{TrashEnabled: req.TrashEnabled}
		if req.Name != "" {
			patch.Name = &req.Name
		}
		if req.Host != "" {
			patch.Host = &req.Host
		}
		share, err := d.Core.UpdateShare(r.Context(), shareIDOf(id), patch)
		if err != nil {
			return err
		}
		sharesChanged(r, d)
		return writeJSON(w, http.StatusOK, shareResponse{ID: int64(share.ID), Name: share.Name, TrashEnabled: share.TrashEnabled})
	})
}

// ShareDelete answers DELETE /api/admin/shares/{id}.
func ShareDelete(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, aerr := requireAdmin(r, d.Auth); aerr != nil {
			return aerr
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			return apierr.BadRequest("admin.share_id", "id")
		}
		if err := d.Core.DeleteShare(r.Context(), shareIDOf(id)); err != nil {
			return err
		}
		sharesChanged(r, d)
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}
