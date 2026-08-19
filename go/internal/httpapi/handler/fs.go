package handler

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// entryJSON is one listing row. Timestamps are nanoseconds, the store's own
// unit; they do not travel as floats because a nanosecond value does not fit
// a double, and the round-trip guarantee is a browser test, not a Go one.
type entryJSON struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	IsDir    bool   `json:"is_dir"`
	Size     uint64 `json:"size"`
	MTimeNs  int64  `json:"mtime_ns"`
	ETag     string `json:"etag"`
	ETagWeak bool   `json:"etag_weak"`
}

func entryOf(e core.Entry, path string) entryJSON {
	return entryJSON{
		Name: e.Name, Path: path, IsDir: e.IsDir,
		Size: e.Size, MTimeNs: e.MTimeNs, ETag: e.ETag, ETagWeak: e.ETagWeak,
	}
}

// listResponse is one bounded page plus the accounting a grid needs.
type listResponse struct {
	Entries     []entryJSON `json:"entries"`
	Dirs        int         `json:"dirs"`
	Total       int         `json:"total"`
	Next        string      `json:"next,omitempty"`
	DirETag     string      `json:"dir_etag,omitempty"`
	DirETagWeak bool        `json:"dir_etag_weak"`
}

// List answers GET /api/fs/list?path=label/rest&cursor=...
func List(d Deps) http.HandlerFunc {
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
		page, err := d.Core.List(r.Context(), resolved, core.Cursor(r.URL.Query().Get("cursor")))
		if err != nil {
			return err
		}
		out := listResponse{Dirs: page.Dirs, Total: page.Total, DirETag: page.DirEtag, DirETagWeak: page.DirEtagWeak}
		for _, e := range page.Entries {
			out.Entries = append(out.Entries, entryOf(e, vpathString(d, uid, resolved, e.Path)))
		}
		if page.Next != "" {
			out.Next = string(page.Next)
		}
		return writeJSON(w, http.StatusOK, out)
	})
}

// vpathString renders a share-relative path in the client's label form. The
// listing entries carry share-relative paths, and the client addresses paths
// by label, so the crossing goes through the core's own inverse.
func vpathString(d Deps, uid core.UserID, resolved core.Resolved, p vfs.SharePath) string {
	vp, err := d.Core.VpathFor(uid, resolved.Share(), p)
	if err != nil {
		return p.String()
	}
	return vp.String()
}

// Stat answers GET /api/fs/stat?path=...
func Stat(d Deps) http.HandlerFunc {
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
		e, err := d.Core.Stat(r.Context(), resolved)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, entryOf(e, vpathString(d, uid, resolved, e.Path)))
	})
}

// Read answers GET /api/fs/read?path=..., streaming the bytes with single
// range support. It is the one download path on the native surface; the
// content origin is a separate mount.
func Read(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		p, err := pathOf(r)
		if err != nil {
			return err
		}
		resolved, err := d.Core.Resolve(uid, p, acl.Read|acl.Download)
		if err != nil {
			return err
		}
		var rng *[2]uint64
		if raw := r.Header.Get("Range"); raw != "" {
			parsed, rerr := parseRange(raw)
			if rerr != nil {
				return rerr
			}
			rng = &parsed
		}
		entry, stream, err := d.Core.OpenStream(r.Context(), resolved, rng)
		if err != nil {
			return err
		}
		defer stream.Close() //nolint:errcheck // the read path is done either way.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatUint(stream.Remaining(), 10))
		w.Header().Set("ETag", entry.ETag)
		if rng != nil {
			w.WriteHeader(http.StatusPartialContent)
		}
		if _, err := io.Copy(w, stream); err != nil {
			return err
		}
		return nil
	})
}

// parseRange accepts the one shape this surface serves: "bytes=start-end" on
// a single range. Anything else is 416, which is what an unsatisfiable range
// means, and the unit and the shape are the caller's contract to keep.
func parseRange(raw string) ([2]uint64, error) {
	if !strings.HasPrefix(raw, "bytes=") || strings.Contains(raw, ",") {
		return [2]uint64{}, unsatisfiableRange()
	}
	spec := strings.TrimPrefix(raw, "bytes=")
	startS, endS, ok := strings.Cut(spec, "-")
	if !ok {
		return [2]uint64{}, unsatisfiableRange()
	}
	start, err := strconv.ParseUint(startS, 10, 64)
	if err != nil {
		return [2]uint64{}, unsatisfiableRange()
	}
	end, err := strconv.ParseUint(endS, 10, 64)
	if err != nil || end < start {
		return [2]uint64{}, unsatisfiableRange()
	}
	return [2]uint64{start, end}, nil
}

func unsatisfiableRange() error {
	return &apierr.RequestError{Status: http.StatusRequestedRangeNotSatisfiable,
		Code: apierr.CodeInvalidRequest, Message: "unsatisfiable range", Key: "fs.range"}
}

// Mkdir answers POST /api/fs/mkdir with the path to create, or a parent path
// and the name to create under it.
func Mkdir(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			Path string `json:"path"`
			Name string `json:"name,omitempty"`
		}
		if derr := decodeJSON(r, &req); derr != nil {
			return derr
		}
		vp, err := vfs.ParseVpath(joinPath(req.Path, req.Name))
		if err != nil {
			return err
		}
		resolved, err := d.Core.Resolve(uid, vp, acl.Create)
		if err != nil {
			return err
		}
		e, err := d.Core.Mkdir(r.Context(), resolved)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusCreated, entryOf(e, vpathString(d, uid, resolved, e.Path)))
	})
}

// Delete answers POST /api/fs/delete: the path, and whether the trash is
// bypassed.
func Delete(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			Path      string `json:"path"`
			Permanent bool   `json:"permanent"`
		}
		if derr := decodeJSON(r, &req); derr != nil {
			return derr
		}
		vp, err := vfs.ParseVpath(req.Path)
		if err != nil {
			return err
		}
		resolved, err := d.Core.Resolve(uid, vp, acl.Delete)
		if err != nil {
			return err
		}
		if err := d.Core.Delete(r.Context(), resolved, req.Permanent); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// Rename answers POST /api/fs/rename: the path, the new name within the same
// directory, and an optional validator.
func Rename(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			Path    string `json:"path"`
			NewName string `json:"new_name"`
			IfMatch string `json:"if_match,omitempty"`
		}
		if derr := decodeJSON(r, &req); derr != nil {
			return derr
		}
		vp, err := vfs.ParseVpath(req.Path)
		if err != nil {
			return err
		}
		resolved, err := d.Core.Resolve(uid, vp, acl.Rename)
		if err != nil {
			return err
		}
		e, err := d.Core.Rename(r.Context(), resolved, req.NewName, tokenFrom(req.IfMatch))
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, entryOf(e, vpathString(d, uid, resolved, e.Path)))
	})
}

// Move answers POST /api/fs/move: from and to paths, an overwrite flag, and
// an optional validator. The response names the path the entry landed at and
// warns when the move had to become a copy.
func Move(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			From      string `json:"from"`
			To        string `json:"to"`
			Overwrite bool   `json:"overwrite"`
			IfMatch   string `json:"if_match,omitempty"`
		}
		if derr := decodeJSON(r, &req); derr != nil {
			return derr
		}
		fromV, err := vfs.ParseVpath(req.From)
		if err != nil {
			return err
		}
		toV, err := vfs.ParseVpath(req.To)
		if err != nil {
			return err
		}
		from, err := d.Core.Resolve(uid, fromV, acl.Move)
		if err != nil {
			return err
		}
		to, err := d.Core.Resolve(uid, toV, acl.Create)
		if err != nil {
			return err
		}
		res, err := d.Core.Move(r.Context(), from, to, core.MoveOpts{
			Overwrite: req.Overwrite,
			IfMatch:   tokenFrom(req.IfMatch),
		})
		if err != nil {
			return err
		}
		out, verr := d.Core.VpathFor(uid, to.Share(), res.Created.Share())
		if verr != nil {
			return verr
		}
		return writeJSON(w, http.StatusOK, map[string]any{
			"will_copy": res.WillCopy, "path": out.String(),
		})
	})
}

// Copy answers POST /api/fs/copy: it starts a recursive copy as a background
// operation and returns its id, which /api/jobs/{id} tracks.
func Copy(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if derr := decodeJSON(r, &req); derr != nil {
			return derr
		}
		fromV, err := vfs.ParseVpath(req.From)
		if err != nil {
			return err
		}
		toV, err := vfs.ParseVpath(req.To)
		if err != nil {
			return err
		}
		from, err := d.Core.Resolve(uid, fromV, acl.Read)
		if err != nil {
			return err
		}
		to, err := d.Core.Resolve(uid, toV, acl.Create)
		if err != nil {
			return err
		}
		id, err := d.Core.StartCopy(r.Context(), uid, from, to)
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusAccepted, map[string]any{"job_id": id})
	})
}

// Write answers POST /api/fs/write, replacing a file's content atomically.
// The body is JSON carrying the new text, and a supplied validator must match
// or the write is refused carrying the current token.
func Write(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req struct {
			Path    string `json:"path"`
			Content string `json:"content"`
			IfMatch string `json:"if_match,omitempty"`
		}
		if derr := decodeJSON(r, &req); derr != nil {
			return derr
		}
		vp, err := vfs.ParseVpath(req.Path)
		if err != nil {
			return err
		}
		resolved, err := d.Core.Resolve(uid, vp, acl.Write)
		if err != nil {
			return err
		}
		e, err := d.Core.CreateFile(r.Context(), resolved, vfs.DurableOpts{Mode: 0o664}, tokenFrom(req.IfMatch), func(f *vfs.File) error {
			_, werr := f.WriteAt([]byte(req.Content), 0)
			if werr != nil {
				return werr
			}
			return f.Truncate(int64(len(req.Content)))
		})
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, entryOf(e, vpathString(d, uid, resolved, e.Path)))
	})
}

// Size answers GET /api/fs/size?path=... with the recursive rollup, which is
// what a grid shows under a directory without loading its rows.
func Size(d Deps) http.HandlerFunc {
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
		agg, err := d.Core.Aggregate(r.Context(), resolved.Share(), resolved.Path())
		if err != nil {
			return err
		}
		return writeJSON(w, http.StatusOK, map[string]any{"size": agg.RSize, "count": agg.RCount})
	})
}

// tokenFrom turns a wire validator into the core's typed form. An empty
// string is a nil validator, which is "no precondition", never a weak one.
func tokenFrom(s string) *core.Token {
	if s == "" {
		return nil
	}
	t := core.Token(s)
	return &t
}

func joinPath(path, name string) string {
	if name == "" {
		return path
	}
	if path == "" || strings.HasSuffix(path, "/") {
		return path + name
	}
	return path + "/" + name
}
