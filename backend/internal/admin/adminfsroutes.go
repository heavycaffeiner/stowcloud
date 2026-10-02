//go:build linux

package admin

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/fs/vault"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
)

const adminHostFSEntryCap = 2000

var (
	errAdminHostFSNotAbsolute  = errors.New("path must be absolute and clean")
	errAdminHostFSNotDirectory = errors.New("path is not a directory")
)

// AdminFSDeps supplies the registry and host paths used by the browse dialog.
type AdminFSDeps struct {
	Auth        *auth.Service
	Core        *files.Core
	DataDir     string
	SetupVerify func(context.Context, string) error
}

// NewAdminFSHandlers builds administrator and first-run host filesystem routes.
func NewAdminFSHandlers(d AdminFSDeps) *AdminFSHandlers {
	return &AdminFSHandlers{d: d}
}

type AdminFSHandlers struct{ d AdminFSDeps }

type hostBrowseInput struct {
	Path string `query:"path"`
}

type hostListingOutput struct{ Body HostListingView }

func (h *AdminFSHandlers) Browse(_ context.Context, in *hostBrowseInput) (*hostListingOutput, error) {
	listing, err := h.browseHost(in.Path)
	if err != nil {
		return nil, hostFSErr(err)
	}
	return &hostListingOutput{Body: listing}, nil
}

type adminSetupFSRequest struct {
	Token string `json:"token"`
	Path  string `json:"path"`
}

type setupBrowseInput struct{ Body adminSetupFSRequest }

func (h *AdminFSHandlers) SetupBrowse(ctx context.Context, in *setupBrowseInput) (*hostListingOutput, error) {
	if h.d.SetupVerify == nil {
		return nil, apierr.AsClassified(apierr.SetupComplete, "setup.complete")
	}
	if err := h.d.SetupVerify(ctx, in.Body.Token); err != nil {
		return nil, err
	}
	listing, err := h.browseHost(in.Body.Path)
	if err != nil {
		return nil, hostFSErr(err)
	}
	return &hostListingOutput{Body: listing}, nil
}

func (h *AdminFSHandlers) browseHost(path string) (HostListingView, error) {
	if path == "" {
		return h.hostFSRoots(), nil
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return HostListingView{}, errAdminHostFSNotAbsolute
	}
	info, err := os.Stat(path)
	if err != nil {
		return HostListingView{}, err
	}
	if !info.IsDir() {
		return HostListingView{}, errAdminHostFSNotDirectory
	}
	raw, err := os.ReadDir(path)
	if err != nil {
		return HostListingView{}, err
	}
	entries, truncated := adminHostEntriesOf(path, raw)
	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	}
	return HostListingView{Path: path, Parent: parent, Entries: entries, Truncated: truncated}, nil
}

func adminHostEntriesOf(dir string, raw []os.DirEntry) ([]HostEntryView, bool) {
	type row struct {
		name string
		dir  bool
	}
	rows := make([]row, len(raw))
	for i, d := range raw {
		rows[i] = row{name: d.Name(), dir: adminEntryIsDir(dir, d)}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].dir != rows[j].dir {
			return rows[i].dir
		}
		return strings.ToLower(rows[i].name) < strings.ToLower(rows[j].name)
	})
	truncated := len(rows) > adminHostFSEntryCap
	if truncated {
		rows = rows[:adminHostFSEntryCap]
	}
	out := make([]HostEntryView, len(rows))
	for i, r := range rows {
		out[i] = HostEntryView{Name: r.name, Path: filepath.Join(dir, r.name), IsDir: r.dir}
	}
	return out, truncated
}

func adminEntryIsDir(dir string, d os.DirEntry) bool {
	if d.Type()&fs.ModeSymlink == 0 {
		return d.IsDir()
	}
	info, err := os.Stat(filepath.Join(dir, d.Name()))
	return err == nil && info.IsDir()
}

func (h *AdminFSHandlers) hostFSRoots() HostListingView {
	candidates := []string{"/", "/srv", "/mnt", "/media", "/data", "/home", "/opt"}
	if h.d.DataDir != "" {
		candidates = append(candidates, h.d.DataDir)
	}
	if h.d.Core != nil {
		for _, s := range h.d.Core.Shares() {
			candidates = append(candidates, adminHostFSShareCandidates(s)...)
		}
	}
	seen := map[string]bool{}
	var roots []string
	for _, p := range candidates {
		p = filepath.Clean(p)
		if seen[p] {
			continue
		}
		seen[p] = true
		if adminProbeOpenable(p) {
			roots = append(roots, p)
		}
	}
	sort.Strings(roots)
	entries := make([]HostEntryView, len(roots))
	for i, p := range roots {
		entries[i] = HostEntryView{Name: p, Path: p, IsDir: true}
	}
	return HostListingView{Entries: entries}
}

func adminHostFSShareCandidates(s files.ShareDef) []string {
	switch s.Backend {
	case files.BackendVeracrypt:
		cfg, err := vault.ParseConfig(s.Config)
		if err != nil || cfg.Container == "" {
			return nil
		}
		return []string{filepath.Dir(cfg.Container)}
	case files.BackendS3:
		return nil
	default:
		if s.Host == "" {
			return nil
		}
		return []string{s.Host, filepath.Dir(s.Host)}
	}
}

func adminProbeOpenable(dir string) bool {
	f, err := os.Open(dir) //nolint:gosec // G304: fixed candidates and admin-configured share paths, not request input.
	if err != nil {
		return false
	}
	_, rerr := f.Readdirnames(1)
	cerr := f.Close()
	return (rerr == nil || errors.Is(rerr, io.EOF)) && cerr == nil
}

// hostFSErr classifies a host path failure. The host filesystem's own errors
// are classified here because elsewhere they are internal faults.
func hostFSErr(err error) error {
	switch {
	case errors.Is(err, errAdminHostFSNotAbsolute):
		return apierr.AsClassified(apierr.Unprocessable, "settings.path_must_be_absolute")
	case errors.Is(err, errAdminHostFSNotDirectory):
		return apierr.AsClassified(apierr.Unprocessable, "settings.path_is_not_a_directory")
	case errors.Is(err, fs.ErrNotExist):
		return errNotFound()
	case errors.Is(err, fs.ErrPermission):
		return apierr.AsClassified(apierr.Denied, "admin.fs_denied")
	default:
		return err
	}
}
