//go:build linux

package vault

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/clock"
	"github.com/heavycaffeiner/stowcloud/go/engine/kit/secret"
)

// Config is a veracrypt share's configuration, persisted verbatim alongside
// the share definition. It carries no secret: the container password lives
// in Options.Password, sealed at rest the same way the OIDC client secret
// is.
type Config struct {
	Container     string `json:"container"`
	CreateSizeMiB uint64 `json:"create_size_mib"`
	// PIM is VeraCrypt's Personal Iterations Multiplier. Zero means the
	// container was created without one, which is the default. It is not a
	// secret: it changes the iteration count, and a wrong one is
	// indistinguishable from a wrong passphrase.
	PIM uint32 `json:"pim,omitempty"`
	// Hash names the container's key derivation, spelled as VeraCrypt's own
	// command line spells it. Empty tries all of them, which is correct but
	// slow: a container using the last one pays for every earlier
	// derivation first, and two of those run 500000 iterations of a
	// software hash.
	Hash string `json:"hash,omitempty"`
}

// maxPIM bounds the multiplier a stored config may carry. VeraCrypt's own
// dialog stops well below this; the ceiling is here because the value
// becomes an iteration count and an Argon2id pass count, and both come
// from a database column this package does not own.
const maxPIM = 10_000

// maxConfigBytes and maxContainerPathBytes bound a config blob before this
// driver touches it: the blob arrived as a JSON column in the store, which
// is untrusted input the moment it did not come from this package's own
// Marshal.
const (
	maxConfigBytes        = 16 << 10
	maxContainerPathBytes = 4096
)

// ParseConfig is the trust boundary for a veracrypt config blob: every
// field is bounded and validated before anything downstream treats it as a
// filesystem path or an allocation size.
func ParseConfig(b []byte) (Config, error) {
	if len(b) == 0 {
		return Config{}, fmt.Errorf("vault: empty config")
	}
	if len(b) > maxConfigBytes {
		return Config{}, fmt.Errorf("vault: config exceeds %d bytes", maxConfigBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("vault: parse config: %w", err)
	}
	if c.Container == "" {
		return Config{}, fmt.Errorf("vault: container path is required")
	}
	if len(c.Container) > maxContainerPathBytes {
		return Config{}, fmt.Errorf("vault: container path exceeds %d bytes", maxContainerPathBytes)
	}
	if !filepath.IsAbs(c.Container) {
		return Config{}, fmt.Errorf("vault: container path %q must be absolute", c.Container)
	}
	if bytes.ContainsRune([]byte(c.Container), 0) {
		return Config{}, fmt.Errorf("vault: container path contains a NUL byte")
	}
	if c.CreateSizeMiB != 0 && (c.CreateSizeMiB < minContainerDataMiB || c.CreateSizeMiB > maxContainerDataMiB) {
		return Config{}, ErrContainerSize
	}
	if c.PIM > maxPIM {
		return Config{}, fmt.Errorf("vault: PIM %d exceeds %d", c.PIM, maxPIM)
	}
	if _, err := headerKDFsFor(c.Hash); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Marshal is ParseConfig's inverse, what core persists as backend_config.
func (c Config) Marshal() ([]byte, error) {
	return json.Marshal(c)
}

// Describe is the redacted, human-readable location core stores in
// ShareDef.Source: the container path, and nothing about the password.
func (c Config) Describe() string {
	return c.Container
}

// Options is everything Open needs to bring up one veracrypt share.
type Options struct {
	Share      vfs.ShareID
	Config     Config
	Password   secret.Secret
	Create     bool
	ScratchDir string
	Policy     vfs.SharePolicy
	Logger     *slog.Logger

	// Clock stamps a directory's own "." and ".." entries and a freshly
	// created file's initial write time. Nil takes the system clock.
	Clock clock.Clock
}

// Root serves one VeraCrypt container as a vfs.Root. Every method takes
// fs's single mutex for its whole body by calling into FS, whose own
// documentation carries the reason: a FAT filesystem has no concurrency
// story, so this driver never lets two operations touch it at once.
type Root struct {
	id           vfs.ShareID
	container    string
	dev          *volumeDevice
	fs           filesystem
	scratch      *vfs.ShareRoot
	policy       vfs.SharePolicy
	logger       *slog.Logger
	clk          clock.Clock
	syntheticDev uint64

	partsMu sync.Mutex
	parts   map[string]vfs.SafePath
}

var _ vfs.Root = (*Root)(nil)

// Open brings up opt.Config.Container as a Root: creating and formatting it
// first when it does not exist and opt.Create is set, then decrypting its
// header with opt.Password and mounting the FAT filesystem inside it.
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

	_, statErr := os.Stat(opt.Config.Container)
	missing := errors.Is(statErr, os.ErrNotExist)
	switch {
	case statErr == nil:
		// Already there; openContainer below opens it as is.
	case missing && opt.Create:
		sizeMiB := opt.Config.CreateSizeMiB
		if sizeMiB == 0 {
			sizeMiB = minContainerDataMiB
		}
		if err := createContainer(opt.Config.Container, sizeMiB, opt.Password); err != nil {
			return nil, err
		}
	case missing:
		return nil, fmt.Errorf("vault: container %q: %w", opt.Config.Container, vfs.ErrNotFound)
	default:
		return nil, fmt.Errorf("vault: stat container %q: %w", opt.Config.Container, statErr)
	}

	dev, dataSize, err := openContainer(opt.Config.Container, opt.Password, opt.Config.PIM, opt.Config.Hash)
	if err != nil {
		return nil, err
	}
	closeDev := true
	defer func() {
		if closeDev {
			if cerr := dev.f.Close(); cerr != nil {
				logger.Warn("vault: closing container after a failed open", "error", cerr)
			}
		}
	}()

	if missing && opt.Create {
		if ferr := Format(dev, dataSize); ferr != nil {
			return nil, fmt.Errorf("vault: format new container: %w", ferr)
		}
	}
	fsys, err := mountFilesystem(dev, dataSize, clk)
	if err != nil {
		return nil, err
	}
	scratch, err := vfs.OpenScratchRoot(opt.ScratchDir, opt.Policy)
	if err != nil {
		return nil, err
	}

	closeDev = false
	logger.Info("vault: opened container", "share", opt.Share, "container", opt.Config.Container)
	return &Root{
		id:           opt.Share,
		container:    opt.Config.Container,
		dev:          dev,
		fs:           fsys,
		scratch:      scratch,
		policy:       opt.Policy,
		logger:       logger,
		clk:          clk,
		syntheticDev: syntheticDevFor(opt.Config.Container),
		parts:        map[string]vfs.SafePath{},
	}, nil
}

// syntheticDevFor derives a stable device number from the container path, so
// two entries the domain sees from the same container always agree on Dev
// even across a process restart, without colliding with a real device
// number: the high bit is always set, which no real dev_t on this platform
// carries.
func syntheticDevFor(container string) uint64 {
	h := fnv.New64a()
	if _, err := h.Write([]byte(container)); err != nil {
		panicInfallibleWrite(err)
	}
	return h.Sum64() | (1 << 63)
}

// mintScratchName picks a fresh, unique control name in the scratch root's
// own namespace, unrelated to any name in the FAT tree this Root serves.
func mintScratchName() (vfs.SafePath, error) {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return vfs.SafePath{}, fmt.Errorf("vault: read system randomness: %w", err)
	}
	return vfs.RootPath().JoinControl(".scpart-" + hex.EncodeToString(suffix[:]))
}

func (r *Root) ID() vfs.ShareID { return r.id }

func (r *Root) Stat(p vfs.SafePath) (vfs.Stat, error) {
	info, err := r.fs.Stat(p)
	if err != nil {
		return vfs.Stat{}, err
	}
	return r.toVfsStat(info), nil
}

func (r *Root) toVfsStat(info StatInfo) vfs.Stat {
	kind := vfs.KindFile
	mode := uint32(0o100000) | r.policy.ModeFile
	nlink := uint32(1)
	if info.IsDir {
		kind = vfs.KindDir
		mode = uint32(0o040000) | r.policy.ModeDir
		nlink = 2
	}
	var uid, gid uint32
	if r.policy.Chown != nil {
		uid, gid = r.policy.Chown.UID, r.policy.Chown.GID
	}
	return vfs.Stat{
		Dev:     r.syntheticDev,
		Ino:     info.Ino,
		BtimeNs: nil,
		MtimeNs: info.MtimeNs,
		CtimeNs: nil,
		Size:    info.Size,
		Mode:    mode,
		UID:     uid,
		GID:     gid,
		Nlink:   nlink,
		Kind:    kind,
	}
}

func (r *Root) ReadDir(p vfs.SafePath, policy vfs.ReservedPolicy) ([]vfs.DirEntry, error) {
	entries, err := r.fs.ReadDir(p)
	if err != nil {
		return nil, err
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
			return nil
		}
	}
	return nil
}

// OpenRead copies p's whole content into scratch space and returns a
// descriptor onto that, unlinking the scratch name immediately so the
// returned descriptor is the only reference: the FAT filesystem this
// content actually lives on has no descriptor-based access of its own to
// hand out.
func (r *Root) OpenRead(p vfs.SafePath, intent vfs.AccessIntent) (*vfs.File, error) {
	scratchPath, err := mintScratchName()
	if err != nil {
		return nil, err
	}
	f, err := r.scratch.CreatePart(scratchPath)
	if err != nil {
		return nil, err
	}
	if err := r.fs.ReadFile(p, &fileWriterAt{f: f}); err != nil {
		if cerr := f.Close(); cerr != nil {
			r.logger.Warn("vault: closing a materialized read after a failed copy", "error", cerr)
		}
		if uerr := r.scratch.Unlink(scratchPath); uerr != nil {
			r.logger.Warn("vault: unlinking a materialized read after a failed copy", "error", uerr)
		}
		return nil, err
	}
	if err := r.scratch.Unlink(scratchPath); err != nil {
		if cerr := f.Close(); cerr != nil {
			r.logger.Warn("vault: closing a materialized read after a failed unlink", "error", cerr)
		}
		return nil, err
	}
	if _, err := f.OSFile().Seek(0, io.SeekStart); err != nil {
		if cerr := f.Close(); cerr != nil {
			r.logger.Warn("vault: closing a materialized read after a failed rewind", "error", cerr)
		}
		return nil, fmt.Errorf("vault: rewind materialized read: %w", err)
	}
	return f, nil
}

// fileWriterAt adapts a *vfs.File's positional write into the sequential
// io.Writer FS.ReadFile streams into.
type fileWriterAt struct {
	f   *vfs.File
	off int64
}

func (w *fileWriterAt) Write(p []byte) (int, error) {
	n, err := w.f.WriteAt(p, w.off)
	w.off += int64(n)
	return n, err
}

// fileReaderAt adapts a *vfs.File's positional read into the sequential
// io.Reader FS.WriteFileStaged reads from.
type fileReaderAt struct {
	f   *vfs.File
	off int64
}

func (r *fileReaderAt) Read(p []byte) (int, error) {
	n, err := r.f.ReadAt(p, r.off)
	r.off += int64(n)
	return n, err
}

// CreatePart creates the upload engine's part file in scratch space,
// remembered against p so a later PublishPart naming the same p finds it.
func (r *Root) CreatePart(p vfs.SafePath) (*vfs.File, error) {
	scratchPath, err := mintScratchName()
	if err != nil {
		return nil, err
	}
	f, err := r.scratch.CreatePart(scratchPath)
	if err != nil {
		return nil, err
	}
	r.partsMu.Lock()
	r.parts[p.String()] = scratchPath
	r.partsMu.Unlock()
	return f, nil
}

// takeScratchPart removes and returns the scratch path CreatePart recorded
// for p, so a part is published at most once.
func (r *Root) takeScratchPart(p vfs.SafePath) (vfs.SafePath, bool) {
	r.partsMu.Lock()
	defer r.partsMu.Unlock()
	sp, ok := r.parts[p.String()]
	if ok {
		delete(r.parts, p.String())
	}
	return sp, ok
}

// WriteDurable runs write against a scratch file, then copies its bytes
// into the FAT filesystem via a staging name and rename, which is what
// makes the publish atomic from a reader's point of view.
func (r *Root) WriteDurable(p vfs.SafePath, opt vfs.DurableOpts, write func(*vfs.File) error) (vfs.Durable, error) {
	scratchPath, err := mintScratchName()
	if err != nil {
		return vfs.Durable{}, err
	}
	f, err := r.scratch.CreatePart(scratchPath)
	if err != nil {
		return vfs.Durable{}, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			r.logger.Warn("vault: closing a durable write's scratch file", "error", cerr)
		}
		if uerr := r.scratch.Unlink(scratchPath); uerr != nil {
			r.logger.Warn("vault: unlinking a durable write's scratch file", "error", uerr)
		}
	}()

	if werr := write(f); werr != nil {
		return vfs.Durable{}, werr
	}
	if serr := f.SyncData(); serr != nil {
		return vfs.Durable{}, serr
	}
	replaced, err := r.fs.WriteFileStaged(p, &fileReaderAt{f: f}, opt.NoClobber, r.clk.Nanos())
	if err != nil {
		return vfs.Durable{}, err
	}
	return vfs.Durable{Replaced: replaced}, nil
}

// PublishPart moves the scratch file CreatePart made for part onto dest,
// the same staging-and-rename way WriteDurable publishes.
func (r *Root) PublishPart(part, dest vfs.SafePath, replacing bool) (vfs.Durable, error) {
	scratchPath, ok := r.takeScratchPart(part)
	if !ok {
		return vfs.Durable{}, fmt.Errorf("publish part %q: %w", part.String(), vfs.ErrNotFound)
	}
	f, err := r.scratch.OpenRead(scratchPath, vfs.IntentRead)
	if err != nil {
		return vfs.Durable{}, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			r.logger.Warn("vault: closing a published part's scratch file", "error", cerr)
		}
		if uerr := r.scratch.Unlink(scratchPath); uerr != nil {
			r.logger.Warn("vault: unlinking a published part's scratch file", "error", uerr)
		}
	}()
	replaced, err := r.fs.WriteFileStaged(dest, &fileReaderAt{f: f}, !replacing, r.clk.Nanos())
	if err != nil {
		return vfs.Durable{}, err
	}
	return vfs.Durable{Replaced: replaced}, nil
}

func (r *Root) SetTimes(p vfs.SafePath, mtimeNs int64) error {
	return r.fs.SetModTime(p, mtimeNs)
}

func (r *Root) Mkdir(p vfs.SafePath) error  { return r.fs.Mkdir(p) }
func (r *Root) Rmdir(p vfs.SafePath) error  { return r.fs.Rmdir(p) }
func (r *Root) Unlink(p vfs.SafePath) error { return r.fs.Remove(p) }

func (r *Root) Rename(from, to vfs.SafePath, noReplace bool) error {
	return r.fs.Rename(from, to, noReplace)
}

// Space reports the FAT filesystem's own free space. Available equals Free:
// FAT has no root reserve to subtract, unlike a Linux-native filesystem.
func (r *Root) Space(p vfs.SafePath) (vfs.FsSpace, error) {
	total, free := r.fs.Space()
	return vfs.FsSpace{Total: total, Free: free, Available: free}, nil
}

// DirDev is the same synthetic device for every path: the whole container
// is one FAT filesystem, so nothing inside it can be a nested mount.
func (r *Root) DirDev(p vfs.SafePath) (uint64, error) { return r.syntheticDev, nil }

func (r *Root) Policy() vfs.SharePolicy { return r.policy }
func (r *Root) Dev() uint64             { return r.syntheticDev }
func (r *Root) FsType() vfs.FsType      { return vfs.FsType(0) }
func (r *Root) HasBtime() bool          { return false }
func (r *Root) IsScratch() bool         { return false }

func (r *Root) Alive() error { return r.fs.Alive() }

// Close flushes the FAT filesystem's own bookkeeping, syncs the container
// file, closes it, and releases the scratch root.
func (r *Root) Close() error {
	syncErr := r.fs.Sync()
	devErr := r.dev.Sync()
	closeErr := r.dev.f.Close()
	scratchErr := r.scratch.Close()
	return errors.Join(syncErr, devErr, closeErr, scratchErr)
}
