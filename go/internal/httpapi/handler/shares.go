// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
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

// shareRequest is the create and patch body.
//
// host_path is the one spelling: it is what the response carries, what the
// config file calls it and what the admin screen sends. This struct alone read
// "host", so every create from the screen decoded to an empty path and was
// refused as a malformed request naming the wrong field.
type shareRequest struct {
	Name         string `json:"name"`
	HostPath     string `json:"host_path"`
	TrashEnabled *bool  `json:"trash_enabled,omitempty"`
}

type shareResponse struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// HostPath is where the folder lives as this process sees it, which is
	// what the admin screen shows under the name and what tells two shares
	// with similar labels apart.
	HostPath     string `json:"host_path"`
	TrashEnabled bool   `json:"trash_enabled"`
	// ConfigDefined marks a share the config file declares. The screen hides
	// the delete affordance for one, because the next restart re-declares it
	// and the deletion would look like it silently failed.
	ConfigDefined  bool `json:"config_defined"`
	SharedExternal bool `json:"shared_external"`
	// RestartRequired marks a share that is registered and not yet reachable.
	// The sandbox's domain is built at startup from the shares known then and
	// cannot be widened, so a folder added afterwards answers permission
	// denied until the process restarts. Absent on every other response,
	// because it is only ever true for the share that was just created.
	RestartRequired bool `json:"restart_required,omitempty"`
}

func shareOf(def core.ShareDef, configDefined bool) shareResponse {
	return shareResponse{
		ID: int64(def.ID), Name: def.Name, HostPath: def.Host,
		TrashEnabled: def.TrashEnabled, ConfigDefined: configDefined,
		SharedExternal: def.SharedExternally,
	}
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
			// A bare array, which is what the client reads. Wrapped in an
			// object it parsed as no shares at all, so the admin screen showed
			// an empty list while the same folders browsed correctly one tab
			// over.
			out := make([]shareResponse, 0, len(defs))
			for _, def := range defs {
				out = append(out, shareOf(def, d.ConfigShare(def.Name)))
			}
			return writeJSON(w, http.StatusOK, out)
		}
		var req shareRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		// The refused field is named as the one that is actually missing. Both
		// were reported as "name", which pointed at the wrong input.
		if req.Name == "" {
			return apierr.BadRequest("admin.share_fields", "name")
		}
		if req.HostPath == "" {
			return apierr.BadRequest("admin.share_fields", "host_path")
		}
		actor, aerr := requireAdmin(r, d.Auth)
		if aerr != nil {
			return aerr
		}
		share, err := d.Core.CreateShare(r.Context(), core.ShareSpec{Name: req.Name, Host: req.HostPath})
		record(r, d, actor, "share.create", req.Name, err == nil)
		if err != nil {
			return err
		}
		if gerr := grantToCreator(r, d, actor, share); gerr != nil {
			return gerr
		}
		sharesChanged(r, d)
		out := shareOf(share, false)
		// Said rather than left to be discovered. Under a sandbox the folder is
		// registered, granted and listed on this screen, and every attempt to
		// open it answers permission denied, which reads as a broken save
		// instead of a restart that has not happened yet.
		out.RestartRequired = d.Sandboxed != nil && d.Sandboxed()
		return writeJSON(w, http.StatusCreated, out)
	})
}

// grantToCreator gives the administrator who added a share full access to it.
//
// A share is only reachable through a grant, and creating one wrote no grant
// at all: the folder appeared on the admin screen, was absent from the file
// browser, and listing it answered 404. From the outside that reads as a save
// that did not take.
//
// Only the creator, and only this share. Deny-by-default is the model and this
// does not widen it: every other account still needs a grant an administrator
// writes. What it fixes is the one case where the person who just named a
// folder could not open it.
//
// The same permission set the first administrator gets, for the same reason: a
// share you can see and cannot write is a second dead end.
func grantToCreator(r *http.Request, d Deps, actor int64, share core.ShareDef) error {
	const all = acl.Read | acl.Write | acl.Create | acl.Delete |
		acl.Rename | acl.Move | acl.Share | acl.Download

	if _, err := acl.CreateGrant(r.Context(), d.State.SQL(), acl.Grant{
		User:    actor,
		Share:   int64(share.ID),
		Allow:   all,
		Inherit: true,
		// The share's own name, which is what the interface draws as the
		// folder; an unlabeled grant falls back to a generated "share-N".
		Label: share.Name,
	}, d.Clock.Nanos()); err != nil {
		return err
	}
	// The evaluator serves requests from its own copy, so a grant only in the
	// database is one the next listing does not see.
	return d.ReloadACL(r.Context())
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
		if req.HostPath != "" {
			patch.Host = &req.HostPath
		}
		share, err := d.Core.UpdateShare(r.Context(), shareIDOf(id), patch)
		if err != nil {
			return err
		}
		sharesChanged(r, d)
		return writeJSON(w, http.StatusOK, shareOf(share, d.ConfigShare(share.Name)))
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
		actor, aerr2 := requireAdmin(r, d.Auth)
		if aerr2 != nil {
			return aerr2
		}
		derr := d.Core.DeleteShare(r.Context(), shareIDOf(id))
		record(r, d, actor, "share.delete", strconv.FormatInt(id, 10), derr == nil)
		if derr != nil {
			return derr
		}
		sharesChanged(r, d)
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}
