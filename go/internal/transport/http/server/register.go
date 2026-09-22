// Linux only, because it serves a Linux-only engine.
//go:build linux

// Registering the route table on the framework.
package server

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
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
