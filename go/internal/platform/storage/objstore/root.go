//go:build linux

package objstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/security/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	"github.com/stowcloud/storage"
	publics3 "github.com/stowcloud/storage/s3"
)

const objstoreDeviceTag = uint64(0x6f626a73_00000000)

func syntheticDevice(share vfs.ShareID) uint64 { return objstoreDeviceTag ^ uint64(share) }

type Options struct {
	Share      vfs.ShareID
	Config     Config
	Secret     secret.Secret
	ScratchDir string
	Policy     vfs.SharePolicy
	Logger     *slog.Logger
	Client     *http.Client
	Clock      clock.Clock
}

type partHandle struct{ scratch vfs.SafePath }

type Root struct {
	share   vfs.ShareID
	cfg     Config
	policy  vfs.SharePolicy
	scratch *vfs.ShareRoot
	backend *publics3.Root
	dev     uint64
	mu      sync.Mutex
	parts   map[string]partHandle
}

var _ vfs.Root = (*Root)(nil)

func Open(ctx context.Context, opt Options) (*Root, error) {
	scratch, err := vfs.OpenScratchRoot(opt.ScratchDir, opt.Policy)
	if err != nil {
		return nil, fmt.Errorf("objstore: open scratch root: %w", err)
	}
	backend, err := publics3.Open(ctx, publics3.Options{Config: opt.Config, Secret: string(opt.Secret.Reveal()), ScratchDir: opt.ScratchDir, Client: opt.Client})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("objstore: open %s: %w", opt.Config.Describe(), err), scratch.Close())
	}
	return &Root{share: opt.Share, cfg: opt.Config, policy: opt.Policy, scratch: scratch, backend: backend, dev: syntheticDevice(opt.Share), parts: make(map[string]partHandle)}, nil
}

func (r *Root) ID() vfs.ShareID                         { return r.share }
func (r *Root) Policy() vfs.SharePolicy                 { return r.policy }
func (r *Root) Dev() uint64                             { return r.dev }
func (r *Root) DirDev(vfs.SafePath) (uint64, error)     { return r.dev, nil }
func (r *Root) FsType() vfs.FsType                      { return 0 }
func (r *Root) HasBtime() bool                          { return false }
func (r *Root) IsScratch() bool                         { return false }
func (r *Root) Alive() error                            { return mapErr(r.backend.Alive(context.Background())) }
func (r *Root) Close() error                            { return errors.Join(r.backend.Close(), r.scratch.Close()) }
func (r *Root) Space(vfs.SafePath) (vfs.FsSpace, error) { return r.scratch.Space(vfs.RootPath()) }
func (r *Root) SetTimes(vfs.SafePath, int64) error      { return nil }
func (r *Root) ObjectKey(p vfs.SafePath) string         { return r.backend.ObjectKey(mustStoragePath(p)) }

func storagePath(p vfs.SafePath) (storage.Path, error) { return storage.ParsePath(p.String()) }
func mustStoragePath(p vfs.SafePath) storage.Path {
	q, err := storagePath(p)
	if err != nil {
		panic(fmt.Sprintf("objstore: SafePath conversion violated: %v", err))
	}
	return q
}
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, publics3.ErrNotFound):
		return fmt.Errorf("objstore: %w", vfs.ErrNotFound)
	case errors.Is(err, publics3.ErrDenied):
		return fmt.Errorf("objstore: %w", vfs.ErrDenied)
	case errors.Is(err, publics3.ErrExists):
		return fmt.Errorf("objstore: %w", vfs.ErrExists)
	case errors.Is(err, publics3.ErrNotEmpty):
		return fmt.Errorf("objstore: %w", vfs.ErrNotEmpty)
	default:
		return err
	}
}
func mintScratchPath() (vfs.SafePath, error) {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return vfs.SafePath{}, err
	}
	return vfs.RootPath().JoinControl(".scpart-" + hex.EncodeToString(suffix[:]))
}
func (r *Root) newScratchFile() (*vfs.File, vfs.SafePath, error) {
	p, err := mintScratchPath()
	if err != nil {
		return nil, vfs.SafePath{}, err
	}
	f, err := r.scratch.CreatePart(p)
	return f, p, err
}

func (r *Root) Stat(p vfs.SafePath) (vfs.Stat, error) {
	e, err := r.backend.Stat(context.Background(), mustStoragePath(p))
	if err != nil {
		return vfs.Stat{}, mapErr(err)
	}
	st := vfs.Stat{Dev: r.dev, Ino: syntheticIno(r.ObjectKey(p)), Nlink: 1}
	if !e.ModTime.IsZero() {
		st.MtimeNs = e.ModTime.UnixNano()
	}
	if e.Kind == storage.KindDirectory {
		st.Kind = vfs.KindDir
		st.Mode = r.policy.ModeDir
	} else {
		st.Kind = vfs.KindFile
		st.Mode = r.policy.ModeFile
		if e.Size >= 0 {
			st.Size = uint64(e.Size)
		}
	}
	return st, nil
}
func syntheticIno(key string) uint64 {
	var h uint64 = 14695981039346656037
	for i := range key {
		h ^= uint64(key[i])
		h *= 1099511628211
	}
	return h
}
func childObjectKey(parent string, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}
func (r *Root) ReadDir(p vfs.SafePath, policy vfs.ReservedPolicy) ([]vfs.DirEntry, error) {
	es, err := r.backend.ReadDir(context.Background(), mustStoragePath(p))
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]vfs.DirEntry, 0, len(es))
	parentKey := r.ObjectKey(p)
	for _, e := range es {
		name := e.Path.Name()
		if policy == vfs.HideReserved && vfs.IsReservedName(name) {
			continue
		}
		kind := vfs.KindFile
		if e.Kind == storage.KindDirectory {
			kind = vfs.KindDir
		}
		out = append(out, vfs.DirEntry{Name: name, Kind: kind, Ino: syntheticIno(childObjectKey(parentKey, name))})
	}
	if len(out) > limits.DirEntriesBuffered {
		return nil, limits.Exceed("directory entries buffered", limits.DirEntriesBuffered, int64(len(out)))
	}
	return out, nil
}
func (r *Root) ReadDirFunc(p vfs.SafePath, policy vfs.ReservedPolicy, fn func(vfs.DirEntry) bool) error {
	es, err := r.ReadDir(p, policy)
	if err != nil {
		return err
	}
	for _, e := range es {
		if !fn(e) {
			break
		}
	}
	return nil
}
func (r *Root) OpenRead(p vfs.SafePath, _ vfs.AccessIntent) (*vfs.File, error) {
	m, err := r.backend.Materialize(context.Background(), mustStoragePath(p))
	if err != nil {
		return nil, mapErr(err)
	}
	f, scratch, err := r.newScratchFile()
	if err != nil {
		return nil, errors.Join(err, m.Release())
	}
	if _, err := io.Copy(f.OSFile(), m); err != nil {
		return nil, errors.Join(err, f.Close(), r.scratch.Unlink(scratch), m.Release())
	}
	if err := m.Release(); err != nil {
		return nil, errors.Join(err, f.Close(), r.scratch.Unlink(scratch))
	}
	if err := r.scratch.Unlink(scratch); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}
func (r *Root) CreatePart(p vfs.SafePath) (*vfs.File, error) {
	f, scratch, err := r.newScratchFile()
	if err == nil {
		r.mu.Lock()
		r.parts[p.String()] = partHandle{scratch}
		r.mu.Unlock()
	}
	return f, err
}
func (r *Root) PublishPart(part, dest vfs.SafePath, replacing bool) (d vfs.Durable, retErr error) {
	r.mu.Lock()
	ph, ok := r.parts[part.String()]
	delete(r.parts, part.String())
	r.mu.Unlock()
	if !ok {
		return vfs.Durable{}, fmt.Errorf("publish part: %w", vfs.ErrNotFound)
	}
	f, err := r.scratch.OpenRead(ph.scratch, vfs.IntentRead)
	if err != nil {
		return vfs.Durable{}, errors.Join(err, r.scratch.Unlink(ph.scratch))
	}
	defer func() { retErr = errors.Join(retErr, f.Close(), r.scratch.Unlink(ph.scratch)) }()
	st, err := f.Stat()
	if err != nil {
		return vfs.Durable{}, err
	}
	if st.Size > math.MaxInt64 {
		return vfs.Durable{}, fmt.Errorf("publish part: size %d exceeds capability range", st.Size)
	}
	existed, statErr := r.backend.Stat(context.Background(), mustStoragePath(dest))
	if statErr != nil && !errors.Is(statErr, publics3.ErrNotFound) {
		return vfs.Durable{}, mapErr(statErr)
	}
	if statErr == nil && !replacing {
		return vfs.Durable{}, fmt.Errorf("publish part: %w", vfs.ErrExists)
	}
	size := int64(st.Size)
	if putErr := r.backend.PutObject(context.Background(), mustStoragePath(dest), io.NewSectionReader(f.OSFile(), 0, size), size); putErr != nil {
		return vfs.Durable{}, mapErr(putErr)
	}
	return vfs.Durable{Replaced: statErr == nil && existed.Kind != storage.KindOther}, nil
}
func (r *Root) WriteDurable(p vfs.SafePath, opt vfs.DurableOpts, write func(*vfs.File) error) (d vfs.Durable, retErr error) {
	f, scratch, err := r.newScratchFile()
	if err != nil {
		return vfs.Durable{}, err
	}
	defer func() { retErr = errors.Join(retErr, f.Close(), r.scratch.Unlink(scratch)) }()
	if writeErr := write(f); writeErr != nil {
		return vfs.Durable{}, writeErr
	}
	if syncErr := f.SyncData(); syncErr != nil {
		return vfs.Durable{}, syncErr
	}
	st, err := f.Stat()
	if err != nil {
		return vfs.Durable{}, err
	}
	if st.Size > math.MaxInt64 {
		return vfs.Durable{}, fmt.Errorf("write durable: size %d exceeds capability range", st.Size)
	}
	existing, statErr := r.backend.Stat(context.Background(), mustStoragePath(p))
	if statErr != nil && !errors.Is(statErr, publics3.ErrNotFound) {
		return vfs.Durable{}, mapErr(statErr)
	}
	if statErr == nil && opt.NoClobber {
		return vfs.Durable{}, fmt.Errorf("write durable: %w", vfs.ErrExists)
	}
	size := int64(st.Size)
	if putErr := r.backend.PutObject(context.Background(), mustStoragePath(p), io.NewSectionReader(f.OSFile(), 0, size), size); putErr != nil {
		return vfs.Durable{}, mapErr(putErr)
	}
	return vfs.Durable{Replaced: statErr == nil && existing.Kind != storage.KindOther}, nil
}
func (r *Root) Mkdir(p vfs.SafePath) error {
	return mapErr(r.backend.Mkdir(context.Background(), mustStoragePath(p)))
}
func (r *Root) Rmdir(p vfs.SafePath) error {
	return mapErr(r.backend.Rmdir(context.Background(), mustStoragePath(p)))
}
func (r *Root) Unlink(p vfs.SafePath) error {
	return mapErr(r.backend.Unlink(context.Background(), mustStoragePath(p)))
}
func (r *Root) Rename(from, to vfs.SafePath, noReplace bool) error {
	var err error
	if noReplace {
		err = r.backend.RenameNoReplace(context.Background(), mustStoragePath(from), mustStoragePath(to))
	} else {
		err = r.backend.Rename(context.Background(), mustStoragePath(from), mustStoragePath(to))
	}
	return mapErr(err)
}

// Product tests and callers should use the public backend API rather than these internals.
