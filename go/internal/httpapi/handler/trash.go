package handler

import (
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// Trash answers GET /api/trash?path=... with the share's trash listing.
func Trash(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		p, err := pathOf(r)
		if err != nil {
			return err
		}
		resolved, err := d.Core.Resolve(uid, p, acl.Read)
		if err != nil {
			return err
		}
		rows, err := d.Core.TrashList(r.Context(), resolved)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, map[string]any{"entries": rows})
	})
}

// TrashRestore answers POST /api/trash/restore: the path whose trash is
// addressed, and the trash entry id. The response is the path the entry
// landed back at.
func TrashRestore(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			Path string `json:"path"`
			ID   string `json:"id"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		vp, err := vfs.ParseVpath(req.Path)
		if err != nil {
			return err
		}
		resolved, err := d.Core.Resolve(uid, vp, acl.Create)
		if err != nil {
			return err
		}
		restored, err := d.Core.TrashRestore(r.Context(), resolved, req.ID)
		if err != nil {
			return err
		}
		out, verr := d.Core.VpathFor(uid, resolved.Share(), restored.Share())
		if verr != nil {
			return verr
		}
		return writeJSON(w, http.StatusOK, map[string]any{"path": out.String()})
	})
}

// TrashPurge answers POST /api/trash/purge, emptying one share's trash or a
// single entry of it.
func TrashPurge(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			Path string `json:"path"`
			ID   string `json:"id,omitempty"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
		vp, err := vfs.ParseVpath(req.Path)
		if err != nil {
			return err
		}
		resolved, err := d.Core.Resolve(uid, vp, acl.Delete)
		if err != nil {
			return err
		}
		var id *string
		if req.ID != "" {
			id = &req.ID
		}
		if err := d.Core.TrashPurge(r.Context(), resolved, id); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}
