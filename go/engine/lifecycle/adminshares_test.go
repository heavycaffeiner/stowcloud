//go:build linux

package lifecycle_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// The share listing never carries a host path.
//
// Where a share lives on the server's disk is configuration. A client that
// learns it learns the layout of the machine, which is the first thing worth
// knowing to anyone trying to reach past the shares they were given.
func TestTheShareListingHidesHostPaths(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	host := t.TempDir()
	status, created := mutate(t, http.MethodPost, base+"/api/v1/admin/shares", cookie, csrf,
		map[string]string{"name": "docs", "host": host})
	if status != http.StatusCreated {
		t.Fatalf("creating a share answered %d: %v", status, created)
	}

	// The create response, which is the one most likely to echo its input.
	assertNoHostPath(t, fmt.Sprint(created), host)

	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/shares", cookie)
	if code != http.StatusOK {
		t.Fatalf("listing answered %d: %s", code, raw)
	}
	assertNoHostPath(t, string(raw), host)

	// The share is really there, so the absence above is not the absence of
	// the whole share.
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || stringField(rows[0], "name") != "docs" {
		t.Fatalf("the listing does not show the share: %s", raw)
	}
}

// assertNoHostPath fails if a body carries the path or any component of it
// that would disclose the server's layout.
func assertNoHostPath(t *testing.T, body, host string) {
	t.Helper()

	if strings.Contains(body, host) {
		t.Errorf("the response carries the host path %q: %s", host, body)
	}
	// The full path is the obvious leak; a parent directory is the same
	// disclosure one level up, and a field named for it invites one later.
	for _, fragment := range []string{"/tmp/", "/home/", `"host"`, `"path"`} {
		if strings.Contains(body, fragment) {
			t.Errorf("the response carries %q: %s", fragment, body)
		}
	}
}

// A share can be registered, renamed, have its trash toggled and be removed.
func TestTheShareLifecycle(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, created := mutate(t, http.MethodPost, base+"/api/v1/admin/shares", cookie, csrf,
		map[string]string{"name": "docs", "host": t.TempDir()})
	if status != http.StatusCreated {
		t.Fatalf("creating answered %d: %v", status, created)
	}
	id := stringField(created, "id")
	if id == "" {
		t.Fatal("the created share has no id")
	}
	if boolField(created, "trash") {
		t.Error("a new share has the trash on, which no request asked for")
	}

	on := true
	status, updated := mutate(t, http.MethodPatch,
		fmt.Sprintf("%s/api/v1/admin/shares/%s", base, id), cookie, csrf,
		map[string]any{"name": "documents", "trash_enabled": on})
	if status != http.StatusOK {
		t.Fatalf("updating answered %d: %v", status, updated)
	}
	if stringField(updated, "name") != "documents" {
		t.Errorf("the share is named %v after a rename", updated["name"])
	}
	if !boolField(updated, "trash") {
		t.Error("the trash is still off after being turned on")
	}

	status, body := mutate(t, http.MethodDelete,
		fmt.Sprintf("%s/api/v1/admin/shares/%s", base, id), cookie, csrf, nil)
	if status != http.StatusNoContent {
		t.Fatalf("deleting answered %d: %v", status, body)
	}

	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/shares", cookie)
	if code != http.StatusOK {
		t.Fatalf("listing answered %d", code)
	}
	if strings.Contains(string(raw), "documents") {
		t.Errorf("the share survived its own deletion: %s", raw)
	}
}

// A grant is created with named permissions and read back with the same ones.
func TestCreatingAGrant(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	share := makeShare(t, base, cookie, csrf, "docs")
	user := accountID(t, base, cookie, loginName)

	status, created := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
		map[string]any{
			"user":    user,
			"share":   share,
			"allow":   []string{"read", "download"},
			"inherit": true,
			"label":   "docs",
		})
	if status != http.StatusCreated {
		t.Fatalf("creating a grant answered %d: %v", status, created)
	}

	rows := listGrants(t, base, cookie)
	if len(rows) != 1 {
		t.Fatalf("%d grants listed, want 1", len(rows))
	}

	// Names rather than a bitmask, and exactly the ones asked for. A grant
	// stored weaker than the one requested is an administrator believing they
	// gave access nobody has.
	allow := stringsField(t, rows[0], "allow")
	if len(allow) != 2 || allow[0] != "read" || allow[1] != "download" {
		t.Errorf("the grant allows %v, want read and download", allow)
	}
	if got := stringsField(t, rows[0], "deny"); len(got) != 0 {
		t.Errorf("the grant denies %v, though none was requested", got)
	}
}

// makeShare registers one and returns its id.
func makeShare(t *testing.T, base string, cookie *http.Cookie, csrf, name string) string {
	t.Helper()

	status, created := mutate(t, http.MethodPost, base+"/api/v1/admin/shares", cookie, csrf,
		map[string]string{"name": name, "host": t.TempDir()})
	if status != http.StatusCreated {
		t.Fatalf("creating the share answered %d: %v", status, created)
	}
	return stringField(created, "id")
}

// listGrants reads the grant listing.
func listGrants(t *testing.T, base string, cookie *http.Cookie) []map[string]any {
	t.Helper()

	status, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/grants", cookie)
	if status != http.StatusOK {
		t.Fatalf("listing grants answered %d: %s", status, raw)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// stringsField reads a string list out of a decoded body.
func stringsField(t *testing.T, body map[string]any, name string) []string {
	t.Helper()

	raw, ok := body[name].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, isString := v.(string)
		if !isString {
			t.Fatalf("%s holds a non-string %v", name, v)
		}
		out = append(out, s)
	}
	return out
}

// An unknown permission name refuses the whole grant.
//
// Dropping it would store a grant that differs from the one described, and
// the difference is silent: the screen shows the name that was typed while
// the system holds a set without it.
func TestAnUnknownPermissionRefusesTheGrant(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	share := makeShare(t, base, cookie, csrf, "docs")
	user := accountID(t, base, cookie, loginName)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
		map[string]any{
			"user":  user,
			"share": share,
			"allow": []string{"read", "administer"},
		})
	if status == http.StatusCreated {
		t.Fatalf("a grant with an unknown permission was stored: %v", body)
	}

	// Nothing was stored, so the caller is not left with a partial grant they
	// did not ask for.
	if rows := listGrants(t, base, cookie); len(rows) != 0 {
		t.Errorf("%d grants exist after a refused create: %v", len(rows), rows)
	}
}

// A grant must name exactly one subject.
func TestAGrantNamesExactlyOneSubject(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	share := makeShare(t, base, cookie, csrf, "docs")
	user := accountID(t, base, cookie, loginName)

	status, group := mutate(t, http.MethodPost, base+"/api/v1/admin/groups", cookie, csrf,
		map[string]string{"name": "editors"})
	if status != http.StatusCreated {
		t.Fatalf("creating a group answered %d: %v", status, group)
	}

	cases := map[string]map[string]any{
		"neither": {"share": share, "allow": []string{"read"}},
		"both": {
			"user": user, "group": stringField(group, "id"),
			"share": share, "allow": []string{"read"},
		},
	}

	for name, body := range cases {
		status, got := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf, body)
		if status == http.StatusCreated {
			t.Errorf("a grant naming %s was stored: %v", name, got)
		}
	}
	if rows := listGrants(t, base, cookie); len(rows) != 0 {
		t.Errorf("%d grants exist after refusals", len(rows))
	}
}

// Revoking a grant takes effect immediately, not at the next restart.
//
// The evaluator answers from a set it loaded, so a delete that did not reload
// leaves the row gone and the access intact. Nobody would notice until a
// restart, which is exactly the window a revocation is meant to close.
func TestRevokingAGrantTakesEffectNow(t *testing.T) {
	base, cookie, csrf, plainCookie, _ := adminEngine(t)

	share := makeShare(t, base, cookie, csrf, "docs")
	user := accountID(t, base, cookie, loginName)

	status, created := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
		map[string]any{
			"user": user, "share": share,
			"allow": []string{"read", "download"}, "inherit": true, "label": "docs",
		})
	if status != http.StatusCreated {
		t.Fatalf("creating the grant answered %d: %v", status, created)
	}

	// The account can see the share now.
	code, raw := withCookie(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/docs"), plainCookie)
	if code != http.StatusOK {
		t.Fatalf("the granted account cannot list the share: %d %s", code, raw)
	}

	id := stringField(listGrants(t, base, cookie)[0], "id")
	if status, body := mutate(t, http.MethodDelete,
		fmt.Sprintf("%s/api/v1/admin/grants/%s", base, id), cookie, csrf, nil); status != http.StatusNoContent {
		t.Fatalf("revoking answered %d: %v", status, body)
	}

	// The same session, no restart. Access has to be gone already.
	code, raw = withCookie(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/docs"), plainCookie)
	if code == http.StatusOK {
		t.Errorf("the revoked account still lists the share: %s", raw)
	}
}

// Narrowing a grant takes effect immediately too.
func TestNarrowingAGrantTakesEffectNow(t *testing.T) {
	base, cookie, csrf, plainCookie, _ := adminEngine(t)

	share := makeShare(t, base, cookie, csrf, "docs")
	user := accountID(t, base, cookie, loginName)

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
		map[string]any{
			"user": user, "share": share,
			"allow": []string{"read", "download"}, "inherit": true, "label": "docs",
		}); status != http.StatusCreated {
		t.Fatalf("creating answered %d: %v", status, body)
	}

	if code, _ := withCookie(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/docs"), plainCookie); code != http.StatusOK {
		t.Fatal("the granted account cannot list the share")
	}

	// Down to nothing usable, keeping the row.
	id := stringField(listGrants(t, base, cookie)[0], "id")
	if status, body := mutate(t, http.MethodPatch,
		fmt.Sprintf("%s/api/v1/admin/grants/%s", base, id), cookie, csrf,
		map[string]any{"allow": []string{}, "inherit": true, "label": "docs"}); status != http.StatusNoContent {
		t.Fatalf("narrowing answered %d: %v", status, body)
	}

	if code, raw := withCookie(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/docs"), plainCookie); code == http.StatusOK {
		t.Errorf("the narrowed account still lists the share: %s", raw)
	}
}

// A deny on an updated grant takes away what the allow gives.
//
// Deny is the half that removes access, so a path that carried allow through
// and dropped deny would widen every grant it touched while the screen showed
// the restriction the administrator had just applied.
func TestADeniedPermissionIsRemovedOnUpdate(t *testing.T) {
	base, cookie, csrf, plainCookie, _ := adminEngine(t)

	share := makeShare(t, base, cookie, csrf, "docs")
	user := accountID(t, base, cookie, loginName)

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
		map[string]any{
			"user": user, "share": share,
			"allow": []string{"read", "download"}, "inherit": true, "label": "docs",
		}); status != http.StatusCreated {
		t.Fatalf("creating answered %d: %v", status, body)
	}

	if code, _ := withCookie(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/docs"), plainCookie); code != http.StatusOK {
		t.Fatal("the granted account cannot list the share")
	}

	// Read stays in the allow set and is denied alongside it. Deny wins, so
	// the listing has to stop working; a path that dropped deny would leave
	// it working and report success.
	id := stringField(listGrants(t, base, cookie)[0], "id")
	status, body := mutate(t, http.MethodPatch,
		fmt.Sprintf("%s/api/v1/admin/grants/%s", base, id), cookie, csrf,
		map[string]any{
			"allow": []string{"read", "download"},
			"deny":  []string{"read"},
			"label": "docs", "inherit": true,
		})
	if status != http.StatusNoContent {
		t.Fatalf("updating answered %d: %v", status, body)
	}

	if code, raw := withCookie(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/docs"), plainCookie); code == http.StatusOK {
		t.Errorf("the denied account still lists the share: %s", raw)
	}

	// And the stored grant reports the deny, so an administrator reopening
	// the screen sees the restriction they applied.
	rows := listGrants(t, base, cookie)
	if len(rows) != 1 {
		t.Fatalf("%d grants listed", len(rows))
	}
	if got := stringsField(t, rows[0], "deny"); len(got) != 1 || got[0] != "read" {
		t.Errorf("the grant denies %v, want read", got)
	}
}

// A deny is honoured on create too.
func TestADeniedPermissionIsRemovedOnCreate(t *testing.T) {
	base, cookie, csrf, plainCookie, _ := adminEngine(t)

	share := makeShare(t, base, cookie, csrf, "docs")
	user := accountID(t, base, cookie, loginName)

	if status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
		map[string]any{
			"user": user, "share": share,
			"allow":   []string{"read", "download"},
			"deny":    []string{"read"},
			"inherit": true, "label": "docs",
		}); status != http.StatusCreated {
		t.Fatalf("creating answered %d: %v", status, body)
	}

	if code, raw := withCookie(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/docs"), plainCookie); code == http.StatusOK {
		t.Errorf("a grant denying read still lists the share: %s", raw)
	}
}

// A share id past the width of a share id names nothing.
//
// The id is narrower than the decimal a path can carry. Converting rather
// than narrowing wraps: 4294967297 becomes 1, so a mistyped number would
// delete or rename whichever share happens to hold the wrapped value.
func TestAnOversizedShareIDNamesNothing(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	first := makeShare(t, base, cookie, csrf, "docs")
	if first == "" {
		t.Fatal("no share was created")
	}

	// 2^32 above the share's own id, so truncating to 32 bits lands exactly
	// on it. Computed rather than written as a constant, because ids do not
	// start at 1 and a hand-picked number quietly tested nothing.
	own, err := strconv.ParseUint(first, 10, 64)
	if err != nil {
		t.Fatalf("the share id %q is not a number: %v", first, err)
	}
	wrapped := strconv.FormatUint(own+(1<<32), 10)
	if uint32(own) != uint32(own+(1<<32)) { //nolint:gosec // the truncation is the property under test.
		t.Fatalf("%s does not truncate onto %s, so this test proves nothing", wrapped, first)
	}

	status, body := mutate(t, http.MethodDelete,
		base+"/api/v1/admin/shares/"+wrapped, cookie, csrf, nil)
	if status == http.StatusNoContent {
		t.Errorf("an id past the width deleted a share: %v", body)
	}

	status, body = mutate(t, http.MethodPatch,
		base+"/api/v1/admin/shares/"+wrapped, cookie, csrf,
		map[string]any{"name": "renamed"})
	if status == http.StatusOK {
		t.Errorf("an id past the width renamed a share: %v", body)
	}

	// The real share is untouched, which is the damage this prevents.
	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/shares", cookie)
	if code != http.StatusOK {
		t.Fatalf("listing answered %d", code)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || stringField(rows[0], "name") != "docs" {
		t.Errorf("the share was changed by an oversized id: %s", raw)
	}
}
