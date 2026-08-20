package handler

import (
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/auth"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// The remaining administrator surfaces: storage accounting, the audit log, the
// search index and the settings sections.

// AdminStorage answers GET /api/admin/storage.
//
// It reports what this server can account for from its own records rather than
// walking the tree: a walk of a twelve-terabyte array to draw a settings screen
// is a screen nobody opens twice.
func AdminStorage(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		dbBytes, err := d.State.FileBytes()
		if err != nil {
			return err
		}

		type shareUsage struct {
			Share      int64  `json:"share"`
			Name       string `json:"name"`
			TotalBytes uint64 `json:"total_bytes"`
			FreeBytes  uint64 `json:"free_bytes"`
		}
		shares := []shareUsage{}
		for _, sh := range d.Core.Shares() {
			root, ok := d.Core.ShareRoot(sh.ID)
			if !ok {
				continue
			}
			space, serr := root.Space(vfs.RootPath())
			if serr != nil {
				// A share whose filesystem cannot be measured is reported with
				// zeroes rather than dropped: a missing row reads as a share
				// that does not exist.
				shares = append(shares, shareUsage{Share: int64(sh.ID), Name: sh.Name})
				continue
			}
			shares = append(shares, shareUsage{
				Share: int64(sh.ID), Name: sh.Name,
				TotalBytes: space.Total, FreeBytes: space.Available,
			})
		}

		return writeJSON(w, http.StatusOK, map[string]any{
			"db_bytes": dbBytes,
			"shares":   shares,
		})
	})
}

// AdminAudit answers GET /api/admin/audit.
//
// The page is bounded and cursor-paged rather than offset-paged, so a page
// boundary stays correct while new rows keep landing ahead of it.
func AdminAudit(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		q := r.URL.Query()
		limit := int(queryInt(r, "limit"))
		if limit <= 0 || limit > auditPageMax {
			limit = auditPageMax
		}
		rows, next, err := d.Auth.AuditPage(r.Context(), auth.AuditFilter{
			Actor:   queryInt(r, "actor"),
			Event:   q.Get("event"),
			SinceNs: queryInt(r, "since_ns"),
			UntilNs: queryInt(r, "until_ns"),
			Before:  queryInt(r, "before"),
			Limit:   limit,
		})
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "next": next})
	})
}

// auditPageMax bounds one page. The log is unbounded and a client that asks
// for all of it is asking the server to hold all of it.
const auditPageMax = 500

// AdminIndexEstimate answers GET /api/admin/index/estimate.
//
// An estimate rather than a measurement, and the response says which: building
// the index is the expensive act, and an administrator deciding whether to
// start one needs a number before it runs, not after.
func AdminIndexEstimate(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		// The estimator exists and the walk that feeds it does not yet run
		// from here. Answering honestly beats answering a number this server
		// did not measure, which an administrator would size a disk against.
		return notImplemented("search.index_build_unavailable")
	})
}

// AdminIndexBuild answers POST /api/admin/index/build.
func AdminIndexBuild(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		return notImplemented("search.index_build_unavailable")
	})
}

// AdminIndexSettings answers GET and PATCH /api/admin/index/settings.
func AdminIndexSettings(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		if r.Method == http.MethodGet {
			enabled, err := d.State.IndexNameEnabled(r.Context())
			if err != nil {
				return err
			}
			return writeJSON(w, http.StatusOK, map[string]bool{"name_enabled": enabled})
		}
		var patch struct {
			NameEnabled *bool `json:"name_enabled"`
		}
		if err := decodeJSON(r, &patch); err != nil {
			return err
		}
		if patch.NameEnabled == nil {
			return apierr.BadRequest("admin.index_fields", "name_enabled")
		}
		if err := d.State.SetIndexNameEnabled(r.Context(), *patch.NameEnabled); err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, map[string]bool{"name_enabled": *patch.NameEnabled})
	})
}

// AdminUploadSettings answers PATCH /api/admin/upload-settings: the chunk
// floor and default every account's session response reports.
func AdminUploadSettings(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := requireAdmin(r, d.Auth); err != nil {
			return err
		}
		if d.Uploads == nil {
			return notImplemented("upload.unavailable")
		}
		var patch struct {
			ChunkMin  *uint64 `json:"chunk_min"`
			ChunkSize *uint64 `json:"chunk_size"`
		}
		if err := decodeJSON(r, &patch); err != nil {
			return err
		}
		// The compiled-in floor beats an administrator, which is what stops a
		// misconfiguration turning every upload into a request per byte.
		if patch.ChunkMin != nil && *patch.ChunkMin < limits.UploadChunkFloor {
			return apierr.BadRequest("admin.chunk_below_floor", "chunk_min")
		}
		if err := d.Uploads.ApplySettings(r.Context(), patch.ChunkMin, patch.ChunkSize); err != nil {
			return err
		}
		s := d.Uploads.Settings()
		return writeJSON(w, http.StatusOK, map[string]uint64{
			"chunk_min": s.Min(), "chunk_size": s.Default(),
		})
	})
}

// notImplemented is the refusal for a surface whose subsystem is not wired up
// in this build. It names the subsystem: a bare failure tells an administrator
// nothing about whether to wait or to configure something.
func notImplemented(key string) error {
	return &apierr.RequestError{
		Status:  http.StatusNotImplemented,
		Code:    apierr.CodeNotImplemented,
		Message: "not implemented in this build",
		Key:     apierr.MessageKey(key),
	}
}
