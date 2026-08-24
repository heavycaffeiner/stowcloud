//go:build linux && compat_nc

package nc

import (
	"context"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// The unified-search provider.
//
// One endpoint advertises the providers and another answers them. This is an
// envelope over the same filename search the WebDAV surface uses, so the two
// screens cannot disagree about what a term matches.

// Paging. Both apps page with a cursor, so a small page is cheap.
const (
	searchDefaultLimit = 25
	searchMaxLimit     = 100
)

// SearchProviders is the provider list.
//
// One entry, because files are the only thing this server has to search.
func SearchProviders() Val {
	return VList(VMap(
		F("id", VStr("files")),
		F("name", VStr("Files")),
		F("order", VInt(0)),
	))
}

// search answers the provider.
func (l *Layer) search(ctx context.Context, user ncport.UserID, term string, limit, cursor int) (Val, *OCSError) {
	if l.deps.Search == nil {
		return Val{}, ServerError("search is not available")
	}
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	if limit > searchMaxLimit {
		limit = searchMaxLimit
	}
	if cursor < 0 {
		cursor = 0
	}
	if strings.TrimSpace(term) == "" {
		return searchPage("Files", nil, -1), nil
	}

	// One page past the offset plus one extra row, so the cursor can say
	// whether there is more without a second query.
	want := cursor + limit + 1
	hits, err := l.deps.Search.Search(ctx, user, term, want)
	if err != nil {
		return Val{}, ServerError("search failed")
	}

	total := len(hits)
	if cursor > total {
		cursor = total
	}
	end := cursor + limit
	if end > total {
		end = total
	}

	entries := make([]Val, 0, end-cursor)
	for _, e := range hits[cursor:end] {
		entries = append(entries, l.searchEntry(ctx, user, e))
	}

	next := -1
	if total > cursor+limit {
		next = cursor + limit
	}
	return searchPage("Files", entries, next), nil
}

func (l *Layer) searchEntry(ctx context.Context, user ncport.UserID, e ncport.Entry) Val {
	absolute := "/" + strings.TrimPrefix(e.Path.String(), "/")
	parent := parentOf(absolute)

	thumb := ""
	if l.deps.Preview != nil {
		if u, ok, err := l.deps.Preview.SignedThumbURL(ctx, user, absolute, 64, 64); err == nil && ok {
			thumb = u
		}
	}

	id := ""
	if l.deps.FileID != nil {
		if fid, ok := l.deps.FileID(e); ok {
			id = strconv.FormatUint(uint64(fid), 10)
		}
	}

	return VMap(
		F("thumbnailUrl", VStr(thumb)),
		F("title", VStr(e.Name)),
		// The parent path, which is what both apps show under the title.
		F("subline", VStr(parent)),
		F("resourceUrl", VStr(strings.TrimRight(l.caps.CanonicalURL, "/")+
			"/apps/files/?dir="+parent)),
		F("icon", VStr("")),
		F("rounded", VBool(false)),
		// Both apps read the path out of the attributes in preference to
		// parsing the resource URL, so it is always populated.
		F("attributes", VMap(
			F("fileId", VStr(id)),
			F("path", VStr(absolute)),
		)),
	)
}

// searchPage wraps a result set. A cursor below zero means there is no next
// page, which renders as a null the client stops on.
func searchPage(name string, entries []Val, cursor int) Val {
	next := VNull()
	if cursor >= 0 {
		next = VInt(int64(cursor))
	}
	if entries == nil {
		entries = []Val{}
	}
	return VMap(
		F("name", VStr(name)),
		F("isPaginated", VBool(true)),
		F("entries", Val{Kind: KindList, List: entries}),
		F("cursor", next),
	)
}

func parentOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

// recent answers the recency query.
//
// The bound is a full timestamp rather than a bare date, which is the recorded
// defect: the value is compared against a timestamp and a date literal is not
// one, so a bare date made both apps' request fail.
func (l *Layer) recent(ctx context.Context, user ncport.UserID, q RecentQuery) (Val, *OCSError) {
	if l.deps.Search == nil {
		return Val{}, ServerError("search is not available")
	}
	hits, err := l.deps.Search.Recent(ctx, user, q.Since.UnixNano(), q.Limit)
	if err != nil {
		return Val{}, ServerError("the recency query failed")
	}
	entries := make([]Val, 0, len(hits))
	for _, e := range hits {
		entries = append(entries, l.searchEntry(ctx, user, e))
	}
	return searchPage("Recent", entries, -1), nil
}
