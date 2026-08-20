//go:build compat_nc

package nc

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/compat/ncport"
)

// The thumbnail endpoints the clients call.
//
// No image bytes are ever served from the app origin. User-supplied content is
// rendered only on the separate content origin, behind a signed URL, so that a
// malicious upload cannot execute in the origin holding session cookies. That
// invariant gets no exception for a compat client.
//
// So this resolves the file, mints a signed URL on the content origin, and
// redirects. Mobile clients follow redirects, so nothing is lost. Proxying the
// bytes here would be a one-line change and would quietly delete the whole
// origin-isolation design.

// PreviewQuery is a parsed thumbnail request.
type PreviewQuery struct {
	// FileID addresses by id, and Path by path. One endpoint uses each.
	FileID *FileID
	Path   string
	Width  int
	Height int
	// ForceIcon asks for a generic placeholder, which is refused. See Redirect.
	ForceIcon bool
}

// The thumbnail bounds. A client asking for something absurd gets it clamped
// rather than refused: the size is advisory and a refusal shows the user a
// broken image where a smaller thumbnail would have done.
const (
	previewDefaultSize = 64
	previewMaxSize     = 4096
)

// ParsePreviewQuery reads a thumbnail request.
func ParsePreviewQuery(q map[string][]string) PreviewQuery {
	get := func(k string) string {
		if v, ok := q[k]; ok && len(v) > 0 {
			return v[0]
		}
		return ""
	}

	out := PreviewQuery{
		Path:      get("file"),
		Width:     clampSize(get("x")),
		Height:    clampSize(get("y")),
		ForceIcon: get("forceIcon") == "1",
	}
	if raw := get("fileId"); raw != "" {
		if id, err := ParseFileID(raw); err == nil {
			out.FileID = &id
		}
	}
	return out
}

func clampSize(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return previewDefaultSize
	}
	if n > previewMaxSize {
		return previewMaxSize
	}
	return n
}

// preview answers a thumbnail request with a redirect to the content origin.
func (l *Layer) preview(w http.ResponseWriter, r *http.Request, user ncport.UserID, q PreviewQuery) {
	if l.deps.Preview == nil {
		http.NotFound(w, r)
		return
	}

	// Both branches have to arrive at a path, and only one of them starts with
	// one. The id branch resolves through the core, which projects it back
	// into the caller's own tree: substituting a root and passing the client's
	// path through as if it were already relative is what once put every
	// path-addressed thumbnail one label too deep.
	path := ""
	switch {
	case q.FileID != nil:
		if l.deps.Direct == nil {
			http.NotFound(w, r)
			return
		}
		p, err := l.deps.Direct.Locate(r.Context(), user, *q.FileID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		path = p
	case q.Path != "":
		path = strings.TrimPrefix(q.Path, "/")
	default:
		http.NotFound(w, r)
		return
	}

	url, ok, err := l.deps.Preview.SignedThumbURL(r.Context(), user, path, q.Width, q.Height)
	if err != nil || !ok || url == "" {
		// A request for a generic placeholder is answered the same way, and
		// the client draws its own icon. Serving a placeholder would mean
		// serving bytes from the app origin for a file that has no preview,
		// and every client ships icons anyway.
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Location", url)
	// The signed URL is short-lived and scoped to one caller, so no shared
	// cache may keep it.
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusFound)
}

// PreviewMounts are the thumbnail routes.
//
// Three, because the clients use three shapes: two that address by id and one
// that addresses by path when the client has one rather than an id.
func (l *Layer) previewMounts() []Mount {
	byQuery := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, ok := l.authenticate(r)
		if !ok {
			http.Error(w, "sign in first", http.StatusUnauthorized)
			return
		}
		l.preview(w, r, ncport.UserID(who.User), ParsePreviewQuery(r.URL.Query()))
	})

	return []Mount{
		{Method: "GET", Pattern: "/index.php/core/preview", Handler: byQuery},
		{Method: "GET", Pattern: "/index.php/core/preview.png", Handler: byQuery},
		{
			Method:  "GET",
			Pattern: "/index.php/apps/files/api/v1/thumbnail/{x}/{y}/{path...}",
			Handler: http.HandlerFunc(l.thumbnailByPath),
		},
	}
}

// thumbnailByPath is the second thumbnail shape, used when the client has a
// path rather than an id.
func (l *Layer) thumbnailByPath(w http.ResponseWriter, r *http.Request) {
	who, ok := l.authenticate(r)
	if !ok {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	l.preview(w, r, ncport.UserID(who.User), PreviewQuery{
		Path:   r.PathValue("path"),
		Width:  clampSize(r.PathValue("x")),
		Height: clampSize(r.PathValue("y")),
	})
}
