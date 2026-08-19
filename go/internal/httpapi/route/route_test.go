package route

import (
	"net/http"
	"testing"
)

func h() http.HandlerFunc { return func(w http.ResponseWriter, r *http.Request) {} }

// The route table's startup contract: a route with no declared requirement is
// refused, duplicates are refused, and every pattern this surface uses parses.
func TestValidate(t *testing.T) {
	good := []Route{
		{Method: "GET", Pattern: "/api/fs/list", Req: Requirement{Access: AccessPerms, Perms: 1}, Handler: h()},
		{Method: "POST", Pattern: "/api/fs/delete", Req: Requirement{Access: AccessPerms, Perms: 8}, Handler: h()},
		{Method: "GET", Pattern: "/api/admin/shares/{id}", Req: Requirement{Access: AccessSelfAdmin}, Handler: h()},
	}
	if err := Validate(good); err != nil {
		t.Fatalf("a valid table: %v", err)
	}

	dupe := append([]Route(nil), good...)
	dupe = append(dupe, Route{Method: "GET", Pattern: "/api/fs/list", Req: Requirement{Access: AccessAny}, Handler: h()})
	if err := Validate(dupe); err == nil {
		t.Fatal("duplicate method and pattern must be refused")
	}

	noScope := append([]Route(nil), good...)
	noScope = append(noScope, Route{Method: "GET", Pattern: "/api/new", Handler: h()})
	if err := Validate(noScope); err == nil {
		t.Fatal("a route with no requirement must be refused at startup")
	}

	badPattern := append([]Route(nil), good...)
	badPattern = append(badPattern, Route{Method: "GET", Pattern: "not-a-path", Req: Requirement{Access: AccessAny}, Handler: h()})
	if err := Validate(badPattern); err == nil {
		t.Fatal("a pattern this surface cannot dispatch must be refused")
	}
}

// The scope lookup and the mux must agree on which route a request hit, and
// the wildcard shape is the one the surface uses.
func TestLookupMatchesWildcards(t *testing.T) {
	table := []Route{
		{Method: "GET", Pattern: "/api/jobs/{id}", Req: Requirement{Access: AccessAny}, Handler: h()},
		{Method: "GET", Pattern: "/api/fs/list", Req: Requirement{Access: AccessPerms, Perms: 1}, Handler: h()},
	}
	lookup := From(table)
	if req, ok := lookup("GET", "/api/jobs/42"); !ok || req.Access != AccessAny {
		t.Fatalf("the wildcard route did not match: %+v", req)
	}
	if req, ok := lookup("GET", "/api/fs/list"); !ok || req.Perms != 1 {
		t.Fatalf("the exact route did not match: %+v", req)
	}
	if _, ok := lookup("GET", "/api/fs/delete"); ok {
		t.Fatal("a route the table does not own must not match")
	}
	if _, ok := lookup("POST", "/api/fs/list"); ok {
		t.Fatal("a method the table does not own must not match")
	}
}
