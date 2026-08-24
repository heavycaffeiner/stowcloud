// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The share-link surface: mint a link on a path the caller may share, list
// the caller's links, and revoke one. Revocation is permanent and the same
// link is never recreated.

type linkRequest struct {
	Path     string `json:"path"`
	Perms    uint16 `json:"perms,omitempty"`
	Password string `json:"password,omitempty"`
	Expires  string `json:"expires,omitempty"`
	MaxDown  int32  `json:"max_downloads,omitempty"`
	Label    string `json:"label,omitempty"`
	Note     string `json:"note,omitempty"`
}

type linkResponse struct {
	ID          int64  `json:"id"`
	URL         string `json:"url,omitempty"`
	Path        string `json:"path"`
	Perms       uint16 `json:"perms"`
	HasPassword bool   `json:"has_password"`
	Expires     int64  `json:"expires_ns,omitempty"`
	MaxDown     int32  `json:"max_downloads"`
	Downs       int32  `json:"downloads"`
	Label       string `json:"label,omitempty"`
	Note        string `json:"note,omitempty"`
}

// Links answers GET and POST /api/fs/link.
func Links(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		if r.Method == http.MethodGet {
			links, err := d.Core.ListLinks(r.Context(), uid, nil)
			if err != nil {
				return err
			}
			out := make([]linkResponse, 0, len(links))
			for _, l := range links {
				out = append(out, linkToResponse(d, uid, l))
			}
			return writeJSON(w, http.StatusOK, map[string]any{"links": out})
		}
		var req linkRequest
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		vp, err := vfs.ParseVpath(req.Path)
		if err != nil {
			return err
		}
		resolved, err := d.Core.Resolve(uid, vp, acl.Share)
		if err != nil {
			return err
		}
		spec := core.LinkSpec{
			Perms:   acl.Perms(req.Perms),
			Label:   req.Label,
			Note:    req.Note,
			MaxDown: req.MaxDown,
		}
		if req.Password != "" {
			spec.Password = &req.Password
		}
		link, token, err := d.Core.CreateLink(r.Context(), resolved, spec)
		if err != nil {
			return err
		}
		out := linkToResponse(d, uid, link)
		out.URL = "/s/" + string(token.Reveal())
		return writeJSON(w, http.StatusCreated, out)
	})
}

// LinkDelete answers DELETE /api/fs/link/{id}.
func LinkDelete(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			return apierr.BadRequest("fs.link_id", "id")
		}
		if err := d.Core.DeleteLink(r.Context(), uid, id); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

func linkToResponse(d Deps, uid core.UserID, l core.Link) linkResponse {
	out := linkResponse{
		ID: l.ID, Perms: uint16(l.Perms), HasPassword: l.HasPassword,
		MaxDown: l.MaxDown, Downs: l.Downs, Label: l.Label, Note: l.Note,
	}
	if l.Expires != 0 {
		out.Expires = l.Expires
	}
	if vp, err := d.Core.VpathFor(uid, l.Share, l.Path); err == nil {
		out.Path = vp.String()
	}
	return out
}

// LinkPublic answers GET /s/{token}, the unauthenticated view of a public
// link. The token authenticates the request; the password, when one is set,
// is the second gate.
func LinkPublic(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		token := r.PathValue("token")
		link, entry, err := d.Core.LinkPublic(r.Context(), token)
		if err != nil {
			return err
		}
		if link.HasPassword {
			// The metadata is public once the token is right; the bytes need
			// the password, answered through POST /s/{token}/password.
			if _, err := d.Core.LinkCheckPassword(r.Context(), link, ""); err != nil {
				return err
			}
		}
		return writeJSON(w, http.StatusOK, map[string]any{
			"id": link.ID, "name": entry.Name, "is_dir": entry.IsDir,
			"size": entry.Size, "label": link.Label, "note": link.Note,
			"has_password": link.HasPassword,
		})
	})
}

// LinkDownload answers GET /s/{token}/download, serving the bytes. A
// password-protected link verifies the password header first; each download
// counts against the cap, which is what turns an exhausted link into a 410.
func LinkDownload(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		link, _, err := d.Core.LinkPublic(r.Context(), r.PathValue("token"))
		if err != nil {
			return err
		}
		if link.HasPassword {
			ok, aerr := d.Core.LinkCheckPassword(r.Context(), link, r.Header.Get("X-Link-Password"))
			if aerr != nil {
				return aerr
			}
			if !ok {
				return apierr.BadRequest("fs.link_password", "password")
			}
		}
		entry, stream, err := d.Core.LinkStream(r.Context(), link, nil)
		if err != nil {
			return err
		}
		defer stream.Close() //nolint:errcheck // the download is done either way.
		if err := d.Core.NoteLinkDownload(r.Context(), link); err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatUint(entry.Size, 10))
		w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(entry.Name)+`"`)
		if _, err := io.Copy(w, stream); err != nil {
			return err
		}
		return nil
	})
}

func sanitizeFilename(name string) string {
	out := make([]rune, 0, len(name))
	for _, c := range name {
		if c == '"' || c == '\\' || c == '\n' || c == '\r' {
			out = append(out, '_')
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// LinkUpdate answers PATCH /api/shares/{id}.
//
// Widening the permissions re-checks against what the owner may do now rather
// than what they could when the link was minted. A grant revoked since then
// must not be re-widened through an update.
func LinkUpdate(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		id, perr := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if perr != nil {
			return apierr.BadRequest("shares.bad_id", "id")
		}

		// Every field is a pointer, so a field the client left out is left
		// alone. The two that can also be cleared are pointers to pointers,
		// because "leave the expiry" and "remove the expiry" are different
		// requests and one nil cannot say both.
		var req struct {
			Perms    *uint16 `json:"perms"`
			Password *string `json:"password"`
			Expires  *string `json:"expires_ns"`
			MaxDown  *int32  `json:"max_downloads"`
			Label    *string `json:"label"`
			Note     *string `json:"note"`
		}
		raw, rerr := readBody(r)
		if rerr != nil {
			return rerr
		}
		if uerr := json.Unmarshal(raw, &req); uerr != nil {
			return apierr.BadRequest("shares.bad_patch", "body")
		}
		// Which keys were present at all, which is what tells a clear apart
		// from an omission.
		var present map[string]json.RawMessage
		if uerr := json.Unmarshal(raw, &present); uerr != nil {
			return apierr.BadRequest("shares.bad_patch", "body")
		}

		patch := core.LinkPatch{Label: req.Label, Note: req.Note}
		if req.Perms != nil {
			p := acl.Perms(*req.Perms)
			patch.Perms = &p
		}
		if _, ok := present["password"]; ok {
			patch.Password = &req.Password
		}
		if _, ok := present["max_downloads"]; ok {
			patch.MaxDown = &req.MaxDown
		}
		if _, ok := present["expires_ns"]; ok {
			var exp *int64
			if req.Expires != nil {
				n, cerr := strconv.ParseInt(*req.Expires, 10, 64)
				if cerr != nil {
					return apierr.BadRequest("shares.bad_expiry", "expires_ns")
				}
				exp = &n
			}
			patch.Expires = &exp
		}

		link, uerr := d.Core.UpdateLink(r.Context(), uid, id, patch)
		if uerr != nil {
			return uerr
		}
		return writeJSON(w, http.StatusOK, linkToResponse(d, uid, link))
	})
}
