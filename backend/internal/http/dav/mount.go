//go:build linux

// The WebDAV mount boundary.
package dav

import (
	"context"
	"encoding/xml"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	core "github.com/heavycaffeiner/stowcloud/backend/internal/feature/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/feature/shares/acl"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/clock"
	"github.com/heavycaffeiner/stowcloud/backend/internal/storage/vfs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/store/state"
)

// DavPrefix is the native WebDAV mount point.
const DavPrefix = "/dav"

// Deps supplies the narrow application dependencies needed by the mount.
type Deps struct {
	Core            *core.Core
	State           *state.DB
	Locks           *StateLocks
	Props           Store
	Handler         *Handler
	Clock           clock.Clock
	Logger          *slog.Logger
	InfinityEntries int
}

func handlerOf(d Deps) *Handler {
	if d.Handler != nil {
		return d.Handler
	}
	locks := d.Locks
	if locks == nil && d.State != nil {
		locks = NewStateLocks(d.State, d.Clock, d.Logger)
	}
	props := d.Props
	if props == nil && d.State != nil {
		props = NewStateProps(d.State)
	}
	var tokens func(context.Context, uint32, string) []string
	var at func(context.Context, uint32, string) []Lock
	var taker LockTaker
	if locks != nil {
		tokens, at, taker = locks.Tokens, locks.At, locks
	}
	return New(Options{
		Core: d.Core, Locks: locks, TokensAt: tokens, LocksAt: at, Taker: taker,
		Store: props, KeyOf: EntryKey, InfinityEntries: d.InfinityEntries, Logger: d.Logger,
	})
}

// NewMount builds the standard-library handler for the native WebDAV mount.
func NewMount(d Deps) http.Handler {
	handler := handlerOf(d)
	logger := d.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.EscapedPath()
		for strings.Contains(path, "//") {
			path = strings.ReplaceAll(path, "//", "/")
		}
		if r.Method == http.MethodOptions && IsRoot(path) {
			handler.MountOptions(w)
			return
		}
		principal, ok := principalOf(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="WebDAV", charset="UTF-8"`)
			apierr.WriteClassified(w, apierr.Classified{Class: apierr.AuthRequired})
			return
		}
		user := core.UserID(principal.UserID)
		r = r.WithContext(context.WithValue(r.Context(), davContextKey{}, path))
		if IsRoot(path) {
			if r.Method == "PROPFIND" {
				baseProps, children := rootProps(d.Core, principal)
				handler.RootPropfind(w, r, baseProps, children)
				return
			}
			if r.Method == http.MethodHead || r.Method == http.MethodGet {
				w.Header().Set("DAV", "1, 2")
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		res, err := resolve(d.Core, r, user, path, acl.Read)
		if err != nil {
			refused(logger, r, path, err)
			apierr.Write(w, err, apierr.VisibilityHidden)
			return
		}
		switch r.Method {
		case "MOVE", "COPY":
			target, terr := destination(d.Core, r, user)
			if terr != nil {
				apierr.Write(w, terr, apierr.VisibilityHidden)
				return
			}
			if r.Method == "MOVE" {
				handler.Move(w, r, res, target)
				return
			}
			handler.Copy(w, r, res, target)
		default:
			handler.ServeMethod(w, r, res)
		}
	})
}

// Mount registers the WebDAV methods on a Gin router.
func Mount(app *gin.Engine, d Deps) {
	h := requestScoped(NewMount(d))
	bridge := func(c *gin.Context) {
		r := c.Request
		if p, ok := c.Get(string(middleware.KeyCredential)); ok {
			r = r.WithContext(context.WithValue(r.Context(), middleware.KeyCredential, p))
		}
		h.ServeHTTP(c.Writer, r)
		c.Abort()
	}
	methods := []string{
		http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead,
		http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut,
		"COPY", "LOCK", "MKCOL", "MOVE", "PROPFIND", "PROPPATCH", "REPORT", "SEARCH", "UNLOCK",
	}
	for _, method := range methods {
		app.Handle(method, DavPrefix, bridge)
		app.Handle(method, DavPrefix+"/*path", bridge)
	}
}

type davContextKey struct{}

func requestScoped(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
		defer cancel()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// IsRoot reports whether path names the virtual root, with or without a slash.
func IsRoot(path string) bool {
	rest := strings.TrimPrefix(path, DavPrefix)
	for len(rest) > 0 && rest[0] == '/' {
		rest = rest[1:]
	}
	return rest == ""
}

func principalOf(r *http.Request) (middleware.Principal, bool) {
	p, ok := r.Context().Value(middleware.KeyCredential).(middleware.Principal)
	if !ok || p.UserID == 0 {
		return middleware.Principal{}, false
	}
	return p, true
}

func resolve(c *core.Core, r *http.Request, user core.UserID, urlPath string, want acl.Perms) (core.Resolved, error) {
	parts, err := SplitPath(strings.TrimPrefix(urlPath, DavPrefix))
	if err != nil {
		return core.Resolved{}, apierr.BadRequest("dav.bad_path", "path")
	}
	vp, perr := vfs.ParseVpath(strings.Join(parts, "/"))
	if perr != nil {
		return core.Resolved{}, core.ErrNotFound
	}
	res, rerr := c.Resolve(user, vp, want)
	if rerr != nil {
		return res, rerr
	}
	if p, ok := r.Context().Value(middleware.KeyCredential).(middleware.Principal); ok {
		if len(p.Shares) > 0 && len(parts) > 0 {
			allowed := false
			for _, s := range p.Shares {
				if s == parts[0] {
					allowed = true
					break
				}
			}
			if !allowed {
				return core.Resolved{}, core.ErrNotFound
			}
		}
		if !p.Mask.IsEmpty() {
			res = res.WithMask(p.Mask)
			if !res.Has(want) {
				return core.Resolved{}, core.ErrDenied
			}
		}
	}
	return res, nil
}

func destination(c *core.Core, r *http.Request, user core.UserID) (Target, error) {
	segments, err := ParseDestination(r.Header.Get("Destination"), r.Host)
	switch {
	case errors.Is(err, ErrNoDestination):
		return Target{}, apierr.BadRequest("dav.no_destination", "Destination")
	case errors.Is(err, ErrForeignDestination):
		return Target{}, apierr.BadGatewayError("dav.foreign_destination", "Destination")
	case err != nil:
		return Target{}, apierr.BadRequest("dav.bad_destination", "Destination")
	}
	path := "/" + strings.Join(segments, "/")
	res, rerr := resolve(c, r, user, path, acl.Read)
	if rerr != nil {
		return Target{}, rerr
	}
	return Target{Resolved: res, Overwrite: Overwrite(r.Header.Get("Overwrite"))}, nil
}

func rootProps(c *core.Core, p middleware.Principal) ([]Prop, []RootChild) {
	roots := c.Roots(core.UserID(p.UserID))
	children := make([]RootChild, 0, len(roots))
	for _, rt := range roots {
		if !rootVisible(p, rt) {
			continue
		}
		children = append(children, RootChild{Label: rt.Label, Props: []Prop{{Name: xml.Name{Space: "DAV:", Local: "displayname"}, Value: rt.Label}}})
	}
	return nil, children
}

func rootVisible(p middleware.Principal, rt acl.RootEntry) bool {
	if !rt.Perms.Has(acl.Read) {
		return false
	}
	if !p.Mask.IsEmpty() && !p.Mask.Has(acl.Read) {
		return false
	}
	if len(p.Shares) == 0 {
		return true
	}
	for _, label := range p.Shares {
		if label == rt.Label {
			return true
		}
	}
	return false
}

func refused(logger *slog.Logger, r *http.Request, path string, err error) {
	if _, served := MethodRequirement(r.Method); !served {
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, "PROPFIND", "OPTIONS":
		return
	}
	logger.Warn("the write was refused before it reached the protocol", "method", r.Method, "path", path, "subsystem", "dav", "error", err)
}
