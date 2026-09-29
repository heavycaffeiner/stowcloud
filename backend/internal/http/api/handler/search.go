// Linux only, for the same reason as the rest of this package.
//go:build linux

// The search family's projection.
package handler

import (
	"strconv"

	search "github.com/stowcloud/namesearch"
)

// SearchHitView is one match.
//
// Size and the modification time are pointers on the service side and stay
// absent here when the query never stated. Reporting a zero for "not measured"
// would show a client a 0-byte file that is not one.
type SearchHitView struct {
	Path    string  `json:"path"`
	Name    string  `json:"name"`
	IsDir   bool    `json:"is_dir"`
	Share   string  `json:"share"`
	Size    *string `json:"size,omitempty"`
	MTimeNs *string `json:"mtime_ns,omitempty"`
	Score   float32 `json:"score"`
}

// SearchHitViewOf projects one hit, which is what a streamed answer sends one
// frame at a time.
func SearchHitViewOf(h search.Hit) SearchHitView {
	v := SearchHitView{
		Path:  h.Path,
		Name:  h.Name,
		IsDir: h.IsDir,
		Share: strconv.FormatUint(uint64(h.Share), 10),
		Score: h.Score,
	}
	if h.Size != nil {
		s := strconv.FormatUint(*h.Size, 10)
		v.Size = &s
	}
	if h.MTimeNs != nil {
		m := strconv.FormatInt(*h.MTimeNs, 10)
		v.MTimeNs = &m
	}
	return v
}
