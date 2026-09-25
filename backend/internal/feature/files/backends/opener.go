//go:build linux

// Opening configured share backends: local directories, S3-compatible buckets,
// and VeraCrypt containers.
package backends

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/objstore"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vault"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
)

// opener is core.BackendOpener's real implementation. The domain receives this
// narrow adapter without importing any concrete storage backend.
type opener struct {
	dataDir string
	logger  *slog.Logger
}

var _ core.BackendOpener = opener{}

// New returns the opener used by the application composition boundary.
func New(dataDir string, logger *slog.Logger) core.BackendOpener {
	if logger == nil {
		logger = slog.Default()
	}
	return opener{dataDir: dataDir, logger: logger}
}

// Open switches on def.Backend and brings up the matching package.
func (o opener) Open(ctx context.Context, def core.ShareDef) (vfs.Root, vfs.Admission, error) {
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

// Describe renders def's location, redacted. A malformed configuration names
// the backend rather than exposing a raw error or panicking.
func (o opener) Describe(def core.ShareDef) string {
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

func (o opener) scratchDir(def core.ShareDef) (string, error) {
	dir := filepath.Join(o.dataDir, "backend", strconv.FormatUint(uint64(def.ID), 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("preparing scratch space for share %d: %w", def.ID, err)
	}
	return dir, nil
}

const (
	warnObjectStore = "objects are staged through server-owned scratch space, " +
		"which bounds the largest file this share can serve, and the store reports no birth time"
	warnVault = "the container's contents are staged through server-owned scratch space, " +
		"which bounds the largest file this share can serve, and FAT reports no birth time"
)

func (o opener) openS3(ctx context.Context, def core.ShareDef) (vfs.Root, vfs.Admission, error) {
	cfg, err := objstore.ParseConfig(def.Config)
	if err != nil {
		return nil, vfs.Admission{}, err
	}
	dir, err := o.scratchDir(def)
	if err != nil {
		return nil, vfs.Admission{}, err
	}
	root, err := objstore.Open(ctx, objstore.Options{
		Share: def.ID, Config: cfg, Secret: def.Secret, ScratchDir: dir,
		Policy: def.Policy, Logger: o.logger,
	})
	if err != nil {
		return nil, vfs.Admission{}, err
	}
	return root, vfs.Admission{OK: true, Warn: warnObjectStore}, nil
}

func (o opener) openVault(ctx context.Context, def core.ShareDef) (vfs.Root, vfs.Admission, error) {
	cfg, err := vault.ParseConfig(def.Config)
	if err != nil {
		return nil, vfs.Admission{}, err
	}
	dir, err := o.scratchDir(def)
	if err != nil {
		return nil, vfs.Admission{}, err
	}
	root, err := vault.Open(ctx, vault.Options{
		Share: def.ID, Config: cfg, Password: def.Secret,
		Create: cfg.CreateSizeMiB > 0, ScratchDir: dir,
		Policy: def.Policy, Logger: o.logger,
	})
	if err != nil {
		return nil, vfs.Admission{}, classifyVaultOpen(err)
	}
	return root, vfs.Admission{OK: true, Warn: warnVault}, nil
}

// classifyVaultOpen names the ways a container refuses to open so callers can
// report a useful repair action while unrelated errors retain their identity.
func classifyVaultOpen(err error) error {
	switch {
	case errors.Is(err, vault.ErrWrongPassword):
		return &core.RejectedError{Kind: "passphrase", Err: err}
	case errors.Is(err, vault.ErrUnsupportedVolume):
		return &core.RejectedError{Kind: "container_unsupported", Err: err}
	case errors.Is(err, vault.ErrHeaderCorrupt), errors.Is(err, vault.ErrHeaderFieldsInvalid):
		return &core.RejectedError{Kind: "container_corrupt", Err: err}
	case errors.Is(err, vault.ErrUnsupportedFilesystem):
		return &core.RejectedError{Kind: "container_filesystem", Err: err}
	default:
		return err
	}
}
