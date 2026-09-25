//go:build linux

package vfs

import (
	"os"

	local "github.com/stowcloud/storage/local"
)

// File is an open descriptor returned by the local backend. Product code keeps
// this wrapper so identity and policy types remain product-owned.
type File struct{ f *os.File }

func WrapFile(f *os.File) *File {
	if f == nil {
		return nil
	}
	return &File{f: f}
}
func (f *File) Close() error {
	if f == nil || f.f == nil {
		return nil
	}
	return f.f.Close()
}
func (f *File) OSFile() *os.File {
	if f == nil {
		return nil
	}
	return f.f
}
func (f *File) ReadAt(b []byte, off int64) (int, error)  { return f.f.ReadAt(b, off) }
func (f *File) WriteAt(b []byte, off int64) (int, error) { return f.f.WriteAt(b, off) }
func (f *File) Truncate(n int64) error                   { return f.f.Truncate(n) }
func (f *File) Stat() (Stat, error) {
	st, err := local.WrapFile(f.f).Stat()
	return fromLocalStat(st), mapLocalErr(err)
}
func (f *File) Space() (FsSpace, error) {
	s, err := local.WrapFile(f.f).Space()
	return FsSpace{Total: s.Total, Free: s.Free, Available: s.Available}, mapLocalErr(err)
}
func (f *File) SyncData() error           { return mapLocalErr(local.WrapFile(f.f).SyncData()) }
func (f *File) SetMode(mode uint32) error { return mapLocalErr(local.WrapFile(f.f).SetMode(mode)) }
func (f *File) SetOwner(o Owner) error {
	return mapLocalErr(local.WrapFile(f.f).SetOwner(local.Owner{UID: o.UID, GID: o.GID}))
}

func localPathFor(p SafePath) (local.Path, error) { return local.ParsePath(p.String()) }
func (r *ShareRoot) OpenRead(p SafePath, intent AccessIntent) (*File, error) {
	q, e := localPathFor(p)
	if e != nil {
		return nil, mapErrno("parse path", e)
	}
	f, e := r.backend.OpenRead(q, local.AccessIntent(intent))
	if e != nil {
		return nil, mapLocalErr(e)
	}
	return WrapFile(f.OSFile()), nil
}
func (r *ShareRoot) Stat(p SafePath) (Stat, error) {
	q, e := localPathFor(p)
	if e != nil {
		return Stat{}, mapErrno("parse path", e)
	}
	st, e := r.backend.Stat(q)
	if e != nil {
		return Stat{}, mapLocalErr(e)
	}
	return fromLocalStat(st), nil
}
func (r *ShareRoot) ReadDirFunc(p SafePath, policy ReservedPolicy, fn func(DirEntry) bool) error {
	q, e := localPathFor(p)
	if e != nil {
		return mapErrno("parse path", e)
	}
	return mapLocalErr(r.backend.ReadDirFunc(q, local.ReservedPolicy(policy), func(e local.DirEntry) bool {
		return fn(DirEntry{Name: e.Name, Kind: fromLocalKind(e.Kind), Ino: e.Ino})
	}))
}
func (r *ShareRoot) ReadDir(p SafePath, policy ReservedPolicy) ([]DirEntry, error) {
	q, e := localPathFor(p)
	if e != nil {
		return nil, mapErrno("parse path", e)
	}
	entries, e := r.backend.ReadDir(q, local.ReservedPolicy(policy))
	if e != nil {
		return nil, mapLocalErr(e)
	}
	out := make([]DirEntry, 0, len(entries))
	for _, v := range entries {
		out = append(out, DirEntry{Name: v.Name, Kind: fromLocalKind(v.Kind), Ino: v.Ino})
	}
	return out, nil
}
func (r *ShareRoot) Space(p SafePath) (FsSpace, error) {
	q, e := localPathFor(p)
	if e != nil {
		return FsSpace{}, mapErrno("parse path", e)
	}
	s, e := r.backend.Space(q)
	if e != nil {
		return FsSpace{}, mapLocalErr(e)
	}
	return FsSpace{Total: s.Total, Free: s.Free, Available: s.Available}, nil
}
func (r *ShareRoot) DirDev(p SafePath) (uint64, error) {
	q, e := localPathFor(p)
	if e != nil {
		return 0, mapErrno("parse path", e)
	}
	v, e := r.backend.DirDev(q)
	return v, mapLocalErr(e)
}
