package server

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/route"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/acl"
)

// The specification says its tables are the fixture, so this reads them.
//
// A table copied from a document drifts from it the moment either is edited,
// and the drift is silent: both halves stay internally consistent and nothing
// compares them. That is the same failure contractcheck exists for between the
// client and the server, one level up.
//
// So the document is parsed, not paraphrased. Every `METHOD /api/v1/...` in its
// route tables becomes an expected entry, and the comparison runs both ways: a
// route in the table and not the document is one nobody specified, and a route
// in the document and not the table is one nobody built.

const specPath = "../../../../docs/refactor/http/09-api-consistency.md"

// specRoutes reads the v1 routes the document names.
func specRoutes(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Clean(specPath))
	if err != nil {
		t.Skipf("the specification is not where this test expects: %v", err)
	}
	// Only the left column: a `METHOD /api/v1/...` inside backticks. The
	// right-hand "Replaces" column names old spellings, which never carry the
	// version prefix, so the prefix is what separates them.
	re := regexp.MustCompile("`([A-Z]+) (" + regexp.QuoteMeta(Base) + "/[^`]+)`")
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(string(body), -1) {
		key := m[1] + " " + m[2]
		if seen[key] || isWorkedExample(m[2]) {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

// isWorkedExample reports whether a path the document spells out is a
// substitution into a parameterised route rather than a route of its own.
//
// The settings section route is written as `{section}` in its table row, and
// the prose beneath then names two sections by hand to show what the route
// covers, saying they "are examples of `{section}`, not two extra overlapping
// Fiber registrations". Reading those as routes would mount the same handler
// three times, which is the duplicate-spelling problem v1 exists to end.
func isWorkedExample(path string) bool {
	const settings = Base + "/admin/settings/"
	return strings.HasPrefix(path, settings) && path != settings+"{section}"
}

// tableRoutes is the same shape, from the table this package builds.
func tableRoutes() []string {
	out := make([]string, 0, len(Table()))
	for _, r := range Table() {
		out = append(out, r.Method+" "+r.Path)
	}
	slices.Sort(out)
	return out
}

// The table is exactly what the document specifies, both ways.
func TestTheTableMatchesTheSpecification(t *testing.T) {
	spec := specRoutes(t)
	built := tableRoutes()

	if len(spec) == 0 {
		t.Fatal("the specification parsed to no routes, so this comparison checks nothing")
	}

	for _, r := range spec {
		if !slices.Contains(built, r) {
			t.Errorf("the specification names %q and the table does not mount it", r)
		}
	}
	for _, r := range built {
		if !slices.Contains(spec, r) {
			t.Errorf("the table mounts %q and the specification does not name it", r)
		}
	}
	t.Logf("the specification names %d routes; the table mounts %d", len(spec), len(built))
}

// Every route is valid on its own terms: named, uniquely mounted, with a
// declared access class and a well-formed path.
func TestTheTableValidates(t *testing.T) {
	if err := route.Validate(Table()); err != nil {
		t.Fatalf("the v1 table does not validate:\n%v", err)
	}
}

// Rule 1: everything lives under the version prefix, so the next breaking
// change is a v2 beside v1 rather than another flag day.
func TestEveryRouteCarriesTheVersionPrefix(t *testing.T) {
	for _, r := range Table() {
		if !strings.HasPrefix(r.Path, Base+"/") {
			t.Errorf("%s %s is outside %s", r.Method, r.Path, Base)
		}
	}
}

// Rule 2: one spelling per operation. Two routes reaching the same handler
// under different paths is what the old surface had, and it is what the
// duplicate-name check below prevents recurring.
func TestNoOperationHasTwoSpellings(t *testing.T) {
	byName := map[string][]string{}
	for _, r := range Table() {
		byName[r.Name] = append(byName[r.Name], r.Method+" "+r.Path)
	}
	for name, spellings := range byName {
		if len(spellings) > 1 {
			t.Errorf("%q is mounted %d times: %v", name, len(spellings), spellings)
		}
	}
}

// Rule 6: path segments are kebab-case. Validate enforces lower case; this
// checks the rest of the rule, that a word break is a hyphen rather than an
// underscore, which is the JSON convention leaking into the URL.
func TestPathSegmentsAreKebabCase(t *testing.T) {
	for _, r := range Table() {
		for _, seg := range strings.Split(strings.TrimPrefix(r.Path, "/"), "/") {
			if strings.HasPrefix(seg, "{") {
				continue
			}
			if strings.Contains(seg, "_") {
				t.Errorf("%s %s: the segment %q uses an underscore rather than a hyphen",
					r.Method, r.Path, seg)
			}
		}
	}
}

// Rule 8: the access class comes from the category, and a route departing from
// its category's default carries a reason.
func TestEveryAccessExceptionCarriesAReason(t *testing.T) {
	for _, r := range Table() {
		def := defaultAccess()[categoryOf(r.Path)]
		if r.Requirement == def {
			continue
		}
		why, ok := ExceptionReason(r.Method, r.Path)
		if !ok {
			t.Errorf("%s %s departs from its category default and is not in the exception list",
				r.Method, r.Path)
			continue
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("%s %s is an exception with no reason", r.Method, r.Path)
		}
	}
}

// The other direction: an exception naming a route that is not mounted is
// stale, and would sit there justifying nothing.
func TestNoExceptionNamesAnUnmountedRoute(t *testing.T) {
	mounted := tableRoutes()
	for key := range exceptions() {
		if !slices.Contains(mounted, key) {
			t.Errorf("the exception list names %q, which the table does not mount", key)
		}
	}
}

// Every category a route falls into has a declared default, or the route would
// silently take the zero value, which is AccessUnset and fails validation.
//
// Validation catching it is the backstop. This says which category is missing,
// which is what somebody adding a category needs to read.
func TestEveryCategoryHasADeclaredDefault(t *testing.T) {
	for _, r := range Table() {
		cat := categoryOf(r.Path)
		if _, ok := defaultAccess()[cat]; !ok {
			t.Errorf("%s %s is in category %q, which has no declared default", r.Method, r.Path, cat)
		}
	}
}

// The three duplicate spellings the document retires are gone.
//
// Named individually because each was load-bearing in the old tree and
// documented as such: a reader wondering whether the retirement happened gets
// an answer here rather than by grepping.
func TestTheRetiredSpellingsAreAbsent(t *testing.T) {
	built := tableRoutes()
	for _, gone := range []struct{ route, why string }{
		{"PUT " + Base + "/files/write", "the PUT alias for a file write"},
		{"DELETE " + Base + "/jobs/{id}", "the DELETE alias for cancelling a job"},
		{"GET " + Base + "/files/link", "the fs/link family, merged into links"},
		{"POST " + Base + "/files/link", "the fs/link family, merged into links"},
	} {
		if slices.Contains(built, gone.route) {
			t.Errorf("%s is mounted, and it is %s, which v1 retires", gone.route, gone.why)
		}
	}
}

// The admin category is session-only throughout, which is the rule that keeps a
// filesystem credential handed to a device from creating users or rewriting the
// settings document.
func TestTheAdminSurfaceIsSessionOnly(t *testing.T) {
	count := 0
	for _, r := range Table() {
		if categoryOf(r.Path) != "admin" {
			continue
		}
		count++
		if r.Requirement.Access != route.AccessSession {
			t.Errorf("%s %s is an admin route with %s access", r.Method, r.Path, r.Requirement.Access)
		}
	}
	if count == 0 {
		t.Fatal("no admin routes were examined, so this proves nothing")
	}
}

// The account category is session-only for the same reason: an app password is
// a credential a device holds, and it must not be able to change the password
// that would revoke it.
func TestTheAccountSurfaceIsSessionOnly(t *testing.T) {
	count := 0
	for _, r := range Table() {
		if categoryOf(r.Path) != "account" {
			continue
		}
		count++
		if r.Requirement.Access != route.AccessSession {
			t.Errorf("%s %s is an account route with %s access", r.Method, r.Path, r.Requirement.Access)
		}
	}
	if count == 0 {
		t.Fatal("no account routes were examined")
	}
}

// Minting a link for a stranger demands the sharing bit, which is the
// tightening the document records: the old fs/link half required only Read.
func TestTheLinkSurfaceDemandsTheSharingBit(t *testing.T) {
	count := 0
	for _, r := range Table() {
		if categoryOf(r.Path) != "links" {
			continue
		}
		count++
		if !r.Requirement.Perms.Has(acl.Share) {
			t.Errorf("%s %s mints or manages a link without demanding the sharing bit", r.Method, r.Path)
		}
	}
	if count != 4 {
		t.Errorf("the links category has %d routes, want the four the document lists", count)
	}
}

// A public route is one of the small set the document names, and no other.
//
// This is the check worth having in this file: a route becoming public by
// accident is the failure that matters, and enumerating the permitted set means
// a new one has to be added here deliberately.
func TestOnlyTheNamedRoutesArePublic(t *testing.T) {
	allowed := []string{
		"POST " + Base + "/auth/login",
		"POST " + Base + "/auth/login/totp",
		"GET " + Base + "/auth/oidc/config",
		"GET " + Base + "/auth/oidc/start",
		"GET " + Base + "/auth/oidc/callback",
		"OPTIONS " + Base + "/uploads",
		"OPTIONS " + Base + "/uploads/{id}",
		"GET " + Base + "/system/health",
		"GET " + Base + "/system/setup",
		"POST " + Base + "/system/setup",
	}
	var got []string
	for _, r := range Table() {
		if r.Requirement.Access == route.AccessPublic {
			got = append(got, r.Method+" "+r.Path)
		}
	}
	slices.Sort(got)
	slices.Sort(allowed)
	if !slices.Equal(got, allowed) {
		t.Errorf("the public routes are not the ones the document names\n  got:  %v\n  want: %v", got, allowed)
	}
}

// A body class is declared everywhere it matters: a route that takes a JSON
// body says so, and one that streams says that instead, because the boundary
// picks a size limit from it.
func TestEveryMutatingRouteDeclaresItsBody(t *testing.T) {
	for _, r := range Table() {
		switch r.Method {
		case "POST", "PATCH", "PUT":
		default:
			continue
		}
		// A few mutations legitimately carry nothing: an action route whose
		// whole argument is in the path.
		if r.Body == route.BodyNone && !slices.Contains(bodylessActions(), r.Name) {
			t.Errorf("%s %s mutates and declares no body class", r.Method, r.Path)
		}
	}
}

// bodylessActions names the mutations whose entire argument is the path.
func bodylessActions() []string {
	return []string{
		"auth.logout",
		"admin.system.restart",
		"account.app-passwords.wipe",
		"account.totp.setup",
		"account.smb.password.delete",
		"account.oidc-link.delete",
		"jobs.cancel",
		"uploads.create",
		"admin.shares.retry",
		"admin.smb.apply",
	}
}
