// Linux only, because it serves a Linux-only engine.
//go:build linux

// Registering the route table on the framework.
package server

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/http/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/route"
)

// Handlers supplies the function for each route, by name.
type Handlers map[string]gin.HandlerFunc

// Register mounts every route in the table.
func Register(app *gin.Engine, table []route.Route, h Handlers) error {
	if err := route.Validate(table); err != nil {
		return err
	}
	if err := checkHandlers(table, h); err != nil {
		return err
	}
	for _, r := range table {
		meta := r
		handler := h[r.Name]
		app.Handle(r.Method, GinPath(r.Path), func(c *gin.Context) {
			middleware.SetRequirement(c, meta.Requirement, meta.Body, meta.Name)
			handler(c)
		})
	}
	return nil
}

// Announce attaches every route's metadata before the chain runs.
func Announce(app *gin.Engine, table []route.Route) {
	meta := make(map[string]route.Route, len(table))
	for _, r := range table {
		meta[r.Method+" "+GinPath(r.Path)] = r
	}
	app.Use(func(c *gin.Context) {
		if r, ok := meta[c.Request.Method+" "+c.FullPath()]; ok {
			middleware.SetRequirement(c, r.Requirement, r.Body, r.Name)
		}
		c.Next()
	})
}

func checkHandlers(table []route.Route, h Handlers) error {
	var problems []string
	named := make(map[string]bool, len(table))
	for _, r := range table {
		named[r.Name] = true
		if _, ok := h[r.Name]; !ok {
			problems = append(problems, fmt.Sprintf("the route %s has no handler", r.Name))
		}
	}
	for name := range h {
		if !named[name] {
			problems = append(problems, fmt.Sprintf("the handler %s names no route", name))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("routes: %s", strings.Join(problems, "; "))
}

// GinPath translates the documents' notation into Gin's named parameters.
func GinPath(path string) string {
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		if !strings.HasPrefix(seg, "{") || !strings.HasSuffix(seg, "}") {
			continue
		}
		inner := seg[1 : len(seg)-1]
		if strings.HasSuffix(inner, "...") {
			segments[i] = "*" + strings.TrimSuffix(inner, "...")
			continue
		}
		segments[i] = ":" + inner
	}
	return strings.Join(segments, "/")
}

// Binding is the product-specific input to native HTTP assembly.
//
// The transport owns validation and framework binding; the application supplies
// the handlers and service-backed middleware dependencies without making the
// transport import the application package.
type Binding struct {
	Routes   []route.Route
	Roots    []string
	Chain    []middleware.Step
	Handlers Handlers
	Deps     middleware.Deps

	// BeforeAnnounce installs product routes that must precede route metadata.
	BeforeAnnounce func(*gin.Engine)
	// AfterAnnounce installs product metadata that must precede the chain.
	AfterAnnounce func(*gin.Engine)
}

// Bind validates and mounts a native HTTP assembly without starting process work.
// Application startup starts recurring tasks only after every mount succeeds.
func Bind(app *gin.Engine, b Binding) error {
	if app == nil {
		return fmt.Errorf("mounting routes: Gin engine is nil")
	}
	if err := Check(Preflight{
		Routes: b.Routes, Roots: b.Roots, Chain: b.Chain,
		Handlers: b.Handlers,
	}); err != nil {
		return fmt.Errorf("the assembly is not servable: %w", err)
	}
	if b.BeforeAnnounce != nil {
		b.BeforeAnnounce(app)
	}
	Announce(app, b.Routes)
	if b.AfterAnnounce != nil {
		b.AfterAnnounce(app)
	}
	if err := middleware.Mount(app, b.Chain, b.Deps, nil); err != nil {
		return fmt.Errorf("mounting the chain: %w", err)
	}
	if err := Register(app, b.Routes, b.Handlers); err != nil {
		return fmt.Errorf("registering routes: %w", err)
	}
	return nil
}
