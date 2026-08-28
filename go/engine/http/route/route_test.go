package route

import (
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// A route that forgot to declare its access class must not default into the
// permissive one, which is why the zero value is the invalid value.
func TestAnUndeclaredAccessClassIsRefused(t *testing.T) {
	err := Validate([]Route{{Method: "GET", Path: "/x", Name: "x"}})
	if err == nil {
		t.Fatal("a route with no access class validated")
	}
	if !strings.Contains(err.Error(), "unset") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
}

// A permission-scoped route with no bits demands nothing, which is a public
// route wearing a stricter name.
func TestPermissionAccessWithNoBitsIsRefused(t *testing.T) {
	err := Validate([]Route{{
		Method: "GET", Path: "/x", Name: "x",
		Requirement: Requirement{Access: AccessPerms},
	}})
	if err == nil {
		t.Fatal("a perms route with no bits validated")
	}
}

// Bits on a route whose class never consults them read as a guarantee nothing
// enforces, which is worse than no guarantee.
func TestPermissionBitsOnANonPermissionRouteAreRefused(t *testing.T) {
	for _, access := range []Access{AccessPublic, AccessSession, AccessAnyCredential} {
		err := Validate([]Route{{
			Method: "GET", Path: "/x", Name: "x",
			Requirement: Requirement{Access: access, Perms: acl.Read},
		}})
		if err == nil {
			t.Errorf("%s access carrying permission bits validated", access)
		}
	}
}

// A duplicate mount is the shape the old surface had: two routes, one handler,
// and no rule saying which spelling is real.
func TestADuplicateMountIsRefused(t *testing.T) {
	err := Validate([]Route{
		{Method: "GET", Path: "/x", Name: "one", Requirement: Requirement{Access: AccessPublic}},
		{Method: "GET", Path: "/x", Name: "two", Requirement: Requirement{Access: AccessPublic}},
	})
	if err == nil {
		t.Fatal("two routes on the same method and path validated")
	}
}

// A name identifies exactly one route, so a log line naming a route is not
// ambiguous about which one it means.
func TestADuplicateNameIsRefused(t *testing.T) {
	err := Validate([]Route{
		{Method: "GET", Path: "/x", Name: "same", Requirement: Requirement{Access: AccessPublic}},
		{Method: "GET", Path: "/y", Name: "same", Requirement: Requirement{Access: AccessPublic}},
	})
	if err == nil {
		t.Fatal("two routes sharing a name validated")
	}
}

// Validation reports every problem at once. A startup that fails on one missing
// declaration at a time turns a table-wide mistake into a sequence of restarts.
func TestValidationReportsEveryProblemAtOnce(t *testing.T) {
	err := Validate([]Route{
		{Method: "GET", Path: "/a", Name: ""},
		{Method: "get", Path: "/b", Name: "b", Requirement: Requirement{Access: AccessPublic}},
		{Method: "GET", Path: "c", Name: "c", Requirement: Requirement{Access: AccessPublic}},
	})
	if err == nil {
		t.Fatal("a table with three problems validated")
	}
	// The unnamed route, the lower-case method, the missing leading slash and
	// the unset access class: four lines, not one.
	if n := strings.Count(err.Error(), "\n"); n < 4 {
		t.Errorf("the report has %d lines and should name every problem:\n%v", n, err)
	}
}

// An empty table is a mistake rather than a server with nothing mounted.
func TestAnEmptyTableIsRefused(t *testing.T) {
	if Validate(nil) == nil {
		t.Error("an empty table validated")
	}
}

// The path grammar is small: a literal, {name}, and {name...} last.
func TestTheWellFormedPathsValidate(t *testing.T) {
	ok := []Route{
		{Method: "GET", Path: "/api/v1/files/list", Name: "a", Requirement: Requirement{Access: AccessPublic}},
		{Method: "GET", Path: "/api/v1/jobs/{id}", Name: "b", Requirement: Requirement{Access: AccessPublic}},
		{Method: "GET", Path: "/dav/{path...}", Name: "c", Requirement: Requirement{Access: AccessPublic}},
		{Method: "DELETE", Path: "/g/{id}/members/{user}", Name: "d", Requirement: Requirement{Access: AccessPublic}},
	}
	if err := Validate(ok); err != nil {
		t.Errorf("a well-formed table did not validate:\n%v", err)
	}
}

// A tail that is not last would swallow the segments after it, so the router
// would never reach them.
func TestATailThatIsNotLastIsRefused(t *testing.T) {
	err := Validate([]Route{{
		Method: "GET", Path: "/x/{path...}/y", Name: "x",
		Requirement: Requirement{Access: AccessPublic},
	}})
	if err == nil {
		t.Fatal("a tail parameter mid-path validated")
	}
}

// A half-written parameter is a spelling the registration step would have to
// guess at, so it is refused rather than interpreted.
func TestAMalformedParameterIsRefused(t *testing.T) {
	for _, path := range []string{"/x/{id", "/x/id}", "/x/pre{id}", "/x/{}"} {
		err := Validate([]Route{{
			Method: "GET", Path: path, Name: "x",
			Requirement: Requirement{Access: AccessPublic},
		}})
		if err == nil {
			t.Errorf("the path %q validated", path)
		}
	}
}

// Params is what the registration step and the handler both read, so neither
// re-parses and the two cannot disagree about what a route captured.
func TestParamsReadsTheNamesInOrder(t *testing.T) {
	for _, c := range []struct {
		path string
		want []string
	}{
		{"/api/v1/files/list", nil},
		{"/api/v1/jobs/{id}", []string{"id"}},
		{"/g/{id}/members/{user}", []string{"id", "user"}},
		{"/dav/{path...}", []string{"path"}},
	} {
		got := Params(c.path)
		if len(got) != len(c.want) {
			t.Errorf("%s produced %v, want %v", c.path, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s produced %v, want %v", c.path, got, c.want)
				break
			}
		}
	}
}

// HasTail decides whether the registered pattern can be followed by anything,
// which is the difference between matching one segment and matching the rest.
func TestHasTailDistinguishesATailFromAParameter(t *testing.T) {
	if !HasTail("/dav/{path...}") {
		t.Error("a tail was not recognised")
	}
	if HasTail("/api/v1/jobs/{id}") {
		t.Error("a named parameter was read as a tail")
	}
	if HasTail("/api/v1/files/list") {
		t.Error("a literal was read as a tail")
	}
}

// The access classes print as themselves, because they appear in refusals and
// in route dumps an operator reads.
func TestTheAccessClassesHaveNames(t *testing.T) {
	for _, c := range []struct {
		access Access
		want   string
	}{
		{AccessUnset, "unset"},
		{AccessPublic, "public"},
		{AccessSession, "session"},
		{AccessAnyCredential, "any-credential"},
		{AccessPerms, "perms"},
	} {
		if got := c.access.String(); got != c.want {
			t.Errorf("the access class printed as %q, want %q", got, c.want)
		}
	}
}

// So do the body classes, for the same reason.
func TestTheBodyClassesHaveNames(t *testing.T) {
	for _, c := range []struct {
		body BodyClass
		want string
	}{
		{BodyNone, "none"},
		{BodyJSON, "json"},
		{BodyDAVXML, "dav-xml"},
		{BodyStream, "stream"},
	} {
		if got := c.body.String(); got != c.want {
			t.Errorf("the body class printed as %q, want %q", got, c.want)
		}
	}
}

// A body class this build does not know is refused rather than treated as the
// zero value, which would silently make it BodyNone.
func TestAnUnknownBodyClassIsRefused(t *testing.T) {
	err := Validate([]Route{{
		Method: "POST", Path: "/x", Name: "x",
		Requirement: Requirement{Access: AccessPublic},
		Body:        BodyStream + 1,
	}})
	if err == nil {
		t.Fatal("an unknown body class validated")
	}
}
