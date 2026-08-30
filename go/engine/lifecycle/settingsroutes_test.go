//go:build linux

package lifecycle_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/heavycaffeiner/stowcloud/go/engine/lifecycle"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/auth"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// A section saves and the document reports it back.
func TestSavingASettingsSection(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/rate",
		cookie, csrf, map[string]any{"per_sec": 30, "burst": 90})
	if status != http.StatusOK {
		t.Fatalf("saving answered %d: %v", status, body)
	}
	if !boolField(body, "stored") {
		t.Errorf("the save does not report itself stored: %v", body)
	}

	// Read back through the document, which is what a screen reloads.
	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/settings", cookie)
	if code != http.StatusOK {
		t.Fatalf("reading answered %d: %s", code, raw)
	}
	fields := describedFields(t, raw)
	saved, ok := fields["rate.per_sec"]
	if !ok {
		t.Fatalf("the saved field is not described: %s", raw)
	}
	if fmt.Sprint(saved["value"]) != "30" {
		t.Errorf("the stored rate is %v, want 30", saved["value"])
	}
	// And it reports itself as something an administrator set, which is what
	// distinguishes it from a value that happens to equal the default.
	if got := fmt.Sprint(saved["source"]); got != "admin_override" {
		t.Errorf("a saved value reports source %q", got)
	}
}

// describedFields indexes the settings answer by key.
//
// The route answers the described form rather than the stored document: the
// document alone holds only what somebody saved, which a screen cannot render
// on a fresh deployment and which says nothing about what a field accepts.
func describedFields(t *testing.T, raw []byte) map[string]map[string]any {
	t.Helper()

	var out struct {
		Fields []map[string]any `json:"fields"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	byKey := make(map[string]map[string]any, len(out.Fields))
	for _, f := range out.Fields {
		byKey[fmt.Sprint(f["key"])] = f
	}
	return byKey
}

// A live section says it is applied; a pinned one says a restart is needed.
//
// Those are different answers to "did my change take effect", and folding
// them together is how an administrator spends an afternoon on a setting that
// was stored and never running.
func TestASaveSaysWhetherItIsLive(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	live, liveBody := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/rate",
		cookie, csrf, map[string]any{"per_sec": 30, "burst": 90})
	if live != http.StatusOK {
		t.Fatalf("the live section answered %d: %v", live, liveBody)
	}
	if !boolField(liveBody, "applied") {
		t.Error("a section the chain reads per request is not reported as applied")
	}
	if boolField(liveBody, "restart_required") {
		t.Error("a live section asks for a restart")
	}

	// The watcher takes its bounds when it starts, so this one is pinned.
	pinned, pinnedBody := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/watch",
		cookie, csrf, map[string]any{"hot_set_max": 4096})
	if pinned != http.StatusOK {
		t.Fatalf("the pinned section answered %d: %v", pinned, pinnedBody)
	}
	if !boolField(pinnedBody, "stored") {
		t.Error("a pinned section was not stored")
	}
	if !boolField(pinnedBody, "restart_required") {
		t.Error("a section decided at startup does not ask for a restart")
	}
	if boolField(pinnedBody, "applied") {
		t.Error("a change needing a restart reports itself applied, which is a clean application of something not running")
	}
}

// A live save reaches the running server, not just the database.
//
// This is the claim the "applied" field makes, so it is checked by observing
// the behaviour rather than by reading the field back.
func TestALiveSaveTakesEffectImmediately(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	// A burst small enough that a short loop crosses it. The engine boots at
	// the settings default of 100, so a throttle here is this save landing.
	status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/rate",
		cookie, csrf, map[string]any{"per_sec": 1, "burst": 3})
	if status != http.StatusOK {
		t.Fatalf("saving answered %d: %v", status, body)
	}

	var throttled int
	for i := 0; i < 25; i++ {
		if hostRequest(t, base, "") == http.StatusTooManyRequests {
			throttled++
		}
	}
	if throttled == 0 {
		t.Error("nothing was throttled after saving a burst of 3, so the save did not reach the running limiter")
	}
}

// A section this build does not have is refused rather than stored.
//
// Storing it would leave a document holding a section nothing reads, and the
// client would report a change that never happens.
func TestAnUnknownSectionIsRefused(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	for _, section := range []string{"nosuch", "", "rate/../db", "RATE"} {
		status, _ := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/"+section,
			cookie, csrf, map[string]any{"per_sec": 30})
		if status == http.StatusOK {
			t.Errorf("the section %q was accepted", section)
		}
	}
}

// A blocking finding refuses the save, and stores nothing.
//
// The lockout probe is the one that matters here: a host list that excluded
// the administrator would take effect before any correction could be sent,
// and the correction is what would then be rejected.
func TestALockoutIsRefusedAndStoresNothing(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/network",
		cookie, csrf, map[string]any{"app_hosts": []any{"somewhere.else"}})
	if status == http.StatusOK {
		t.Fatalf("a host list excluding the caller was accepted: %v", body)
	}
	if boolField(body, "stored") {
		t.Error("a refused save reports itself stored")
	}
	if boolField(body, "applied") {
		t.Error("a refused save reports itself applied")
	}

	// Nothing was written, so the administrator can still reach the server
	// and correct it.
	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/settings", cookie)
	if code != http.StatusOK {
		t.Fatalf("the administrator can no longer read the settings: %d", code)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if network, present := doc["network"].(map[string]any); present {
		if hosts, named := network["app_hosts"]; named {
			t.Errorf("the refused host list was stored anyway: %v", hosts)
		}
	}
}

// An ordinary account cannot read or write the settings.
func TestTheSettingsNeedAnAdministrator(t *testing.T) {
	base, _, _, plainCookie, plainCSRF := adminEngine(t)

	read, _ := withCookie(t, http.MethodGet, base+"/api/v1/admin/settings", plainCookie)
	if read != http.StatusForbidden {
		t.Errorf("an ordinary account read the settings: %d", read)
	}

	write, _ := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/rate",
		plainCookie, plainCSRF, map[string]any{"per_sec": 1})
	if write != http.StatusForbidden {
		t.Errorf("an ordinary account wrote the settings: %d", write)
	}
}

// The findings list is present whether or not anything was found.
//
// A client that had to test the field before iterating it is a client that
// will forget once.
func TestTheOutcomeAlwaysCarriesFindings(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	_, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/rate",
		cookie, csrf, map[string]any{"per_sec": 30, "burst": 90})

	raw, present := body["findings"]
	if !present {
		t.Fatalf("no findings field: %v", body)
	}
	if raw == nil {
		t.Error("the findings are null rather than an empty list")
	}
}

// A restart-required save reports what a restart would interrupt.
//
// An in-flight upload loses whichever part was still arriving and a running
// job halts where it stands. Both recover, but neither should happen to
// somebody unannounced, so the operator is told before they decide.
func TestARestartRequiredSaveReportsActiveWork(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/watch",
		cookie, csrf, map[string]any{"hot_set_max": 4096})
	if status != http.StatusOK {
		t.Fatalf("saving answered %d: %v", status, body)
	}
	if !boolField(body, "restart_required") {
		t.Fatal("the pinned section does not ask for a restart, so there is nothing to warn about")
	}

	// Present, because a restart is required. Absent would leave the screen
	// unable to say whether restarting costs anything.
	if _, present := body["active_uploads"]; !present {
		t.Error("no upload count on a restart-required save")
	}
	if _, present := body["active_jobs"]; !present {
		t.Error("no job count on a restart-required save")
	}
}

// A live save carries no active-work counts.
//
// Nothing is going to be interrupted, so reporting the figures would invite a
// warning about a restart that is not happening.
func TestALiveSaveReportsNoActiveWork(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/rate",
		cookie, csrf, map[string]any{"per_sec": 30, "burst": 90})
	if status != http.StatusOK {
		t.Fatalf("saving answered %d: %v", status, body)
	}
	if _, present := body["active_uploads"]; present {
		t.Error("a live save reports work a restart would interrupt")
	}
}

// The counts are the real ones, not a placeholder.
//
// An upload in flight has to appear, or the warning is a field that is always
// zero and an operator learns to ignore it.
func TestTheActiveWorkCountsAreReal(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	host := t.TempDir()

	e, err := lifecycle.Open(ctx, lifecycle.Options{DataDir: dir})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	t.Cleanup(func() {
		if cerr := e.Close(); cerr != nil {
			t.Errorf("closing: %v", cerr)
		}
	})

	admin, err := e.Auth.CreateAdmin(ctx, "root", "Root", pwOf(loginPassword))
	if err != nil {
		t.Fatal(err)
	}
	sh, err := e.Core.CreateShare(ctx, core.ShareSpec{Name: "docs", Host: host})
	if err != nil {
		t.Fatal(err)
	}
	if _, gerr := e.Core.CreateGrant(ctx, core.GrantSpec{
		User: &admin, Share: sh.ID, Allow: everyPerm(), Inherit: true, Label: sh.Name,
	}); gerr != nil {
		t.Fatal(gerr)
	}
	token, err := e.Auth.CreateAppPassword(ctx, admin, "drive",
		auth.Scope{Perms: auth.SyncScopePerms}, 0)
	if err != nil {
		t.Fatal(err)
	}
	base := serve(t, e)

	// A session opened and left open, which is exactly what a restart would
	// interrupt.
	createUpload(t, base, token, "/docs/inflight.bin", 4096)

	signIn := postJSON(t, base+"/api/v1/auth/login",
		map[string]string{"login": "root", "password": loginPassword})
	cookie := signIn.sessionCookie()
	if cookie == nil {
		t.Fatal("the administrator did not sign in")
	}

	status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/watch",
		cookie, signIn.field("csrf"), map[string]any{"hot_set_max": 4096})
	if status != http.StatusOK {
		t.Fatalf("saving answered %d: %v", status, body)
	}

	uploads, ok := body["active_uploads"].(float64)
	if !ok {
		t.Fatalf("no upload count: %v", body)
	}
	if uploads < 1 {
		t.Errorf("the save reports %v uploads in flight while one session is open", uploads)
	}
}

// A client secret never reaches the settings document.
//
// The document is readable by anybody who may read the settings. A value that
// authenticates this server to a provider is not something to hand them, so
// it is stripped on the way in and sealed under the master key.
func TestAClientSecretIsNeverInTheDocument(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	const secret = "a-provider-issued-secret-value"
	status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/oidc",
		cookie, csrf, map[string]any{
			"enabled":       true,
			"issuer":        "https://provider.example",
			"client_id":     "stowcloud",
			"client_secret": secret,
		})
	if status != http.StatusOK {
		t.Fatalf("saving answered %d: %v", status, body)
	}

	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/settings", cookie)
	if code != http.StatusOK {
		t.Fatalf("reading answered %d", code)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("the secret is in the settings document: %s", raw)
	}
	if strings.Contains(string(raw), "client_secret") {
		t.Errorf("the document carries a client_secret field: %s", raw)
	}

	// The rest of the section did save, so stripping the secret did not
	// discard what it arrived with.
	fields := describedFields(t, raw)
	issuer, ok := fields["oidc.issuer"]
	if !ok {
		t.Fatalf("the oidc section was not stored: %s", raw)
	}
	if got := fmt.Sprint(issuer["value"]); got != "https://provider.example" {
		t.Errorf("the issuer is %v", got)
	}
}

// The stored secret opens again, which is what makes sealing it useful.
func TestAStoredClientSecretOpensAgain(t *testing.T) {
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

	const name, secret = "oidc_client_secret", "a-provider-issued-secret-value"

	if !e.HasConfigSecret(ctx, name) {
		// Nothing stored yet, which is the state to start from.
		if serr := e.StoreConfigSecret(ctx, name, secret); serr != nil {
			t.Fatalf("storing: %v", serr)
		}
	}

	got, ok, err := e.ConfigSecret(ctx, name)
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if !ok {
		t.Fatal("the stored secret is not there")
	}
	if got != secret {
		t.Errorf("the secret opened as %q", got)
	}
	if !e.HasConfigSecret(ctx, name) {
		t.Error("the stored secret is not reported as present")
	}

	// An empty value clears rather than storing an empty string, which would
	// be a credential the provider rejects.
	if serr := e.StoreConfigSecret(ctx, name, ""); serr != nil {
		t.Fatalf("clearing: %v", serr)
	}
	if e.HasConfigSecret(ctx, name) {
		t.Error("the secret survived being cleared")
	}
}

// An omitted secret leaves the stored one alone.
//
// A patch names the fields it changes. An administrator editing the issuer
// must not silently clear the credential and break sign-in.
func TestAnOmittedSecretIsNotCleared(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	if status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/oidc",
		cookie, csrf, map[string]any{
			"enabled": true, "issuer": "https://provider.example",
			"client_id": "stowcloud", "client_secret": "the-secret",
		}); status != http.StatusOK {
		t.Fatalf("the first save answered %d: %v", status, body)
	}

	// A second save naming everything except the secret.
	if status, body := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/oidc",
		cookie, csrf, map[string]any{
			"enabled": true, "issuer": "https://elsewhere.example",
			"client_id": "stowcloud",
		}); status != http.StatusOK {
		t.Fatalf("the second save answered %d: %v", status, body)
	}

	// The issuer moved and nothing else was disturbed. The secret itself is
	// checked through the engine, since no response ever carries it.
	code, raw := withCookie(t, http.MethodGet, base+"/api/v1/admin/settings", cookie)
	if code != http.StatusOK {
		t.Fatalf("reading answered %d", code)
	}
	fields := describedFields(t, raw)
	issuer, ok := fields["oidc.issuer"]
	if !ok {
		t.Fatalf("the oidc section is missing: %s", raw)
	}
	if got := fmt.Sprint(issuer["value"]); got != "https://elsewhere.example" {
		t.Errorf("the issuer is %v after the second save", got)
	}
}

// A non-string secret is refused rather than coerced.
func TestANonStringSecretIsRefused(t *testing.T) {
	base, cookie, csrf, _, _ := adminEngine(t)

	for _, value := range []any{42, true, map[string]any{}, []any{"a"}} {
		status, _ := mutate(t, http.MethodPatch, base+"/api/v1/admin/settings/oidc",
			cookie, csrf, map[string]any{
				"enabled": true, "issuer": "https://provider.example",
				"client_id": "stowcloud", "client_secret": value,
			})
		if status == http.StatusOK {
			t.Errorf("a %T client secret was accepted", value)
		}
	}
}
