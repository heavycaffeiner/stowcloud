package handler

import (
	"net/http"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// The archive-listing surface. The listing reads the file itself, never the
// directory, and the cost bound is stated in the response: an archive over
// the listed-entry ceiling is truncated and says so.

type archiveEntry struct {
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  uint64 `json:"size,omitempty"`
	MTime int64  `json:"mtime_ns,omitempty"`
}

// ArchiveList answers GET /api/fs/archive/list?path=...
func ArchiveList(d Deps) http.HandlerFunc {
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
		out := struct {
			Entries   []archiveEntry `json:"entries"`
			Truncated bool           `json:"truncated"`
			Limit     int            `json:"limit"`
		}{Limit: limits.ArchiveEntriesListed}
		err = d.Core.ArchiveWalk(r.Context(), resolved, func(e core.WalkEntry, s *core.Stream) error {
			if s != nil {
				defer s.Close() //nolint:errcheck // per-entry handle, released at once.
			}
			if len(out.Entries) >= limits.ArchiveEntriesListed {
				out.Truncated = true
				return nil
			}
			out.Entries = append(out.Entries, archiveEntry{
				Path: e.RelPath, IsDir: e.IsDir, Size: e.Size, MTime: e.MTimeNs,
			})
			return nil
		})
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, out)
	})
}

// Recent answers GET /api/recent. The recency query's backing index arrives
// with the search phase; this build owns the route and the response shape,
// and says plainly that the work is not here yet.
func Recent(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		return &apierr.RequestError{Status: http.StatusNotImplemented,
			Code: apierr.CodeNotImplemented, Message: "not implemented in this build", Key: "recent.unavailable"}
	})
}

// ArchiveCreate answers POST /api/fs/archive: pack the named paths.
//
// The core walks an archive for reading and has no writer, so packing is not
// something this build can do. It answers as much rather than accepting the
// request and returning a job id for work nothing performs, which a client
// would poll until it gave up.
func ArchiveCreate(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, cerr := userOf(r); cerr != nil {
			return cerr
		}
		return notImplemented("archive.create_unavailable")
	})
}
