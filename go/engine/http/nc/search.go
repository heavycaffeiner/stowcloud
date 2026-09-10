//go:build linux && compat_nc

package nc

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/svc"
)

// Unified search, recent files and favourites: the three ways a client asks
// "what files, without telling me a folder first".
//
// Each answers the file-entry shape the files app reads, built from the same
// numbers the DAV surface reports for the same file, so the two views of one
// file never disagree about its id, its permissions or its etag.

// searchLimitDefault and searchLimitMax bound a unified search query the
// same way the reference clients' own default and ceiling do: an absent
// limit asks for a page, never for everything a one-character query matches.
const (
	searchLimitDefault = 100
	searchLimitMax     = 1000
)

// searchProviders answers the unified search provider list.
//
// Only the providers this engine can actually answer are advertised: the
// iOS client drops any provider missing an id, a name or an order, and one
// advertised without a working searchQuery case would be asked about and
// refused instead of simply never asked about.
func (s *Server) searchProviders(c *fiber.Ctx, p Principal) (Val, bool, *Error) {
	items := []Val{
		Obj(P("id", Str("files")), P("name", Str("Files")), P("order", Int(0))),
		Obj(P("id", Str("files_recent")), P("name", Str("Recently changed files")), P("order", Int(1))),
	}
	if s.deps.Features().Favorites {
		items = append(items,
			Obj(P("id", Str("files_favorites")), P("name", Str("Favorites")), P("order", Int(2))))
	}
	return List(items...), true, nil
}

// searchQuery answers one unified search provider's results.
func (s *Server) searchQuery(c *fiber.Ctx, p Principal, provider string) (Val, bool, *Error) {
	switch provider {
	case "files":
		return s.searchFilesByName(c, p)
	case "files_recent":
		return s.searchRecentEntries(c, p)
	case "files_favorites":
		return s.searchFavoriteEntries(c, p)
	}
	return Val{}, false, NotFound("no such search provider")
}

// searchFilesByName answers the name-search provider through the search
// index, scoped to the caller's own sources.
//
// s.deps.Search nil, or no index built, answers an empty entry list rather
// than a refusal: a client renders a refused search as a broken account,
// and an index that is merely absent is not that.
func (s *Server) searchFilesByName(c *fiber.Ctx, p Principal) (Val, bool, *Error) {
	const name = "Files"
	term := strings.TrimSpace(c.Query("term"))
	if s.deps.Search == nil || term == "" {
		return searchResultVal(name, nil), true, nil
	}

	ctx := c.UserContext()
	sources := s.searchSourcesOf(user(p))
	limit := parseSearchLimit(c.Query("limit"))
	results, err := s.deps.Search.Query(ctx, sources, svc.QueryOptions{Query: term, Limit: limit})
	if err != nil {
		if errors.Is(err, svc.ErrBusy) {
			// A gate that turned this query away is a "try again". An empty
			// entry list here is the panel saying nothing matched.
			return Val{}, false, Unavailable("too many searches are already running")
		}
		return searchResultVal(name, nil), true, nil
	}

	entries := make([]Val, 0, len(results.Hits))
	for _, h := range results.Hits {
		// Folders included. A name search that answers with files alone
		// leaves the folder someone typed the name of out of its own
		// result list, which reads as "no such folder".
		if v, ok := s.searchEntryAt(ctx, p, h.Path); ok {
			entries = append(entries, v)
		}
	}
	return searchResultVal(name, entries), true, nil
}

// searchRecentEntries answers the unified search form of the recent-files
// listing.
func (s *Server) searchRecentEntries(c *fiber.Ctx, p Principal) (Val, bool, *Error) {
	const name = "Recently changed files"
	ctx := c.UserContext()
	hits, err := s.deps.Core.Recent(ctx, user(p), core.RecentQuery{Limit: parseSearchLimit(c.Query("limit"))})
	if err != nil {
		return searchResultVal(name, nil), true, nil
	}
	entries := make([]Val, 0, len(hits))
	for _, h := range hits {
		if v, ok := s.searchEntryAt(ctx, p, h.Vpath.String()); ok {
			entries = append(entries, v)
		}
	}
	return searchResultVal(name, entries), true, nil
}

// searchFavoriteEntries answers the unified search form of the starred set.
func (s *Server) searchFavoriteEntries(c *fiber.Ctx, p Principal) (Val, bool, *Error) {
	const name = "Favorites"
	if !s.deps.Features().Favorites {
		return searchResultVal(name, nil), true, nil
	}
	ctx := c.UserContext()
	set, err := s.deps.Store.Favorites(ctx, user(p))
	if err != nil {
		return searchResultVal(name, nil), true, nil
	}
	favs := set.List()
	entries := make([]Val, 0, len(favs))
	for _, f := range favs {
		vpath, ok := s.vpathOfFavorite(user(p), f)
		if !ok {
			continue
		}
		if v, ok := s.searchEntryAt(ctx, p, vpath); ok {
			entries = append(entries, v)
		}
	}
	return searchResultVal(name, entries), true, nil
}

// searchResultVal renders the shared envelope every provider answers:
// unpaginated, named, and carrying whatever entries were found.
func searchResultVal(name string, entries []Val) Val {
	return Obj(
		P("name", Str(name)),
		P("isPaginated", Bool(false)),
		P("entries", List(entries...)),
		P("cursor", Absent()),
	)
}

// searchEntryAt resolves one vpath and renders it as a search entry,
// dropping it silently on a refusal: a hit or a journal row is a moment-old
// answer, not a guarantee the path still resolves.
func (s *Server) searchEntryAt(ctx context.Context, p Principal, vpath string) (Val, bool) {
	res, err := s.resolve(ctx, p, vpath, acl.Read)
	if err != nil {
		return Val{}, false
	}
	entry, err := s.deps.Core.Stat(ctx, res)
	if err != nil {
		return Val{}, false
	}
	id := s.fileID(ctx, entry)

	// The vpath, not the share-relative path a resolution carries: a result
	// under the share labelled "Files" lives at "/Files/reports/x.pdf", and
	// reporting "/reports/x.pdf" sent the app to a folder that is not there.
	path := "/" + strings.Trim(vpath, "/")
	parent := parentOf(path)
	return Obj(
		P("thumbnailUrl", Str(previewURL(id))),
		P("title", Str(entry.Name)),
		P("subline", Str(parent)),
		P("resourceUrl", Str(filesAppURL(parent, entry.Name))),
		P("icon", Str("")),
		P("rounded", Bool(false)),
		P("attributes", Obj(
			P("fileId", Str(strconv.FormatUint(id, 10))),
			P("path", Str(path)),
		)),
	), true
}

// previewURL is where a search entry's thumbnail is fetched from: the same
// preview endpoint a PROPFIND-driven thumbnail request would use. Empty for
// an entry with no stable identity, since the preview endpoint has nothing
// to resolve it by.
func previewURL(fileID uint64) string {
	if fileID == 0 {
		return ""
	}
	return "/core/preview?fileId=" + strconv.FormatUint(fileID, 10) + "&x=256&y=256&a=1&mode=cover&forceIcon=0"
}

// filesAppURL is where a search entry's title opens: the file's containing
// folder in the web interface.
func filesAppURL(parent, name string) string {
	return "/apps/files/?dir=" + parent + "&scrollto=" + name
}

// parentOf reads the containing folder of an absolute path, "/" at the root.
func parentOf(path string) string {
	i := strings.LastIndexByte(strings.TrimSuffix(path, "/"), '/')
	if i <= 0 {
		return "/"
	}
	return path[:i]
}

// searchSourcesOf adapts the caller's own readable shares into search
// sources, scoped to a device credential's share allowlist and prefixed with
// the label the caller navigates each one under, so a hit names a path the
// caller can act on rather than a share-relative fragment.
func (s *Server) searchSourcesOf(u core.UserID) []search.Source {
	scan := s.deps.Core.UserScanSources(u)
	out := make([]search.Source, 0, len(scan))
	for _, src := range scan {
		label := s.deps.Core.ShareLabel(u, src.Share)
		if label == "" {
			continue
		}
		out = append(out, search.Source{
			Share:  uint32(src.Share),
			Root:   src.Root,
			Base:   src.Base,
			Prefix: "/" + label + "/",
			Allow:  src.Allow,
		})
	}
	return out
}

// parseSearchLimit bounds a client-supplied limit: an absent or
// non-positive value asks for the shared default, and nothing is ever
// unbounded.
func parseSearchLimit(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return searchLimitDefault
	}
	return min(n, searchLimitMax)
}

// recentFiles answers GET /apps/files/api/v1/recent through core.Recent.
func (s *Server) recentFiles(c *fiber.Ctx, p Principal) (Val, bool, *Error) {
	ctx := c.UserContext()
	hits, err := s.deps.Core.Recent(ctx, user(p), core.RecentQuery{})
	if err != nil {
		return Val{}, false, ocsErrorOf(err, apierr.VisibilityHidden)
	}
	items := make([]Val, 0, len(hits))
	for _, h := range hits {
		if v, ok := s.fileEntryAt(ctx, p, h.Vpath.String()); ok {
			items = append(items, v)
		}
	}
	return List(items...), true, nil
}

// favoriteFiles answers GET /apps/files/api/v1/favorites through the
// store's starred set.
func (s *Server) favoriteFiles(c *fiber.Ctx, p Principal) (Val, bool, *Error) {
	ctx := c.UserContext()
	set, err := s.deps.Store.Favorites(ctx, user(p))
	if err != nil {
		return Val{}, false, ocsErrorOf(err, apierr.VisibilityHidden)
	}
	favs := set.List()
	items := make([]Val, 0, len(favs))
	for _, f := range favs {
		vpath, ok := s.vpathOfFavorite(user(p), f)
		if !ok {
			continue
		}
		if v, ok := s.fileEntryAt(ctx, p, vpath); ok {
			items = append(items, v)
		}
	}
	return List(items...), true, nil
}

// vpathOfFavorite crosses a favourite's stored (share, share-relative path)
// pair back into the wire vpath the resolve path expects. A row whose path
// no longer parses, or whose share the account can no longer reach under
// any label, is skipped rather than surfaced as an error: the row still
// exists, but there is nothing left to render it as.
func (s *Server) vpathOfFavorite(u core.UserID, f Favorite) (string, bool) {
	if s.deps.VpathOf == nil {
		return "", false
	}
	vpath, err := s.deps.VpathOf(u, f.Share, f.Path)
	if err != nil {
		return "", false
	}
	return vpath, true
}

// fileEntryAt resolves one vpath and renders the file-entry shape the files
// app reads, or reports false when the path no longer resolves.
func (s *Server) fileEntryAt(ctx context.Context, p Principal, vpath string) (Val, bool) {
	res, err := s.resolve(ctx, p, vpath, acl.Read)
	if err != nil {
		return Val{}, false
	}
	return s.fileEntryVal(ctx, res, p)
}

// fileEntryVal renders the file-entry shape the files app reads for one
// resolved path: the same numbers the DAV surface reports for the same
// file, so the two views agree. A path this deployment cannot mint a stable
// identity for is skipped, the same as any other file a listing cannot
// vouch for.
func (s *Server) fileEntryVal(ctx context.Context, res core.Resolved, p Principal) (Val, bool) {
	entry, err := s.deps.Core.Stat(ctx, res)
	if err != nil {
		return Val{}, false
	}
	id := s.fileID(ctx, entry)
	if id == 0 {
		return Val{}, false
	}

	favorite := false
	if set, ferr := s.deps.Store.Favorites(ctx, user(p)); ferr == nil {
		favorite = set.Has(entry)
	}

	kind := "file"
	// A directory reports no length of its own, which is the same answer the
	// listing surface gives: a client reads the recursive size elsewhere. A
	// length too large to render as a signed number reports as unknown for
	// the same reason.
	var size int64 = -1
	if !entry.IsDir {
		if narrowed, nerr := num.Narrow[int64](entry.Size); nerr == nil {
			size = narrowed
		}
	} else {
		kind = "dir"
	}

	return Obj(
		P("id", Str(strconv.FormatUint(id, 10))),
		P("fileid", Int(fileIDNumber(id))),
		P("path", Str("/"+res.Path().String())),
		P("name", Str(entry.Name)),
		P("mtime", Int(entry.MTimeNs/int64(1e9))),
		P("mimetype", Str(ContentTypeOf(entry.IsDir, entry.Name))),
		P("type", Str(kind)),
		P("size", Int(size)),
		P("etag", Str(entry.ETag)),
		P("permissions", Str(PermString(entry.Perms, entry.IsDir, false, false))),
		P("favorite", Bool(favorite)),
	), true
}

// fileIDNumber renders a stable identity as the number the files app reads.
// An id too large to be a signed number answers zero, which the app treats as
// an entry it cannot open rather than as a different file.
func fileIDNumber(id uint64) int64 {
	narrowed, err := num.Narrow[int64](id)
	if err != nil {
		return 0
	}
	return narrowed
}
