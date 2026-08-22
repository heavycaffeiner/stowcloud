package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
)

// The archive-listing surface. The listing reads the file itself, never the
// directory, and the cost bound is stated in the response: an archive over
// the listed-entry ceiling is truncated and says so.

// errListingFull ends the walk at the listed-entry bound. It never reaches the
// caller: the response says the listing is truncated instead.
var errListingFull = errors.New("archive: the listing is full")

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
				// The walk ends here rather than running to the end of the tree
				// discarding what it finds. It used to keep going, opening a
				// stream per entry it then threw away, so a listing of a large
				// tree cost the whole tree to produce its first ten thousand
				// names.
				out.Truncated = true
				return errListingFull
			}
			out.Entries = append(out.Entries, archiveEntry{
				Path: e.RelPath, IsDir: e.IsDir, Size: e.Size, MTime: e.MTimeNs,
			})
			return nil
		})
		if err != nil && !errors.Is(err, errListingFull) {
			return err
		}
		return writeJSON(w, http.StatusOK, out)
	})
}

// Recent answers GET /api/recent: every file this account wrote through this
// server inside the window, newest first.
//
// Exact rather than truncated: it reads the write journal, so there is no walk
// to cut short. Every row is re-checked before it is returned, because the row
// records that the account wrote the file and not that they may still read it.
func Recent(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}

		q := core.RecentQuery{Scope: r.URL.Query().Get("scope")}
		if v := r.URL.Query().Get("limit"); v != "" {
			n, perr := strconv.Atoi(v)
			if perr != nil || n <= 0 {
				return apierr.BadRequest("recent.limit", "limit")
			}
			q.Limit = n
		}
		// An instant, not a day count. A day count has to be resolved against
		// somebody's clock, and the two ends of this are in different time
		// zones often enough that the same request meant two different windows
		// depending on which side did the arithmetic.
		if v := r.URL.Query().Get("since"); v != "" {
			t, perr := time.Parse(time.RFC3339, v)
			if perr != nil {
				return apierr.BadRequest("recent.since", "since")
			}
			q.SinceNs = t.UnixNano()
		}

		hits, err := d.Core.Recent(r.Context(), uid, q)
		if err != nil {
			return err
		}

		type wireHit struct {
			Vpath   string `json:"vpath"`
			Share   string `json:"share"`
			Subpath string `json:"subpath"`
			Name    string `json:"name"`
			Size    uint64 `json:"size"`
			MTimeNs string `json:"mtime_ns"`
			AtNs    string `json:"at_ns"`
			Op      string `json:"op"`
		}
		out := make([]wireHit, 0, len(hits))
		for _, h := range hits {
			out = append(out, wireHit{
				Vpath:   h.Vpath.String(),
				Share:   h.Share,
				Subpath: h.Subpath.String(),
				Name:    h.Name,
				Size:    h.Size,
				// Nanosecond counts go out as strings: they are past the
				// precision a JSON number survives in a browser, and one that
				// arrives rounded is a timestamp that sorts wrongly.
				MTimeNs: strconv.FormatInt(h.MTimeNs, 10),
				AtNs:    strconv.FormatInt(h.AtNs, 10),
				Op:      h.Op.String(),
			})
		}
		return writeJSON(w, http.StatusOK, map[string]any{"hits": out})
	})
}
