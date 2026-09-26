//go:build linux

// Package preflight gathers the settings and filesystem paths needed before
// Hanami installs the process sandbox and constructs the application.
package preflight

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/admin/settings/runtimecfg"
	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/system/mountinfo"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vault"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/dbfile"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/instance"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

// DefaultListen is used when neither the command line nor stored settings
// provide a listen address.
const DefaultListen = "127.0.0.1:8081"

// Config is the complete result of the startup read.
type Config struct {
	DataDir    string
	Values     runtimecfg.Values
	Roots      []string
	ShareHosts []string
	ExactPaths []string
	Address    string
	Pinned     bool
	Plain      bool
	Lock       *instance.Lock
}

// Options controls the startup read.
type Options struct {
	Addr              string
	DataDir           string
	Plain             bool
	Logger            *slog.Logger
	SkipRootDiscovery bool
}

// Load takes the data-directory lock, checks the resolver, reads persisted
// settings, and discovers filesystem roots before the runtime is constructed.
// The returned Config holds the lock; a failed Load releases it.
func Load(ctx context.Context, options Options) (Config, error) {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	abs, err := filepath.Abs(options.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolving the data directory: %w", err)
	}
	if mkErr := os.MkdirAll(abs, 0o700); mkErr != nil {
		return Config{}, fmt.Errorf("creating the data directory: %w", mkErr)
	}
	lock, err := instance.Take(abs)
	if err != nil {
		return Config{}, fmt.Errorf("the data directory is in use: %w", err)
	}
	failed := true
	defer func() {
		if !failed {
			return
		}
		if rerr := lock.Release(); rerr != nil {
			logger.Warn("releasing the data-directory lock after a failed start", "error", rerr)
		}
	}()
	if resolverErr := vfs.RequireResolver(vfs.Probe()); resolverErr != nil {
		return Config{}, resolverErr
	}
	values, shareHosts, exactPaths, err := bootSettings(ctx, abs, logger)
	if err != nil {
		return Config{}, err
	}
	var roots []string
	if !options.SkipRootDiscovery {
		mounts, err := mountinfo.Self()
		if err != nil {
			logger.Warn("reading the mount table; new folders cannot be added until this is fixed", "error", err)
		} else {
			roots = shareRoots(mounts)
		}
	}
	address := options.Addr
	if address == "" {
		address = values.Listen
	}
	if address == "" {
		address = DefaultListen
	}
	failed = false
	return Config{
		DataDir: abs, Values: values, Roots: roots, ShareHosts: shareHosts,
		ExactPaths: exactPaths, Address: address, Pinned: options.Addr != "", Plain: options.Plain, Lock: lock,
	}, nil
}

// bootSettings reads persisted settings and every path a registered share
// makes the process open. The probe is closed before runtime construction.
func bootSettings(ctx context.Context, dataDir string, log *slog.Logger) (
	values runtimecfg.Values, shareHosts, exactPaths []string, err error,
) {
	stateFile, err := dbfile.Open(ctx, state.Spec(filepath.Join(dataDir, "state.db")))
	if err != nil {
		if _, statErr := os.Stat(filepath.Join(dataDir, "state.db")); errors.Is(statErr, os.ErrNotExist) {
			return runtimecfg.Defaults(), nil, nil, nil
		}
		return runtimecfg.Values{}, nil, nil, fmt.Errorf("opening the state database: %w", err)
	}
	defer func() {
		if closeErr := stateFile.Close(); closeErr != nil {
			log.Warn("closing the settings probe", "error", closeErr)
			if err == nil {
				err = fmt.Errorf("closing the settings probe: %w", closeErr)
			}
		}
	}()
	st := state.New(stateFile)
	values = runtimecfg.Load(ctx, st, runtimecfg.Defaults(), log)
	rows, err := st.ListShares(ctx)
	if err != nil {
		return values, nil, nil, fmt.Errorf("reading registered shares: %w", err)
	}
	for _, row := range rows {
		switch row.Backend {
		case string(core.BackendVeracrypt):
			cfg, parseErr := vault.ParseConfig([]byte(row.BackendConfig))
			if parseErr != nil {
				log.Warn("a veracrypt share's configuration is unreadable, so its container is not granted", "share", row.Name, "error", parseErr)
				continue
			}
			exactPaths = append(exactPaths, cfg.Container)
		case string(core.BackendS3):
		default:
			shareHosts = append(shareHosts, row.Host)
		}
	}
	return values, shareHosts, exactPaths, nil
}

func namedShareDirs() []string {
	return []string{"/srv", "/mnt", "/media", "/data", "/home", "/opt"}
}

func shareRoots(mounts []mountinfo.Mount) []string {
	var out []string
	for _, m := range mounts {
		point := filepath.Clean(m.Point)
		if adm, _ := vfs.AdmitFsType(vfs.ParseFsType(m.FsType)); !adm.OK {
			continue
		}
		if point == "/" || underAny(point, "/proc", "/sys", "/dev", "/run") {
			continue
		}
		info, err := os.Stat(point)
		if err != nil || !info.IsDir() {
			continue
		}
		out = append(out, point)
	}
	for _, dir := range namedShareDirs() {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		out = append(out, filepath.Clean(dir))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func underAny(point string, bases ...string) bool {
	for _, base := range bases {
		if point == base || strings.HasPrefix(point, base+"/") {
			return true
		}
	}
	return false
}
