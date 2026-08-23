package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
)

// The grant surface: who may do what, where.
//
// A write here reloads the evaluator before it answers. A grant that is live
// in the database and not in the process serving requests is a permission
// decision that depends on which half was asked, and the window is exactly as
// long as nobody notices.
//
// The same applies to the other protocol. A whole-share grant becomes an
// account list in the SMB configuration, so a write here that stops at this
// process leaves the daemon serving the previous list: a revocation the admin
// screen reports as done, and access that is still live.

// grantsChanged reloads the evaluator and republishes SMB.
//
// Both, always, and in that order: the evaluator is what this server enforces
// with and a stale one is wrong now, while the SMB push is a second surface
// that has to catch up. A publish failure does not fail the request, because
// the grant is already committed and the web surface is already enforcing it;
// it is recorded as a degradation instead.
func grantsChanged(r *http.Request, d Deps) error {
	if err := d.ReloadACL(r.Context()); err != nil {
		return err
	}
	if d.SMBChanged != nil {
		d.SMBChanged(r.Context())
	}
	return nil
}

// grantPrincipal is who a grant is written for.
//
// One object rather than two optional ids, which is what the screen reads. It
// was sent as a bare "user" or "group" field, so every row arrived with an
// undefined principal and the grant list could not say who anything was for.
type grantPrincipal struct {
	Kind string `json:"kind"` // "user" or "group"
	ID   int64  `json:"id"`
}

// adminGrant is one grant as the admin screen reads it.
type adminGrant struct {
	ID        int64          `json:"id"`
	Principal grantPrincipal `json:"principal"`
	// User and Group stay beside it: the compatibility surface and the
	// query parameters this same handler takes still name them, and both are
	// derived from the one principal above rather than set separately.
	User      *int64   `json:"user,omitempty"`
	Group     *int64   `json:"group,omitempty"`
	Share     int64    `json:"share"`
	Subpath   string   `json:"subpath"`
	Allow     []string `json:"allow"`
	Deny      []string `json:"deny"`
	Inherit   bool     `json:"inherit"`
	Label     *string  `json:"label"`
	CreatedNs string   `json:"created_ns"`
}

// AdminGrants answers GET and POST /api/admin/grants.
func AdminGrants(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		actor, err := requireAdmin(r, d.Auth)
		if err != nil {
			return err
		}

		if r.Method == http.MethodGet {
			filter := acl.GrantFilter{
				User:  queryInt(r, "user"),
				Group: queryInt(r, "group"),
				Share: queryInt(r, "share"),
			}
			grants, err := acl.ListGrants(r.Context(), d.State.SQL(), filter)
			if err != nil {
				return err
			}
			out := make([]adminGrant, 0, len(grants))
			for _, g := range grants {
				out = append(out, toAdminGrant(g))
			}
			return writeJSON(w, http.StatusOK, out)
		}

		var req struct {
			User    int64    `json:"user"`
			Group   int64    `json:"group"`
			Share   int64    `json:"share"`
			Subpath string   `json:"subpath"`
			Allow   []string `json:"allow"`
			Deny    []string `json:"deny"`
			Inherit bool     `json:"inherit"`
			Label   string   `json:"label"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		// A grant for both an account and a group is two rules written as one,
		// and which one applied would depend on how it was read.
		if (req.User == 0) == (req.Group == 0) {
			return apierr.BadRequest("admin.grant_principal", "user")
		}
		allow, aerr := parsePerms(req.Allow)
		if aerr != nil {
			return aerr
		}
		deny, derr := parsePerms(req.Deny)
		if derr != nil {
			return derr
		}
		path := acl.ParsePath(req.Subpath)

		g, cerr := acl.CreateGrant(r.Context(), d.State.SQL(), acl.Grant{
			User: req.User, Group: req.Group, Share: req.Share,
			Subpath: path, Allow: allow, Deny: deny,
			Inherit: req.Inherit, Label: req.Label,
		}, d.Clock.Nanos())
		// Recorded either way. A grant is who may read what, so an attempt that
		// was refused is as much a thing an operator reads this log for as one
		// that succeeded.
		record(r, d, actor, "grant.create", grantTarget(req.User, req.Group, req.Share), cerr == nil)
		if cerr != nil {
			if errors.Is(cerr, acl.ErrNoSuchGrant) {
				return apierr.BadRequest("admin.grant_principal", "user")
			}
			return cerr
		}
		if rerr := grantsChanged(r, d); rerr != nil {
			return rerr
		}
		return writeJSON(w, http.StatusCreated, toAdminGrant(g))
	})
}

// AdminGrant answers PATCH and DELETE /api/admin/grants/{id}.
func AdminGrant(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		actor, err := requireAdmin(r, d.Auth)
		if err != nil {
			return err
		}
		id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if perr != nil {
			return apierr.BadRequest("admin.bad_grant_id", "id")
		}

		if r.Method == http.MethodDelete {
			derr := acl.DeleteGrant(r.Context(), d.State.SQL(), id)
			// A revocation is the entry an operator comes to this log for.
			record(r, d, actor, "grant.delete", strconv.FormatInt(id, 10), derr == nil)
			if derr != nil {
				return grantError(derr)
			}
			if rerr := grantsChanged(r, d); rerr != nil {
				return rerr
			}
			w.WriteHeader(http.StatusNoContent)
			return nil
		}

		var patch struct {
			Allow   []string `json:"allow"`
			Deny    []string `json:"deny"`
			Inherit *bool    `json:"inherit"`
		}
		if derr := decodeJSON(r, &patch); derr != nil {
			return derr
		}
		allow, aerr := parsePerms(patch.Allow)
		if aerr != nil {
			return aerr
		}
		deny, derr := parsePerms(patch.Deny)
		if derr != nil {
			return derr
		}
		inherit := patch.Inherit != nil && *patch.Inherit

		uerr := acl.UpdateGrant(r.Context(), d.State.SQL(), id, allow, deny, inherit)
		record(r, d, actor, "grant.update", strconv.FormatInt(id, 10), uerr == nil)
		if uerr != nil {
			return grantError(uerr)
		}
		if rerr := grantsChanged(r, d); rerr != nil {
			return rerr
		}

		grants, lerr := acl.ListGrants(r.Context(), d.State.SQL(), acl.GrantFilter{})
		if lerr != nil {
			return lerr
		}
		for _, g := range grants {
			if g.ID == id {
				return writeJSON(w, http.StatusOK, toAdminGrant(g))
			}
		}
		return grantError(acl.ErrNoSuchGrant)
	})
}

// grantTarget names what a grant was about, for the log.
func grantTarget(user, group, share int64) string {
	who := "group " + strconv.FormatInt(group, 10)
	if user != 0 {
		who = "user " + strconv.FormatInt(user, 10)
	}
	return who + " on share " + strconv.FormatInt(share, 10)
}

func grantError(err error) error {
	if errors.Is(err, acl.ErrNoSuchGrant) {
		return &apierr.RequestError{
			Status: http.StatusNotFound, Code: apierr.CodeFsNotFound,
			Message: "no such grant", Key: "admin.grant_missing",
		}
	}
	return err
}

func toAdminGrant(g acl.Grant) adminGrant {
	out := adminGrant{
		ID: g.ID, Share: g.Share, Subpath: g.Subpath.String(),
		Allow: permNames(g.Allow), Deny: permNames(g.Deny),
		Inherit: g.Inherit,
		// Nanoseconds as a string: the value does not fit a double, and the
		// client declares it as text for that reason.
		CreatedNs: strconv.FormatInt(g.CreatedNs, 10),
	}
	// A label the screen can distinguish from "not set": an empty string is a
	// label somebody typed nothing into, and null is one nobody set.
	if g.Label != "" {
		label := g.Label
		out.Label = &label
	}
	if g.User != 0 {
		u := g.User
		out.User = &u
		out.Principal = grantPrincipal{Kind: "user", ID: u}
	}
	if g.Group != 0 {
		gr := g.Group
		out.Group = &gr
		out.Principal = grantPrincipal{Kind: "group", ID: gr}
	}
	return out
}

// parsePerms turns the client's names into the bits.
//
// An unknown name is refused rather than ignored. Ignoring it stores a grant
// that is missing a permission the administrator asked for, and the screen
// then shows the grant they wrote while the server holds a weaker one.
func parsePerms(names []string) (acl.Perms, error) {
	var out acl.Perms
	for _, n := range names {
		p, ok := acl.PermByName(n)
		if !ok {
			return 0, apierr.BadRequest("admin.grant_perm", "allow")
		}
		out |= p
	}
	return out, nil
}

func permNames(p acl.Perms) []string {
	out := []string{}
	for _, np := range acl.NamedPerms() {
		if p.Has(np.Perm) {
			out = append(out, np.Name)
		}
	}
	return out
}

func queryInt(r *http.Request, key string) int64 {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
