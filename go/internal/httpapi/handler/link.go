// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"encoding/base64"
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

// linkRequest is the create body.
//
// perms is an object and the expiry is expires_ns, which is what the response
// carries and what the share dialog sends. They were a bit field and "expires"
// here, so every link the interface tried to create arrived with no
// permissions and an ignored expiry, and the refusal that produced said only
// that the request was malformed.
type linkRequest struct {
	Path     string     `json:"path"`
	Perms    *permsJSON `json:"perms,omitempty"`
	Password string     `json:"password,omitempty"`
	Expires  string     `json:"expires_ns,omitempty"`
	// MaxDown is a pointer because unlimited is -1 and absent is unlimited.
	// As a plain int32 an omitted field decoded to zero, which the core reads
	// as a cap of none: every link the dialog created without a download limit
	// was exhausted before it was handed out, and opening it once answered
	// gone.
	MaxDown *int32 `json:"max_downloads,omitempty"`
	Label   string `json:"label,omitempty"`
	Note    string `json:"note,omitempty"`
}

type linkResponse struct {
	ID          int64     `json:"id"`
	URL         string    `json:"url,omitempty"`
	Path        string    `json:"path"`
	Perms       permsJSON `json:"perms"`
	HasPassword bool      `json:"has_password"`
	Expires     int64     `json:"expires_ns,omitempty"`
	MaxDown     int32     `json:"max_downloads"`
	Downs       int32     `json:"downloads"`
	Label       string    `json:"label,omitempty"`
	Note        string    `json:"note,omitempty"`
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
				out = append(out, linkToResponse(d, uid, l, requestBase(r)))
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
			Label: req.Label,
			Note:  req.Note,
			// Unlimited unless the caller named a cap.
			MaxDown: -1,
		}
		if req.MaxDown != nil {
			spec.MaxDown = *req.MaxDown
		}
		if req.Perms != nil {
			spec.Perms = permsFrom(*req.Perms)
		}
		if req.Expires != "" {
			n, cerr := strconv.ParseInt(req.Expires, 10, 64)
			if cerr != nil {
				return apierr.BadRequest("shares.bad_expiry", "expires_ns")
			}
			spec.Expires = n
		}
		if req.Password != "" {
			spec.Password = &req.Password
		}
		link, token, err := d.Core.CreateLink(r.Context(), resolved, spec)
		if err != nil {
			return err
		}
		out := linkToResponse(d, uid, link, requestBase(r))
		out.URL = requestBase(r) + "/s/" + string(token.Reveal())
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

// requestBase is the scheme and host this request arrived on.
//
// Always https: this server has no plaintext listener, so a link naming http
// would name a port nothing answers on.
func requestBase(r *http.Request) string {
	if r.Host == "" {
		return ""
	}
	return "https://" + r.Host
}

// linkToResponse renders one link.
//
// base is the scheme and host to build the shareable URL from. The URL is
// absolute because the one thing anybody does with a share link is copy it and
// send it somewhere else: a path alone only works for somebody who already
// knows the address, which is the opposite of what the link is for.
//
// It is filled for every link the token can be recovered for, not only the one
// just created. A list that showed no address made an existing link something
// an operator could see and not hand out.
func linkToResponse(d Deps, uid core.UserID, l core.Link, base string) linkResponse {
	out := linkResponse{
		ID: l.ID, Perms: permsOf(l.Perms), HasPassword: l.HasPassword,
		MaxDown: l.MaxDown, Downs: l.Downs, Label: l.Label, Note: l.Note,
	}
	if l.Token != nil && base != "" {
		out.URL = base + "/s/" + string(l.Token.Reveal())
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
//
// The shape it answers with is what the page reads: a shared folder carries
// its listing, and a file carries its own size. Both come back from one
// endpoint because a visitor holding a link does not know which they have, and
// two addresses would mean the page guessing before it asks.
func LinkPublic(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		token := r.PathValue("token")
		link, _, err := d.Core.LinkPublic(r.Context(), token)
		if err != nil {
			return err
		}
		// A locked link answers with nothing but the fact that it is locked.
		// The name and the listing are behind the password too: a link whose
		// contents are readable without it is one where the password only
		// guards the bytes.
		if link.HasPassword && !linkUnlocked(r, d, link) {
			return writeJSON(w, http.StatusOK, map[string]any{"protected": true})
		}

		listing, lerr := d.Core.LinkBrowse(r.Context(), link, r.URL.Query().Get("path"))
		if lerr != nil {
			return lerr
		}
		out := map[string]any{
			"protected": false,
			"id":        link.ID,
			"name":      listing.Name,
			"is_dir":    listing.IsDir,
			"size":      listing.Size,
			"label":     link.Label,
			"note":      link.Note,
			"path":      listing.Path,
			// What the visitor may do, which the page draws its buttons from.
			"can_download": link.Perms.Has(acl.Download),
			"drop":         link.Perms.Has(acl.Create) && !link.Perms.Has(acl.Read),
			"has_password": link.HasPassword,
		}
		// A drop link admits files and shows none: whoever holds it can put
		// something in and cannot see what is already there. Sending the listing
		// anyway would make the page draw what the link exists not to reveal.
		if listing.IsDir && link.Perms.Has(acl.Read) {
			entries := make([]map[string]any, 0, len(listing.Entries))
			for _, e := range listing.Entries {
				kind := "file"
				if e.IsDir {
					kind = "dir"
				}
				entries = append(entries, map[string]any{
					"name": e.Name, "kind": kind, "size": e.Size,
				})
			}
			out["entries"] = entries
		}
		return writeJSON(w, http.StatusOK, out)
	})
}

// linkCookie is where an unlocked link's proof lives.
//
// One cookie per link, scoped to that link's own path, so unlocking one does
// not unlock another and the proof is not sent anywhere it is not needed.
func linkCookie(id int64) string {
	return "sc_link_" + strconv.FormatInt(id, 10)
}

// linkUnlocked reports whether this visitor has already answered the password.
//
// The cookie holds the password rather than a token of its own. It is scoped
// to the link's path, sent only over TLS, and unreadable to script; what it
// protects is one link's contents, and a separate session table for public
// links would be a second credential store for the same fact.
func linkUnlocked(r *http.Request, d Deps, link core.Link) bool {
	c, err := r.Cookie(linkCookie(link.ID))
	if err != nil || c.Value == "" {
		return false
	}
	raw, derr := base64.RawURLEncoding.DecodeString(c.Value)
	if derr != nil {
		return false
	}
	ok, cerr := d.Core.LinkCheckPassword(r.Context(), link, string(raw))
	return cerr == nil && ok
}

// LinkUnlock answers POST /s/{token}/auth, which is how a visitor answers the
// password.
//
// A correct password sets the cookie the reads above look for. A wrong one
// answers the same way whether the link is locked or not, so the endpoint does
// not report which links have passwords.
func LinkUnlock(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		link, _, err := d.Core.LinkPublic(r.Context(), r.PathValue("token"))
		if err != nil {
			return err
		}
		var req struct {
			Password string `json:"password"`
		}
		if derr := decodeJSON(r, &req); derr != nil {
			return derr
		}
		ok, cerr := d.Core.LinkCheckPassword(r.Context(), link, req.Password)
		if cerr != nil {
			return cerr
		}
		if !ok {
			return apierr.BadRequest("fs.link_password", "password")
		}
		http.SetCookie(w, &http.Cookie{
			Name:     linkCookie(link.ID),
			Value:    base64.RawURLEncoding.EncodeToString([]byte(req.Password)),
			Path:     "/s/" + r.PathValue("token"),
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})
		w.WriteHeader(http.StatusNoContent)
		return nil
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
			Perms    *permsJSON `json:"perms"`
			Password *string    `json:"password"`
			Expires  *string    `json:"expires_ns"`
			MaxDown  *int32     `json:"max_downloads"`
			Label    *string    `json:"label"`
			Note     *string    `json:"note"`
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
			p := permsFrom(*req.Perms)
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
		return writeJSON(w, http.StatusOK, linkToResponse(d, uid, link, requestBase(r)))
	})
}
