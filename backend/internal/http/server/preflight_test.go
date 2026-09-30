// Linux only, matching the file under test.
//go:build linux

package server

import (
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/route"
)

func shippedPreflight(t *testing.T) Preflight {
	t.Helper()
	table := Table()
	h := make(Handlers, len(table))
	for _, r := range table {
		h[r.Name] = func(c *gin.Context) {}
	}
	return Preflight{Routes: table, Roots: []string{"/api"}, Chain: middleware.Chain(), Protocols: []middleware.ProtocolPaths{{FilePrefixes: []string{"/dav", "/remote.php"}, PublicReads: []middleware.MethodPath{{Method: "GET", Path: "/s/{token}"}}, CredentialFlows: []middleware.MethodPath{{Method: "POST", Path: "/login/v2/poll"}}}}, Handlers: h}
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
	}{{"a route with no access class", func(p *Preflight) {
		p.Routes = append(p.Routes, route.Route{Method: "GET", Path: "/api/v1/unset", Name: "unset", Body: route.BodyNone})
		p.Handlers["unset"] = func(c *gin.Context) {}
	}}, {"a route with no handler", func(p *Preflight) { delete(p.Handlers, p.Routes[0].Name) }}, {"a route under no root", func(p *Preflight) {
		p.Routes = append(p.Routes, route.Route{Method: "GET", Path: "/stray", Name: "stray", Requirement: route.Requirement{Access: route.AccessPublic}, Body: route.BodyNone})
		p.Handlers["stray"] = func(c *gin.Context) {}
	}}, {"a chain missing its mapper", func(p *Preflight) { p.Chain = []middleware.Step{middleware.StepRequestID, middleware.StepAuth} }}, {"a protocol declaration that overlaps itself", func(p *Preflight) {
		p.Protocols[0].PublicReads = append(p.Protocols[0].PublicReads, middleware.MethodPath{Method: "GET", Path: "/dav/public"})
	}}} {
		p := shippedPreflight(t)
		c.break_(&p)
		if err := Check(p); err == nil {
			t.Errorf("%s was accepted", c.what)
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
	p.Roots = nil
	err := Check(p)
	if err == nil {
		t.Fatal("assembly accepted")
	}
	// The chain problem names the missing mapper and the root problem names
	// the stranded route.
	for _, want := range []string{"ErrorMapper", p.Routes[0].Path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("report omits %q: %v", want, err)
		}
	}
}
