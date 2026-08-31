//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
)

// With no provider configured, the sign-in screen is told so.
//
// Public, because a caller with no session is exactly who is looking at that
// screen and deciding whether to draw the button.
func TestSignOnConfigIsPublicAndSaysOffByDefault(t *testing.T) {
	base := boot(t)

	resp, err := testClient().Get(base + "/api/v1/auth/oidc/config")
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the config answered %d without a credential", resp.StatusCode)
	}

	var view map[string]any
	if derr := json.NewDecoder(resp.Body).Decode(&view); derr != nil {
		t.Fatal(derr)
	}
	if boolField(view, "enabled") {
		t.Error("a deployment that configured nothing reports sign-on as enabled")
	}
}

// The config never carries the issuer, the client id or the secret.
//
// It is the one response an unauthenticated caller can read, so what it says
// about the deployment is what anybody can learn about it.
func TestSignOnConfigRevealsNothingAboutTheProvider(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	if status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/oidc",
		cookie, csrf, map[string]any{
			"enabled":       true,
			"issuer":        "https://provider.example",
			"client_id":     "a-client-identifier",
			"client_secret": "a-provider-issued-secret",
			"display_name":  "Company sign-in",
		}); status != http.StatusOK {
		t.Fatalf("configuring answered %d: %v", status, body)
	}

	resp, err := testClient().Get(base + "/api/v1/auth/oidc/config")
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	body := readAll(t, resp)
	for _, leak := range []string{
		"provider.example", "a-client-identifier", "a-provider-issued-secret",
	} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the public config carries %q: %s", leak, body)
		}
	}
}

// Starting a flow with no provider configured is refused, not crashed.
func TestStartingSignOnWithNoProviderIsRefused(t *testing.T) {
	base, token := bootWithUser(t)

	status, _ := authed(t, http.MethodGet, base+"/api/v1/auth/oidc/start", token)
	if status == http.StatusOK {
		t.Error("a sign-on flow started with no provider configured")
	}
}

// A callback carrying no state is refused before anything is consumed.
func TestACallbackWithoutAStateIsRefused(t *testing.T) {
	base := boot(t)

	for _, query := range []string{"", "?code=abc", "?state=abc", "?error=access_denied"} {
		resp, err := testClient().Get(base + "/api/v1/auth/oidc/callback" + query)
		if err != nil {
			t.Fatalf("requesting: %v", err)
		}
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
		if resp.StatusCode == http.StatusOK {
			t.Errorf("the callback %q was accepted", query)
		}
	}
}

// A state value nobody issued completes nothing.
//
// This is the whole of the callback's authority: it takes the account, the
// return path and the redirect URI from the stored flow, so a browser
// arriving with an invented state has nothing to consume.
func TestAnInventedStateCompletesNothing(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	if status, _ := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/oidc",
		cookie, csrf, map[string]any{
			"enabled": true, "issuer": "https://provider.example",
			"client_id": "stowcloud", "client_secret": "a-secret",
		}); status != http.StatusOK {
		t.Fatal("configuring failed")
	}

	resp, err := testClient().Get(base +
		"/api/v1/auth/oidc/callback?code=anything&state=never-issued")
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	if resp.StatusCode == http.StatusOK {
		t.Fatalf("an invented state was accepted: %s", readAll(t, resp))
	}
	// And no session was established by it.
	for _, c := range resp.Cookies() {
		if c.Name == "__Host-sc_sid" && c.Value != "" {
			t.Error("an invented state produced a session cookie")
		}
	}
}

// Detaching a provider link needs the account's own password.
//
// The link is what the account signs in with. Removing it on a session alone
// would let somebody who walked past an unlocked screen change how the
// account authenticates.
func TestDetachingASignOnLinkNeedsThePassword(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	status, body := mutate(t, http.MethodDelete, base+"/api/v1/account/oidc-link",
		cookie, csrf, map[string]string{"current": "not-the-password"})
	if status == http.StatusNoContent {
		t.Fatalf("a wrong password detached the link: %v", body)
	}

	status, body = mutate(t, http.MethodDelete, base+"/api/v1/account/oidc-link",
		cookie, csrf, map[string]string{})
	if status == http.StatusNoContent {
		t.Fatalf("a session alone detached the link: %v", body)
	}
}

// Attaching a provider link needs the account's own password too.
//
// Detaching is guarded above for a reason that applies at least as strongly
// here: attaching mints a second, permanent way into the account, so somebody
// who walked past an unlocked screen could leave themselves a way back in
// after the session is gone.
func TestAttachingASignOnLinkNeedsThePassword(t *testing.T) {
	base, _, _ := bootForLogin(t)
	cookie, csrf := signedIn(t, base)

	// No provider is configured here, so a request that got past the proof
	// would be refused for that instead. The two refusals are different
	// statuses, and what this asserts is that the proof is the one that
	// answers: an unauthenticated body must never reach the provider check.
	status, body := mutate(t, http.MethodPost, base+"/api/v1/account/oidc-link/start",
		cookie, csrf, map[string]string{"current": "not-the-password"})
	if status != http.StatusUnauthorized {
		t.Fatalf("a wrong password answered %d, want 401: %v", status, body)
	}

	status, body = mutate(t, http.MethodPost, base+"/api/v1/account/oidc-link/start",
		cookie, csrf, map[string]string{})
	if status != http.StatusUnauthorized {
		t.Fatalf("a session alone answered %d, want 401: %v", status, body)
	}
}

// An administrator reads whether an account is linked, and the answer carries
// no token.
func TestReadingAnAccountsSignOnLink(t *testing.T) {
	base, cookie, _, _, _ := adminEngine(t)

	id := accountID(t, base, cookie, loginName)
	status, raw := withCookie(t, http.MethodGet,
		base+"/api/v1/admin/users/"+id+"/oidc", cookie)
	if status != http.StatusOK {
		t.Fatalf("answered %d: %s", status, raw)
	}

	var view map[string]any
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	// An account nobody linked reports exactly that, rather than 404: the
	// question "is this account linked" has an answer for every account.
	if boolField(view, "linked") {
		t.Errorf("an unlinked account reports a link: %s", raw)
	}
	for _, forbidden := range []string{"token", "secret", "password"} {
		if strings.Contains(string(raw), `"`+forbidden+`"`) {
			t.Errorf("the link view carries a %q field: %s", forbidden, raw)
		}
	}
}

// The sign-on routes an administrator owns need an administrator.
func TestTheSignOnAdminRoutesNeedAnAdministrator(t *testing.T) {
	base, adminCookie, _, plainCookie, plainCSRF := adminEngine(t)

	id := accountID(t, base, adminCookie, loginName)

	read, _ := withCookie(t, http.MethodGet, base+"/api/v1/admin/users/"+id+"/oidc", plainCookie)
	if read != http.StatusForbidden {
		t.Errorf("an ordinary account read a link: %d", read)
	}

	del, _ := mutate(t, http.MethodDelete,
		base+"/api/v1/admin/users/"+id+"/oidc", plainCookie, plainCSRF, nil)
	if del != http.StatusForbidden {
		t.Errorf("an ordinary account detached a link: %d", del)
	}
}

// A configured provider produces a client, and the flow starts.
//
// The provider is never contacted here: AuthorizeURL fetches discovery, so an
// unreachable issuer fails the start. What this checks is that the client was
// built at all, which is what the stored secret makes possible.
func TestAConfiguredProviderBuildsAClient(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if _, cerr := e.Auth.CreateAdmin(ctx, "root", "Root", pwOf(loginPassword)); cerr != nil {
		t.Fatal(cerr)
	}
	if serr := e.StoreConfigSecret(ctx, "oidc_client_secret", "a-secret"); serr != nil {
		t.Fatalf("storing the secret: %v", serr)
	}
	if merr := e.State.MergeSettings(ctx, "oidc", map[string]any{
		"enabled": true, "issuer": "https://provider.example", "client_id": "stowcloud",
	}); merr != nil {
		t.Fatalf("saving: %v", merr)
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
	base := serve(t, reopened)

	// The config now advertises it, which only happens when the client built.
	resp, err := testClient().Get(base + "/api/v1/auth/oidc/config")
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	var view map[string]any
	if derr := json.NewDecoder(resp.Body).Decode(&view); derr != nil {
		t.Fatal(derr)
	}
	if !boolField(view, "enabled") {
		t.Errorf("a configured provider with a stored secret is not enabled: %v", view)
	}
}

// A provider configured with no stored secret stays off.
//
// An exchange needs the secret, so a client built without one produces a flow
// that cannot complete. Off with a line beats a button that always fails.
func TestAProviderWithoutASecretStaysOff(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if merr := e.State.MergeSettings(ctx, "oidc", map[string]any{
		"enabled": true, "issuer": "https://provider.example", "client_id": "stowcloud",
	}); merr != nil {
		t.Fatalf("saving: %v", merr)
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
	base := serve(t, reopened)

	resp, err := testClient().Get(base + "/api/v1/auth/oidc/config")
	if err != nil {
		t.Fatalf("requesting: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	}()

	var view map[string]any
	if derr := json.NewDecoder(resp.Body).Decode(&view); derr != nil {
		t.Fatal(derr)
	}
	if boolField(view, "enabled") {
		t.Error("a provider with no stored secret is advertised as enabled")
	}
}
