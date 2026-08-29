// Linux only, matching the file under test.
//go:build linux

package server

import (
	"context"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/middleware"
	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
)

// shippedPreflight is the assembly as it is meant to be: the real route table,
// the real chain, the real reserved prefixes.
func shippedPreflight(t *testing.T) Preflight {
	t.Helper()
	table := Table()
	h := make(Handlers, len(table))
	for _, r := range table {
		h[r.Name] = func(c *fiber.Ctx) error { return nil }
	}
	return Preflight{
		Routes: table,
		Roots:  []string{"/api"},
		Chain:  middleware.Chain(),
		Protocols: []middleware.ProtocolPaths{{
			FilePrefixes:    []string{"/dav", "/remote.php"},
			PublicReads:     []middleware.MethodPath{{Method: "GET", Path: "/s/{token}"}},
			CredentialFlows: []middleware.MethodPath{{Method: "POST", Path: "/login/v2/poll"}},
		}},
		Tasks:    completeTable(),
		Handlers: h,
	}
}

// The assembly this server actually ships passes every startup check.
//
// This is the one that matters: the checks are only worth having if the real
// tables satisfy them, and a check nobody can pass gets deleted rather than
// fixed.
func TestTheShippedAssemblyPasses(t *testing.T) {
	if err := Check(shippedPreflight(t)); err != nil {
		t.Fatalf("the shipped assembly: %v", err)
	}
}

// Each check reaches the report, so a failure in any one of them stops startup
// rather than being masked by another passing.
func TestEveryCheckReachesTheReport(t *testing.T) {
	for _, c := range []struct {
		what   string
		break_ func(*Preflight)
		want   string
	}{
		{
			"a route with no access class",
			func(p *Preflight) {
				p.Routes = append(p.Routes, route.Route{
					Method: "GET", Path: "/api/v1/unset", Name: "unset", Body: route.BodyNone,
				})
				p.Handlers["unset"] = func(c *fiber.Ctx) error { return nil }
			},
			"access",
		},
		{
			"a route with no handler",
			func(p *Preflight) {
				delete(p.Handlers, p.Routes[0].Name)
			},
			"no handler",
		},
		{
			"a route under no root",
			func(p *Preflight) {
				p.Routes = append(p.Routes, route.Route{
					Method: "GET", Path: "/stray", Name: "stray",
					Requirement: route.Requirement{Access: route.AccessPublic},
					Body:        route.BodyNone,
				})
				p.Handlers["stray"] = func(c *fiber.Ctx) error { return nil }
			},
			"under no declared root",
		},
		{
			"a chain missing its mapper",
			func(p *Preflight) {
				p.Chain = []middleware.Step{middleware.StepRequestID, middleware.StepAuth}
			},
			"escape",
		},
		{
			"a protocol declaration that overlaps itself",
			func(p *Preflight) {
				p.Protocols[0].PublicReads = append(p.Protocols[0].PublicReads,
					middleware.MethodPath{Method: "GET", Path: "/dav/public"})
			},
			"under the file prefix",
		},
		{
			"a missing periodic task",
			func(p *Preflight) { p.Tasks = p.Tasks[1:] },
			"is missing",
		},
	} {
		p := shippedPreflight(t)
		c.break_(&p)

		err := Check(p)
		if err == nil {
			t.Errorf("%s was accepted", c.what)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s reported %q, which does not mention %q", c.what, err, c.want)
		}
	}
}

// A native route under a protocol's file prefix is refused. Which one serves
// it depends on registration order, and that is a decision nobody wrote down.
//
// Neither half can see this on its own: the route table does not know what a
// protocol declared, and the declaration does not know the table.
func TestANativeRouteUnderAProtocolPrefixIsRefused(t *testing.T) {
	p := shippedPreflight(t)
	p.Routes = append(p.Routes, route.Route{
		Method: "GET", Path: "/dav/something", Name: "collision",
		Requirement: route.Requirement{Access: route.AccessPublic},
		Body:        route.BodyNone,
	})
	p.Handlers["collision"] = func(c *fiber.Ctx) error { return nil }
	p.Roots = append(p.Roots, "/dav")

	err := Check(p)
	if err == nil {
		t.Fatal("a route under a protocol file prefix was accepted")
	}
	if !strings.Contains(err.Error(), "collision") {
		t.Errorf("the report does not name it: %v", err)
	}
}

// A native route outside every reserved prefix is refused, because the
// interface fallback answers anything not reserved and would claim it.
func TestANativeRouteTheFallbackCouldClaimIsRefused(t *testing.T) {
	p := shippedPreflight(t)
	p.Routes = append(p.Routes, route.Route{
		Method: "GET", Path: "/files/list", Name: "shadowed",
		Requirement: route.Requirement{Access: route.AccessPublic},
		Body:        route.BodyNone,
	})
	p.Handlers["shadowed"] = func(c *fiber.Ctx) error { return nil }
	p.Roots = append(p.Roots, "/files")

	err := Check(p)
	if err == nil {
		t.Fatal("a route the fallback could claim was accepted")
	}
	if !strings.Contains(err.Error(), "shadowed") {
		t.Errorf("the report does not name it: %v", err)
	}
}

// Every problem is reported at once, since the list is short enough to read
// whole and meeting them one restart at a time teaches it slowly.
func TestEveryStartupProblemIsReportedAtOnce(t *testing.T) {
	p := shippedPreflight(t)
	p.Chain = []middleware.Step{middleware.StepAuth}
	p.Tasks = nil
	p.Roots = nil

	err := Check(p)
	if err == nil {
		t.Fatal("an assembly with several problems was accepted")
	}
	msg := err.Error()
	for _, want := range []string{"escape", "is missing", "under no declared root"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the report omits %q:\n  %s", want, msg)
		}
	}
}

// The task table is checked with its own functions present, so a table that
// validates here is one that can actually run.
func TestTheCheckedTasksAreRunnable(t *testing.T) {
	p := shippedPreflight(t)
	if err := Check(p); err != nil {
		t.Fatalf("the shipped assembly: %v", err)
	}
	for _, task := range p.Tasks {
		if task.Run == nil {
			t.Errorf("the task %s has no function", task.Name)
			continue
		}
		if task.Every <= 0 {
			t.Errorf("the task %s has no interval", task.Name)
		}
		if err := task.Run(context.Background()); err != nil {
			t.Errorf("the task %s returned %v", task.Name, err)
		}
	}
	if len(p.Tasks) == 0 {
		t.Fatal("no task was checked")
	}
}
