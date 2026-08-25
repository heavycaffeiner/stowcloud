// Linux only: it depends on packages that are Linux only.
//go:build linux

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
	"github.com/heavycaffeiner/stowcloud/go/internal/preview"
)

// The archive-listing surface: what is inside a zip file, read from the
// archive's own central directory. Nothing is extracted to produce it, so a
// zip bomb costs the directory parse and nothing else, and the cost bound is
// stated in the response: an archive over the listed-entry ceiling is
// truncated and says so.
//
// It used to walk the filesystem tree under the path instead, which is a
// different question with a different answer shape: listing a zip returned the
// directory containing it, under field names the client does not read, so the
// preview never rendered a single member.

// archiveEntry is one member of the archive, in the shape the preview reads.
// A directory carries the trailing slash the archive itself stores, which is
// the only thing that distinguishes one in a zip.
type archiveEntry struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Size  uint64 `json:"size"`
	MTime int64  `json:"mtime_ns,omitempty"`
}

// ArchiveList answers GET /api/fs/archive/list?path=...
//
// A path the caller cannot read and a file that is not a zip are the same 404:
// whether a file this account may not see happens to be an archive is not
// something the answer should say.
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
		// Download as well as read: the members' names and sizes are what the
		// file contains, so listing them is seeing into it.
		resolved, err := d.Core.Resolve(uid, p, acl.Read|acl.Download)
		if err != nil {
			return err
		}

		_, src, oerr := d.Core.OpenRandom(r.Context(), resolved)
		if oerr != nil {
			return oerr
		}
		defer src.Close() //nolint:errcheck // the listing is the answer either way.

		listing, lerr := preview.ListArchive(r.Context(), src, src.Size)
		if errors.Is(lerr, preview.ErrNotArchive) {
			return core.ErrNotFound
		}
		if lerr != nil {
			return lerr
		}

		out := struct {
			Entries   []archiveEntry `json:"entries"`
			Truncated bool           `json:"truncated"`
			Limit     int            `json:"limit"`
			Skipped   int            `json:"skipped"`
		}{
			Entries:   make([]archiveEntry, 0, len(listing.Entries)),
			Truncated: listing.Truncated,
			Limit:     limits.ArchiveEntriesListed,
			// Members left out because their names cannot be handed to a client
			// safely: a path escape, a raw Windows separator, a control
			// character. Counted rather than fatal, so one odd entry does not
			// hide the archive.
			Skipped: listing.Skipped,
		}
		for _, e := range listing.Entries {
			kind := "file"
			if e.IsDir {
				kind = "dir"
			}
			out.Entries = append(out.Entries, archiveEntry{
				Name: e.Name, Kind: kind, Size: e.Size, MTime: e.ModTimeNs,
			})
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
