// Linux only: it depends on packages that are Linux only.
//go:build linux

package handler

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/acl"
	"github.com/heavycaffeiner/stowcloud/go/internal/apierr"
	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/vfs"
)

// entryJSON is one listing row. Timestamps are nanoseconds, the store's own
// unit; they do not travel as floats because a nanosecond value does not fit
// a double, and the round-trip guarantee is a browser test, not a Go one.
type entryJSON struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Kind is what the client reads. is_dir is sent alongside it because the
	// compatibility layer and older callers read that one, and the two are
	// derived from the same bool so they cannot disagree.
	Kind  string `json:"kind"`
	IsDir bool   `json:"is_dir"`
	Size  uint64 `json:"size"`
	// A string, because a nanosecond value does not fit a double and JSON has
	// no other number. Sent as one it came back off the wire rounded to the
	// nearest few hundred nanoseconds, which is a timestamp that no longer
	// matches the file it describes. Search and the archive manifest already
	// sent it this way.
	MTimeNs  string    `json:"mtime_ns"`
	ETag     string    `json:"etag"`
	ETagWeak bool      `json:"etag_weak"`
	Perms    permsJSON `json:"perms"`
	// Preview says whether a thumbnail is worth asking for. Absent for a
	// directory, which never has one.
	Preview *previewJSON `json:"preview,omitempty"`
}

// permsJSON is what the caller may do with this entry, as the eight named
// booleans the client reads.
//
// Sent per entry rather than inferred from the share: a grant can deny below
// the root it was written at, so two rows of one listing can carry different
// answers. Without it the interface has to guess, and it guessed by reading a
// field that was never sent: selecting a row threw on `perms.share` being
// undefined, which took the selection, the details panel and every action
// behind them down together.
type permsJSON struct {
	Read     bool `json:"read"`
	Write    bool `json:"write"`
	Create   bool `json:"create"`
	Delete   bool `json:"delete"`
	Rename   bool `json:"rename"`
	Move     bool `json:"move"`
	Share    bool `json:"share"`
	Download bool `json:"download"`
}

// permsFrom is the reverse of permsOf, for the surfaces that take a permission
// set from a client rather than reporting one.
//
// An absent field is false rather than an error: a client sending only what it
// wants granted is the common case, and the share dialog sends exactly two of
// the eight.
func permsFrom(j permsJSON) acl.Perms {
	var out acl.Perms
	for _, m := range []struct {
		on bool
		p  acl.Perms
	}{
		{j.Read, acl.Read}, {j.Write, acl.Write},
		{j.Create, acl.Create}, {j.Delete, acl.Delete},
		{j.Rename, acl.Rename}, {j.Move, acl.Move},
		{j.Share, acl.Share}, {j.Download, acl.Download},
	} {
		if m.on {
			out |= m.p
		}
	}
	return out
}

func permsOf(p acl.Perms) permsJSON {
	return permsJSON{
		Read:     p.Has(acl.Read),
		Write:    p.Has(acl.Write),
		Create:   p.Has(acl.Create),
		Delete:   p.Has(acl.Delete),
		Rename:   p.Has(acl.Rename),
		Move:     p.Has(acl.Move),
		Share:    p.Has(acl.Share),
		Download: p.Has(acl.Download),
	}
}

func entryOf(e core.Entry, path string) entryJSON {
	// The kind the filesystem reported, not "dir or else file". A symlink was
	// reported as a file, so the interface drew one and opening it tried to
	// read a file that is not there; the client has declared all four kinds
	// since before this handler was written.
	//
	// An entry the directory read could not type, and one lost to a delete
	// race, both land on "other" rather than being called a file.
	kind := e.Kind.String()
	out := entryJSON{
		Name: e.Name, Path: path, Kind: kind, IsDir: e.IsDir,
		Size: e.Size, MTimeNs: strconv.FormatInt(e.MTimeNs, 10),
		ETag: e.ETag, ETagWeak: e.ETagWeak,
		Perms: permsOf(e.Perms),
	}
	// Only for something that could have one. A caller reads this to decide
	// whether to make the request at all, and it was never sent: the guard on
	// the other side is `preview?.available === true`, so it was false for
	// every entry and no thumbnail was ever asked for.
	if !e.IsDir && thumbnailable(e.Name) {
		out.Preview = &previewJSON{Available: true}
	}
	return out
}

// listResponse is one bounded page plus the accounting a grid needs.
type listResponse struct {
	Entries []entryJSON `json:"entries"`
	Dirs    int         `json:"dirs"`
	Total   int         `json:"total"`
	// Cursor is where the next page begins, or null at the end. The client
	// reads this name; it was sent as "next" and omitted when empty, so the
	// field the client paginates on was always undefined and a directory
	// larger than one page could not be walked past its first.
	Cursor *string `json:"cursor"`
	// Listing is the handle a windowed fetch passes back. This server keeps no
	// per-listing session and re-walks from the cursor instead, so it is the
	// path itself: enough for the client to hold and hand back, and it names
	// what it addresses.
	Listing     string `json:"listing"`
	DirETag     string `json:"dir_etag,omitempty"`
	DirETagWeak bool   `json:"dir_etag_weak"`
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
		// The order the client asked for. Both were sent on every listing
		// request and read by nothing, so the sort control in the interface
		// changed the query string and never the order.
		q := r.URL.Query()
		opt := core.ListOptions{
			Sort: core.ParseSortKey(q.Get("sort")),
			Desc: q.Get("order") == "desc",
		}
		page, err := d.Core.ListSorted(r.Context(), resolved, core.Cursor(q.Get("cursor")), opt)
		if err != nil {
			return err
		}
		out := listResponse{
			Dirs: page.Dirs, Total: page.Total,
			Listing:     p.String(),
			DirETag:     page.DirEtag,
			DirETagWeak: page.DirEtagWeak,
		}
		// Never nil: an empty directory has to encode as [] rather than null,
		// because the client iterates it without checking.
		out.Entries = make([]entryJSON, 0, len(page.Entries))
		for _, e := range page.Entries {
			out.Entries = append(out.Entries, entryOf(e, vpathString(d, uid, resolved, e.Path)))
		}
		if page.Next != "" {
			next := string(page.Next)
			out.Cursor = &next
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
		// ?download=1 asks for it as a file rather than as something to render.
		// The name is quoted and its quotes and backslashes escaped, and the
		// RFC 5987 form carries anything outside ASCII: a header built by
		// pasting a filename in is one a filename can break out of.
		if r.URL.Query().Get("download") == "1" {
			w.Header().Set("Content-Disposition", contentDisposition(entry.Name))
		}
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
			Paths     []string `json:"paths"`
			Permanent bool     `json:"permanent"`
		}
		if derr := decodeJSON(r, &req); derr != nil {
			return derr
		}
		if len(req.Paths) == 0 {
			return apierr.BadRequest("fs.no_paths", "paths")
		}
		if len(req.Paths) > limits.BatchPaths {
			return limits.Exceed("delete paths", limits.BatchPaths, int64(len(req.Paths)))
		}

		// Per item rather than all-or-nothing. Deleting five things where one
		// is already gone should remove the other four, and the caller is told
		// which one did not go: a whole-batch refusal leaves the client
		// guessing how much of what it asked for actually happened.
		results := make([]batchItem, 0, len(req.Paths))
		for _, raw := range req.Paths {
			item := batchItem{Path: raw}
			switch err := deleteOne(r, d, uid, raw, req.Permanent); {
			case err != nil:
				item.Error = itemError(err)
			default:
				item.OK = true
			}
			results = append(results, item)
		}
		return writeJSON(w, http.StatusOK, map[string]any{"results": results})
	})
}

func deleteOne(r *http.Request, d Deps, uid core.UserID, raw string, permanent bool) error {
	vp, err := vfs.ParseVpath(raw)
	if err != nil {
		return err
	}
	resolved, err := d.Core.Resolve(uid, vp, acl.Delete)
	if err != nil {
		return err
	}
	return d.Core.Delete(r.Context(), resolved, permanent)
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

// transferRequest is what move and copy both take: a set of sources and one
// destination directory, which is how a person selects in a file manager.
type transferRequest struct {
	Paths      []string `json:"paths"`
	Dest       string   `json:"dest"`
	OnConflict string   `json:"on_conflict,omitempty"`
	// DryRun asks what would happen without doing it, which is what the
	// destination picker calls to warn that a move will become a copy.
	DryRun bool `json:"dry_run,omitempty"`
}

// resolveTransfer validates the batch and the destination once.
func resolveTransfer(d Deps, uid core.UserID, req transferRequest) (core.Resolved, error) {
	if len(req.Paths) == 0 {
		return core.Resolved{}, apierr.BadRequest("fs.no_paths", "paths")
	}
	if len(req.Paths) > limits.BatchPaths {
		return core.Resolved{}, limits.Exceed("transfer paths", limits.BatchPaths, int64(len(req.Paths)))
	}
	destV, err := vfs.ParseVpath(req.Dest)
	if err != nil {
		return core.Resolved{}, err
	}
	return d.Core.Resolve(uid, destV, acl.Create)
}

// Move answers POST /api/fs/move: a set of paths and the directory to move
// them into. The response reports each one, and says where a move had to
// become a copy.
func Move(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req transferRequest
		if derr := decodeJSON(r, &req); derr != nil {
			return derr
		}
		dest, err := resolveTransfer(d, uid, req)
		if err != nil {
			return err
		}

		results := make([]batchItem, 0, len(req.Paths))
		for _, raw := range req.Paths {
			results = append(results, moveOne(r, d, uid, dest, raw, req))
		}
		return writeJSON(w, http.StatusOK, map[string]any{"results": results})
	})
}

func moveOne(
	r *http.Request, d Deps, uid core.UserID, dest core.Resolved, raw string, req transferRequest,
) batchItem {
	item := batchItem{Path: raw}

	fromV, err := vfs.ParseVpath(raw)
	if err != nil {
		item.Error = itemError(err)
		return item
	}
	from, err := d.Core.Resolve(uid, fromV, acl.Move)
	if err != nil {
		item.Error = itemError(err)
		return item
	}
	// The destination is the directory; the target is the name inside it.
	toV, err := vfs.ParseVpath(joinPath(req.Dest, fromV.Name()))
	if err != nil {
		item.Error = itemError(err)
		return item
	}
	to, err := d.Core.Resolve(uid, toV, acl.Create)
	if err != nil {
		item.Error = itemError(err)
		return item
	}

	// A dry run answers what would happen and changes nothing, which is what
	// the destination picker asks before it lets somebody commit.
	if req.DryRun {
		item.OK = true
		item.WillCopy = d.Core.WouldCopy(from, to)
		item.Path = toV.String()
		return item
	}

	res, err := d.Core.Move(r.Context(), from, to, core.MoveOpts{
		Overwrite: req.OnConflict == "overwrite",
	})
	if err != nil {
		item.Error = itemError(err)
		return item
	}
	item.OK = true
	item.WillCopy = res.WillCopy
	if out, verr := d.Core.VpathFor(uid, dest.Share(), res.Created.Share()); verr == nil {
		item.Path = out.String()
	}
	return item
}

// Copy answers POST /api/fs/copy: it starts one background copy per source and
// returns the ids, which /api/jobs/{id} tracks.
func Copy(d Deps) http.HandlerFunc {
	return Wrap(func(w http.ResponseWriter, r *http.Request) error {
		uid, cerr := userOf(r)
		if cerr != nil {
			return cerr
		}
		var req transferRequest
		if derr := decodeJSON(r, &req); derr != nil {
			return derr
		}
		if _, err := resolveTransfer(d, uid, req); err != nil {
			return err
		}

		var jobs []int64
		results := make([]batchItem, 0, len(req.Paths))
		for _, raw := range req.Paths {
			item := batchItem{Path: raw}
			id, err := copyOne(r, d, uid, raw, req.Dest)
			if err != nil {
				item.Error = itemError(err)
				results = append(results, item)
				continue
			}
			item.OK = true
			jobs = append(jobs, id)
			results = append(results, item)
		}

		out := map[string]any{"results": results}
		// The client tracks one job for the batch, so the first is what it
		// polls. Every id is reported as well, because a multi-source copy
		// genuinely has several and reporting one would lose the rest.
		if len(jobs) > 0 {
			out["job"] = strconv.FormatInt(jobs[0], 10)
			out["jobs"] = jobs
		}
		return writeJSON(w, http.StatusAccepted, out)
	})
}

func copyOne(r *http.Request, d Deps, uid core.UserID, raw, dest string) (int64, error) {
	fromV, err := vfs.ParseVpath(raw)
	if err != nil {
		return 0, err
	}
	from, err := d.Core.Resolve(uid, fromV, acl.Read)
	if err != nil {
		return 0, err
	}
	toV, err := vfs.ParseVpath(joinPath(dest, fromV.Name()))
	if err != nil {
		return 0, err
	}
	to, err := d.Core.Resolve(uid, toV, acl.Create)
	if err != nil {
		return 0, err
	}
	return d.Core.StartCopy(r.Context(), uid, from, to)
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
		// The names the client reads. It sent "size" and "count", so the panel
		// rendered an undefined byte total and "undefined files": both fields
		// were present and both were under the wrong name.
		return writeJSON(w, http.StatusOK, map[string]any{
			"bytes": agg.RSize,
			"files": agg.RCount,
		})
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
