//go:build linux

// Browsing the host filesystem: the one primitive behind every "browse"
// button beside a path field, whether it names a share host, a VeraCrypt
// container, or the setup screen's first share before an account exists.
//
// This is deliberately not vfs. vfs resolves inside a share that is already
// registered; the whole point here is choosing a path before anything is
// registered against it, so a directory listing is built straight from
// os.ReadDir and os.Stat instead.
package lifecycle

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/apierr"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/handler"
	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vault"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// hostFsEntryCap bounds one listing. A directory holding more answers the
// first hostFsEntryCap names truncated, rather than a request whose cost
// grows with however many files somebody dropped in one place.
const hostFsEntryCap = 2000

// errHostFsNotAbsolute and errHostFsNotADirectory are refused before any
// syscall that would need the sandbox's blessing, since neither depends on
// what is actually on disk.
var (
	errHostFsNotAbsolute   = errors.New("path must be absolute and clean")
	errHostFsNotADirectory = errors.New("path is not a directory")
)

// adminFsBrowse lists a directory on the host, or the fixed starting points
// when the caller has not chosen one yet.
func (e *Engine) adminFsBrowse(c *fiber.Ctx) error {
	if _, ok, written := e.admin(c); !ok {
		return written
	}

	listing, err := e.browseHost(c.Query("path"))
	if err != nil {
		return refuse(c, hostFsRefusal(err))
	}
	return writeJSON(c, fiber.StatusOK, listing)
}

// setupFsBrowseRequest carries the same token the setup form itself spends,
// since browsing during setup needs proof of the same authority creating the
// first administrator does.
type setupFsBrowseRequest struct {
	Token string `json:"token"`
	Path  string `json:"path"`
}

// setupFsBrowse answers the same listing while setup is still open, gated by
// the setup token rather than a session because no account exists yet to
// hold one.
func (e *Engine) setupFsBrowse(c *fiber.Ctx) error {
	if e.setup == nil {
		return refuse(c, apierr.Classified{Class: apierr.SetupComplete, Key: "setup.complete"})
	}

	var req setupFsBrowseRequest
	if err := decodeBody(c, &req); err != nil {
		return refuse(c, apierr.Classified{Class: apierr.Malformed})
	}

	// Verified, not spent: the form calls this any number of times while an
	// operator is still choosing a path, and only submitting the form itself
	// consumes the token.
	if err := e.setup.Verify(c.UserContext(), req.Token); err != nil {
		return refuse(c, setupRefusal(err))
	}

	listing, err := e.browseHost(req.Path)
	if err != nil {
		return refuse(c, hostFsRefusal(err))
	}
	return writeJSON(c, fiber.StatusOK, listing)
}

// browseHost lists one directory, or the fixed starting points when path is
// empty.
func (e *Engine) browseHost(path string) (handler.HostListingView, error) {
	if path == "" {
		return e.hostFsRoots(), nil
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return handler.HostListingView{}, errHostFsNotAbsolute
	}

	info, err := os.Stat(path)
	if err != nil {
		return handler.HostListingView{}, err
	}
	if !info.IsDir() {
		return handler.HostListingView{}, errHostFsNotADirectory
	}

	raw, err := os.ReadDir(path)
	if err != nil {
		return handler.HostListingView{}, err
	}

	entries, truncated := hostEntriesOf(path, raw)
	return handler.HostListingView{
		Path:      path,
		Parent:    hostFsParent(path),
		Entries:   entries,
		Truncated: truncated,
	}, nil
}

// hostFsParent answers what "up" means from path, empty when path is already
// the filesystem root and there is nowhere above it to go.
func hostFsParent(path string) string {
	parent := filepath.Dir(path)
	if parent == path {
		return ""
	}
	return parent
}

// hostEntriesOf sorts a directory's entries and caps them at hostFsEntryCap.
//
// Directories first, then files, each group folded to lower case: the order
// a file manager draws, so a listing looks like one rather than like a raw
// directory dump.
func hostEntriesOf(dir string, raw []os.DirEntry) ([]handler.HostEntryView, bool) {
	type row struct {
		name  string
		isDir bool
	}
	rows := make([]row, len(raw))
	for i, d := range raw {
		rows[i] = row{name: d.Name(), isDir: entryIsDir(dir, d)}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].isDir != rows[j].isDir {
			return rows[i].isDir
		}
		return strings.ToLower(rows[i].name) < strings.ToLower(rows[j].name)
	})

	truncated := len(rows) > hostFsEntryCap
	if truncated {
		rows = rows[:hostFsEntryCap]
	}
	entries := make([]handler.HostEntryView, len(rows))
	for i, r := range rows {
		entries[i] = handler.HostEntryView{
			Name:  r.name,
			Path:  filepath.Join(dir, r.name),
			IsDir: r.isDir,
		}
	}
	return entries, truncated
}

// entryIsDir reports whether a directory entry names a directory, resolving
// a symlink to what it points at. A symlink that cannot be resolved, broken
// or denied by the sandbox, is reported as a file: it names something, but
// not something this browser can descend into.
func entryIsDir(dir string, d os.DirEntry) bool {
	if d.Type()&fs.ModeSymlink == 0 {
		return d.IsDir()
	}
	info, err := os.Stat(filepath.Join(dir, d.Name()))
	if err != nil {
		return false
	}
	return info.IsDir()
}

// hostFsRoots answers the fixed starting points: directories this process
// can actually open, from a wider list of candidates.
//
// Under Landlock an ungranted path answers EACCES to the open itself, so a
// candidate that merely exists is not one a click would actually serve.
// Probing every candidate is the only honest way to answer.
func (e *Engine) hostFsRoots() handler.HostListingView {
	candidates := []string{"/", "/srv", "/mnt", "/media", "/data", "/home", "/opt"}
	if e.dataDir != "" {
		candidates = append(candidates, e.dataDir)
	}
	if e.Core != nil {
		for _, s := range e.Core.Shares() {
			candidates = append(candidates, hostFsShareCandidates(s)...)
		}
	}

	seen := make(map[string]bool, len(candidates))
	var roots []string
	for _, c := range candidates {
		c = filepath.Clean(c)
		if seen[c] {
			continue
		}
		seen[c] = true
		if probeOpenable(c) {
			roots = append(roots, c)
		}
	}
	sort.Strings(roots)

	entries := make([]handler.HostEntryView, len(roots))
	for i, r := range roots {
		entries[i] = handler.HostEntryView{Name: r, Path: r, IsDir: true}
	}
	return handler.HostListingView{Entries: entries}
}

// hostFsShareCandidates names the host directories worth offering for one
// registered share: its host path for a local share, and for a VeraCrypt
// share the directory holding the container file, since the container's own
// bytes are not a directory to browse into. An s3 share names nothing on
// this host.
func hostFsShareCandidates(s core.ShareDef) []string {
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

// probeOpenable reports whether this process can actually list dir.
//
// Opening and reading one entry is the probe rather than a permission check
// against the mode bits, because Landlock's rules are not visible in the
// mode bits at all: a directory this process cannot see into still reports
// whatever permissions it was created with.
func probeOpenable(dir string) bool {
	f, err := os.Open(dir) //nolint:gosec // G304: fixed candidates and admin-configured share paths, not request input.
	if err != nil {
		return false
	}
	_, rerr := f.Readdirnames(1)
	cerr := f.Close()
	// The close is folded into the answer rather than deferred and dropped: a
	// handle this process could not even let go of is not a place to offer as
	// a starting point.
	return (rerr == nil || errors.Is(rerr, io.EOF)) && cerr == nil
}

// hostFsRefusal maps a browseHost failure onto the wire.
func hostFsRefusal(err error) apierr.Classified {
	switch {
	case errors.Is(err, errHostFsNotAbsolute):
		return apierr.Classified{Class: apierr.Unprocessable, Key: "settings.path_must_be_absolute"}
	case errors.Is(err, errHostFsNotADirectory):
		return apierr.Classified{Class: apierr.Unprocessable, Key: "settings.path_is_not_a_directory"}
	case errors.Is(err, fs.ErrNotExist):
		return apierr.Classified{Class: apierr.NotFound}
	case errors.Is(err, fs.ErrPermission):
		// The Landlock sandbox's usual refusal: a path that exists and is a
		// real directory, just not one this process was granted.
		return apierr.Classified{Class: apierr.Denied, Key: "admin.fs_denied"}
	default:
		return apierr.Classify(err, apierr.VisibilityKnown)
	}
}
