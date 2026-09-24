//go:build linux

package vault

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/security/secret"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
	storage "github.com/stowcloud/storage"
	publicvault "github.com/stowcloud/storage/veracrypt"
)

// Config and configuration validation belong to the public backend. The alias
// keeps the product's persisted shape source-compatible while ensuring there is
// one trust-boundary implementation.
type Config = publicvault.Config

const (
	minContainerDataMiB = publicvault.MinContainerDataMiB
	maxContainerDataMiB = publicvault.MaxContainerDataMiB
)

var (
	ErrContainerSize         = publicvault.ErrContainerSize
	ErrUnknownHash           = publicvault.ErrUnknownHash
	ErrWrongPassword         = publicvault.ErrWrongPassword
	ErrHeaderCorrupt         = publicvault.ErrHeaderCorrupt
	ErrUnsupportedVolume     = publicvault.ErrUnsupportedVolume
	ErrHeaderFieldsInvalid   = publicvault.ErrHeaderFieldsInvalid
	ErrUnsupportedFilesystem = publicvault.ErrUnsupportedFilesystem
)

func ParseConfig(b []byte) (Config, error) { return publicvault.ParseConfig(b) }

// Options retains product policy and identity. The backend receives only the
// neutral config, credential bytes and scratch staging directory.
type Options struct {
	Share      vfs.ShareID
	Config     Config
	Password   secret.Secret
	Create     bool
	ScratchDir string
	Policy     vfs.SharePolicy
	Logger     *slog.Logger
	Clock      clock.Clock
}

type Root struct {
	id           vfs.ShareID
	backend      *publicvault.Root
	scratch      *vfs.ShareRoot
	policy       vfs.SharePolicy
	logger       *slog.Logger
	clk          clock.Clock
	syntheticDev uint64

	partsMu sync.Mutex
	parts   map[string]vfs.SafePath
}

var _ vfs.Root = (*Root)(nil)

func Open(ctx context.Context, opt Options) (*Root, error) {
	if opt.Config.Container == "" {
		return nil, fmt.Errorf("vault: container path is required")
	}
	if opt.ScratchDir == "" {
		return nil, fmt.Errorf("vault: scratch dir is required")
	}
	logger := opt.Logger
	if logger == nil {
		logger = slog.Default()
	}
	clk := opt.Clock
	if clk == nil {
		clk = clock.System()
	}
	backend, err := publicvault.Open(ctx, publicvault.Options{
		Config: opt.Config, Password: opt.Password.Reveal(), Create: opt.Create,
		ScratchDir: opt.ScratchDir, Clock: clk,
	})
	if err != nil {
		return nil, mapPublicError(err)
	}
	scratch, err := vfs.OpenScratchRoot(opt.ScratchDir, opt.Policy)
	if err != nil {
		return nil, errors.Join(err, backend.Close())
	}
	logger.Info("vault: opened container", "share", opt.Share, "container", opt.Config.Container)
	return &Root{
		id: opt.Share, backend: backend, scratch: scratch, policy: opt.Policy,
		logger: logger, clk: clk, syntheticDev: backend.Device(), parts: map[string]vfs.SafePath{},
	}, nil
}

func mapPublicError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, publicvault.ErrNotFound):
		return fmt.Errorf("%w: %v", vfs.ErrNotFound, err)
	case errors.Is(err, publicvault.ErrDenied):
		return fmt.Errorf("%w: %v", vfs.ErrDenied, err)
	case errors.Is(err, publicvault.ErrExists):
		return fmt.Errorf("%w: %v", vfs.ErrExists, err)
	case errors.Is(err, publicvault.ErrNotEmpty):
		return fmt.Errorf("%w: %v", vfs.ErrNotEmpty, err)
	case errors.Is(err, publicvault.ErrNoSpace):
		return fmt.Errorf("%w: %v", vfs.ErrNoSpace, err)
	case errors.Is(err, publicvault.ErrNotADirectory):
		return fmt.Errorf("%w: %v", vfs.ErrNotADirectory, err)
	case errors.Is(err, publicvault.ErrIsDirectory):
		return fmt.Errorf("%w: %v", vfs.ErrIsDirectory, err)
	default:
		return err
	}
}

func publicPath(p vfs.SafePath) (storage.Path, error) { return storage.ParsePath(p.String()) }

func (r *Root) ID() vfs.ShareID { return r.id }
func (r *Root) Stat(p vfs.SafePath) (vfs.Stat, error) {
	q, err := publicPath(p)
	if err != nil {
		return vfs.Stat{}, err
	}
	st, err := r.backend.Stat(context.Background(), q)
	if err != nil {
		return vfs.Stat{}, mapPublicError(err)
	}
	return r.toVfsStat(st), nil
}
func (r *Root) toVfsStat(st publicvault.StatInfo) vfs.Stat {
	kind := vfs.KindFile
	mode := uint32(0o100000) | r.policy.ModeFile
	nlink := uint32(1)
	if st.IsDir {
		kind = vfs.KindDir
		mode = uint32(0o040000) | r.policy.ModeDir
		nlink = 2
	}
	var uid, gid uint32
	if r.policy.Chown != nil {
		uid, gid = r.policy.Chown.UID, r.policy.Chown.GID
	}
	return vfs.Stat{Dev: r.syntheticDev, Ino: st.Ino, MtimeNs: st.MtimeNs, Size: st.Size, Mode: mode, UID: uid, GID: gid, Nlink: nlink, Kind: kind}
}
func (r *Root) ReadDir(p vfs.SafePath, policy vfs.ReservedPolicy) ([]vfs.DirEntry, error) {
	q, err := publicPath(p)
	if err != nil {
		return nil, err
	}
	entries, err := r.backend.ReadDir(context.Background(), q)
	if err != nil {
		return nil, mapPublicError(err)
	}
	out := make([]vfs.DirEntry, 0, len(entries))
	for _, e := range entries {
		if policy == vfs.HideReserved && vfs.IsReservedName(e.Name) {
			continue
		}
		kind := vfs.KindFile
		if e.IsDir {
			kind = vfs.KindDir
		}
		out = append(out, vfs.DirEntry{Name: e.Name, Kind: kind, Ino: e.Ino})
	}
	return out, nil
}
func (r *Root) ReadDirFunc(p vfs.SafePath, policy vfs.ReservedPolicy, fn func(vfs.DirEntry) bool) error {
	entries, err := r.ReadDir(p, policy)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !fn(e) {
			break
		}
	}
	return nil
}
func mintScratchName() (vfs.SafePath, error) {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return vfs.SafePath{}, fmt.Errorf("vault: read system randomness: %w", err)
	}
	return vfs.RootPath().JoinControl(".scpart-" + hex.EncodeToString(suffix[:]))
}

func (r *Root) OpenRead(p vfs.SafePath, _ vfs.AccessIntent) (*vfs.File, error) {
	q, err := publicPath(p)
	if err != nil {
		return nil, err
	}
	m, err := r.backend.Materialize(context.Background(), q)
	if err != nil {
		return nil, mapPublicError(err)
	}
	sp, err := mintScratchName()
	if err != nil {
		return nil, errors.Join(err, m.Release())
	}
	f, err := r.scratch.CreatePart(sp)
	if err != nil {
		return nil, errors.Join(err, m.Release())
	}
	_, copyErr := io.Copy(f.OSFile(), m)
	releaseErr := m.Release()
	if copyErr != nil || releaseErr != nil {
		cleanupErr := errors.Join(f.Close(), r.scratch.Unlink(sp))
		return nil, errors.Join(copyErr, releaseErr, cleanupErr)
	}
	if err := r.scratch.Unlink(sp); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	if _, err := f.OSFile().Seek(0, io.SeekStart); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

type fileReader struct {
	f   *vfs.File
	off int64
}

func (r *fileReader) Read(p []byte) (int, error) {
	n, err := r.f.ReadAt(p, r.off)
	r.off += int64(n)
	return n, err
}

func (r *Root) CreatePart(p vfs.SafePath) (*vfs.File, error) {
	sp, err := mintScratchName()
	if err != nil {
		return nil, err
	}
	f, err := r.scratch.CreatePart(sp)
	if err != nil {
		return nil, err
	}
	r.partsMu.Lock()
	r.parts[p.String()] = sp
	r.partsMu.Unlock()
	return f, nil
}
func (r *Root) takeScratchPart(p vfs.SafePath) (vfs.SafePath, bool) {
	r.partsMu.Lock()
	defer r.partsMu.Unlock()
	sp, ok := r.parts[p.String()]
	if ok {
		delete(r.parts, p.String())
	}
	return sp, ok
}
func (r *Root) WriteDurable(p vfs.SafePath, opt vfs.DurableOpts, write func(*vfs.File) error) (durable vfs.Durable, retErr error) {
	sp, err := mintScratchName()
	if err != nil {
		return vfs.Durable{}, err
	}
	f, err := r.scratch.CreatePart(sp)
	if err != nil {
		return vfs.Durable{}, err
	}
	defer func() {
		retErr = errors.Join(retErr, f.Close(), r.scratch.Unlink(sp))
	}()
	if writeErr := write(f); writeErr != nil {
		return vfs.Durable{}, writeErr
	}
	if syncErr := f.SyncData(); syncErr != nil {
		return vfs.Durable{}, syncErr
	}
	if _, seekErr := f.OSFile().Seek(0, io.SeekStart); seekErr != nil {
		return vfs.Durable{}, seekErr
	}
	q, err := publicPath(p)
	if err != nil {
		return vfs.Durable{}, err
	}
	backendDurable, err := r.backend.WriteDurable(context.Background(), q, publicvault.DurableOptions{NoClobber: opt.NoClobber, MtimeNs: r.clk.Nanos()}, &fileReader{f: f})
	if err != nil {
		return vfs.Durable{}, mapPublicError(err)
	}
	return vfs.Durable{Replaced: backendDurable.Replaced}, nil
}
func (r *Root) PublishPart(part, dest vfs.SafePath, replacing bool) (durable vfs.Durable, retErr error) {
	sp, ok := r.takeScratchPart(part)
	if !ok {
		return vfs.Durable{}, fmt.Errorf("publish part %q: %w", part.String(), vfs.ErrNotFound)
	}
	f, err := r.scratch.OpenRead(sp, vfs.IntentRead)
	if err != nil {
		return vfs.Durable{}, err
	}
	defer func() {
		retErr = errors.Join(retErr, f.Close(), r.scratch.Unlink(sp))
	}()
	if _, seekErr := f.OSFile().Seek(0, io.SeekStart); seekErr != nil {
		return vfs.Durable{}, seekErr
	}
	q, err := publicPath(dest)
	if err != nil {
		return vfs.Durable{}, err
	}
	backendDurable, err := r.backend.WriteDurable(context.Background(), q, publicvault.DurableOptions{NoClobber: !replacing, MtimeNs: r.clk.Nanos()}, &fileReader{f: f})
	if err != nil {
		return vfs.Durable{}, mapPublicError(err)
	}
	return vfs.Durable{Replaced: backendDurable.Replaced}, nil
}
func (r *Root) SetTimes(p vfs.SafePath, mtimeNs int64) error {
	q, err := publicPath(p)
	if err != nil {
		return err
	}
	return mapPublicError(r.backend.SetTimes(context.Background(), q, mtimeNs))
}
func (r *Root) Mkdir(p vfs.SafePath) error {
	q, err := publicPath(p)
	if err != nil {
		return err
	}
	return mapPublicError(r.backend.Mkdir(context.Background(), q))
}
func (r *Root) Rmdir(p vfs.SafePath) error {
	q, err := publicPath(p)
	if err != nil {
		return err
	}
	return mapPublicError(r.backend.Rmdir(context.Background(), q))
}
func (r *Root) Unlink(p vfs.SafePath) error {
	q, err := publicPath(p)
	if err != nil {
		return err
	}
	return mapPublicError(r.backend.Unlink(context.Background(), q))
}
func (r *Root) Rename(from, to vfs.SafePath, noReplace bool) error {
	a, err := publicPath(from)
	if err != nil {
		return err
	}
	b, err := publicPath(to)
	if err != nil {
		return err
	}
	if noReplace {
		return mapPublicError(r.backend.RenameNoReplace(context.Background(), a, b))
	}
	return mapPublicError(r.backend.Rename(context.Background(), a, b))
}
func (r *Root) Space(p vfs.SafePath) (vfs.FsSpace, error) {
	q, err := publicPath(p)
	if err != nil {
		return vfs.FsSpace{}, err
	}
	s, err := r.backend.Space(context.Background(), q)
	if err != nil {
		return vfs.FsSpace{}, mapPublicError(err)
	}
	return vfs.FsSpace{Total: s.Total, Free: s.Free, Available: s.Free}, nil
}
func (r *Root) DirDev(vfs.SafePath) (uint64, error) { return r.syntheticDev, nil }
func (r *Root) Policy() vfs.SharePolicy             { return r.policy }
func (r *Root) Dev() uint64                         { return r.syntheticDev }
func (r *Root) FsType() vfs.FsType                  { return vfs.FsType(0) }
func (r *Root) HasBtime() bool                      { return false }
func (r *Root) IsScratch() bool                     { return false }
func (r *Root) Alive() error                        { return mapPublicError(r.backend.Alive()) }
func (r *Root) Close() error                        { return errors.Join(r.backend.Close(), r.scratch.Close()) }
