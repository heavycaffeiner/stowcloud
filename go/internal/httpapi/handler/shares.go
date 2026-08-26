// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/num"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The admin folder-share surface: create, list, update and delete the shares
// an administrator owns. Every route here is access-gated to the admin role
// by the requirement the route table declares; the handlers only do the work.
//
// Every write here republishes SMB. A share is a section in that
// configuration, so adding, renaming, repointing or removing one that stops in
// this process leaves the daemon serving the previous set: a share deleted
// here and still reachable there.

// sharesChanged tells SMB the share set moved, and reports what happened.
//
// A publish failure does not fail the request. The share is already written
// and this server is already serving the new set; what is behind is the second
// surface, and that is a thing to report rather than a refusal for a change
// that already happened.
//
// The outcome goes in the response. Without it a share saved with the sidecar
// stopped answered a clean success, and "saved here, not applied over there"
// surfaced only on the health page whenever somebody next looked at it.
func sharesChanged(r *http.Request, d Deps) SMBOutcome {
	return smbChanged(r, d)
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
	// Force takes the restart a folder outside the sandbox's domain needs
	// even with work in flight. Only the create path reads it.
	Force bool `json:"force,omitempty"`
}

type shareResponse struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// HostPath is where the folder lives as this process sees it, which is
	// what the admin screen shows under the name and what tells two shares
	// with similar labels apart.
	HostPath       string `json:"host_path"`
	TrashEnabled   bool   `json:"trash_enabled"`
	SharedExternal bool   `json:"shared_external"`
	// Applied says whether the new folder is reachable now or after the
	// restart its path needs. Empty on the surfaces that are not answering a
	// create.
	Applied string `json:"applied,omitempty"`
	// BrokenReason is why this share cannot be served right now, or absent
	// when it can. A broken share is still listed, which is the whole point:
	// dropping it left an administrator with a health-endpoint line and no
	// screen offering retry, edit or remove.
	BrokenReason string `json:"broken_reason,omitempty"`
	// SMB is what the republish this write triggered did, absent on a
	// deployment with no sidecar and on the read paths.
	SMB *SMBOutcome `json:"smb,omitempty"`
}

func shareOf(def core.ShareDef) shareResponse {
	return shareResponse{
		ID: int64(def.ID), Name: def.Name, HostPath: def.Host,
		TrashEnabled:   def.TrashEnabled,
		SharedExternal: def.SharedExternally,
		BrokenReason:   def.BrokenReason,
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
				out = append(out, shareOf(def))
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
			return shareRefused(err)
		}
		if gerr := grantToCreator(r, d, actor, share); gerr != nil {
			return gerr
		}
		smb := sharesChanged(r, d)

		// A share is registered and granted the moment it is created, so it is
		// usually reachable at once: the sandbox grants each share's parent
		// directory, and a folder beside one that is already served is inside
		// the domain already.
		//
		// Under a parent the domain has never seen it is not. A Landlock
		// domain cannot be widened in a running process, so that share is
		// registered, granted, listed, and every attempt to open it answers
		// permission denied from the kernel. The restart is what makes it
		// reachable, and it is taken rather than left for somebody to work out.
		out := shareOf(share)
		out.SMB = outcomeOrNil(smb)
		if d.PathInJail != nil && !d.PathInJail(share.Host) {
			if err := requireIdle(d, req.Force); err != nil {
				// The share exists and is stored; what is refused is the
				// restart. The response says the folder is not reachable yet
				// rather than pretending the create failed.
				return err
			}
			out.Applied = AppliedEngineRestart
			if err := writeJSON(w, http.StatusCreated, out); err != nil {
				return err
			}
			if d.RequestRestart != nil {
				d.RequestRestart()
			}
			return nil
		}
		out.Applied = AppliedLive
		return writeJSON(w, http.StatusCreated, out)
	})
}

// shareRefused turns a rejected folder into an answer naming what is wrong
// with it.
//
// Registration already opens the directory and checks its filesystem, so the
// refusal carries the reason; it just arrived as a 500 with "share rejected"
// wrapped around an errno. The screen asks for a path and the answer to a bad
// one belongs beside the field, which is what a 422 with the kind in it gives.
//
// An error that is not a rejection is returned unchanged: a failed write is
// not the operator's typo.
func shareRefused(err error) error {
	// A name collision is already a 409 with its own message.
	if errors.Is(err, core.ErrConflict) {
		return err
	}
	// The three shapes a rejected folder takes: it is not there, it cannot be
	// read, or its filesystem is not one this server will serve. Anything else
	// is not the operator's typo and keeps its own status.
	var adm *vfs.AdmissionError
	if !errors.Is(err, vfs.ErrNotFound) && !errors.Is(err, vfs.ErrDenied) && !errors.As(err, &adm) {
		return err
	}
	return &apierr.RequestError{
		Status: http.StatusUnprocessableEntity, Code: apierr.CodeInvalidRequest,
		Message: "the folder cannot be served",
		Key:     "admin.share_rejected",
		Args: []apierr.Arg{
			{Name: "field", Value: "host_path"},
			{Name: "reason", Value: core.RejectionKind(err)},
		},
	}
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
		before, _ := d.Core.Share(shareIDOf(id))
		share, err := d.Core.UpdateShare(r.Context(), shareIDOf(id), patch)
		if err != nil {
			// A repointed path that will not open leaves the row written and
			// the share broken against it, which the core has already recorded.
			// The answer names what is wrong with the path rather than being a
			// bare failure, because the field the operator just typed is the
			// thing to put it beside.
			return shareRefused(err)
		}
		// Repointing a broken share at a path that works is the other repair,
		// beside retry, and it clears the same degradation. The name before the
		// edit, because a rename in the same request would leave the old name's
		// reason standing forever.
		if d.Health != nil && before.BrokenReason != "" {
			d.Health.ResolveShare(before.Name)
		}
		out := shareOf(share)
		out.SMB = outcomeOrNil(sharesChanged(r, d))
		return writeJSON(w, http.StatusOK, out)
	})
}

// outcomeOrNil drops the zero outcome, so a deployment with no sidecar sends
// no SMB field at all rather than an empty object a screen has to branch on.
func outcomeOrNil(o SMBOutcome) *SMBOutcome {
	if o.State == "" {
		return nil
	}
	return &o
}

// ShareRetry answers POST /api/admin/shares/{id}/retry.
//
// The disk came back and somebody is saying so. It exists as its own route
// rather than as a side effect of an edit because the ordinary repair is a
// remount that changes nothing about the share: a path that was right an hour
// ago is still right, and asking an administrator to retype it to prove it is
// a screen designed around the implementation.
func ShareRetry(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		actor, aerr := requireAdmin(r, d.Auth)
		if aerr != nil {
			return aerr
		}
		id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if perr != nil {
			return apierr.BadRequest("admin.share_id", "id")
		}
		share, err := d.Core.RetryShare(r.Context(), shareIDOf(id))
		record(r, d, actor, "share.retry", strconv.FormatInt(id, 10), err == nil)
		if err != nil {
			// Still broken, and the answer names why: the screen shows the same
			// reason it was already showing rather than a generic failure that
			// says nothing about whether the retry did anything.
			return shareRefused(err)
		}
		// The health surface is told here rather than left to the next probe
		// pass: an administrator who just fixed a share and then reads a status
		// still calling it broken has no way to tell a stale answer from a
		// repair that did not take.
		if d.Health != nil {
			d.Health.ResolveShare(share.Name)
		}
		// The set of live shares moved, so SMB has to catch up: a share that is
		// serving again here and absent there is the same half-applied state
		// every other write on this surface republishes to avoid.
		out := shareOf(share)
		out.SMB = outcomeOrNil(sharesChanged(r, d))
		out.Applied = AppliedLive
		return writeJSON(w, http.StatusOK, out)
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
		gone, _ := d.Core.Share(shareIDOf(id))
		derr := d.Core.DeleteShare(r.Context(), shareIDOf(id))
		record(r, d, actor, "share.delete", strconv.FormatInt(id, 10), derr == nil)
		if derr != nil {
			return derr
		}
		// Removing a broken share is a repair too, and it is the one that would
		// otherwise leave the deployment degraded forever: nothing re-probes a
		// share that no longer exists, so the reason would never clear.
		if d.Health != nil && gone.BrokenReason != "" {
			d.Health.ResolveShare(gone.Name)
		}
		// A delete answers with the outcome rather than 204, because the one
		// thing worth saying about removing a share is whether it also stopped
		// being served over SMB. A share deleted here and still reachable there
		// is the failure this reports.
		if smb := outcomeOrNil(sharesChanged(r, d)); smb != nil {
			return writeJSON(w, http.StatusOK, map[string]any{"smb": smb})
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}
