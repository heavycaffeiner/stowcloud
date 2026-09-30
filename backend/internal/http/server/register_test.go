// Linux only, matching the file under test.
//go:build linux

package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/route"
)

func TestThePathNotationTranslates(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/api/v1/auth/login", "/api/v1/auth/login"},
		{"/api/v1/account/sessions/{id}", "/api/v1/account/sessions/:id"},
		{"/api/v1/files/{path...}", "/api/v1/files/*path"},
		{"/api/v1/shares/{id}/files/{path...}", "/api/v1/shares/:id/files/*path"},
		{"/", "/"},
	} {
		if got := GinPath(c.in); got != c.want {
			t.Errorf("GinPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func handlersFor(table []route.Route) Handlers {
	h := make(Handlers, len(table))
	for _, r := range table {
		name := r.Name
		h[name] = func(c *gin.Context) {
			c.Header("Sc-Test-Route", name)
			c.String(200, name)
		}
	}
	return h
}

func TestEachRouteDispatchesToItsOwnHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	app := gin.New()
	table := Table()
	if err := Register(app, table, handlersFor(table)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, r := range table {
		concrete := concretePath(r.Path)
		req := httptest.NewRequest(r.Method, "http://app.test"+concrete, nil)
		res := httptest.NewRecorder()
		app.ServeHTTP(res, req)
		if res.Code != 200 {
			t.Errorf("%s %s answered %d", r.Method, concrete, res.Code)
			continue
		}
		if got := res.Header().Get("Sc-Test-Route"); got != r.Name {
			t.Errorf("%s dispatched to %q, want %q", r.Name, got, r.Name)
		}
	}
}

func TestRegisterRefusesAMismatchedHandlerSet(t *testing.T) {
	app := gin.New()
	table := Table()
	h := handlersFor(table)
	delete(h, table[0].Name)
	h["nothing.names.this"] = func(c *gin.Context) {}
	err := Register(app, table, h)
	if err == nil {
		t.Fatal("a mismatched handler set was registered")
	}
	for _, name := range []string{table[0].Name, "nothing.names.this"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("report omits %s: %v", name, err)
		}
	}
}

func TestEachRouteCarriesItsOwnMetadata(t *testing.T) {
	table := Table()
	var public, session route.Route
	for _, r := range table {
		if r.Requirement.Access == route.AccessPublic && public.Name == "" {
			public = r
		}
		if r.Requirement.Access == route.AccessSession && session.Name == "" {
			session = r
		}
	}
	if public.Name == "" || session.Name == "" {
		t.Skip("the table has no public and session pair to distinguish")
	}
	app := gin.New()
	seen := map[string]route.Access{}
	h := make(Handlers, len(table))
	for _, r := range table {
		name := r.Name
		h[name] = func(c *gin.Context) {
			if req, ok := middleware.RequirementOf(c); ok {
				seen[name] = req.Access
			}
			c.String(200, name)
		}
	}
	if err := Register(app, table, h); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, r := range []route.Route{public, session} {
		res := httptest.NewRecorder()
		app.ServeHTTP(res, httptest.NewRequest(r.Method, "http://app.test"+concretePath(r.Path), nil))
		if seen[r.Name] != r.Requirement.Access {
			t.Errorf("%s carried %v, want %v", r.Name, seen[r.Name], r.Requirement.Access)
		}
	}
}

func concretePath(path string) string {
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		switch {
		case strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "...}"):
			segments[i] = "some/nested/path"
		case strings.HasPrefix(seg, "{"):
			segments[i] = "42"
		}
	}
	return strings.Join(segments, "/")
}
