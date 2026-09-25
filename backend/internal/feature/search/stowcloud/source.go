//go:build linux

// Package stowcloud adapts Stowcloud's core share roots into the plain search
// source API. Storage validation and ACL decisions stay here, outside search.
package stowcloud

import (
	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
	search "github.com/stowcloud/namesearch"
)

type reader struct{ root vfs.Root }

func (r reader) ReadDir(path string, visit func(search.Entry) bool) error {
	p, err := vfs.ParseSafePath(path)
	if err != nil {
		return err
	}
	return r.root.ReadDirFunc(p, vfs.HideReserved, func(e vfs.DirEntry) bool {
		return visit(search.Entry{Name: e.Name, Kind: search.EntryKind(e.Kind), Ino: e.Ino})
	})
}

func (r reader) Stat(path string) (search.Stat, error) {
	p, err := vfs.ParseSafePath(path)
	if err != nil {
		return search.Stat{}, err
	}
	st, err := r.root.Stat(p)
	if err != nil {
		return search.Stat{}, err
	}
	return search.Stat{Dev: st.Dev, Ino: st.Ino, Size: st.Size, MTimeNs: st.MtimeNs, Kind: search.EntryKind(st.Kind)}, nil
}

func SourceOf(s core.ScanSource) search.Source {
	if s.Root == nil {
		return search.Source{}
	}
	allow := func(path string, isDir bool) bool {
		if s.Allow == nil {
			return true
		}
		p, err := vfs.ParseSafePath(path)
		return err == nil && s.Allow(p, isDir)
	}
	var fn func(string, bool) bool
	if s.Allow != nil {
		fn = allow
	}
	return search.Source{
		Namespace: search.Namespace(uint32(s.Share)),
		Reader:    reader{root: s.Root},
		Base:      s.Base.String(),
		IndexBase: s.Base.String(),
		Allow:     fn,
	}
}

func SourcesOf(sources []core.ScanSource) []search.Source {
	out := make([]search.Source, 0, len(sources))
	for _, s := range sources {
		if src := SourceOf(s); src.Reader != nil {
			out = append(out, src)
		}
	}
	return out
}

func UserSources(c *core.Core, user core.UserID) []search.Source {
	return SourcesOf(c.UserScanSources(user))
}
func LabelSources(c *core.Core, user core.UserID, sources []core.ScanSource) []search.Source {
	out := make([]search.Source, 0, len(sources))
	for _, s := range sources {
		label := c.ShareLabel(user, s.Share)
		if label == "" {
			continue
		}
		src := SourceOf(s)
		if src.Reader == nil {
			continue
		}
		src.Prefix = "/" + label + "/"
		out = append(out, src)
	}
	return out
}
