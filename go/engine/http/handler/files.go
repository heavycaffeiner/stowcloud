// Linux only, for the same reason as the rest of this package.
//go:build linux

// The files family's projection.
//
// A listing is where the wire rules earn their keep: sizes and timestamps
// exceed a JavaScript number, a missing birth time is not a zero one, and the
// counts a grid needs describe the whole directory rather than the page in
// hand.
package handler

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// EntryView is one file or directory.
type EntryView struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	IsDir bool   `json:"is_dir"`

	// Size and MTimeNs are decimal strings. A file past 2^53 bytes and every
	// nanosecond timestamp lose exactness as JavaScript numbers, and a size
	// that comes back wrong is a download that comes back wrong.
	Size    string `json:"size"`
	MTimeNs string `json:"mtime_ns"`

	// BTimeNs is absent where the filesystem has no birth time to report.
	// Zero is a real timestamp, so it cannot stand for "unknown".
	BTimeNs *string `json:"btime_ns,omitempty"`

	ETag     string `json:"etag"`
	ETagWeak bool   `json:"etag_weak"`

	// Perms is the caller's effective permission set at this path, by name.
	Perms []string `json:"perms"`

	// Preview is present on a file whose name suggests the decoder can
	// re-encode it. Absent on everything else, which is what keeps a grid
	// from requesting a thumbnail of every text file it lists.
	Preview *PreviewView `json:"preview,omitempty"`
}

// PreviewView is the thumbnail hint for one entry.
type PreviewView struct {
	// Available is a guess from the name, not a promise. The thumbnail route
	// is the authority: it opens the file, and one that is not what its name
	// claims is refused there.
	Available bool `json:"available"`
}

// previewable reports whether name looks like something the decoder handles.
//
// By extension, because the alternative is opening every file in a listing to
// sniff it. The cost of guessing wrong is one request that comes back refused,
// which the grid already handles by keeping the type icon.
func previewable(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".tif", ".tiff", ".webp":
		return true
	default:
		return false
	}
}

// PageView is one page of a directory listing.
type PageView struct {
	Entries []EntryView `json:"entries"`

	// Dirs and Total describe the whole directory rather than this page.
	// Directories sort first, so Dirs is also where files begin, and a grid
	// needs that boundary without having loaded the rows either side of it.
	Dirs  int `json:"dirs"`
	Total int `json:"total"`

	DirETag     string `json:"dir_etag"`
	DirETagWeak bool   `json:"dir_etag_weak"`

	// Next is absent on the final page, so its presence is what a client
	// tests rather than comparing counts.
	Next string `json:"next,omitempty"`
}

// EntryOf projects one entry, addressed by the path the caller sent it.
//
// vpath is the entry's path as the client addresses it, label included. A
// core.Entry carries only its share-relative path, which names no share and
// therefore resolves to nothing when it comes back on the next request: the
// listing said `Docs/readme.txt` for a file the caller has to ask for as
// `Files/Docs/readme.txt`, so every row's own path was a 404 and download,
// preview and stat all went through it.
func EntryOf(e core.Entry, vpath string) EntryView {
	v := EntryView{
		Name:     e.Name,
		Path:     vpath,
		Kind:     e.KindName(),
		IsDir:    e.IsDir,
		Size:     strconv.FormatUint(e.Size, 10),
		MTimeNs:  strconv.FormatInt(e.MTimeNs, 10),
		ETag:     e.ETag,
		ETagWeak: e.ETagWeak,
		Perms:    e.PermNames(),
	}
	if e.BTimeNs != nil {
		b := strconv.FormatInt(*e.BTimeNs, 10)
		v.BTimeNs = &b
	}
	if !e.IsDir && previewable(e.Name) {
		v.Preview = &PreviewView{Available: true}
	}
	return v
}

// PageOf projects one page of a listing.
//
// An empty page carries an empty list rather than null, so a client iterating
// the entries does not have to test the field first.
func PageOf(p core.Page, vpathOf func(core.Entry) string) PageView {
	out := PageView{
		Entries:     make([]EntryView, 0, len(p.Entries)),
		Dirs:        p.Dirs,
		Total:       p.Total,
		DirETag:     p.DirEtag,
		DirETagWeak: p.DirEtagWeak,
		Next:        string(p.Next),
	}
	for _, e := range p.Entries {
		out.Entries = append(out.Entries, EntryOf(e, vpathOf(e)))
	}
	return out
}
