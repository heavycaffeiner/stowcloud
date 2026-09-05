//go:build linux && compat_nc

// The compatibility vocabulary the WebDAV mount carries when the tag is on:
// the alternative prefixes the clients mount, the header names the chunked
// upload collection reads, and the vendor properties the sync client's
// journal is keyed on.
package lifecycle

import (
	"context"
	"encoding/xml"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/compat"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/limits"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/num"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/search/svc"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/cache"
	"github.com/heavycaffeiner/stowcloud/go/engine/store/state"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// davAliases are the alternative mount points the sync clients address, with
// the segment that names the account dropped rather than checked.
//
// The uploads prefixes come first only for readability; the matcher takes the
// longest match, because "/remote.php/dav/uploads/" sits under the same root
// as the files tree and the shorter prefix would swallow it.
func (e *Engine) davAliases() []DavAlias {
	return []DavAlias{
		{Prefix: "/remote.php/dav/uploads/", Mount: DavUploadPrefix, DropSegments: 1},
		{Prefix: "/index.php/remote.php/dav/uploads/", Mount: DavUploadPrefix, DropSegments: 1},
		{Prefix: "/remote.php/dav/trashbin/", Mount: DavTrashPrefix, DropSegments: 1},
		{Prefix: "/index.php/remote.php/dav/trashbin/", Mount: DavTrashPrefix, DropSegments: 1},
		{Prefix: "/remote.php/dav/files/", DropSegments: 1},
		{Prefix: "/index.php/remote.php/dav/files/", DropSegments: 1},
		{Prefix: "/remote.php/dav/", DropSegments: 0},
		{Prefix: "/index.php/remote.php/dav/", DropSegments: 0},
		{Prefix: "/remote.php/webdav/", DropSegments: 0},
		{Prefix: "/index.php/remote.php/webdav/", DropSegments: 0},
	}
}

// davIsAssemblyMember reports whether an uploads member names the assembly
// target rather than a chunk.
//
// The client MOVEs that member to publish a transfer, so the name is its
// vocabulary and stays behind this seam: the mount asks whether a member
// means assembly and never learns what it is spelled.
func (e *Engine) davIsAssemblyMember(member string) bool {
	return compat.IsAssembly(member)
}

// davUploadHeaders names the headers the chunked upload collection reads.
//
// They are the other product's vocabulary, named here so that package reads
// whatever it is given and learns none of it. A partly filled set disables
// the collection, so all three travel together or none does.
func (e *Engine) davUploadHeaders() dav.UploadHeaders {
	return dav.UploadHeaders{
		TotalLength: "OC-Total-Length",
		MTime:       "X-OC-Mtime",
		ETag:        "OC-ETag",
	}
}

// davVendorProps contributes the properties the sync client reads on every
// PROPFIND.
//
// The instance id is read once, here, because it is minted once and never
// changes: a client that saw one value and then another re-syncs everything
// it holds. A failure to read it is a database this mount cannot answer
// through, so the source is absent rather than half-present, and every
// property the client asked for answers missing, which is the answer it
// already handles by skipping the entry.
func (e *Engine) davVendorProps() func(
	ctx context.Context, res core.Resolved, entry core.Entry, want []xml.Name,
) []dav.Prop {
	id, err := e.State.InstanceID(context.Background())
	if err != nil {
		e.logger.Warn("the instance id could not be read; the sync surfaces are absent",
			"error", err)
		return nil
	}

	source := compat.NewPropSource(compat.PropSourceDeps{
		InstanceID: func() string { return id },
		Shared:     func(compat.PropEntry) bool { return false },
	})

	return func(ctx context.Context, res core.Resolved, entry core.Entry, want []xml.Name) []dav.Prop {
		// The file id is resolved here, where the entry's identity lives: the
		// recorded override wins, and otherwise the id is the pure
		// derivation the allocation policy answers for a first candidate.
		fileID, ferr := e.compatFileID(ctx, entry)
		if ferr != nil {
			e.logger.Warn("an entry reached property emission without a file id",
				"name", entry.Name, "error", ferr)
		} else {
			if vp, verr := e.Core.VpathFor(res.User(), res.Share(), entry.Path); verr == nil {
				e.fileIDCache.Store(fileID, vp)
			}
		}

		isFav := false
		var dirSize *uint64
		for _, w := range want {
			if !isFav && (w.Local == "favorite" || w.Local == "is-favorite") {
				favs := e.favoritesOf(ctx, int64(res.User()))
				for _, f := range favs {
					if f.Ident.Equal(entry.Ident) {
						isFav = true
						break
					}
				}
			}
			if entry.IsDir && w.Local == "size" {
				if sp, sperr := entry.Path.Safe(); sperr == nil {
					if agg, aerr := e.Core.Aggregate(ctx, res.Share(), sp); aerr == nil {
						s := agg.RSize
						dirSize = &s
					}
				}
			}
		}

		return source.Props(compat.PropEntry{
			IsDir:      entry.IsDir,
			Size:       entry.Size,
			DirSize:    dirSize,
			Perms:      permBitsOf(entry.Perms),
			FileID:     fileID,
			HasPreview: !entry.IsDir && isPreviewable(entry.Name),
			Favorite:   isFav,
		}, want)
	}
}

// favoritesOf caches a user's favorites once per PROPFIND request context.
func (e *Engine) favoritesOf(ctx context.Context, user int64) []state.Favorite {
	if val, ok := e.favCache.Load(ctx); ok {
		if favs, ok := val.([]state.Favorite); ok {
			return favs
		}
	}
	favs, err := e.State.Favorites(ctx, user)
	if err != nil {
		return nil
	}
	e.favCache.Store(ctx, favs)
	return favs
}

func isPreviewable(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".tif", ".tiff", ".webp":
		return true
	default:
		return false
	}
}

// compatFileID resolves the stable id a sync journal keys an entry on.
//
// A recorded override wins, because a past collision decision is never
// revisited. Otherwise the id is the pure derivation: the allocation policy
// answers the same value for a first candidate that finds no collision, and
// the collision cases are exactly the ones that left overrides behind.
//
// A read that allocated would give a client an id for a file that may not
// exist by the time it acts, and would write during a listing.
func (e *Engine) compatFileID(ctx context.Context, entry core.Entry) (uint64, error) {
	if recorded, ok, err := e.State.LookupFileID(ctx, entry.Ident); err != nil {
		return 0, err
	} else if ok {
		return num.Narrow[uint64](recorded)
	}
	return num.Narrow[uint64](cache.DeriveID(entry.Ident, 0))
}

// permBitsOf maps the core's permission bitset onto the renderer's.
//
// Seven of the eight bits transfer one for one; Download has no letter on
// this wire, because a client that can read a file can download it and the
// vocabulary simply has nothing to say about the difference.
func permBitsOf(p acl.Perms) compat.PermBits {
	var out compat.PermBits
	if p.Has(acl.Share) {
		out |= compat.PermShare
	}
	if p.Has(acl.Read) {
		out |= compat.PermRead
	}
	if p.Has(acl.Delete) {
		out |= compat.PermDelete
	}
	if p.Has(acl.Rename) {
		out |= compat.PermRename
	}
	if p.Has(acl.Move) {
		out |= compat.PermMove
	}
	if p.Has(acl.Create) {
		out |= compat.PermCreate
	}
	if p.Has(acl.Write) {
		out |= compat.PermWrite
	}
	return out
}

func (e *Engine) davRootProps(ctx context.Context, user core.UserID) ([]dav.Prop, []dav.RootChild) {
	id, err := e.State.InstanceID(ctx)
	if err != nil {
		id = "stowcloud"
	}

	roots := e.Core.Roots(user)

	// The virtual root is where a compatibility client discovers what to
	// mount, so an encrypted share must disappear here: showing it invites a
	// Nextcloud or ownCloud sync client to fill a local folder with
	// ciphertext it can never decrypt. A failure reading the encrypted set
	// fails closed by hiding every share rather than risking one slipping
	// through unfiltered.
	var hidden map[core.ShareID]bool
	hideAll := false
	if davIsCompat(ctx) {
		set, eerr := e.encryptedShareSet(ctx)
		if eerr != nil {
			hideAll = true
		} else {
			hidden = set
		}
	}

	children := make([]dav.RootChild, 0, len(roots))
	for _, rt := range roots {
		if hideAll || (hidden != nil && hiddenShare(hidden, rt.Share)) {
			continue
		}
		label := rt.Label
		props := []dav.Prop{
			{
				Name:     xml.Name{Space: "DAV:", Local: "resourcetype"},
				Children: []dav.Element{{Name: xml.Name{Space: "DAV:", Local: "collection"}}},
			},
			{Name: xml.Name{Space: "DAV:", Local: "getcontenttype"}, Value: "httpd/unix-directory"},
			{Name: xml.Name{Space: "DAV:", Local: "displayname"}, Value: label},
		}

		etag := `"` + label + `"`
		mtime := e.clk().Now().UTC().Format(http.TimeFormat)
		permStr := "RGDNVCK"
		fidStr := "0"
		davID := compat.DavID(0, id)

		var shareSize uint64
		if res, rerr := e.resolve(user, "/"+label, acl.Read); rerr == nil {
			if agg, aerr := e.Core.Aggregate(ctx, res.Share(), res.Path()); aerr == nil {
				shareSize = agg.RSize
			}
			if st, serr := res.Root().Stat(res.Path()); serr == nil {
				entry := e.Core.EntryAt(res, st)
				if fid, ferr := e.compatFileID(ctx, entry); ferr == nil {
					fidStr = strconv.FormatUint(fid, 10)
					davID = compat.DavID(fid, id)
				}
				perms := permBitsOf(entry.Perms)
				permStr = compat.DavPermissions(perms, true, false)
				etag = `"` + entry.ETag + `"`
				if entry.MTimeNs > 0 {
					mtime = time.Unix(0, entry.MTimeNs).UTC().Format(http.TimeFormat)
				}
			}
		}

		props = append(props,
			dav.Prop{Name: xml.Name{Space: "DAV:", Local: "getetag"}, Value: etag},
			dav.Prop{Name: xml.Name{Space: "DAV:", Local: "getlastmodified"}, Value: mtime},
			dav.Prop{Name: xml.Name{Space: compat.NSOwnCloud, Local: "permissions"}, Value: permStr},
			dav.Prop{Name: xml.Name{Space: compat.NSNextcloudX, Local: "permissions"}, Value: permStr},
			dav.Prop{Name: xml.Name{Space: compat.NSOwnCloud, Local: "id"}, Value: davID},
			dav.Prop{Name: xml.Name{Space: compat.NSNextcloudX, Local: "id"}, Value: davID},
			dav.Prop{Name: xml.Name{Space: compat.NSOwnCloud, Local: "fileid"}, Value: fidStr},
			dav.Prop{Name: xml.Name{Space: compat.NSNextcloudX, Local: "fileid"}, Value: fidStr},
			dav.Prop{Name: xml.Name{Space: compat.NSOwnCloud, Local: "size"}, Value: strconv.FormatUint(shareSize, 10)},
			dav.Prop{Name: xml.Name{Space: compat.NSOwnCloud, Local: "has-preview"}, Value: "false"},
			dav.Prop{Name: xml.Name{Space: compat.NSOwnCloud, Local: "favorite"}, Value: "0"},
			dav.Prop{Name: xml.Name{Space: compat.NSNextcloudX, Local: "favorite"}, Value: "0"},
		)

		children = append(children, dav.RootChild{
			Label: label,
			Props: props,
		})
	}

	rootDavID := compat.DavID(1, id)
	rootMtime := e.clk().Now().UTC().Format(http.TimeFormat)
	baseProps := []dav.Prop{
		{
			Name:     xml.Name{Space: "DAV:", Local: "resourcetype"},
			Children: []dav.Element{{Name: xml.Name{Space: "DAV:", Local: "collection"}}},
		},
		{Name: xml.Name{Space: "DAV:", Local: "getcontenttype"}, Value: "httpd/unix-directory"},
		{Name: xml.Name{Space: "DAV:", Local: "getetag"}, Value: `"root"`},
		{Name: xml.Name{Space: "DAV:", Local: "displayname"}, Value: ""},
		{Name: xml.Name{Space: "DAV:", Local: "getlastmodified"}, Value: rootMtime},
		{Name: xml.Name{Space: compat.NSOwnCloud, Local: "permissions"}, Value: "RGDNV"},
		{Name: xml.Name{Space: compat.NSNextcloudX, Local: "permissions"}, Value: "RGDNV"},
		{Name: xml.Name{Space: compat.NSOwnCloud, Local: "id"}, Value: rootDavID},
		{Name: xml.Name{Space: compat.NSNextcloudX, Local: "id"}, Value: rootDavID},
		{Name: xml.Name{Space: compat.NSOwnCloud, Local: "fileid"}, Value: "1"},
		{Name: xml.Name{Space: compat.NSNextcloudX, Local: "fileid"}, Value: "1"},
		{Name: xml.Name{Space: compat.NSOwnCloud, Local: "size"}, Value: "0"},
	}

	return baseProps, children
}

func (e *Engine) davSources() []dav.QuerySource {
	return []dav.QuerySource{&compatQuerySource{engine: e}}
}

type compatQuerySource struct {
	engine *Engine
}

func (s *compatQuerySource) Namespaces() []string {
	return []string{"DAV:", compat.NSOwnCloud, compat.NSNextcloudX, "http://nextcloud.com/ns"}
}

// davQuery is what a parsed filter asked for.
//
// A wire filter names a property and compares it against a literal. The two
// arrive as separate terms, so the property decides which question this is
// and the literals answer the follow-up: which media family, which name,
// which cutoff.
type davQuery struct {
	favorites bool
	media     bool
	image     bool
	video     bool
	name      string
	sinceNs   int64
}

// parseDavQuery reads the filter terms.
//
// Keyed on the property rather than on the literal's text, because the same
// text means different things under different properties: a search for a file
// named "yes" is not a request for the starred set.
func parseDavQuery(leaves []dav.Leaf, want []xml.Name) davQuery {
	var q davQuery
	var literals []string
	byName := false
	byTime := false

	for _, leaf := range leaves {
		switch leaf.Name.Local {
		case "favorite", "is-favorite":
			q.favorites = true
			// The report shape carries the answer in the term itself, and an
			// explicit "0" asks for the unstarred set, which is not offered.
			if leaf.Value == "0" {
				q.favorites = false
			}
		case "getcontenttype":
			q.media = true
		case "displayname":
			byName = true
		case "getlastmodified":
			byTime = true
		case "literal":
			literals = append(literals, leaf.Value)
		}
	}
	// A property named in DAV:prop rather than as a term still selects the
	// starred set: the report shape puts it there.
	for _, prop := range want {
		if prop.Local == "favorite" || prop.Local == "is-favorite" {
			q.favorites = true
		}
	}
	if q.favorites {
		return q
	}

	for _, literal := range literals {
		switch {
		case q.media && strings.HasPrefix(literal, "image/"):
			q.image = true
		case q.media && strings.HasPrefix(literal, "video/"):
			q.video = true
		case byName && q.name == "":
			q.name = strings.Trim(literal, "%")
		case byTime && q.sinceNs == 0:
			if t, err := time.Parse(time.RFC3339, literal); err == nil {
				q.sinceNs = t.UnixNano()
			}
		}
	}
	// A content-type filter whose literal named neither family still asks
	// about media, so it gets both rather than nothing.
	if q.media && !q.image && !q.video {
		q.image, q.video = true, true
	}
	return q
}

func (s *compatQuerySource) Query(
	ctx context.Context, res core.Resolved, leaves []dav.Leaf, want []xml.Name,
) ([]core.Entry, error) {
	user := res.User()
	if user == 0 {
		return nil, nil
	}

	q := parseDavQuery(leaves, want)
	switch {
	case q.favorites:
		return s.favoriteEntries(ctx, res, user), nil
	case q.media:
		return s.mediaEntries(ctx, res, user, q), nil
	case q.name != "":
		return s.namedEntries(ctx, res, user, q.name), nil
	}
	return s.recentEntries(ctx, res, user, q.sinceNs), nil
}

func (s *compatQuerySource) favoriteEntries(
	ctx context.Context, res core.Resolved, user core.UserID,
) []core.Entry {
	favs, err := s.engine.State.Favorites(ctx, int64(user))
	if err != nil {
		return nil
	}
	var out []core.Entry
	for _, f := range favs {
		path := "/" + strings.TrimPrefix(f.Path, "/")
		vp, perr := vfs.ParseVpath(path)
		if perr != nil {
			continue
		}
		r, rerr := s.engine.Core.Resolve(user, vp, acl.Read)
		if rerr != nil {
			continue
		}
		if res.Share() != 0 && r.Share() != res.Share() {
			continue
		}
		st, serr := r.Root().Stat(r.Path())
		if serr != nil {
			continue
		}
		out = append(out, s.engine.Core.EntryAt(r, st))
	}
	return out
}

// mediaExts are the names the gallery asks about.
//
// Extensions rather than sniffed types: the query has to find candidates
// across the whole tree, and reading every file to classify it is not a
// listing.
func mediaExts(image, video bool) []string {
	var out []string
	if image {
		out = append(out,
			".png", ".jpg", ".jpeg", ".gif", ".bmp", ".tif", ".tiff",
			".webp", ".svg", ".heic", ".heif", ".avif")
	}
	if video {
		out = append(out,
			".mp4", ".mov", ".mkv", ".webm", ".avi", ".m4v",
			".3gp", ".mpeg", ".mpg", ".ogv", ".wmv", ".flv")
	}
	return out
}

func extIn(name string, exts []string) bool {
	got := strings.ToLower(filepath.Ext(name))
	for _, ext := range exts {
		if got == ext {
			return true
		}
	}
	return false
}

// mediaEntries answers a content-type filter.
//
// The name index supplies the candidates, because the client asks about the
// whole subtree and a listing of the queried folder answers about one level
// of it: a photo library keeps its photos in folders. Each extension is one
// index query, and the real extension is checked afterwards, so a name that
// merely contains one is not reported as media.
func (s *compatQuerySource) mediaEntries(
	ctx context.Context, res core.Resolved, user core.UserID, q davQuery,
) []core.Entry {
	wanted := mediaExts(q.image, q.video)
	if s.engine.Search == nil {
		// Without an index the queried scope is what can be answered
		// cheaply. One level, and honestly one level.
		page, err := s.engine.Core.List(ctx, res, "")
		if err != nil {
			return nil
		}
		var out []core.Entry
		for _, entry := range page.Entries {
			if !entry.IsDir && extIn(entry.Name, wanted) {
				out = append(out, entry)
			}
		}
		return out
	}

	sources := searchSourcesOf(s.engine.Core.UserScanSources(user), user, s.engine.Core)
	seen := make(map[string]struct{})
	var out []core.Entry
	for _, ext := range wanted {
		if len(out) >= limits.SearchResults {
			break
		}
		results, err := s.engine.Search.Query(ctx, sources,
			svc.QueryOptions{Query: ext, Limit: limits.SearchResults})
		if err != nil {
			continue
		}
		for _, hit := range results.Hits {
			if !extIn(hit.Name, wanted) {
				continue
			}
			path := "/" + strings.TrimPrefix(hit.Path, "/")
			if _, dup := seen[path]; dup {
				continue
			}
			entry, ok := s.entryAt(user, res, path)
			if !ok {
				continue
			}
			seen[path] = struct{}{}
			out = append(out, entry)
			if len(out) >= limits.SearchResults {
				break
			}
		}
	}
	return out
}

// namedEntries answers a display-name filter, which is the client's search
// box: the same question the OCS unified search answers, over the same index.
func (s *compatQuerySource) namedEntries(
	ctx context.Context, res core.Resolved, user core.UserID, needle string,
) []core.Entry {
	if s.engine.Search == nil || needle == "" {
		return nil
	}
	results, err := s.engine.Search.Query(ctx,
		searchSourcesOf(s.engine.Core.UserScanSources(user), user, s.engine.Core),
		svc.QueryOptions{Query: needle, Limit: limits.SearchResults})
	if err != nil {
		return nil
	}
	var out []core.Entry
	for _, hit := range results.Hits {
		entry, ok := s.entryAt(user, res, "/"+strings.TrimPrefix(hit.Path, "/"))
		if !ok {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// recentEntries answers a modification-time filter, and is what an
// unrecognised filter falls back to: the reference's own recent view.
//
// sinceNs of zero takes the default window, which is what a report shape with
// no comparable literal asks for.
func (s *compatQuerySource) recentEntries(
	ctx context.Context, res core.Resolved, user core.UserID, sinceNs int64,
) []core.Entry {
	if sinceNs <= 0 {
		sinceNs = s.engine.clk().Now().Add(-14 * 24 * time.Hour).UnixNano()
	}
	hits, err := s.engine.Core.Recent(ctx, user, core.RecentQuery{
		SinceNs: sinceNs,
		Limit:   50,
	})
	if err != nil {
		return nil
	}
	var out []core.Entry
	for _, hit := range hits {
		entry, ok := s.entryAt(user, res, hit.Vpath.String())
		if !ok {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// entryAt resolves a virtual path for the caller and confines it to the
// queried scope, which is what keeps one share's answer out of another's when
// the root query runs a filter against every share in turn.
func (s *compatQuerySource) entryAt(
	user core.UserID, res core.Resolved, vpath string,
) (core.Entry, bool) {
	r, err := s.engine.resolve(user, vpath, acl.Read)
	if err != nil {
		return core.Entry{}, false
	}
	if res.Share() != 0 && r.Share() != res.Share() {
		return core.Entry{}, false
	}
	st, serr := r.Root().Stat(r.Path())
	if serr != nil {
		return core.Entry{}, false
	}
	return s.engine.Core.EntryAt(r, st), true
}
