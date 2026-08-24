// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The trash surface.
//
// Trash is one screen for the whole account rather than one per share: a
// person who deleted something remembers deleting it, not which share it was
// in. The store underneath is per share, because that is where a trashed file
// physically sits, so an entry id carries the share it came from: "{share}:{id}".
//
// That encoding is the whole of the crossing. The client round-trips the id
// verbatim and never parses it, which is what lets the two sides disagree
// about how trash is organised without either having to know.

// trashRow is one entry as the client reads it.
type trashRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size uint64 `json:"size"`
	// A decimal string for the reason every nanosecond field here is: the
	// value is past what a JSON number survives in a browser.
	DeletedAtNs string `json:"deleted_at_ns"`
}

// batchItem is one outcome in a batch response.
//
// The error is the same envelope a refused request carries, produced by the
// same mapping: a per-item failure a client has to parse differently from a
// whole-request one is two error vocabularies for one surface.
type batchItem struct {
	Path  string       `json:"path"`
	OK    bool         `json:"ok"`
	Error *apierr.Wire `json:"error,omitempty"`
	// WillCopy says a move degraded into a copy, which is only ever true and
	// is omitted otherwise: it is a warning about one item, not a field every
	// batch response carries.
	WillCopy bool `json:"will_copy,omitempty"`
}

// itemError renders a per-item failure the way the envelope renders a whole
// request's.
func itemError(err error) *apierr.Wire {
	_, mapped := apierr.Map(err)
	w := mapped.Wire()
	return &w
}

// Trash answers GET /api/trash with every trashed entry the caller can see.
//
// Across every share they may read, because the screen is account-wide. A
// share whose trash cannot be read is skipped rather than failing the listing:
// one unreadable share must not empty the screen for all the others.
func Trash(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}

		out := []trashRow{}
		for _, root := range d.Core.Roots(uid) {
			resolved, rerr := resolveRoot(d, uid, root.Label, acl.Read)
			if rerr != nil {
				continue
			}
			rows, lerr := d.Core.TrashList(r.Context(), resolved)
			if lerr != nil {
				continue
			}
			for _, e := range rows {
				out = append(out, trashRow{
					ID:          trashID(resolved.Share(), e.ID),
					Name:        e.Name,
					Size:        e.Size,
					DeletedAtNs: strconv.FormatInt(e.DeletedAtNs, 10),
				})
			}
		}
		return writeJSON(w, http.StatusOK, out)
	})
}

// TrashRestore answers POST /api/trash/restore with a batch of entry ids.
//
// Per item rather than all-or-nothing: restoring five things where one target
// name is taken should put the other four back, and the caller is told which
// one did not go.
func TrashRestore(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			IDs []string `json:"ids"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		results := make([]batchItem, 0, len(req.IDs))
		for _, raw := range req.IDs {
			item := batchItem{Path: raw}
			resolved, id, rerr := resolveTrashID(d, uid, raw, acl.Create)
			if rerr != nil {
				item.Error = itemError(rerr)
				results = append(results, item)
				continue
			}
			restored, err := d.Core.TrashRestore(r.Context(), resolved, id)
			if err != nil {
				item.Error = itemError(err)
				results = append(results, item)
				continue
			}
			item.OK = true
			if vp, verr := d.Core.VpathFor(uid, resolved.Share(), restored.Share()); verr == nil {
				item.Path = vp.String()
			}
			results = append(results, item)
		}
		return writeJSON(w, http.StatusOK, map[string]any{"results": results})
	})
}

// TrashPurge answers POST /api/trash/purge, removing named entries for good.
func TrashPurge(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			IDs []string `json:"ids"`
		}
		if err := decodeJSON(r, &req); err != nil {
			return err
		}

		results := make([]batchItem, 0, len(req.IDs))
		for _, raw := range req.IDs {
			item := batchItem{Path: raw}
			resolved, id, rerr := resolveTrashID(d, uid, raw, acl.Delete)
			if rerr != nil {
				item.Error = itemError(rerr)
				results = append(results, item)
				continue
			}
			if err := d.Core.TrashPurge(r.Context(), resolved, &id); err != nil {
				item.Error = itemError(err)
				results = append(results, item)
				continue
			}
			item.OK = true
			results = append(results, item)
		}
		return writeJSON(w, http.StatusOK, map[string]any{"results": results})
	})
}

// trashID is the wire form: the share, then the store's own id.
func trashID(share core.ShareID, id string) string {
	return strconv.FormatUint(uint64(share), 10) + ":" + id
}

// resolveTrashID turns a wire id back into the share it names and the entry
// within it.
//
// The share is resolved through the caller's own roots, so an id naming a
// share they hold no grant over is a missing entry rather than a denial: the
// same existence rule every other path here follows, and the reason an id is
// not something to guess at.
func resolveTrashID(d Deps, uid core.UserID, raw string, need acl.Perms) (core.Resolved, string, error) {
	share, id, ok := strings.Cut(raw, ":")
	if !ok || id == "" {
		return core.Resolved{}, "", apierr.BadRequest("trash.bad_id", "ids")
	}
	n, perr := strconv.ParseUint(share, 10, 32)
	if perr != nil {
		return core.Resolved{}, "", apierr.BadRequest("trash.bad_id", "ids")
	}

	for _, root := range d.Core.Roots(uid) {
		resolved, rerr := resolveRoot(d, uid, root.Label, need)
		if rerr != nil {
			continue
		}
		if uint64(resolved.Share()) == n {
			return resolved, id, nil
		}
	}
	return core.Resolved{}, "", core.ErrNotFound
}

// resolveRoot resolves one of the caller's roots by its label.
func resolveRoot(d Deps, uid core.UserID, label string, need acl.Perms) (core.Resolved, error) {
	vp, err := vfs.ParseVpath(label)
	if err != nil {
		return core.Resolved{}, err
	}
	return d.Core.Resolve(uid, vp, need)
}
