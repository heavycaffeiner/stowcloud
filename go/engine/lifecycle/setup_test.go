//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// bootUnconfigured opens an engine over an empty directory, which is what a
// first boot is: no accounts, and a setup token minted for the form.
func bootUnconfigured(t *testing.T) (base, dataDir string, e *lifecycle.Engine) {
	t.Helper()
	dataDir = t.TempDir()

	e, err := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: dataDir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	return serve(t, e), dataDir, e
}

// setupToken reads the token the boot published.
func setupToken(t *testing.T, dataDir string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dataDir, "setup-token"))
	if err != nil {
		t.Fatalf("reading the setup token: %v", err)
	}
	return strings.TrimSpace(string(body))
}

// A deployment with no accounts says it needs setting up.
//
// The field name is what the client reads. Answering under any other name is
// a first-run screen that never appears: the client reads the absence as
// false and sends the person to sign in to an account nobody has created.
func TestAFreshDeploymentReportsThatSetupIsRequired(t *testing.T) {
	base, _, _ := bootUnconfigured(t)

	status, raw := get(t, base+"/api/v1/system/setup")
	if status != http.StatusOK {
		t.Fatalf("the setup state answered %d: %s", status, raw)
	}
	var view map[string]any
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	if required, isBool := view["required"].(bool); !isBool || !required {
		t.Errorf("a deployment with no accounts reports %v", view)
	}
}

// The state is readable without a credential, because there is no account to
// hold one yet.
func TestTheSetupStateNeedsNoCredential(t *testing.T) {
	base, _, _ := bootUnconfigured(t)

	status, _ := get(t, base+"/api/v1/system/setup")
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		t.Errorf("the setup state demanded a credential: %d", status)
	}
}

// A boot with no accounts publishes a token, since nothing else mints one and
// the form cannot be completed without it.
//
// No listener here. The token is written during Open, and this test reads a
// file rather than making a request.
func TestAFirstBootPublishesASetupToken(t *testing.T) {
	dataDir := t.TempDir()
	e, err := lifecycle.Open(context.Background(), lifecycle.Options{DataDir: dataDir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	path := filepath.Join(dataDir, "setup-token")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("no setup token was published: %v", err)
	}
	// Anyone who reads this file can create the administrator, so it is not
	// readable by anyone but the account that wrote it.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the token file is mode %o, want 600", perm)
	}
	if setupToken(t, dataDir) == "" {
		t.Error("the published token is empty")
	}
}

// The whole first run: the token is spent, the administrator exists, and the
// account can reach the shares it was granted.
func TestCompletingSetupCreatesAnAdministratorWhoCanSignIn(t *testing.T) {
	base, dataDir, _ := bootUnconfigured(t)

	// The host list names the address this test actually reaches the server
	// on. Naming another one is legitimate and is what the lockout warning
	// exists for, but it also takes effect immediately: the boundary would
	// refuse the sign-in below with a 421 rather than a credential failure.
	resp := postJSON(t, base+"/api/v1/system/setup", map[string]any{
		"token":     setupToken(t, dataDir),
		"username":  "root",
		"password":  loginPassword,
		"app_hosts": []string{hostOf(t, base)},
	})
	if resp.status != http.StatusOK {
		t.Fatalf("completing setup answered %d: %v", resp.status, resp.body)
	}

	user, ok := resp.body["user"].(map[string]any)
	if !ok {
		t.Fatalf("no account in %v", resp.body)
	}
	if user["name"] != "root" {
		t.Errorf("the account is named %v", user["name"])
	}
	// Decimal, because ids run past what a double holds exactly and a client
	// that rounded one would name a different account.
	if _, isString := user["id"].(string); !isString {
		t.Errorf("the id came back as %T, want a decimal string", user["id"])
	}

	// Deliberately not a session: the account is created and the client then
	// authenticates through the one path that issues a credential.
	if resp.sessionCookie() != nil {
		t.Error("completing setup issued a session")
	}

	// The warnings field is always present, so a client can render it without
	// testing for it first.
	if _, ok := resp.body["warnings"]; !ok {
		t.Errorf("the answer carries no warnings field: %v", resp.body)
	}
}

// The account the first run creates is a working administrator.
//
// The host list names the address this test browses on, so the boundary keeps
// admitting it and the account can actually be used afterwards.
func TestTheAccountSetupCreatesIsAWorkingAdministrator(t *testing.T) {
	base, dataDir, _ := bootUnconfigured(t)

	if resp := postJSON(t, base+"/api/v1/system/setup", map[string]any{
		"token": setupToken(t, dataDir), "username": "root", "password": loginPassword,
		"app_hosts": []string{hostOf(t, base)},
	}); resp.status != http.StatusOK {
		t.Fatalf("completing setup answered %d: %v", resp.status, resp.body)
	}

	signIn := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "root", "password": loginPassword})
	if signIn.sessionCookie() == nil {
		t.Fatalf("the created administrator could not sign in: %d %v",
			signIn.status, signIn.body)
	}
	status, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/users", signIn.sessionCookie())
	if status != http.StatusOK {
		t.Fatalf("the first account is not an administrator: %d %s", status, raw)
	}
}

// The gate closes for good once an account exists.
//
// A token recovered from a log or a backup after setup is worth nothing,
// because the account count is the authority rather than the token.
func TestSetupRefusesOnceAnAccountExists(t *testing.T) {
	base, dataDir, _ := bootUnconfigured(t)
	token := setupToken(t, dataDir)

	// The caller's own host, so the boundary keeps admitting this test after
	// the list takes effect. A list naming somewhere else answers 421 to the
	// reads below, which is a different refusal from the one under test.
	first := postJSON(t, base+"/api/v1/system/setup", map[string]any{
		"token": token, "username": "root", "password": loginPassword,
		"app_hosts": []string{hostOf(t, base)},
	})
	if first.status != http.StatusOK {
		t.Fatalf("the first run answered %d: %v", first.status, first.body)
	}

	second := postJSON(t, base+"/api/v1/system/setup", map[string]any{
		"token": token, "username": "second", "password": loginPassword,
		"app_hosts": []string{hostOf(t, base)},
	})
	if second.status == http.StatusOK {
		t.Fatal("setup ran twice and created a second administrator")
	}

	// And the state says so, so a client does not offer the form again.
	_, raw := get(t, base+"/api/v1/system/setup")
	var view map[string]any
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	if required, isBool := view["required"].(bool); !isBool || required {
		t.Errorf("a configured deployment still reports setup as required: %v", view)
	}
}

// A wrong token creates nothing, and does not spend the real one.
//
// Spending it would leave an operator whose single token was consumed by
// somebody else's guess.
func TestAWrongSetupTokenCreatesNothingAndSpendsNothing(t *testing.T) {
	base, dataDir, _ := bootUnconfigured(t)

	bad := postJSON(t, base+"/api/v1/system/setup", map[string]any{
		"token":    "0000000000000000000000000000000000000000000000000000000000000000",
		"username": "root", "password": loginPassword,
		"app_hosts": []string{"files.example.test"},
	})
	if bad.status == http.StatusOK {
		t.Fatalf("a wrong token completed setup: %v", bad.body)
	}

	good := postJSON(t, base+"/api/v1/system/setup", map[string]any{
		"token": setupToken(t, dataDir), "username": "root", "password": loginPassword,
		"app_hosts": []string{"files.example.test"},
	})
	if good.status != http.StatusOK {
		t.Fatalf("the real token was refused after a wrong one: %d %v",
			good.status, good.body)
	}
}

// A password under the floor is refused, and the token survives it.
//
// The refusal is the account rule rather than the gate's, so the operator
// corrects the password and submits the same token again.
func TestAWeakSetupPasswordIsRefusedAndTheTokenSurvives(t *testing.T) {
	base, dataDir, _ := bootUnconfigured(t)
	token := setupToken(t, dataDir)

	weak := postJSON(t, base+"/api/v1/system/setup", map[string]any{
		"token": token, "username": "root", "password": "short",
		"app_hosts": []string{"files.example.test"},
	})
	if weak.status == http.StatusOK {
		t.Fatalf("a password under the floor was accepted: %v", weak.body)
	}

	retry := postJSON(t, base+"/api/v1/system/setup", map[string]any{
		"token": token, "username": "root", "password": loginPassword,
		"app_hosts": []string{"files.example.test"},
	})
	if retry.status != http.StatusOK {
		t.Fatalf("the token did not survive a refused password: %d %v",
			retry.status, retry.body)
	}
}

// A name the account rule refuses is refused here too.
//
// One rule, applied at creation. A name this rule admits and the credential
// file cannot carry would cost every account its file-sharing access.
func TestASetupUsernameMustPassTheAccountRule(t *testing.T) {
	base, dataDir, _ := bootUnconfigured(t)

	for _, name := range []string{"Root", "has space", "-leading", "has:colon"} {
		resp := postJSON(t, base+"/api/v1/system/setup", map[string]any{
			"token": setupToken(t, dataDir), "username": name,
			"password": loginPassword, "app_hosts": []string{"files.example.test"},
		})
		if resp.status == http.StatusOK {
			t.Errorf("the name %q was accepted", name)
		}
	}
}

// The first administrator can reach the shares that already exist.
//
// A share is reachable only through a grant and a first run has none, so
// without the grant pass the account signs in to an empty interface with no
// way to give itself anything.
func TestTheFirstAdministratorIsGrantedTheExistingShares(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	// A share registered before the first account, which is the order an
	// operator restoring a configuration lands in.
	first, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dataDir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if _, cerr := first.Core.CreateShare(ctx, core.ShareSpec{Name: "documents", Host: t.TempDir()}); cerr != nil {
		t.Fatalf("registering a share: %v", cerr)
	}
	if cerr := first.Close(); cerr != nil {
		t.Fatalf("closing: %v", cerr)
	}

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dataDir})
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})
	base := serve(t, e)

	resp := postJSON(t, base+"/api/v1/system/setup", map[string]any{
		"token": setupToken(t, dataDir), "username": "root",
		"password": loginPassword, "app_hosts": []string{hostOf(t, base)},
	})
	if resp.status != http.StatusOK {
		t.Fatalf("completing setup answered %d: %v", resp.status, resp.body)
	}

	signIn := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "root", "password": loginPassword})
	if signIn.sessionCookie() == nil {
		t.Fatalf("the administrator could not sign in: %v", signIn.body)
	}

	status, raw := withCookie(t, http.MethodGet, base+"/api/v1/files/list?path=/", signIn.sessionCookie())
	if status != http.StatusOK {
		t.Fatalf("listing the root answered %d: %s", status, raw)
	}
	if !strings.Contains(string(raw), "documents") {
		t.Errorf("the administrator sees no shares:\n%s", raw)
	}
}

// The account password is what the created administrator signs in with, so a
// setup that stored something else would be undetectable until first use.
func TestTheSetupPasswordIsTheOneStored(t *testing.T) {
	base, dataDir, _ := bootUnconfigured(t)

	if resp := postJSON(t, base+"/api/v1/system/setup", map[string]any{
		"token": setupToken(t, dataDir), "username": "root",
		"password": loginPassword, "app_hosts": []string{hostOf(t, base)},
	}); resp.status != http.StatusOK {
		t.Fatalf("completing setup answered %d: %v", resp.status, resp.body)
	}

	wrong := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "root", "password": "not-the-password"})
	if wrong.sessionCookie() != nil {
		t.Error("a wrong password signed in as the created administrator")
	}
}

// hostOf is the name a test reaches the server under, without the port.
//
// An app-host list holds names rather than addresses with ports, and the
// checker refuses a port here. It has to be this name: the list takes effect
// on the next request, so a list naming something else answers 421 to every
// request the test makes afterwards.
func hostOf(t *testing.T, base string) string {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parsing %s: %v", base, err)
	}
	return u.Hostname()
}

// A host list naming somewhere the operator is not browsing from is saved with
// a warning rather than refused, and it takes effect at once.
//
// Warning rather than refusal, because naming the DNS name a deployment will
// be reached under while connected by address is the ordinary way to set one
// up, and no rule separates it from the typo it resembles. The 421 afterwards
// is the boundary enforcing exactly what the form asked for.
func TestAHostListThatExcludesTheCallerIsSavedWithAWarning(t *testing.T) {
	base, dataDir, _ := bootUnconfigured(t)

	resp := postJSON(t, base+"/api/v1/system/setup", map[string]any{
		"token": setupToken(t, dataDir), "username": "root",
		"password": loginPassword, "app_hosts": []string{"files.example.test"},
	})
	if resp.status != http.StatusOK {
		t.Fatalf("completing setup answered %d: %v", resp.status, resp.body)
	}

	warnings, ok := resp.body["warnings"].([]any)
	if !ok || len(warnings) == 0 {
		t.Errorf("no warning about a host list that shuts the caller out: %v", resp.body)
	}
	if status, _ := get(t, base+"/api/v1/system/setup"); status != http.StatusMisdirectedRequest {
		t.Errorf("the saved host list did not take effect: %d", status)
	}
}
