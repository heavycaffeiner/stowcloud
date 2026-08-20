//go:build linux

package dav

import (
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/internal/core"
	"github.com/heavycaffeiner/stowcloud/go/internal/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/store/state"
)

// Handler dispatches a WebDAV method against the core.
//
// It takes an already-resolved path, so the ACL check has happened and a path
// outside every grant has already become a 404. Nothing here re-checks a
// grant, and nothing here parses a virtual path.
type Handler struct {
	core   *core.Core
	state  *state.DB
	locks  *Locks
	limits Limits
	log    *slog.Logger

	// sources contribute vendor properties and claim namespaces. Registering
	// one is also what puts SEARCH and REPORT into Allow, so a build with the
	// compat tag off advertises neither.
	sources []PropSource
	// uploads backs the /dav-uploads collection. Nil when the engine is not
	// wired up, and then the collection does not exist.
	uploads UploadCollection
	// infinityEntries is the collection size above which Depth: infinity is
	// refused. It is a field rather than a direct read of the constant so a
	// test can prove which check refuses without building a directory of a
	// hundred thousand files.
	infinityEntries int
}

// Options configures a handler.
type Options struct {
	Core    *core.Core
	State   *state.DB
	Locks   *Locks
	Limits  Limits
	Logger  *slog.Logger
	Sources []PropSource
	Uploads UploadCollection
	// InfinityEntries overrides the Depth: infinity ceiling. Zero takes the
	// package bound, and a value above it is clamped: a caller cannot raise a
	// D5 bound, only lower it.
	InfinityEntries int
}

// New builds the handler.
func New(o Options) *Handler {
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	infinity := limits.DavInfinityEntries
	if o.InfinityEntries > 0 && o.InfinityEntries < infinity {
		infinity = o.InfinityEntries
	}
	return &Handler{
		core: o.Core, state: o.State, locks: o.Locks,
		limits: o.Limits.withDefaults(), log: log,
		sources: o.Sources, uploads: o.Uploads,
		infinityEntries: infinity,
	}
}

// namespaces is the prefix map the multistatus root declares, built from the
// registered sources so a vendor property does not have to declare its own.
func (h *Handler) namespaces() map[string]string {
	if len(h.sources) == 0 {
		return nil
	}
	out := map[string]string{}
	var all []string
	for _, s := range h.sources {
		all = append(all, s.Namespaces()...)
	}
	sort.Strings(all)
	// Prefixes are minted positionally so the same source set always produces
	// the same document.
	i := 0
	for _, ns := range all {
		if _, seen := out[ns]; seen {
			continue
		}
		out[ns] = "v" + string(rune('0'+i%10))
		i++
	}
	return out
}

// searchEnabled reports whether any source claims a namespace, which is what
// SEARCH and REPORT need to mean anything.
func (h *Handler) searchEnabled() bool { return len(h.sources) > 0 }

// Allow is the method set for a resource.
func (h *Handler) Allow(isDir bool) string {
	m := []string{
		"OPTIONS", "PROPFIND", "PROPPATCH", "HEAD", "GET",
		"DELETE", "COPY", "MOVE", "LOCK", "UNLOCK",
	}
	if isDir {
		m = append(m, "MKCOL", "POST")
	} else {
		m = append(m, "PUT")
	}
	if h.searchEnabled() {
		m = append(m, "SEARCH", "REPORT")
	}
	sort.Strings(m)
	return strings.Join(m, ", ")
}

// ServeMethod dispatches one WebDAV method.
func (h *Handler) ServeMethod(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	switch r.Method {
	case "OPTIONS":
		h.options(w, r, res)
	case "PROPFIND":
		h.propfind(w, r, res)
	case "PROPPATCH":
		h.proppatch(w, r, res)
	case "LOCK":
		h.lock(w, r, res)
	case "UNLOCK":
		h.unlock(w, r, res)
	case "SEARCH":
		if !h.searchEnabled() {
			h.methodNotAllowed(w, r, res)
			return
		}
		h.search(w, r, res)
	case "REPORT":
		if !h.searchEnabled() {
			h.methodNotAllowed(w, r, res)
			return
		}
		h.report(w, r, res)
	default:
		h.methodNotAllowed(w, r, res)
	}
}

func (h *Handler) options(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	isDir := false
	if st, err := res.Root().Stat(res.Path()); err == nil {
		isDir = st.Kind.IsDir()
	}
	// Class 2: locking is offered, so the DAV header says so.
	w.Header().Set("DAV", "1, 2")
	w.Header().Set("Allow", h.Allow(isDir))
	w.Header().Set("MS-Author-Via", "DAV")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) methodNotAllowed(w http.ResponseWriter, r *http.Request, res core.Resolved) {
	isDir := false
	if st, err := res.Root().Stat(res.Path()); err == nil {
		isDir = st.Kind.IsDir()
	}
	w.Header().Set("Allow", h.Allow(isDir))
	w.WriteHeader(http.StatusMethodNotAllowed)
}

func (h *Handler) logger(r *http.Request) *slog.Logger {
	return h.log.With("method", r.Method, "path", r.URL.Path)
}

// fail maps an error to a status and writes it.
//
// The existence rule holds here as it does on the native API: a path outside
// every grant is a 404, never a 403. WebDAV's status vocabulary makes 403 feel
// natural and it is wrong.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	code, cond := StatusOf(err)
	if code >= 500 {
		h.logger(r).Error("the request failed", "error", err)
	}
	if cond.Local != "" {
		if werr := WriteError(w, code, cond, ""); werr != nil {
			h.logger(r).Warn("the error body could not be written", "error", werr)
		}
		return
	}
	http.Error(w, http.StatusText(code), code)
}

// StatusOf maps this package's errors, and the core's, onto WebDAV statuses.
func StatusOf(err error) (int, Name) {
	switch {
	case err == nil:
		return http.StatusOK, Name{}

	case errors.Is(err, ErrDTDForbidden), errors.Is(err, ErrPIForbidden),
		errors.Is(err, ErrBadXML), errors.Is(err, ErrBadRequest):
		return http.StatusBadRequest, Name{}

	case errors.Is(err, ErrTooManyElements), errors.Is(err, ErrTooDeep):
		return http.StatusBadRequest, Name{}

	case errors.Is(err, ErrLocked):
		return http.StatusLocked, DavName("lock-token-submitted")

	case errors.Is(err, ErrPreconditionFailed), errors.Is(err, core.ErrPrecondition):
		return http.StatusPreconditionFailed, Name{}

	// A missing path and a path outside every grant are the same answer, which
	// is what stops a stranger probing for what exists.
	case errors.Is(err, ErrNotFound), errors.Is(err, core.ErrNotFound):
		return http.StatusNotFound, Name{}

	case errors.Is(err, core.ErrDenied):
		return http.StatusForbidden, Name{}

	case errors.Is(err, core.ErrExists):
		return http.StatusMethodNotAllowed, Name{}

	case errors.Is(err, core.ErrNotEmpty):
		return http.StatusConflict, Name{}

	case errors.Is(err, core.ErrNoSpace):
		return http.StatusInsufficientStorage, Name{}

	// Every D5 bound refuses with the same sentinel. Which one it was is in
	// the error's own fields; the status is 507 because the client asked for
	// more of a durable resource than it may have.
	case errors.Is(err, limits.ErrTooLarge):
		return http.StatusInsufficientStorage, Name{}

	default:
		return http.StatusInternalServerError, Name{}
	}
}
