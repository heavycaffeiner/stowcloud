//go:build linux

package handler

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/auth"
	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vault"
)

const adminHostFSEntryCap = 2000

var (
	errAdminHostFSNotAbsolute  = errors.New("path must be absolute and clean")
	errAdminHostFSNotDirectory = errors.New("path is not a directory")
)

// AdminFSDeps supplies the registry and host paths used by the browse dialog.
type AdminFSDeps struct {
	Auth         *auth.Service
	Core         *core.Core
	DataDir      string
	SetupVerify  func(context.Context, string) error
	SetupRefusal func(error) apierr.Classified
}

// NewAdminFSHandlers builds administrator and first-run host filesystem routes.
func NewAdminFSHandlers(d AdminFSDeps) map[string]gin.HandlerFunc {
	h := &adminFSHandlers{d: d}
	return map[string]gin.HandlerFunc{"admin.fs.browse": h.browse, "system.setup.browse": h.setupBrowse}
}

type adminFSHandlers struct{ d AdminFSDeps }

func (h *adminFSHandlers) browse(c *gin.Context) {
	if _, ok := adminPrincipal(c, h.d.Auth); !ok {
		return
	}
	listing, err := h.browseHost(c.Query("path"))
	if err != nil {
		adminRefuse(c, adminHostFSRefusal(err))
		return
	}
	adminJSON(c, http.StatusOK, listing)
}

type adminSetupFSRequest struct {
	Token string `json:"token"`
	Path  string `json:"path"`
}

func (h *adminFSHandlers) setupBrowse(c *gin.Context) {
	if h.d.SetupVerify == nil {
		adminRefuse(c, apierr.Classified{Class: apierr.SetupComplete, Key: "setup.complete"})
		return
	}
	var req adminSetupFSRequest
	if !adminDecode(c, &req) {
		return
	}
	if err := h.d.SetupVerify(c.Request.Context(), req.Token); err != nil {
		if h.d.SetupRefusal != nil {
			adminRefuse(c, h.d.SetupRefusal(err))
		} else {
			adminRefuse(c, apierr.Classify(err, apierr.VisibilityKnown))
		}
		return
	}
	listing, err := h.browseHost(req.Path)
	if err != nil {
		adminRefuse(c, adminHostFSRefusal(err))
		return
	}
	adminJSON(c, http.StatusOK, listing)
}

func adminPrincipal(c *gin.Context, svc *auth.Service) (int64, bool) {
	v, ok := c.Get(string(middleware.KeyCredential))
	if !ok {
		adminRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return 0, false
	}
	p, ok := v.(middleware.Principal)
	if !ok || p.UserID == 0 {
		adminRefuse(c, apierr.Classified{Class: apierr.AuthRequired})
		return 0, false
	}
	if svc == nil {
		adminRefuse(c, apierr.Classified{Class: apierr.Internal})
		return 0, false
	}
	is, err := svc.IsAdmin(c.Request.Context(), p.UserID)
	if err != nil {
		adminFail(c, err)
		return 0, false
	}
	if !is {
		adminRefuse(c, apierr.Classified{Class: apierr.Denied})
		return 0, false
	}
	return p.UserID, true
}

func (h *adminFSHandlers) browseHost(path string) (HostListingView, error) {
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

func (h *adminFSHandlers) hostFSRoots() HostListingView {
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

func adminHostFSShareCandidates(s core.ShareDef) []string {
	switch s.Backend {
	case core.BackendVeracrypt:
		cfg, err := vault.ParseConfig(s.Config)
		if err != nil || cfg.Container == "" {
			return nil
		}
		return []string{filepath.Dir(cfg.Container)}
	case core.BackendS3:
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

func adminHostFSRefusal(err error) apierr.Classified {
	switch {
	case errors.Is(err, errAdminHostFSNotAbsolute):
		return apierr.Classified{Class: apierr.Unprocessable, Key: "settings.path_must_be_absolute"}
	case errors.Is(err, errAdminHostFSNotDirectory):
		return apierr.Classified{Class: apierr.Unprocessable, Key: "settings.path_is_not_a_directory"}
	case errors.Is(err, fs.ErrNotExist):
		return apierr.Classified{Class: apierr.NotFound}
	case errors.Is(err, fs.ErrPermission):
		return apierr.Classified{Class: apierr.Denied, Key: "admin.fs_denied"}
	default:
		return apierr.Classify(err, apierr.VisibilityKnown)
	}
}
