// Linux only, because it assembles a Linux-only engine.
//go:build linux

// The real core.BackendOpener: local directories, S3-compatible buckets and
// VeraCrypt containers, whichever a share's own Backend names.
package lifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vault"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/objstore"
)

// backendScratchDirMode restricts a share's scratch directory to this
// process alone: it stages a bucket's or a container's bytes on the way to
// or from the backing store, and nothing else on the host has a reason to
// read it.
const backendScratchDirMode = 0o700

// backendOpener is core.BackendOpener's real implementation. It is what
// lifecycle wires into core.Options.Backend, so the domain gains every
// backend without importing any of them.
type backendOpener struct {
	dataDir string
	logger  *slog.Logger
}

var _ core.BackendOpener = backendOpener{}

// Open switches on def.Backend and brings up the matching package.
func (o backendOpener) Open(ctx context.Context, def core.ShareDef) (vfs.Root, vfs.Admission, error) {
	switch def.Backend {
	case core.BackendLocal, "":
		return vfs.RegisterShareRoot(def.ID, def.Host, def.Policy)
	case core.BackendS3:
		return o.openS3(ctx, def)
	case core.BackendVeracrypt:
		return o.openVault(ctx, def)
	default:
		return nil, vfs.Admission{}, fmt.Errorf("share backend %q is not one this server can open", def.Backend)
	}
}

// Describe renders def's location, redacted. A configuration that will not
// parse cannot reach here from the admin API, which validates it before
// storing, so seeing one is corruption; the fallback names the backend
// rather than panicking or leaking a raw error onto the admin screen.
func (o backendOpener) Describe(def core.ShareDef) string {
	switch def.Backend {
	case core.BackendLocal, "":
		return def.Host
	case core.BackendS3:
		cfg, err := objstore.ParseConfig(def.Config)
		if err != nil {
			return "s3 (configuration unreadable)"
		}
		return cfg.Describe()
	case core.BackendVeracrypt:
		cfg, err := vault.ParseConfig(def.Config)
		if err != nil {
			return "veracrypt (configuration unreadable)"
		}
		return cfg.Describe()
	default:
		return ""
	}
}

// scratchDir names and creates the server-owned directory one share's
// non-local backend may fill, under the data directory every other durable
// file already lives beneath.
func (o backendOpener) scratchDir(def core.ShareDef) (string, error) {
	dir := filepath.Join(o.dataDir, "backend", strconv.FormatUint(uint64(def.ID), 10))
	if err := os.MkdirAll(dir, backendScratchDirMode); err != nil {
		return "", fmt.Errorf("preparing scratch space for share %d: %w", def.ID, err)
	}
	return dir, nil
}

// The caveats a non-local backend is admitted despite, carried on the
// admission verdict so registration logs them where an operator looks
// rather than leaving them only in a design note.
//
// Neither store reports a birth time, so file identity is (share, dev, ino)
// alone. The identity and link-pinning paths already tolerate an absent
// birth time; what they cannot do is tell an inode reused after a deletion
// apart from the file it replaced.
const (
	warnObjectStore = "objects are staged through server-owned scratch space, " +
		"which bounds the largest file this share can serve, and the store reports no birth time"
	warnVault = "the container's contents are staged through server-owned scratch space, " +
		"which bounds the largest file this share can serve, and FAT reports no birth time"
)

func (o backendOpener) openS3(ctx context.Context, def core.ShareDef) (vfs.Root, vfs.Admission, error) {
	cfg, err := objstore.ParseConfig(def.Config)
	if err != nil {
		return nil, vfs.Admission{}, err
	}
	dir, err := o.scratchDir(def)
	if err != nil {
		return nil, vfs.Admission{}, err
	}
	root, err := objstore.Open(ctx, objstore.Options{
		Share:      def.ID,
		Config:     cfg,
		Secret:     def.Secret,
		ScratchDir: dir,
		Policy:     def.Policy,
		Logger:     o.logger,
	})
	if err != nil {
		return nil, vfs.Admission{}, err
	}
	return root, vfs.Admission{OK: true, Warn: warnObjectStore}, nil
}

func (o backendOpener) openVault(ctx context.Context, def core.ShareDef) (vfs.Root, vfs.Admission, error) {
	cfg, err := vault.ParseConfig(def.Config)
	if err != nil {
		return nil, vfs.Admission{}, err
	}
	dir, err := o.scratchDir(def)
	if err != nil {
		return nil, vfs.Admission{}, err
	}
	root, err := vault.Open(ctx, vault.Options{
		Share:    def.ID,
		Config:   cfg,
		Password: def.Secret,
		// CreateSizeMiB is read only when creating, so a share whose
		// request never named a size never asks vault to create one, on
		// this open or any later one: the size is the durable signal that
		// creation was ever asked for, distinct from an operator pointing a
		// share at a container that must already exist. vault itself never
		// creates over a container that is already there, which is what
		// makes it safe to keep passing Create true on every subsequent
		// open of a share that did ask for one.
		Create:     cfg.CreateSizeMiB > 0,
		ScratchDir: dir,
		Policy:     def.Policy,
		Logger:     o.logger,
	})
	if err != nil {
		return nil, vfs.Admission{}, err
	}
	return root, vfs.Admission{OK: true, Warn: warnVault}, nil
}
