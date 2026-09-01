//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/server"
	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
)

// adminEngine serves an engine with one administrator and one ordinary
// account, returning both sets of credentials.
func adminEngine(t *testing.T) (base string, adminCookie *http.Cookie, adminCSRF string,
	plainCookie *http.Cookie, plainCSRF string,
) {
	t.Helper()
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	if _, cerr := e.Auth.CreateAdmin(ctx, "root", "Root", pwOf(loginPassword)); cerr != nil {
		t.Fatalf("creating the administrator: %v", cerr)
	}
	if _, cerr := e.Auth.CreateUser(ctx, loginName, "Alice", pwOf(loginPassword)); cerr != nil {
		t.Fatalf("creating the account: %v", cerr)
	}
	base = serve(t, e)

	admin := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "root", "password": loginPassword})
	if admin.sessionCookie() == nil {
		t.Fatalf("the administrator did not sign in: %d %v", admin.status, admin.body)
	}
	plain := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	if plain.sessionCookie() == nil {
		t.Fatalf("the ordinary account did not sign in: %d %v", plain.status, plain.body)
	}

	return base, admin.sessionCookie(), admin.field("csrf"),
		plain.sessionCookie(), plain.field("csrf")
}

// No administrative route answers an ordinary signed-in account.
//
// The chain requires a session for this prefix but says nothing about whose,
// so the check lives in the handlers. That makes it something a new route can
// silently omit, which is why this walks the whole table rather than naming
// the routes it happens to know about: a route added without the gate fails
// here on the day it is added.
func TestNoAdminRouteAnswersAnOrdinaryAccount(t *testing.T) {
	base, adminCookie, adminCSRF, plainCookie, plainCSRF := adminEngine(t)

	var checked int
	for _, r := range server.Table() {
		if !strings.HasPrefix(r.Name, "admin.") {
			continue
		}

		url := base + concretePath(r.Path)
		t.Run(r.Name, func(t *testing.T) {
			status, body := mutate(t, r.Method, url, plainCookie, plainCSRF, map[string]any{})

			// 501 is a route with no binding yet, which cannot leak anything.
			// Everything else has to be a refusal.
			if status == http.StatusNotImplemented {
				t.Skip("not bound in this build")
			}
			if status < 400 {
				t.Errorf("%s %s answered %d to a non-administrator: %v",
					r.Method, r.Path, status, body)
			}
			if status != http.StatusForbidden {
				t.Errorf("%s %s answered %d, want 403", r.Method, r.Path, status)
			}
		})
		checked++
	}

	if checked == 0 {
		t.Fatal("no administrative routes were checked, so this test proves nothing")
	}

	// The administrator can reach one, which proves the refusals above came
	// from the identity check rather than from every route being broken.
	if status, body := mutate(t, http.MethodGet,
		base+"/api/v1/admin/users", adminCookie, adminCSRF, nil); status != http.StatusOK {
		t.Fatalf("the administrator cannot list users either (%d: %v), so the test above is vacuous",
			status, body)
	}
}

// concretePath fills a route's parameters with a value that parses, so the
// request reaches the handler's own checks rather than stopping at the router.
func concretePath(pattern string) string {
	out := pattern
	for _, param := range []string{"{id}", "{user}", "{section}", "{token}"} {
		out = strings.ReplaceAll(out, param, "1")
	}
	return out
}

// An app password cannot reach the administrative surface at all.
//
// It is a filesystem credential handed to a device, and a device must not be
// able to create accounts. The chain decides this, not the handlers, so it is
// checked separately from the identity gate above.
func TestAnAppPasswordCannotReachAdmin(t *testing.T) {
	base, token, _ := bootWithUser(t)

	status, body := authed(t, http.MethodGet, base+"/api/v1/admin/users", token)
	if status == http.StatusOK {
		t.Fatalf("an app password listed the accounts: %s", body)
	}
}

// An administrator lists accounts, and no credential material appears.
func TestListingAccounts(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/users", cookie)
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, raw)
	}
	_ = csrf

	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d accounts listed, want 2", len(rows))
	}

	// Nothing that could be used to sign in as anybody.
	for _, forbidden := range []string{"password", "hash", "pw_hash", "secret", "token", "nt_hash"} {
		if strings.Contains(string(raw), `"`+forbidden+`"`) {
			t.Errorf("the listing carries a %q field: %s", forbidden, raw)
		}
	}

	// The administrator is marked, since that is what the screen is for.
	var admins int
	for _, row := range rows {
		if boolField(row, "admin") {
			admins++
		}
	}
	if admins != 1 {
		t.Errorf("%d of %d accounts are marked administrator", admins, len(rows))
	}
}

// Creating an account produces one that can sign in.
func TestCreatingAnAccount(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, body := mutate(t, http.MethodPost, base+"/api/v1/admin/users", cookie, csrf,
		map[string]string{
			"login":    "bob",
			"display":  "Bob",
			"password": "a-long-enough-password",
		})
	if status != http.StatusCreated {
		t.Fatalf("creating answered %d: %v", status, body)
	}
	if stringField(body, "login") != "bob" {
		t.Errorf("the response names %v", body["login"])
	}

	// The account is real: it signs in.
	fresh := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "bob", "password": "a-long-enough-password"})
	if fresh.sessionCookie() == nil {
		t.Errorf("the created account cannot sign in: %d %v", fresh.status, fresh.body)
	}

	// And it is not an administrator, because nothing asked for that. A
	// create that quietly granted administration would hand the whole
	// deployment to whoever the operator was adding as an ordinary user.
	if boolField(body, "admin") {
		t.Error("a plain create produced an administrator")
	}
}

// A name the account rule refuses is refused here too.
//
// The rule gates creation for every surface, and the administrative one used
// to bypass it in the old tree: one name the credential file cannot carry
// costs every account its file-sharing access.
func TestAnInvalidAccountNameIsRefused(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	for _, name := range []string{"", "-leading", "Upper", "with space", "sym!bol",
		strings.Repeat("a", 33)} {
		status, _ := mutate(t, http.MethodPost, base+"/api/v1/admin/users", cookie, csrf,
			map[string]string{"login": name, "password": "a-long-enough-password"})
		if status == http.StatusCreated {
			t.Errorf("the name %q was accepted", name)
		}
	}
}

// A password under the floor is refused when an administrator sets it too.
func TestAWeakPasswordIsRefusedOnCreate(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, _ := mutate(t, http.MethodPost, base+"/api/v1/admin/users", cookie, csrf,
		map[string]string{"login": "bob", "password": "short"})
	if status == http.StatusCreated {
		t.Error("an account was created with a password under the floor")
	}
}

// Disabling an account stops it signing in.
func TestDisablingAnAccount(t *testing.T) {
	base, cookie, csrf, plainCookie, _ := adminEngine(t)

	id := accountID(t, base, cookie, loginName)
	status, body := mutate(t, http.MethodPatch,
		fmt.Sprintf("%s/api/v1/admin/users/%s", base, id), cookie, csrf,
		map[string]any{"disabled": true})
	if status != http.StatusOK {
		t.Fatalf("disabling answered %d: %v", status, body)
	}

	after := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	if after.sessionCookie() != nil {
		t.Error("a disabled account still signs in")
	}

	// Its live session dies too. Leaving it usable means disabling somebody
	// takes effect only when they next sign in, which is exactly when they
	// would not.
	if code, _ := withCookie(t, http.MethodGet,
		base+"/api/v1/auth/session", plainCookie); code == http.StatusOK {
		t.Error("the disabled account's existing session still works")
	}
}

// accountID finds an account's id through the listing.
func accountID(t *testing.T, base string, cookie *http.Cookie, login string) string {
	t.Helper()

	status, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/users", cookie)
	if status != http.StatusOK {
		t.Fatalf("listing answered %d", status)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if stringField(row, "login") == login {
			return stringField(row, "id")
		}
	}
	t.Fatalf("no account named %q in the listing", login)
	return ""
}

// An administrator cannot disable or delete themselves.
//
// The service's last-admin rule catches only the final administrator. With
// two, either could lock themselves out, which is a mistake nobody makes
// deliberately and which nobody else can undo if they were the only one
// paying attention.
func TestAnAdministratorCannotLockThemselvesOut(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	id := accountID(t, base, cookie, "root")

	status, body := mutate(t, http.MethodPatch,
		fmt.Sprintf("%s/api/v1/admin/users/%s", base, id), cookie, csrf,
		map[string]any{"disabled": true})
	if status == http.StatusOK {
		t.Fatalf("the administrator disabled themselves: %v", body)
	}

	status, body = mutate(t, http.MethodDelete,
		fmt.Sprintf("%s/api/v1/admin/users/%s", base, id), cookie, csrf, nil)
	if status == http.StatusNoContent {
		t.Fatalf("the administrator deleted themselves: %v", body)
	}

	// And they are still able to work, which is the point.
	if code, _ := withCookie(t, http.MethodGet, base+"/api/v1/admin/users", cookie); code != http.StatusOK {
		t.Error("the administrator locked themselves out anyway")
	}
}

// Deleting an account removes it.
func TestDeletingAnAccount(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	id := accountID(t, base, cookie, loginName)
	status, body := mutate(t, http.MethodDelete,
		fmt.Sprintf("%s/api/v1/admin/users/%s", base, id), cookie, csrf, nil)
	if status != http.StatusNoContent {
		t.Fatalf("deleting answered %d: %v", status, body)
	}

	after := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": loginName, "password": loginPassword})
	if after.sessionCookie() != nil {
		t.Error("a deleted account still signs in")
	}
}

// Groups can be made, renamed, filled and emptied.
func TestTheGroupLifecycle(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, created := mutate(t, http.MethodPost, base+"/api/v1/admin/groups", cookie, csrf,
		map[string]string{"name": "editors"})
	if status != http.StatusCreated {
		t.Fatalf("creating answered %d: %v", status, created)
	}
	group := stringField(created, "id")
	if group == "" {
		t.Fatal("the created group has no id")
	}

	user := accountID(t, base, cookie, loginName)
	status, body := mutate(t, http.MethodPost,
		fmt.Sprintf("%s/api/v1/admin/groups/%s/members", base, group), cookie, csrf,
		map[string]string{"user": user})
	if status != http.StatusNoContent {
		t.Fatalf("adding a member answered %d: %v", status, body)
	}

	if !groupHasMember(t, base, cookie, group, user) {
		t.Error("the member is not in the group after being added")
	}

	status, body = mutate(t, http.MethodDelete,
		fmt.Sprintf("%s/api/v1/admin/groups/%s/members/%s", base, group, user), cookie, csrf, nil)
	if status != http.StatusNoContent {
		t.Fatalf("removing a member answered %d: %v", status, body)
	}
	if groupHasMember(t, base, cookie, group, user) {
		t.Error("the member is still in the group after being removed")
	}

	status, body = mutate(t, http.MethodDelete,
		fmt.Sprintf("%s/api/v1/admin/groups/%s", base, group), cookie, csrf, nil)
	if status != http.StatusNoContent {
		t.Fatalf("deleting answered %d: %v", status, body)
	}
	if len(listGroups(t, base, cookie)) != 0 {
		t.Error("the group survived its own deletion")
	}
}

// listGroups reads the group listing.
func listGroups(t *testing.T, base string, cookie *http.Cookie) []map[string]any {
	t.Helper()

	status, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/groups", cookie)
	if status != http.StatusOK {
		t.Fatalf("listing groups answered %d: %s", status, raw)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// groupHasMember reports whether the listing shows a membership.
func groupHasMember(t *testing.T, base string, cookie *http.Cookie, group, user string) bool {
	t.Helper()

	for _, row := range listGroups(t, base, cookie) {
		if stringField(row, "id") != group {
			continue
		}
		members, ok := row["members"].([]any)
		if !ok {
			return false
		}
		for _, m := range members {
			if s, isString := m.(string); isString && s == user {
				return true
			}
		}
	}
	return false
}

// The audit log records a sign-in and never carries a credential.
func TestTheAuditLog(t *testing.T) {
	base, cookie, _, _, _ := adminEngine(t)

	status, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/audit", cookie)
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, raw)
	}

	var page map[string]any
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}

	rows, ok := page["rows"].([]any)
	if !ok {
		t.Fatalf("no rows in %s", raw)
	}
	if len(rows) == 0 {
		t.Error("the log is empty after two sign-ins")
	}

	// The log is the one place a password would be most damaging, because it
	// is retained and read.
	for _, forbidden := range []string{loginPassword, "password", "hash", "secret"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("the audit page carries %q", forbidden)
		}
	}
}

// The audit page is bounded whatever a client asks for.
//
// The log grows without limit, so an unbounded request is a scan of every
// event the deployment has ever recorded, and the parameter is one a caller
// controls entirely.
func TestTheAuditLimitIsBounded(t *testing.T) {
	base, cookie, _, _, _ := adminEngine(t)

	for _, limit := range []string{"1", "999999", "-5", "0", "abc"} {
		status, raw := withCookie(t, http.MethodGet,
			base+"/api/v1/admin/audit?limit="+limit, cookie)
		if status != http.StatusOK {
			t.Fatalf("limit=%s answered %d: %s", limit, status, raw)
		}

		var page map[string]any
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		rows, isList := page["rows"].([]any)
		if !isList {
			t.Fatalf("limit=%s returned no rows list: %s", limit, raw)
		}

		// The ceiling itself is checked against auditLimit directly, since
		// proving it here would need more rows than the ceiling. What this
		// covers is that the parameter reaches the query at all.
		if n, err := strconv.Atoi(limit); err == nil && n > 0 && len(rows) > n {
			t.Errorf("limit=%s returned %d rows", limit, len(rows))
		}
	}
}

// With a second administrator present, the self-guard is the only thing left.
//
// The service's last-admin rule refuses a delete that would empty the role,
// which masks the guard entirely in a deployment with one administrator:
// removing the guard changes no answer there. With two, the rule permits it
// and only this check stands between an operator and deleting the account
// they are signed in as.
func TestAnAdministratorCannotDeleteThemselvesWithASecondPresent(t *testing.T) {
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	if _, cerr := e.Auth.CreateAdmin(ctx, "root", "Root", pwOf(loginPassword)); cerr != nil {
		t.Fatal(cerr)
	}
	if _, cerr := e.Auth.CreateAdmin(ctx, "second", "Second", pwOf(loginPassword)); cerr != nil {
		t.Fatal(cerr)
	}
	base := serve(t, e)

	signIn := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "root", "password": loginPassword})
	cookie := signIn.sessionCookie()
	if cookie == nil {
		t.Fatal("the administrator did not sign in")
	}
	csrf := signIn.field("csrf")

	id := accountID(t, base, cookie, "root")

	status, body := mutate(t, http.MethodDelete,
		fmt.Sprintf("%s/api/v1/admin/users/%s", base, id), cookie, csrf, nil)
	if status == http.StatusNoContent {
		t.Fatalf("an administrator deleted their own account: %v", body)
	}

	// Still signed in and still able to work.
	if code, _ := withCookie(t, http.MethodGet, base+"/api/v1/admin/users", cookie); code != http.StatusOK {
		t.Error("the administrator removed their own access anyway")
	}

	// Deleting the other administrator is permitted, which proves the refusal
	// above came from the self-check rather than from deletion being broken.
	other := accountID(t, base, cookie, "second")
	if code, other := mutate(t, http.MethodDelete,
		fmt.Sprintf("%s/api/v1/admin/users/%s", base, other), cookie, csrf, nil); code != http.StatusNoContent {
		t.Errorf("deleting another administrator answered %d: %v", code, other)
	}
}

// The same, for disabling.
func TestAnAdministratorCannotDisableThemselvesWithASecondPresent(t *testing.T) {
	ctx := context.Background()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	if _, cerr := e.Auth.CreateAdmin(ctx, "root", "Root", pwOf(loginPassword)); cerr != nil {
		t.Fatal(cerr)
	}
	if _, cerr := e.Auth.CreateAdmin(ctx, "second", "Second", pwOf(loginPassword)); cerr != nil {
		t.Fatal(cerr)
	}
	base := serve(t, e)

	signIn := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "root", "password": loginPassword})
	cookie := signIn.sessionCookie()
	if cookie == nil {
		t.Fatal("the administrator did not sign in")
	}
	csrf := signIn.field("csrf")
	id := accountID(t, base, cookie, "root")

	status, body := mutate(t, http.MethodPatch,
		fmt.Sprintf("%s/api/v1/admin/users/%s", base, id), cookie, csrf,
		map[string]any{"disabled": true})
	if status == http.StatusOK {
		t.Fatalf("an administrator disabled their own account: %v", body)
	}
	if code, _ := withCookie(t, http.MethodGet, base+"/api/v1/admin/users", cookie); code != http.StatusOK {
		t.Error("the administrator disabled themselves anyway")
	}
}
