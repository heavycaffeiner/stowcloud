// Linux only, matching the file under test.
//go:build linux

package server

import (
	"context"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/internal/transport/http/route"
)

func shippedPreflight(t *testing.T) Preflight {
	t.Helper()
	table := Table()
	h := make(Handlers, len(table))
	for _, r := range table {
		h[r.Name] = func(c *gin.Context) {}
	}
	return Preflight{Routes: table, Roots: []string{"/api"}, Chain: middleware.Chain(), Protocols: []middleware.ProtocolPaths{{FilePrefixes: []string{"/dav", "/remote.php"}, PublicReads: []middleware.MethodPath{{Method: "GET", Path: "/s/{token}"}}, CredentialFlows: []middleware.MethodPath{{Method: "POST", Path: "/login/v2/poll"}}}}, Tasks: completeTable(), Handlers: h}
}

func TestTheShippedAssemblyPasses(t *testing.T) {
	if err := Check(shippedPreflight(t)); err != nil {
		t.Fatalf("the shipped assembly: %v", err)
	}
}

func TestEveryCheckReachesTheReport(t *testing.T) {
	for _, c := range []struct {
		what   string
		break_ func(*Preflight)
		want   string
	}{{"a route with no access class", func(p *Preflight) {
		p.Routes = append(p.Routes, route.Route{Method: "GET", Path: "/api/v1/unset", Name: "unset", Body: route.BodyNone})
		p.Handlers["unset"] = func(c *gin.Context) {}
	}, "access"}, {"a route with no handler", func(p *Preflight) { delete(p.Handlers, p.Routes[0].Name) }, "no handler"}, {"a route under no root", func(p *Preflight) {
		p.Routes = append(p.Routes, route.Route{Method: "GET", Path: "/stray", Name: "stray", Requirement: route.Requirement{Access: route.AccessPublic}, Body: route.BodyNone})
		p.Handlers["stray"] = func(c *gin.Context) {}
	}, "under no declared root"}, {"a chain missing its mapper", func(p *Preflight) { p.Chain = []middleware.Step{middleware.StepRequestID, middleware.StepAuth} }, "escape"}, {"a protocol declaration that overlaps itself", func(p *Preflight) {
		p.Protocols[0].PublicReads = append(p.Protocols[0].PublicReads, middleware.MethodPath{Method: "GET", Path: "/dav/public"})
	}, "under the file prefix"}, {"a missing periodic task", func(p *Preflight) { p.Tasks = p.Tasks[1:] }, "is missing"}} {
		p := shippedPreflight(t)
		c.break_(&p)
		err := Check(p)
		if err == nil {
			t.Errorf("%s was accepted", c.what)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s reported %q, not %q", c.what, err, c.want)
		}
	}
}

func TestANativeRouteUnderAProtocolPrefixIsRefused(t *testing.T) {
	p := shippedPreflight(t)
	p.Routes = append(p.Routes, route.Route{Method: "GET", Path: "/dav/something", Name: "collision", Requirement: route.Requirement{Access: route.AccessPublic}, Body: route.BodyNone})
	p.Handlers["collision"] = func(c *gin.Context) {}
	p.Roots = append(p.Roots, "/dav")
	err := Check(p)
	if err == nil || !strings.Contains(err.Error(), "collision") {
		t.Errorf("collision not refused: %v", err)
	}
}
func TestANativeRouteTheFallbackCouldClaimIsRefused(t *testing.T) {
	p := shippedPreflight(t)
	p.Routes = append(p.Routes, route.Route{Method: "GET", Path: "/files/list", Name: "shadowed", Requirement: route.Requirement{Access: route.AccessPublic}, Body: route.BodyNone})
	p.Handlers["shadowed"] = func(c *gin.Context) {}
	p.Roots = append(p.Roots, "/files")
	err := Check(p)
	if err == nil || !strings.Contains(err.Error(), "shadowed") {
		t.Errorf("shadowed not refused: %v", err)
	}
}
func TestEveryStartupProblemIsReportedAtOnce(t *testing.T) {
	p := shippedPreflight(t)
	p.Chain = []middleware.Step{middleware.StepAuth}
	p.Tasks = nil
	p.Roots = nil
	err := Check(p)
	if err == nil {
		t.Fatal("assembly accepted")
	}
	for _, want := range []string{"escape", "is missing", "under no declared root"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("report omits %q", want)
		}
	}
}
func TestTheCheckedTasksAreRunnable(t *testing.T) {
	p := shippedPreflight(t)
	if err := Check(p); err != nil {
		t.Fatalf("the shipped assembly: %v", err)
	}
	for _, task := range p.Tasks {
		if task.Run == nil {
			t.Errorf("task %s has no runner", task.Name)
		}
		if task.Every <= 0 {
			t.Errorf("task %s has no interval", task.Name)
		}
	}
}

var _ = context.Background
