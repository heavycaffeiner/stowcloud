//go:build linux

// Package adapters contains translations from the product storage backends to
// the small backend-neutral capability contracts.
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
)

// Local adapts an existing vfs.Root without changing the root's product-facing
// API or its path-resolution policy. Paths are parsed by vfs at this boundary,
// so all operations remain confined by the root's existing resolver.
type Local struct {
	root vfs.Root
}

// NewLocal wraps root in the neutral capability contracts.
func NewLocal(root vfs.Root) (*Local, error) {
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
	return &Local{root: root}, nil
}

var _ storage.ReadHierarchy = (*Local)(nil)
var _ storage.HealthChecker = (*Local)(nil)
var _ storage.SpaceReporter = (*Local)(nil)
var _ storage.Materializer = (*Local)(nil)
var _ storage.Renamer = (*Local)(nil)

func (l *Local) safePath(p storage.Path) (vfs.SafePath, error) {
	return vfs.ParseSafePath(p.String())
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func neutralKind(k vfs.Kind) storage.Kind {
	switch k {
	case vfs.KindFile:
		return storage.KindFile
	case vfs.KindDir:
		return storage.KindDirectory
	default:
		// Inode, device, mode and symlink details are intentionally not part
		// of the neutral Entry contract.
		return storage.KindOther
	}
}

func entryFromStat(path storage.Path, st vfs.Stat) (storage.Entry, error) {
	if st.Size > math.MaxInt64 {
		return storage.Entry{}, fmt.Errorf("storage adapters: entry size %d exceeds capability range", st.Size)
	}
	return storage.Entry{
		Path:    path,
		Kind:    neutralKind(st.Kind),
		Size:    int64(st.Size),
		ModTime: time.Unix(0, st.MtimeNs).UTC(),
	}, nil
}

func (l *Local) Stat(ctx context.Context, path storage.Path) (storage.Entry, error) {
	if err := checkContext(ctx); err != nil {
		return storage.Entry{}, err
	}
	p, err := l.safePath(path)
	if err != nil {
		return storage.Entry{}, err
	}
	st, err := l.root.Stat(p)
	if err != nil {
		return storage.Entry{}, err
	}
	return entryFromStat(path, st)
}

func (l *Local) ReadDir(ctx context.Context, path storage.Path) ([]storage.Entry, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	p, err := l.safePath(path)
	if err != nil {
		return nil, err
	}
	entries, err := l.root.ReadDir(p, vfs.HideReserved)
	if err != nil {
		return nil, err
	}
	out := make([]storage.Entry, 0, len(entries))
	for _, dirEntry := range entries {
		child, err := path.Join(dirEntry.Name)
		if err != nil {
			// A POSIX directory may contain names that a portable path cannot
			// represent. The product VFS still lists them by escaped wire name.
			continue
		}
		if dirEntry.Kind != vfs.KindFile && dirEntry.Kind != vfs.KindDir {
			out = append(out, storage.Entry{Path: child, Kind: neutralKind(dirEntry.Kind)})
			continue
		}
		childSafe, err := l.safePath(child)
		if err != nil {
			return nil, err
		}
		st, err := l.root.Stat(childSafe)
		if err != nil {
			return nil, err
		}
		entry, err := entryFromStat(child, st)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

func (l *Local) OpenRead(ctx context.Context, path storage.Path) (io.ReadCloser, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	p, err := l.safePath(path)
	if err != nil {
		return nil, err
	}
	f, err := l.root.OpenRead(p, vfs.IntentRead)
	if err != nil {
		return nil, err
	}
	return f.OSFile(), nil
}

// Rename exposes the local root's rename operation. The neutral contract does
// not promise atomicity, while this backend provides a stronger in-filesystem
// rename; cross-device failures are returned rather than silently emulated.
func (l *Local) Rename(ctx context.Context, from, to storage.Path) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	fromPath, err := l.safePath(from)
	if err != nil {
		return err
	}
	toPath, err := l.safePath(to)
	if err != nil {
		return err
	}
	return l.root.Rename(fromPath, toPath, false)
}

func (l *Local) Space(ctx context.Context, path storage.Path) (storage.Space, error) {
	if err := checkContext(ctx); err != nil {
		return storage.Space{}, err
	}
	p, err := l.safePath(path)
	if err != nil {
		return storage.Space{}, err
	}
	space, err := l.root.Space(p)
	if err != nil {
		return storage.Space{}, err
	}
	return storage.Space{Total: space.Total, Free: space.Available}, nil
}

func (l *Local) Health(ctx context.Context) storage.Health {
	if err := checkContext(ctx); err != nil {
		return storage.Health{Status: storage.HealthFailing, Err: err}
	}
	if err := l.root.Alive(); err != nil {
		return storage.Health{Status: storage.HealthFailing, Err: err}
	}
	return storage.Health{Status: storage.HealthOK}
}

func (l *Local) Materialize(ctx context.Context, path storage.Path) (*storage.Materialized, error) {
	lease, _, err := l.materializeWithStat(ctx, path)
	return lease, err
}

// MaterializeWithStat opens once and returns metadata from that same handle.
// Callers serving bytes and validators must not re-stat the path: a rename
// between two path resolutions can otherwise pair one file's bytes with
// another file's identity.
func (l *Local) MaterializeWithStat(ctx context.Context, path storage.Path) (*storage.Materialized, vfs.Stat, error) {
	return l.materializeWithStat(ctx, path)
}

func (l *Local) materializeWithStat(ctx context.Context, path storage.Path) (*storage.Materialized, vfs.Stat, error) {
	if err := checkContext(ctx); err != nil {
		return nil, vfs.Stat{}, err
	}
	p, err := l.safePath(path)
	if err != nil {
		return nil, vfs.Stat{}, err
	}
	f, err := l.root.OpenRead(p, vfs.IntentRead)
	if err != nil {
		return nil, vfs.Stat{}, err
	}
	st, err := f.Stat()
	if err != nil {
		return nil, vfs.Stat{}, errors.Join(err, f.Close())
	}
	if st.Size > math.MaxInt64 {
		if closeErr := f.Close(); closeErr != nil {
			return nil, vfs.Stat{}, fmt.Errorf("storage adapters: entry size %d exceeds capability range; closing file: %w", st.Size, closeErr)
		}
		return nil, vfs.Stat{}, fmt.Errorf("storage adapters: entry size %d exceeds capability range", st.Size)
	}
	return storage.NewMaterialized(f.OSFile(), int64(st.Size), f.Close), st, nil
}
