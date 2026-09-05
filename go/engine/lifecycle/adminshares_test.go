//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The administrative listing carries the host path; nothing else does.
//
// The administrator typed the path and is the only account that can change
// it, so a screen offering an edit has to show it: renaming a folder
// otherwise meant retyping a path from memory with nothing to check against.
// Every surface an ordinary account reads still omits it, because a client
// that learns the server's layout learns the first thing worth knowing to
// anyone trying to reach past the shares they were given.
func TestTheAdminListingCarriesTheHostPath(t *testing.T) {
	base, cookie, csrf, plainCookie, _ := adminEngine(t)

	host := t.TempDir()
	status, created := mutate(t, http.MethodPost, base+"/api/v1/admin/shares", cookie, csrf,
		map[string]string{"name": "docs", "host": host})
	if status != http.StatusCreated {
		t.Fatalf("creating a share answered %d: %v", status, created)
	}
	if got := stringField(created, "host"); got != host {
		t.Errorf("the create answered host %q, want %q", got, host)
	}

	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/shares", cookie)
	if code != http.StatusOK {
		t.Fatalf("listing answered %d: %s", code, raw)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || stringField(rows[0], "host") != host {
		t.Fatalf("the listing does not carry the host path: %s", raw)
	}

	// An ordinary account cannot reach the administrative surface at all, and
	// the surfaces it can reach do not name the path.
	if code, _ := withCookie(t, http.MethodGet, base+"/api/v1/admin/shares", plainCookie); code != http.StatusForbidden {
		t.Errorf("an ordinary account read the share listing: %d", code)
	}
	sessionCode, session := withCookie(t, http.MethodGet, base+"/api/v1/auth/session", plainCookie)
	if sessionCode != http.StatusOK {
		t.Fatalf("the session answered %d", sessionCode)
	}
	assertNoHostPath(t, string(session), host)

	listCode, listing := withCookie(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/docs"), plainCookie)
	if listCode == http.StatusOK {
		assertNoHostPath(t, string(listing), host)
	}
}

// An s3 object contradicts a request that names backend local, and the
// whole request is refused rather than the s3 object being silently
// ignored.
func TestCreatingAShareRefusesAnS3ObjectWithBackendLocal(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/shares", cookie, csrf,
		map[string]any{
			"name":    "docs",
			"host":    t.TempDir(),
			"backend": "local",
			"s3": map[string]any{
				"endpoint":          "https://minio.example:9000",
				"region":            "us-east-1",
				"bucket":            "photos",
				"access_key_id":     "AKIA",
				"secret_access_key": "shh",
			},
		})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("an s3 object against backend local answered %d: %v", status, body)
	}

	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/shares", cookie)
	if code != http.StatusOK {
		t.Fatalf("listing answered %d", code)
	}
	if strings.Contains(string(raw), "docs") {
		t.Errorf("the refused share was stored anyway: %s", raw)
	}
}

// A veracrypt object asking to create a container needs a password: this
// server cannot open, let alone create, one without it.
func TestCreatingAVeracryptShareWithNoPasswordIsRefused(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/shares", cookie, csrf,
		map[string]any{
			"name":    "vault",
			"backend": "veracrypt",
			"veracrypt": map[string]any{
				"container": filepath.Join(t.TempDir(), "vault.hc"),
				"create":    true,
				"size_mib":  256,
			},
		})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("a veracrypt object with no password answered %d: %v", status, body)
	}
}

// A share the administrator registers is one they can open.
//
// Registration and access are separate by design: everybody else still needs
// a grant. But a folder that is invisible to the person who just added it
// reads as the registration having failed, and the only way out was to hand
// yourself a grant from another screen.
func TestANewShareIsReachableByTheAdministratorWhoAddedIt(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "note.txt"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}

	status, created := mutate(t, http.MethodPost, base+"/api/v1/admin/shares", cookie, csrf,
		map[string]string{"name": "docs", "host": host})
	if status != http.StatusCreated {
		t.Fatalf("creating a share answered %d: %v", status, created)
	}

	// The session reports it as a root, which is what the folder switcher draws.
	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/auth/session", cookie)
	if code != http.StatusOK {
		t.Fatalf("the session answered %d: %s", code, raw)
	}
	if !strings.Contains(string(raw), `"label":"docs"`) {
		t.Errorf("the new share is not among the account's roots: %s", raw)
	}

	// And its contents list, which is what the person does next.
	code, listed := withCookie(t, http.MethodGet, base+"/api/v1/files/list?path=/docs", cookie)
	if code != http.StatusOK {
		t.Fatalf("listing the new share answered %d: %s", code, listed)
	}
	if !strings.Contains(string(listed), "note.txt") {
		t.Errorf("the listing does not show the file that is there: %s", listed)
	}
}

// Creating a grant answers with the grant, not with its id.
//
// The screen appends the response to the list it is already showing, and every
// row reads the permission arrays. An id alone left those undefined, which
// threw while rendering and left the dialog spinning.
func TestCreatingAGrantAnswersTheWholeGrant(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	host := t.TempDir()
	status, created := mutate(t, http.MethodPost, base+"/api/v1/admin/shares", cookie, csrf,
		map[string]string{"name": "docs", "host": host})
	if status != http.StatusCreated {
		t.Fatalf("creating a share answered %d: %v", status, created)
	}
	shareID := fmt.Sprint(created["id"])

	// The ordinary account, not the administrator: registering a share grants
	// its creator the whole tree, so a second grant naming the same subject,
	// share, subpath and reach is refused as the duplicate it is.
	status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
		map[string]any{
			"user": "2", "share": shareID, "subpath": "",
			"allow": []string{"read", "download"}, "deny": []string{},
			"inherit": true, "label": "docs",
		})
	if status != http.StatusCreated {
		t.Fatalf("creating a grant answered %d: %v", status, body)
	}

	for _, field := range []string{"allow", "deny"} {
		if _, present := body[field]; !present {
			t.Errorf("the response has no %q, which the screen reads as a list: %v", field, body)
		}
	}
	if got := fmt.Sprint(body["share"]); got != shareID {
		t.Errorf("the response names share %q, want %q", got, shareID)
	}
}

// Editing a grant answers the grant, as creating one does.
//
// The screen swaps the edited row for what came back and every rendered row
// reads the permission arrays. Answering no content left the client reading a
// field off nothing: it threw, its own catch rendered "could not save", and
// the permission had in fact been applied. This fails against that.
func TestUpdatingAGrantAnswersTheWholeGrant(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	host := t.TempDir()
	status, created := mutate(t, http.MethodPost, base+"/api/v1/admin/shares", cookie, csrf,
		map[string]string{"name": "docs", "host": host})
	if status != http.StatusCreated {
		t.Fatalf("creating a share answered %d: %v", status, created)
	}
	shareID := fmt.Sprint(created["id"])

	status, made := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
		map[string]any{
			"user": "2", "share": shareID, "subpath": "",
			"allow": []string{"read"}, "deny": []string{},
			"inherit": true, "label": "docs",
		})
	if status != http.StatusCreated {
		t.Fatalf("creating a grant answered %d: %v", status, made)
	}
	grantID := fmt.Sprint(made["id"])

	status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/grants/"+grantID, cookie, csrf,
		map[string]any{
			"allow": []string{"read", "download"}, "deny": []string{},
			"inherit": true, "label": "docs",
		})
	if status != http.StatusOK {
		t.Fatalf("updating a grant answered %d: %v", status, body)
	}

	// The whole row, not an id: the fields the screen renders have to be
	// there. An empty subpath and an absent label are omitted by design, and
	// the client's own normalizer fills those in.
	if got := fmt.Sprint(body["id"]); got != grantID {
		t.Errorf("the response names grant %q, want %q", got, grantID)
	}
	for _, field := range []string{"allow", "deny", "share", "principal", "inherit"} {
		if _, present := body[field]; !present {
			t.Errorf("the response has no %q, which the screen reads: %v", field, body)
		}
	}
	// And it describes what was just stored rather than what was there before.
	allow, ok := body["allow"].([]any)
	if !ok {
		t.Fatalf("the response's allow is %T, want a list: %v", body["allow"], body)
	}
	names := make([]string, 0, len(allow))
	for _, a := range allow {
		names = append(names, fmt.Sprint(a))
	}
	if len(names) != 2 {
		t.Errorf("the updated grant allows %v, want the two bits just saved", names)
	}
}

// assertNoHostPath fails if a body carries the path or any component of it
// that would disclose the server's layout.
//
// An empty host is a caller with no specific path to look for, not a match
// against everything: strings.Contains is true for "" on any input, so the
// check has to skip it rather than report every response as a leak.
func assertNoHostPath(t *testing.T, body, host string) {
	t.Helper()

	if host != "" && strings.Contains(body, host) {
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

	rows := grantsFor(t, base, cookie, user)
	if len(rows) != 1 {
		t.Fatalf("%d grants listed for the account, want 1", len(rows))
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

// A grant with no label of its own reads as the folder's name.
//
// The folder switcher draws the label. Without one the listing fell back to a
// placeholder built from the share's id, so a share called "Share" appeared
// as "share-1000001" to the person who had just named it.
func TestAnUnlabelledGrantReadsAsTheShareName(t *testing.T) {
	base, cookie, csrf, plainCookie, _ := adminEngine(t)

	share := makeShare(t, base, cookie, csrf, "Photos")
	user := accountID(t, base, cookie, loginName)

	// No label, and the whole share rather than a subpath: the case with
	// nothing else to name it after.
	status, created := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
		map[string]any{
			"user": user, "share": share, "subpath": "",
			"allow": []string{"read", "download"}, "inherit": true,
		})
	if status != http.StatusCreated {
		t.Fatalf("creating the grant answered %d: %v", status, created)
	}

	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/auth/session", plainCookie)
	if code != http.StatusOK {
		t.Fatalf("the session answered %d: %s", code, raw)
	}
	if !strings.Contains(string(raw), `"label":"Photos"`) {
		t.Errorf("the root is not labelled with the share's name: %s", raw)
	}
	if strings.Contains(string(raw), `"label":"share-`) {
		t.Errorf("the root carries a generated placeholder: %s", raw)
	}
}

// A root the session lists is a root that opens.
//
// The label is both what the switcher draws and what a browse path names, so
// the listing and the resolver have to agree on it. They did not: the listing
// substituted the share's name over the generated placeholder and the
// resolver still matched the placeholder, so clicking the folder that had
// just been shown answered 404 with nothing on screen explaining it.
func TestAListedRootCanBeOpened(t *testing.T) {
	base, cookie, csrf, plainCookie, _ := adminEngine(t)

	share := makeShare(t, base, cookie, csrf, "Photos")
	user := accountID(t, base, cookie, loginName)

	// No label: the case where the listing has to supply one.
	if status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/grants", cookie, csrf,
		map[string]any{
			"user": user, "share": share, "subpath": "",
			"allow": []string{"read", "download"}, "inherit": true,
		}); status != http.StatusCreated {
		t.Fatalf("creating the grant answered %d: %v", status, body)
	}

	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/auth/session", plainCookie)
	if code != http.StatusOK {
		t.Fatalf("the session answered %d: %s", code, raw)
	}
	var session struct {
		Roots []struct {
			Label string `json:"label"`
		} `json:"roots"`
	}
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatal(err)
	}
	if len(session.Roots) != 1 {
		t.Fatalf("%d roots listed, want 1: %s", len(session.Roots), raw)
	}

	// Exactly the path the client builds from the label it was given.
	label := session.Roots[0].Label
	status, body := withCookie(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/"+label), plainCookie)
	if status != http.StatusOK {
		t.Fatalf("the listed root %q does not open: %d %s", label, status, body)
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

// grantsFor reads the grants belonging to one account.
//
// Registering a share grants its creator, so the listing always holds the
// administrator's own grants too. A test that counted every row would be
// asserting on a share it did not create.
func grantsFor(t *testing.T, base string, cookie *http.Cookie, user string) []map[string]any {
	t.Helper()

	var out []map[string]any
	for _, row := range listGrants(t, base, cookie) {
		if fmt.Sprint(row["user"]) == user {
			out = append(out, row)
		}
	}
	return out
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
	if rows := grantsFor(t, base, cookie, user); len(rows) != 0 {
		t.Errorf("%d grants exist for the account after a refused create: %v", len(rows), rows)
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
	if rows := grantsFor(t, base, cookie, user); len(rows) != 0 {
		t.Errorf("%d grants exist for the account after refusals", len(rows))
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

	id := stringField(grantsFor(t, base, cookie, user)[0], "id")
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
	id := stringField(grantsFor(t, base, cookie, user)[0], "id")
	if status, body := mutate(t, http.MethodPatch,
		fmt.Sprintf("%s/api/v1/admin/grants/%s", base, id), cookie, csrf,
		map[string]any{"allow": []string{}, "inherit": true, "label": "docs"}); status != http.StatusOK {
		// The update answers the grant, as the create route does, so the
		// screen can swap the row it just edited for what came back.
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
	id := stringField(grantsFor(t, base, cookie, user)[0], "id")
	status, body := mutate(t, http.MethodPatch,
		fmt.Sprintf("%s/api/v1/admin/grants/%s", base, id), cookie, csrf,
		map[string]any{
			"allow": []string{"read", "download"},
			"deny":  []string{"read"},
			"label": "docs", "inherit": true,
		})
	if status != http.StatusOK {
		t.Fatalf("updating answered %d: %v", status, body)
	}

	if code, raw := withCookie(t, http.MethodGet,
		base+"/api/v1/files/list?path="+urlEscape("/docs"), plainCookie); code == http.StatusOK {
		t.Errorf("the denied account still lists the share: %s", raw)
	}

	// And the stored grant reports the deny, so an administrator reopening
	// the screen sees the restriction they applied.
	rows := grantsFor(t, base, cookie, user)
	if len(rows) != 1 {
		t.Fatalf("%d grants listed for the account", len(rows))
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

// The storage accounting reports the database and every share.
func TestTheStorageAccounting(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)
	makeShare(t, base, cookie, csrf, "docs")

	status, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/storage", cookie)
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, raw)
	}

	var view map[string]any
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}

	// Decimal strings, because a modern array is past 2^53 bytes and a
	// JavaScript number would round the figure an operator decides on.
	dbBytes := stringField(view, "db_bytes")
	if dbBytes == "" {
		t.Fatalf("no db_bytes in %s", raw)
	}
	if n, err := strconv.ParseInt(dbBytes, 10, 64); err != nil || n <= 0 {
		t.Errorf("db_bytes is %q, which is not a positive decimal", dbBytes)
	}

	shares, ok := view["shares"].([]any)
	if !ok {
		t.Fatalf("no shares list in %s", raw)
	}
	if len(shares) != 1 {
		t.Fatalf("%d shares listed, want 1", len(shares))
	}

	row, ok := shares[0].(map[string]any)
	if !ok {
		t.Fatalf("the share row is not an object: %v", shares[0])
	}
	if stringField(row, "label") != "docs" {
		t.Errorf("the row is labelled %v", row["label"])
	}
	// A measurable filesystem reports both figures, and free cannot exceed
	// total: a screen showing more free than exists is one nobody trusts.
	total, free := stringField(row, "total_bytes"), stringField(row, "free_bytes")
	if total == "" || free == "" {
		t.Fatalf("the share was not measured: %v", row)
	}
	t64, terr := strconv.ParseUint(total, 10, 64)
	f64, ferr := strconv.ParseUint(free, 10, 64)
	if terr != nil || ferr != nil {
		t.Fatalf("the figures are not decimals: total=%q free=%q", total, free)
	}
	if f64 > t64 {
		t.Errorf("free %d exceeds total %d", f64, t64)
	}

	// No host path, for the same reason the share listing carries none.
	assertNoHostPath(t, string(raw), "")
}

// An ordinary account cannot read the storage accounting.
func TestTheStorageAccountingNeedsAnAdministrator(t *testing.T) {
	base, _, _, plainCookie, _ := adminEngine(t)

	status, _ := withCookie(t, http.MethodGet, base+"/api/v1/admin/storage", plainCookie)
	if status != http.StatusForbidden {
		t.Errorf("an ordinary account read the storage accounting: %d", status)
	}
}

// A share whose filesystem cannot be measured is listed without figures.
//
// Dropping the row reads as a share that does not exist; reporting zero reads
// as a full disk. Both are answers an operator would act on, and neither is
// what happened.
//
// The state is built by registering a share, closing the engine, removing the
// directory and reopening: the root then never opens, which is what a disk
// that did not come back looks like. Removing it under a running engine does
// not produce this, because the measurement goes through a descriptor the
// root already holds.
func TestAnUnmeasurableShareIsListedWithoutFigures(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	host := t.TempDir()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if _, cerr := e.Auth.CreateAdmin(ctx, "root", "Root", pwOf(loginPassword)); cerr != nil {
		t.Fatal(cerr)
	}
	if _, serr := e.Core.CreateShare(ctx, core.ShareSpec{Name: "gone", Host: host}); serr != nil {
		t.Fatalf("creating the share: %v", serr)
	}
	if cerr := e.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}
	if rerr := os.RemoveAll(host); rerr != nil {
		t.Fatalf("removing the backing directory: %v", rerr)
	}

	reopened, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := reopened.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	base := serve(t, reopened)

	signIn := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "root", "password": loginPassword})
	cookie := signIn.sessionCookie()
	if cookie == nil {
		t.Fatal("the administrator did not sign in")
	}

	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/storage", cookie)
	if code != http.StatusOK {
		t.Fatalf("answered %d: %s", code, raw)
	}

	var view map[string]any
	if uerr := json.Unmarshal(raw, &view); uerr != nil {
		t.Fatal(uerr)
	}
	shares, isList := view["shares"].([]any)
	if !isList {
		t.Fatalf("no shares list in %s", raw)
	}

	var found bool
	for _, entry := range shares {
		row, isMap := entry.(map[string]any)
		if !isMap || stringField(row, "label") != "gone" {
			continue
		}
		found = true

		// Absent, not zero. A zero total would show the operator a disk with
		// no room on a share whose disk is simply not there.
		if _, present := row["total_bytes"]; present {
			t.Errorf("an unmeasurable share reports a total: %v", row["total_bytes"])
		}
		if _, present := row["free_bytes"]; present {
			t.Errorf("an unmeasurable share reports free space: %v", row["free_bytes"])
		}
	}
	if !found {
		t.Errorf("the unmeasurable share was dropped from the listing: %s", raw)
	}
}

// A registered share survives a restart.
//
// The registry is in-memory and the rows are on disk, so something has to
// reload them. Nothing did: every share an operator had configured vanished
// from a restarted server, taking every grant over it with it, while the rows
// sat in the database.
func TestSharesSurviveARestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	host := t.TempDir()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if _, cerr := e.Auth.CreateAdmin(ctx, "root", "Root", pwOf(loginPassword)); cerr != nil {
		t.Fatal(cerr)
	}
	if _, serr := e.Core.CreateShare(ctx, core.ShareSpec{Name: "docs", Host: host}); serr != nil {
		t.Fatalf("creating the share: %v", serr)
	}
	if cerr := e.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	reopened, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := reopened.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	if got := len(reopened.Core.Shares()); got != 1 {
		t.Fatalf("%d shares after a restart, want the 1 that was registered", got)
	}
	if name := reopened.Core.Shares()[0].Name; name != "docs" {
		t.Errorf("the reloaded share is named %q", name)
	}
}
