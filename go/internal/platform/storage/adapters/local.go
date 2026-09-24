//go:build linux

package adapters

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"time"

	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	storage "github.com/stowcloud/storage"
	local "github.com/stowcloud/storage/local"
)

// Local is the neutral adapter. New code supplies local.Root directly. The
// vfs.Root case remains a narrow product bridge while callers migrate; all
// Linux mechanics still execute in local.RootHandle beneath ShareRoot.
type Local struct {
	localRoot local.Root
	vfsRoot   vfs.Root
}

func NewLocal(root any) (*Local, error) {
	if root == nil {
		return nil, fmt.Errorf("storage adapters: nil local root")
	}
	v := reflect.ValueOf(root)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return nil, fmt.Errorf("storage adapters: nil local root")
		}
	}
	switch r := root.(type) {
	case local.Root:
		return &Local{localRoot: r}, nil
	case vfs.Root:
		return &Local{vfsRoot: r}, nil
	default:
		return nil, fmt.Errorf("storage adapters: unsupported local root %T", root)
	}
}

var _ storage.ReadHierarchy = (*Local)(nil)
var _ storage.HealthChecker = (*Local)(nil)
var _ storage.SpaceReporter = (*Local)(nil)
var _ storage.Materializer = (*Local)(nil)
var _ storage.Renamer = (*Local)(nil)

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
func localPath(p storage.Path) (local.Path, error) { return local.ParsePath(p.String()) }
func vfsPath(p storage.Path) (vfs.SafePath, error) { return vfs.ParseSafePath(p.String()) }
func neutralKind(k local.Kind) storage.Kind {
	switch k {
	case local.KindFile:
		return storage.KindFile
	case local.KindDir:
		return storage.KindDirectory
	default:
		return storage.KindOther
	}
}
func entryFromStat(path storage.Path, st local.Stat) (storage.Entry, error) {
	size, err := checkedSize(st.Size)
	if err != nil {
		return storage.Entry{}, err
	}
	return storage.Entry{Path: path, Kind: neutralKind(st.Kind), Size: size, ModTime: time.Unix(0, st.MtimeNs).UTC()}, nil
}
func vfsEntry(path storage.Path, st vfs.Stat) (storage.Entry, error) {
	size, err := checkedSize(st.Size)
	if err != nil {
		return storage.Entry{}, err
	}
	k := storage.KindOther
	switch st.Kind {
	case vfs.KindFile:
		k = storage.KindFile
	case vfs.KindDir:
		k = storage.KindDirectory
	}
	return storage.Entry{Path: path, Kind: k, Size: size, ModTime: time.Unix(0, st.MtimeNs).UTC()}, nil
}

func (l *Local) OpenRead(ctx context.Context, path storage.Path) (io.ReadCloser, error) {
	if e := checkContext(ctx); e != nil {
		return nil, e
	}
	lease, _, e := l.openRead(path)
	if e != nil {
		return nil, e
	}
	return materializedReadCloser{lease: lease}, nil
}

func (l *Local) stat(path storage.Path) (local.Stat, error) {
	if l.localRoot != nil {
		p, e := localPath(path)
		if e != nil {
			return local.Stat{}, e
		}
		return l.localRoot.Stat(p)
	}
	p, e := vfsPath(path)
	if e != nil {
		return local.Stat{}, e
	}
	st, e := l.vfsRoot.Stat(p)
	if e != nil {
		return local.Stat{}, e
	}
	return local.Stat{Dev: st.Dev, Ino: st.Ino, BtimeNs: st.BtimeNs, MtimeNs: st.MtimeNs, CtimeNs: st.CtimeNs, Size: st.Size, Mode: st.Mode, UID: st.UID, GID: st.GID, Nlink: st.Nlink, Kind: local.Kind(st.Kind)}, nil
}
func (l *Local) readDir(path storage.Path) ([]local.DirEntry, error) {
	if l.localRoot != nil {
		p, e := localPath(path)
		if e != nil {
			return nil, e
		}
		return l.localRoot.ReadDir(p, local.HideReserved)
	}
	p, e := vfsPath(path)
	if e != nil {
		return nil, e
	}
	es, e := l.vfsRoot.ReadDir(p, vfs.HideReserved)
	if e != nil {
		return nil, e
	}
	out := make([]local.DirEntry, 0, len(es))
	for _, x := range es {
		out = append(out, local.DirEntry{Name: x.Name, Kind: local.Kind(x.Kind), Ino: x.Ino})
	}
	return out, nil
}
func checkedSize(size uint64) (int64, error) {
	if size > math.MaxInt64 {
		return 0, fmt.Errorf("storage adapters: entry size %d exceeds capability range", size)
	}
	return int64(size), nil
}

func (l *Local) openRead(path storage.Path) (*storage.Materialized, local.Stat, error) {
	if l.localRoot != nil {
		p, e := localPath(path)
		if e != nil {
			return nil, local.Stat{}, e
		}
		f, e := l.localRoot.OpenRead(p, local.IntentRead)
		if e != nil {
			return nil, local.Stat{}, e
		}
		st, e := f.Stat()
		if e != nil {
			return nil, local.Stat{}, errors.Join(e, f.Close())
		}
		size, e := checkedSize(st.Size)
		if e != nil {
			return nil, local.Stat{}, errors.Join(e, f.Close())
		}
		return storage.NewMaterialized(f.OSFile(), size, f.Close), st, nil
	}
	p, e := vfsPath(path)
	if e != nil {
		return nil, local.Stat{}, e
	}
	f, e := l.vfsRoot.OpenRead(p, vfs.IntentRead)
	if e != nil {
		return nil, local.Stat{}, e
	}
	st, e := f.Stat()
	if e != nil {
		return nil, local.Stat{}, errors.Join(e, f.Close())
	}
	size, e := checkedSize(st.Size)
	if e != nil {
		return nil, local.Stat{}, errors.Join(e, f.Close())
	}
	return storage.NewMaterialized(f.OSFile(), size, f.Close), local.Stat{Dev: st.Dev, Ino: st.Ino, BtimeNs: st.BtimeNs, MtimeNs: st.MtimeNs, CtimeNs: st.CtimeNs, Size: st.Size, Mode: st.Mode, UID: st.UID, GID: st.GID, Nlink: st.Nlink, Kind: local.Kind(st.Kind)}, nil
}

type materializedReadCloser struct{ lease *storage.Materialized }

func (r materializedReadCloser) Read(p []byte) (int, error) { return r.lease.Read(p) }
func (r materializedReadCloser) Close() error               { return r.lease.Release() }

func (l *Local) Stat(ctx context.Context, path storage.Path) (storage.Entry, error) {
	if e := checkContext(ctx); e != nil {
		return storage.Entry{}, e
	}
	if l.localRoot != nil {
		st, e := l.stat(path)
		if e != nil {
			return storage.Entry{}, e
		}
		return entryFromStat(path, st)
	}
	p, e := vfsPath(path)
	if e != nil {
		return storage.Entry{}, e
	}
	st, e := l.vfsRoot.Stat(p)
	if e != nil {
		return storage.Entry{}, e
	}
	return vfsEntry(path, st)
}
func (l *Local) ReadDir(ctx context.Context, path storage.Path) ([]storage.Entry, error) {
	if e := checkContext(ctx); e != nil {
		return nil, e
	}
	es, e := l.readDir(path)
	if e != nil {
		return nil, e
	}
	out := make([]storage.Entry, 0, len(es))
	for _, de := range es {
		child, e := path.Join(de.Name)
		if e != nil {
			continue
		}
		st, e := l.stat(child)
		if e != nil {
			return nil, e
		}
		entry, e := entryFromStat(child, st)
		if e != nil {
			return nil, e
		}
		out = append(out, entry)
	}
	return out, nil
}
func (l *Local) Rename(ctx context.Context, from, to storage.Path) error {
	if e := checkContext(ctx); e != nil {
		return e
	}
	if l.localRoot != nil {
		f, e := localPath(from)
		if e != nil {
			return e
		}
		t, e := localPath(to)
		if e != nil {
			return e
		}
		return l.localRoot.Rename(f, t, false)
	}
	f, e := vfsPath(from)
	if e != nil {
		return e
	}
	t, e := vfsPath(to)
	if e != nil {
		return e
	}
	return l.vfsRoot.Rename(f, t, false)
}
func (l *Local) Space(ctx context.Context, path storage.Path) (storage.Space, error) {
	if e := checkContext(ctx); e != nil {
		return storage.Space{}, e
	}
	if l.localRoot != nil {
		p, e := localPath(path)
		if e != nil {
			return storage.Space{}, e
		}
		s, e := l.localRoot.Space(p)
		if e != nil {
			return storage.Space{}, e
		}
		return storage.Space{Total: s.Total, Free: s.Available}, nil
	}
	p, e := vfsPath(path)
	if e != nil {
		return storage.Space{}, e
	}
	s, e := l.vfsRoot.Space(p)
	if e != nil {
		return storage.Space{}, e
	}
	return storage.Space{Total: s.Total, Free: s.Available}, nil
}
func (l *Local) Health(ctx context.Context) storage.Health {
	if e := checkContext(ctx); e != nil {
		return storage.Health{Status: storage.HealthFailing, Err: e}
	}
	var e error
	if l.localRoot != nil {
		e = l.localRoot.Alive()
	} else {
		e = l.vfsRoot.Alive()
	}
	if e != nil {
		return storage.Health{Status: storage.HealthFailing, Err: e}
	}
	return storage.Health{Status: storage.HealthOK}
}
func (l *Local) Materialize(ctx context.Context, path storage.Path) (*storage.Materialized, error) {
	lease, _, e := l.openRead(path)
	return lease, e
}
func (l *Local) MaterializeWithStat(ctx context.Context, path storage.Path) (*storage.Materialized, vfs.Stat, error) {
	lease, st, e := l.openRead(path)
	if e != nil {
		return nil, vfs.Stat{}, e
	}
	return lease, vfs.Stat{Dev: st.Dev, Ino: st.Ino, BtimeNs: st.BtimeNs, MtimeNs: st.MtimeNs, CtimeNs: st.CtimeNs, Size: st.Size, Mode: st.Mode, UID: st.UID, GID: st.GID, Nlink: st.Nlink, Kind: vfs.Kind(st.Kind)}, nil
}
